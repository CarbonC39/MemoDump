// Pure reconciliation engine mirroring internal/cloudsync/reconcile.go.
//
// decideEntity implements the known-baseline and no-baseline tables from the
// lite spec exactly once, as pure functions with no I/O and no retry loops;
// decideRepository orders the decisions repository-wide. The Go and TypeScript
// implementations must emit identical decisions for every shared scenario
// trace (testdata/sync/scenarios).

import { type Entity, computeContentHash } from './entity'
import { stateHash } from './canonical'
import { deriveConflictSyncId, conflictFilename } from './names'

export type LocalState = 'live' | 'absent' | 'unknown'

export type RemoteState = 'live' | 'tombstone' | 'missing' | 'invalid'

/** The immutable local input for one Sync ID. */
export interface LocalObservation {
  syncId: string
  kind?: string
  state: LocalState
  /** canonical entity when state === 'live' */
  entity?: Entity
  /** opaque local CAS token; '' when absent/unknown */
  revision?: string
}

/** The immutable remote input for one Sync ID. */
export interface RemoteObservation {
  syncId: string
  kind?: string
  state: RemoteState
  /** when state is 'live' or 'tombstone' */
  entity?: Entity
  /** opaque provider version */
  version?: string
  /** when invalid: a retryable outcome vs a hard error */
  retryable?: boolean
}

/** The usable snapshot baseline for one Sync ID, when present. */
export interface Baseline {
  contentHash: string
  deleted: boolean
  remoteVersion: string
}

/** Pre-computed structural conflicts that force a block. */
export interface Annotations {
  pathConflict?: boolean
  parentCycle?: boolean
  structuralConflict?: boolean
}

export type DecisionKind =
  | 'noop'
  | 'establish-baseline'
  | 'pull-live'
  | 'push-live'
  | 'push-tombstone'
  | 'apply-tombstone'
  | 'create-conflict'
  | 'repair-index'
  | 'block'
  | 'retry'

export interface ConflictInfo {
  sourceSyncId: string
  conflictSyncId: string
  conflictEntity: Entity
  /** the original Sync ID becomes (or stays) a tombstone */
  originalTombstone: boolean
  /** remote CAS version to tombstone the original ('' when already tombstoned) */
  originalVersion?: string
  /** accept the remote live entity onto the original Sync ID */
  acceptRemoteOriginal: boolean
  /** the entity to apply to the original Sync ID locally (case A pull) */
  originalEntity?: Entity
  /** the tombstone record to push to the original remotely (case C) */
  originalTombstoneEntity?: Entity
  localStateHash: string
  remoteStateHash: string
}

/** The normalized output for one Sync ID. */
export interface Decision {
  syncId: string
  kind: DecisionKind
  reason?: string
  parentId?: string
  contentHash?: string
  deleted?: boolean
  /** remote version: CAS for pushes, target for pulls/establishes */
  version?: string
  /** expected local CAS token for local mutations */
  localRevision?: string
  entity?: Entity
  conflict?: ConflictInfo
}

function entityKind(l: LocalObservation, r: RemoteObservation): string {
  return l.kind ?? l.entity?.kind ?? r.kind ?? r.entity?.kind ?? ''
}

function entityHash(e?: Entity): string {
  return e?.contentHash ?? ''
}

function parentOf(l: LocalObservation, r: RemoteObservation): string {
  return l.entity?.parentId ?? r.entity?.parentId ?? ''
}

/**
 * Computes the normalized decision for one Sync ID from its immutable local
 * observation, remote observation, optional usable baseline, and structural
 * annotations. It performs no I/O. Blocked/unknown and invalid inputs always
 * produce block/retry, never a deletion.
 */
export function decideEntity(
  l: LocalObservation,
  r: RemoteObservation,
  b?: Baseline,
  ann: Annotations = {},
): Decision {
  const kind = entityKind(l, r)
  const d: Decision = {
    syncId: l.syncId || r.syncId,
    kind: 'block',
    parentId: parentOf(l, r),
  }

  if (ann.pathConflict || ann.parentCycle || ann.structuralConflict) {
    return block(d, 'path/graph structural conflict')
  }
  if (l.state === 'unknown') {
    return block(d, 'local unknown (blocked/unstable/unreadable)')
  }
  if (r.state === 'invalid') {
    return r.retryable ? retry(d, 'invalid remote record, retryable') : block(d, 'invalid remote record')
  }

  // A physically missing remote object is damage, never a tombstone: with local
  // content we re-create it (create-if-absent heals the loss); without it the
  // absence is ambiguous and needs repair.
  if (r.state === 'missing') {
    if (l.state === 'live') return pushLive(d, l.entity!, '', l.revision ?? '')
    return repairIndex(d, 'indexed local absence plus physically absent remote object is ambiguous')
  }

  if (!b) return decideNoBaseline(d, l, r, kind)
  return decideWithBaseline(d, l, r, b, kind)
}

function decideNoBaseline(d: Decision, l: LocalObservation, r: RemoteObservation, kind: string): Decision {
  const lHash = entityHash(l.entity)

  switch (l.state) {
    case 'absent':
      switch (r.state) {
        case 'live':
          // Remote-only live content: reserve the Sync ID/path in the index,
          // then create it locally only-if-absent.
          return pullLive(d, r.entity!, r.version ?? '')
        case 'tombstone':
          // Local absence plus a remote tombstone establishes a deleted
          // baseline and removes no user content.
          return establishBaseline(d, entityHash(r.entity), true, r.version ?? '')
        case 'missing':
          // handled in decideEntity before this branch.
          return block(d, 'no usable baseline and no matching rule')
        case 'invalid':
          return block(d, 'invalid remote record')
      }
      break
    case 'live':
      switch (r.state) {
        case 'live':
          if (lHash === r.entity!.contentHash) {
            return establishBaseline(d, lHash, false, r.version ?? '')
          }
          if (kind === 'note') return createConflict(d, l, r, false, '')
          return block(d, 'structural divergence without a baseline')
        case 'tombstone':
          if (kind === 'note') return createConflict(d, l, r, true, '')
          return block(d, 'folder live vs remote tombstone without a baseline')
        case 'missing':
          // Local-only content creates the remote object only-if-absent.
          return pushLive(d, l.entity!, '', l.revision ?? '')
        case 'invalid':
          return block(d, 'invalid remote record')
      }
      break
    case 'unknown':
      return block(d, 'local unknown (blocked/unstable/unreadable)')
  }
  return block(d, 'no usable baseline and no matching rule')
}

function decideWithBaseline(
  d: Decision,
  l: LocalObservation,
  r: RemoteObservation,
  b: Baseline,
  kind: string,
): Decision {
  const lHash = entityHash(l.entity)

  switch (l.state) {
    case 'live':
      switch (r.state) {
        case 'live': {
          const rHash = r.entity!.contentHash
          if (lHash === rHash) {
            // L == R: establish/refresh the baseline; no-op when it already
            // matches.
            if (!b.deleted && b.contentHash === lHash) return noop(d, 'local and remote unchanged')
            return establishBaseline(d, lHash, false, r.version ?? '')
          }
          if (b.deleted) {
            // Baseline deleted but local is live: the user recreated the
            // entity. If the remote tombstone equals the baseline, push the
            // recreation (R == B); otherwise keep-both.
            if (rHash === b.contentHash) return pushLive(d, l.entity!, b.remoteVersion, l.revision ?? '')
            if (kind === 'note') return createConflict(d, l, r, true, '')
            return block(d, 'folder recreated over a divergent tombstone')
          }
          if (lHash === b.contentHash) {
            // L == B and R != B: pull the remote change with the local
            // revision CAS.
            return pullLive(d, r.entity!, r.version ?? '', l.revision ?? '')
          }
          if (rHash === b.contentHash) {
            // R == B and L != B: push the local change with the baseline
            // remote version CAS.
            return pushLive(d, l.entity!, b.remoteVersion, l.revision ?? '')
          }
          // L != B, R != B, L != R: divergent live edits.
          if (kind === 'note') return createConflict(d, l, r, false, '')
          return block(d, 'folder structural conflict')
        }
        case 'tombstone': {
          const rHash = r.entity!.contentHash
          if (b.deleted) {
            // Baseline deleted; local recreated; remote tombstone. When the
            // remote tombstone matches the baseline, push the recreation.
            if (rHash === b.contentHash) return pushLive(d, l.entity!, b.remoteVersion, l.revision ?? '')
            if (kind === 'note') return createConflict(d, l, r, true, '')
            return block(d, 'folder recreated over a divergent tombstone')
          }
          if (lHash === b.contentHash) {
            // Local unchanged live baseline vs remote tombstone: write a
            // recovery copy, then delete locally with the revision CAS.
            return applyTombstone(d, r.version ?? '', l.revision ?? '')
          }
          // Locally edited live vs remote tombstone: preserve the local edit
          // as a conflict entity, then recover/delete the original.
          if (kind === 'note') return createConflict(d, l, r, true, '')
          return block(d, 'folder edited over a remote tombstone')
        }
        case 'missing':
          // handled in decideEntity before this branch.
          return block(d, 'no matching rule')
        case 'invalid':
          return block(d, 'invalid remote record')
      }
      break
    case 'absent':
      switch (r.state) {
        case 'live': {
          const rHash = r.entity!.contentHash
          if (b.deleted) {
            // L == B (deleted) and R != B: pull the recreated remote entity.
            return pullLive(d, r.entity!, r.version ?? '')
          }
          if (!b.deleted && rHash === b.contentHash) {
            // Local absent, remote unchanged live baseline: replace the remote
            // with a tombstone using its version CAS. The tombstone is the
            // live entity with deleted=true, so its content hash is preserved.
            return pushTombstone(d, r.entity!, b.remoteVersion)
          }
          if (kind === 'note') {
            // Local absent, remote edited live: preserve the remote edit as a
            // deterministic conflict entity, then tombstone the original with
            // its current version.
            return createConflictRemote(d, l, r, rHash)
          }
          return block(d, 'folder absent vs divergent remote live')
        }
        case 'tombstone':
          // Local absent + remote tombstone: the deletion has converged. When
          // the baseline is already deleted and matches, nothing to do;
          // otherwise record the deleted baseline. The coordinator removes the
          // live path mapping.
          if (b.deleted && b.contentHash === r.entity!.contentHash) {
            return noop(d, 'converged deletion')
          }
          return establishBaseline(d, r.entity!.contentHash, true, r.version ?? '')
        case 'missing':
          // handled in decideEntity before this branch.
          return block(d, 'no matching rule')
        case 'invalid':
          return block(d, 'invalid remote record')
      }
      break
    case 'unknown':
      return block(d, 'local unknown (blocked/unstable/unreadable)')
  }
  return block(d, 'no matching rule')
}

// --- decision builders ------------------------------------------------------

function noop(d: Decision, reason: string): Decision {
  return { ...d, kind: 'noop', reason }
}

function establishBaseline(d: Decision, contentHash: string, deleted: boolean, version: string): Decision {
  return { ...d, kind: 'establish-baseline', contentHash, deleted, version, reason: 'local and remote known equal' }
}

function pullLive(d: Decision, entity: Entity, version: string, localRevision = ''): Decision {
  return {
    ...d,
    kind: 'pull-live',
    entity,
    contentHash: entity.contentHash,
    version,
    localRevision,
    reason: 'remote changed',
  }
}

function pushLive(d: Decision, entity: Entity, version: string, localRevision: string): Decision {
  return {
    ...d,
    kind: 'push-live',
    entity,
    contentHash: entity.contentHash,
    version,
    localRevision,
    reason: version ? 'local changed' : 'local-only; create remote if-absent',
  }
}

function pushTombstone(d: Decision, live: Entity, version: string): Decision {
  // The tombstone is the live entity with deleted=true, so its content hash
  // (over kind/parent/name/markdown) is preserved and the record stays valid.
  const entity: Entity = { ...live, deleted: true }
  return {
    ...d,
    kind: 'push-tombstone',
    entity,
    contentHash: entity.contentHash,
    deleted: true,
    version,
    localRevision: '',
    reason: 'locally deleted; replace remote with tombstone',
  }
}

function applyTombstone(d: Decision, version: string, localRevision: string): Decision {
  return {
    ...d,
    kind: 'apply-tombstone',
    deleted: true,
    version,
    localRevision,
    reason: 'remote tombstone; write recovery and delete locally',
  }
}

function repairIndex(d: Decision, reason: string): Decision {
  return { ...d, kind: 'repair-index', reason }
}

function block(d: Decision, reason: string): Decision {
  return { ...d, kind: 'block', reason }
}

function retry(d: Decision, reason: string): Decision {
  return { ...d, kind: 'retry', reason }
}

/** Preserves the LOCAL side as the conflict entity for a divergent note. */
function createConflict(
  d: Decision,
  l: LocalObservation,
  r: RemoteObservation,
  originalTombstone: boolean,
  originalVersion: string,
): Decision {
  const lHash = entityHash(l.entity)
  const rHash = entityHash(r.entity)
  const localStateHash = stateHash(lHash, false)
  const remoteStateHash = stateHash(rHash, r.state === 'tombstone')
  const conflictSyncId = deriveConflictSyncId(d.syncId, localStateHash, remoteStateHash)
  const conflictEntity = cloneAsConflict(l.entity!, conflictSyncId)
  return {
    ...d,
    kind: 'create-conflict',
    contentHash: conflictEntity.contentHash,
    reason: 'divergent edits; keep both via a deterministic conflict copy',
    conflict: {
      sourceSyncId: d.syncId,
      conflictSyncId,
      conflictEntity,
      originalTombstone,
      originalVersion,
      acceptRemoteOriginal: !originalTombstone,
      originalEntity: originalTombstone ? undefined : r.entity,
      localStateHash,
      remoteStateHash,
    },
  }
}

/** Preserves the REMOTE live side as the conflict entity (local absent vs
 * remote edited live), then tombstones the original with its current version. */
function createConflictRemote(d: Decision, l: LocalObservation, r: RemoteObservation, rHash: string): Decision {
  const lHash = entityHash(l.entity)
  const localStateHash = stateHash(lHash, true) // local absent side is deleted
  const remoteStateHash = stateHash(rHash, false)
  const conflictSyncId = deriveConflictSyncId(d.syncId, localStateHash, remoteStateHash)
  const conflictEntity = cloneAsConflict(r.entity!, conflictSyncId)
  const originalTombstoneEntity: Entity = { ...r.entity!, deleted: true }
  return {
    ...d,
    kind: 'create-conflict',
    contentHash: conflictEntity.contentHash,
    reason: 'local absent vs remote edit; keep remote edit as conflict, tombstone original',
    conflict: {
      sourceSyncId: d.syncId,
      conflictSyncId,
      conflictEntity,
      originalTombstone: true,
      originalVersion: r.version ?? '',
      acceptRemoteOriginal: false,
      originalTombstoneEntity,
      localStateHash,
      remoteStateHash,
    },
  }
}

/** Renames a note copy into a deterministic conflict entity while preserving
 * its parent, kind, markdown, and attribution. */
function cloneAsConflict(src: Entity, conflictSyncId: string): Entity {
  const cp: Entity = { ...src, syncId: conflictSyncId, deleted: false }
  cp.name = conflictFilename(src.name, conflictSyncId).replace(/\.md$/, '')
  cp.contentHash = computeContentHash(cp)
  return cp
}

// --- repository-wide planning order ------------------------------------------

const LIVE_KINDS = new Set(['pull-live', 'push-live', 'establish-baseline'])
const TOMBSTONE_KINDS = new Set(['push-tombstone', 'apply-tombstone'])
const BOOKKEEPING_KINDS = new Set(['noop', 'repair-index'])
const BLOCKED_KINDS = new Set(['block', 'retry'])

/**
 * Orders the per-entity decisions repository-wide: conflict creation before any
 * destructive action on the original, parents before live children,
 * tombstones child-first, bookkeeping after actions, and blocks/retries last.
 * The result is deterministic so Go and TypeScript emit the identical sequence.
 */
export function decideRepository(decisions: Decision[]): Decision[] {
  const conflicts: Decision[] = []
  const live: Decision[] = []
  const tombstones: Decision[] = []
  const bookkeeping: Decision[] = []
  const blocked: Decision[] = []
  for (const d of decisions) {
    if (d.kind === 'create-conflict') conflicts.push(d)
    else if (LIVE_KINDS.has(d.kind)) live.push(d)
    else if (TOMBSTONE_KINDS.has(d.kind)) tombstones.push(d)
    else if (BOOKKEEPING_KINDS.has(d.kind)) bookkeeping.push(d)
    else if (BLOCKED_KINDS.has(d.kind)) blocked.push(d)
  }
  const out: Decision[] = []
  out.push(...stableParentsFirst(conflicts))
  out.push(...stableParentsFirst(live))
  out.push(...stableChildrenFirst(tombstones))
  out.push(...stableParentsFirst(bookkeeping))
  out.push(...stableBySyncId(blocked))
  return out
}

/** Moves every node whose parent appears in the slice before it (stable). */
function stableParentsFirst(inp: Decision[]): Decision[] {
  if (inp.length < 2) return inp
  const out = [...inp]
  const byId = new Map<string, number>()
  out.forEach((d, i) => byId.set(d.syncId, i))
  let changed = true
  while (changed) {
    changed = false
    for (let i = 0; i < out.length; i++) {
      const pid = out[i].parentId
      if (!pid) continue
      const j = byId.get(pid)
      if (j === undefined || j < i) continue
      const v = out.splice(i, 1)[0]
      out.splice(j, 0, v)
      for (let k = j; k <= i; k++) byId.set(out[k].syncId, k)
      changed = true
      break
    }
  }
  return out
}

/** stableParentsFirst in reverse: children precede their parents. */
function stableChildrenFirst(inp: Decision[]): Decision[] {
  if (inp.length < 2) return inp
  const reversed = [...inp].reverse()
  const out = stableParentsFirst(reversed)
  return out.reverse()
}

function stableBySyncId(inp: Decision[]): Decision[] {
  return [...inp].sort((a, b) => (a.syncId < b.syncId ? -1 : a.syncId > b.syncId ? 1 : 0))
}

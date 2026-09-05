// The pure per-note reconciliation decision for the browser sync port (R6.1).
// This is a faithful port of internal/cloudsync/reconcile_note.go (spec §7's
// known-baseline and no-baseline tables) with the fixed outcome set and the
// deterministic conflict preservation plan. It performs no IndexedDB, Vue,
// timer, or network I/O; it is async only because Web Crypto's SHA-256 (state
// hashes, tombstone content hashes) and SHA-1 (UUID v5 conflict identity) are
// async — the derivation bytes are identical to the Go engine.
import { canonicalSha256 } from './canonical.js'
import { computeContentHash, NOTE_SCHEMA_VERSION } from './note.js'
import { deriveConflictSyncID } from './uuid.js'
import { conflictPath } from './paths.js'

export const LocalState = Object.freeze({ LIVE: 'live', ABSENT: 'absent', UNKNOWN: 'unknown' })
export const RemoteState = Object.freeze({ LIVE: 'live', TOMBSTONE: 'tombstone', MISSING: 'missing', INVALID: 'invalid' })

// NoteDecisionKind is the fixed outcome set (spec §7/§8). The compound
// preservation outcomes are named in execution order: preserve the divergent
// side as a deterministic conflict note FIRST, then act on the original.
export const NoteDecisionKind = Object.freeze({
  NOOP: 'noop',
  ESTABLISH_BASELINE: 'establish_baseline',
  PUSH_LIVE: 'push_live',
  PULL_LIVE: 'pull_live',
  PUSH_TOMBSTONE: 'push_tombstone',
  APPLY_TOMBSTONE: 'apply_tombstone',
  PRESERVE_LOCAL_THEN_PULL: 'preserve_local_then_pull',
  PRESERVE_LOCAL_THEN_DELETE: 'preserve_local_then_delete',
  PRESERVE_REMOTE_THEN_TOMBSTONE: 'preserve_remote_then_tombstone',
  BLOCK: 'block',
  RETRY: 'retry',
})

// stateHash is the canonical digest of a note's complete state: the tuple
// (contentHash, deleted). Two states are equal only when both fields match.
export async function stateHash(contentHash, deleted) {
  return canonicalSha256({ contentHash, deleted })
}

async function noteTombstoneHash(syncId, path) {
  return computeContentHash({
    schemaVersion: NOTE_SCHEMA_VERSION,
    syncId,
    path,
    markdown: '',
    deleted: true,
  })
}

function emptyDecision(syncId) {
  return {
    syncId,
    kind: null,
    reason: '',
    contentHash: '',
    deleted: false,
    path: '',
    markdown: '',
    version: '',
    localRevision: '',
    conflict: null,
  }
}

// decideNote computes the normalized decision for one Sync ID from its immutable
// local observation, remote observation, optional usable baseline, and the
// precomputed path-conflict flag. A path conflict, unknown local state, and
// invalid remote data always produce block/retry, never a deletion.
//
//   local:  { syncId, state, path, markdown, contentHash, revision }
//   remote: { syncId, state, path, markdown, contentHash, version, retryable }
//   baseline: { contentHash, deleted, remoteVersion } | null
export async function decideNote({ local, remote, baseline, pathConflict }) {
  let d = emptyDecision(local.syncId || remote.syncId)

  if (pathConflict) return block(d, 'path conflict')
  if (local.state === LocalState.UNKNOWN) return block(d, 'local unknown (blocked/unstable/unreadable)')
  if (remote.state === RemoteState.INVALID) {
    if (remote.retryable) return retry(d, 'invalid remote record, retryable')
    return block(d, 'invalid remote record')
  }

  // A physically missing remote object is repository damage, never a tombstone.
  if (remote.state === RemoteState.MISSING) {
    if (baseline != null) return block(d, 'remote record physically missing though a baseline expected it')
    if (local.state === LocalState.LIVE) return pushLive(d, local, '', local.revision)
    return block(d, 'indexed local absence plus physically absent remote object is ambiguous')
  }

  if (baseline == null) return decideNoteNoBaseline(d, local, remote)
  return decideNoteWithBaseline(d, local, remote, baseline)
}

async function decideNoteNoBaseline(d, local, remote) {
  switch (local.state) {
    case LocalState.ABSENT:
      switch (remote.state) {
        case RemoteState.LIVE:
          return pullLive(d, remote, '', remote.version)
        case RemoteState.TOMBSTONE:
          return establishBaseline(d, remote.contentHash, true, remote.version)
      }
      break
    case LocalState.LIVE:
      switch (remote.state) {
        case RemoteState.LIVE:
          if (local.contentHash === remote.contentHash) {
            return establishBaseline(d, local.contentHash, false, remote.version)
          }
          return preserveLocalThenPull(d, local, remote)
        case RemoteState.TOMBSTONE:
          return preserveLocalThenDelete(d, local, remote)
      }
      break
  }
  return block(d, 'no usable baseline and no matching rule')
}

async function decideNoteWithBaseline(d, local, remote, b) {
  switch (local.state) {
    case LocalState.LIVE:
      switch (remote.state) {
        case RemoteState.LIVE:
          if (local.contentHash === remote.contentHash) {
            // L == R: refresh the baseline; noop only when the baseline already
            // holds the same content AND the current remote version.
            if (!b.deleted && b.contentHash === local.contentHash && b.remoteVersion === remote.version) {
              return noop(d, 'local and remote unchanged')
            }
            return establishBaseline(d, local.contentHash, false, remote.version)
          }
          if (b.deleted) {
            // Recreated over a deleted baseline; the sides diverge.
            return preserveLocalThenPull(d, local, remote)
          }
          if (local.contentHash === b.contentHash) {
            return pullLive(d, remote, local.revision, remote.version)
          }
          if (remote.contentHash === b.contentHash) {
            return pushLive(d, local, remote.version, local.revision)
          }
          return preserveLocalThenPull(d, local, remote)
        case RemoteState.TOMBSTONE:
          if (b.deleted) {
            if (remote.contentHash === b.contentHash) {
              return pushLive(d, local, remote.version, local.revision)
            }
            return preserveLocalThenDelete(d, local, remote)
          }
          if (local.contentHash === b.contentHash) {
            return applyTombstone(d, remote, local.revision)
          }
          return preserveLocalThenDelete(d, local, remote)
      }
      break
    case LocalState.ABSENT:
      switch (remote.state) {
        case RemoteState.LIVE:
          if (b.deleted) {
            return pullLive(d, remote, '', remote.version)
          }
          if (remote.contentHash === b.contentHash) {
            return pushTombstone(d, remote, remote.version)
          }
          return preserveRemoteThenTombstone(d, local, remote, b)
        case RemoteState.TOMBSTONE:
          if (b.deleted && b.contentHash === remote.contentHash && b.remoteVersion === remote.version) {
            return noop(d, 'converged deletion')
          }
          return establishBaseline(d, remote.contentHash, true, remote.version)
      }
      break
  }
  return block(d, 'no matching rule')
}

// --- decision builders -------------------------------------------------------

function noop(d, reason) {
  d.kind = NoteDecisionKind.NOOP
  d.reason = reason
  return d
}

function establishBaseline(d, hash, deleted, version) {
  d.kind = NoteDecisionKind.ESTABLISH_BASELINE
  d.contentHash = hash
  d.deleted = deleted
  d.version = version
  d.reason = 'local and remote known equal'
  return d
}

function pushLive(d, local, version, localRevision) {
  d.kind = NoteDecisionKind.PUSH_LIVE
  d.contentHash = local.contentHash
  d.path = local.path
  d.markdown = local.markdown
  d.version = version // '' = create-if-absent
  d.localRevision = localRevision
  d.reason = version === '' ? 'local-only; create remote if-absent' : 'local changed'
  return d
}

function pullLive(d, remote, localRevision, version) {
  d.kind = NoteDecisionKind.PULL_LIVE
  d.contentHash = remote.contentHash
  d.path = remote.path
  d.markdown = remote.markdown
  d.version = version
  d.localRevision = localRevision
  d.reason = 'remote changed'
  return d
}

async function pushTombstone(d, remote, version) {
  d.kind = NoteDecisionKind.PUSH_TOMBSTONE
  d.deleted = true
  d.contentHash = await noteTombstoneHash(d.syncId, remote.path)
  d.path = remote.path
  d.version = version
  d.reason = 'locally deleted; replace remote with tombstone'
  return d
}

function applyTombstone(d, remote, localRevision) {
  d.kind = NoteDecisionKind.APPLY_TOMBSTONE
  d.deleted = true
  d.path = remote.path
  d.contentHash = remote.contentHash
  d.version = remote.version
  d.localRevision = localRevision
  d.reason = 'remote tombstone; write recovery and delete locally'
  return d
}

function block(d, reason) {
  d.kind = NoteDecisionKind.BLOCK
  d.reason = reason
  return d
}

function retry(d, reason) {
  d.kind = NoteDecisionKind.RETRY
  d.reason = reason
  return d
}

function conflictInfo(sourceSyncId, conflictSyncId, conflictPath, conflictMarkdown, localStateHash, remoteStateHash, opts = {}) {
  return {
    sourceSyncId,
    conflictSyncId,
    conflictPath,
    conflictMarkdown,
    localStateHash,
    remoteStateHash,
    originalTombstone: opts.originalTombstone || false,
    originalVersion: opts.originalVersion || '',
  }
}

async function preserveLocalThenPull(d, local, remote) {
  const lState = await stateHash(local.contentHash, false)
  const rState = await stateHash(remote.contentHash, false)
  const conflictSyncId = await deriveConflictSyncID(d.syncId, lState, rState)
  d.kind = NoteDecisionKind.PRESERVE_LOCAL_THEN_PULL
  d.contentHash = remote.contentHash
  d.path = remote.path
  d.markdown = remote.markdown
  d.version = remote.version
  d.localRevision = local.revision
  d.conflict = conflictInfo(d.syncId, conflictSyncId, conflictPath(local.path, conflictSyncId), local.markdown, lState, rState)
  d.reason = 'divergent edits; preserve local as conflict, accept remote at original'
  return d
}

async function preserveLocalThenDelete(d, local, remote) {
  const lState = await stateHash(local.contentHash, false)
  const rState = await stateHash(remote.contentHash, true)
  const conflictSyncId = await deriveConflictSyncID(d.syncId, lState, rState)
  d.kind = NoteDecisionKind.PRESERVE_LOCAL_THEN_DELETE
  d.deleted = true
  d.path = remote.path
  d.contentHash = remote.contentHash
  d.version = remote.version
  d.localRevision = local.revision
  d.conflict = conflictInfo(d.syncId, conflictSyncId, conflictPath(local.path, conflictSyncId), local.markdown, lState, rState, { originalTombstone: true })
  d.reason = 'local edit versus remote tombstone; preserve local as conflict, accept deletion'
  return d
}

async function preserveRemoteThenTombstone(d, local, remote, b) {
  // The local absent side is a deletion of the last-known baseline content.
  const lState = await stateHash(b.contentHash, true)
  const rState = await stateHash(remote.contentHash, false)
  const conflictSyncId = await deriveConflictSyncID(d.syncId, lState, rState)
  d.kind = NoteDecisionKind.PRESERVE_REMOTE_THEN_TOMBSTONE
  d.deleted = true
  d.contentHash = await noteTombstoneHash(d.syncId, remote.path)
  d.path = remote.path
  d.version = remote.version
  d.conflict = conflictInfo(d.syncId, conflictSyncId, conflictPath(remote.path, conflictSyncId), remote.markdown, lState, rState, {
    originalTombstone: true,
    originalVersion: remote.version,
  })
  d.reason = 'local absent versus remote edit; preserve remote as conflict, tombstone original'
  return d
}

// isConvergedDeletion reports whether a noop decision is a fully-converged
// deletion: local absent, remote tombstone, and a matching deleted baseline.
export function isConvergedDeletion(d) {
  return d.kind === NoteDecisionKind.NOOP && d.reason === 'converged deletion'
}

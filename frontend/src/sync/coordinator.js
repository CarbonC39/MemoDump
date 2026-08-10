// R6.4 serialized browser sync cycle. One coordinator mirrors the reviewed Go
// cycle (internal/syncrun) without copying its filesystem machinery: acquire
// the exclusive Web Lock, validate connection/provider/repository, enumerate
// IndexedDB notes once, assign missing IDs atomically, load the disposable
// snapshot, fully list/read the remote, build the union by Sync ID, detect
// portable path collisions, call the pure R6.1 decision function in sorted ID
// order, execute conditionally (remote CAS writes, local revision-CAS writes,
// deterministic conflict reservations, recovery-before-delete), commit one
// consolidated snapshot, and release the lock.
//
// Cancellation is checked between notes; a remote precondition/uncertain
// response re-reads that key; a local CAS loser survives for the next cycle.
// observeAndDecide and executeDecisions are exported separately so a test can
// drive a dirty-editor race between the observation and the execution.

import {
  loadIdentity, loadConnection, loadSnapshot, replaceSnapshot,
  loadSyncIndex, assignMissingSyncIds, removeIndexEntry, reserveConflictNote,
  applyNoteMutation, writeRecovery, SyncStateError, SNAPSHOT_SCHEMA_VERSION,
} from '../storage/syncDb.js'
import { allOf, getNoteRec } from '../storage/localVaultDb.js'
import { StoreError } from './s3store.js'
import { normalizeMarkdown } from './markdown.js'
import {
  NOTE_SCHEMA_VERSION, NOTE_KEY_PREFIX, noteKey, parseNoteKey,
  computeContentHash, serializeNoteRecord, parseNoteRecord, validNotePath,
} from './note.js'
import { parseRepositoryDescriptor } from './repo.js'
import { decideNote, stateHash, isConvergedDeletion } from './decision.js'
import { portablePathKey } from './paths.js'
import { utf8Bytes } from './hash.js'

export const LocalState = Object.freeze({ LIVE: 'live', ABSENT: 'absent', UNKNOWN: 'unknown' })
export const RemoteState = Object.freeze({ LIVE: 'live', TOMBSTONE: 'tombstone', MISSING: 'missing', INVALID: 'invalid' })

const PRESERVE_KINDS = new Set([
  'preserve_local_then_pull', 'preserve_local_then_delete', 'preserve_remote_then_tombstone',
])

const FATAL_STORE_KINDS = new Set([
  'auth', 'permission', 'quota', 'unsupported-capability', 'invalid-response', 'incomplete-list',
])

function runError(code, message) {
  const e = new Error(message)
  e.code = code
  return e
}

function abortError() {
  return new DOMException('The operation was aborted.', 'AbortError')
}

function isRetryableStoreError(e) {
  return e instanceof StoreError && (e.kind === 'retryable-transport' || e.kind === 'rate-limit')
}

// withReplicaLock serializes one replica's cycles with the exclusive Web Lock
// scoped to the vault. ifAvailable:true means a second Run now is REFUSED
// (locked), never queued. A browser without Web Locks cannot run sync.
async function withReplicaLock(vaultId, fn, locks) {
  if (!locks) throw runError('unsupported-lock', 'Web Locks are unavailable')
  return locks.request(`memodump-sync-${vaultId}`, { ifAvailable: true }, (lock) => {
    if (!lock) throw runError('locked', 'another sync cycle holds the replica lock')
    return fn()
  })
}

// runSyncCycle runs one full serialized cycle for the current replica. store is
// the RemoteStore (S3Store) the enable flow configured. locks defaults to
// navigator.locks and is injectable for tests. signal carries cancellation.
export async function runSyncCycle(store, { locks = globalThis.navigator?.locks, signal } = {}) {
  const identity = await loadIdentity()
  if (!identity) throw runError('not-enabled', 'sync is not enabled')
  return withReplicaLock(identity.vaultId, async () => {
    const state = await observeAndDecide(store, { signal })
    const { deferred } = await executeDecisions(store, state.decisions, state.baselines, { index: state.index, signal })
    await commitSnapshot(state)
    const blocked = state.decisions.filter((d) => d.kind === 'block').length
    const retry = state.decisions.filter((d) => d.kind === 'retry').length + deferred
    return {
      scanned: state.decisions.length,
      blocked,
      retry,
      conflicts: state.decisions.filter((d) => PRESERVE_KINDS.has(d.kind)).length,
      snapshotCommitted: true,
      decisions: state.decisions,
    }
  }, locks)
}

// validateRepository re-checks the strict connection pin inside the lock: the
// provider fingerprint must equal the pinned profile, and repo.json must exist
// and carry the pinned Repository ID. A lost or changed repository is never
// reinterpreted as a new empty repository.
async function validateRepository(store, connection, signal) {
  const profile = await store.profile()
  if (profile !== connection.providerProfile) {
    throw runError('profile-mismatch', 'provider profile mismatch')
  }
  let data
  try {
    ;({ data } = await store.read('repo.json', { signal }))
  } catch (e) {
    if (e instanceof StoreError && e.kind === 'not-found') {
      throw runError('repository-loss', 'repo.json is missing though sync is connected')
    }
    throw e
  }
  let repo
  try {
    repo = parseRepositoryDescriptor(data)
  } catch (e) {
    throw new Error(`invalid repo.json: ${e.message}`)
  }
  if (repo.repositoryId !== connection.repositoryId) {
    throw runError('repository-mismatch', 'repository ID mismatch')
  }
  return repo
}

// observeAndDecide reads the durable local state and the complete remote, then
// produces the immutable decision plan. It performs no writes except the atomic
// ID assignment for newly discovered notes.
export async function observeAndDecide(store, { signal } = {}) {
  const identity = await loadIdentity()
  if (!identity) throw runError('not-enabled', 'sync is not enabled')
  const connection = await loadConnection()
  if (!connection || !connection.connected) throw runError('not-enabled', 'sync is not enabled')

  const repo = await validateRepository(store, connection, signal)
  const expected = {
    vaultId: identity.vaultId,
    replicaId: identity.replicaId,
    providerProfile: connection.providerProfile,
    repositoryId: repo.repositoryId,
  }

  // Assign durable identities to definite new notes BEFORE any upload.
  await assignMissingSyncIds()
  const index = await loadSyncIndex()
  const notes = await allOf('notes')

  // Load the disposable snapshot; a missing/corrupt snapshot means conservative
  // onboarding with no baseline. A profile/repository mismatch stops.
  const { reason, snapshot } = await loadSnapshot(expected)
  if (reason === 'provider-profile-mismatch' || reason === 'repository-id-mismatch') {
    throw runError('snapshot-mismatch', `snapshot ${reason}; requires explicit reconnect or re-enable`)
  }
  const baselines = snapshot ? { ...snapshot.notes } : {}

  // A complete remote listing; an incomplete listing stops before any decision.
  const listed = await store.list(NOTE_KEY_PREFIX, { signal })
  const keys = new Set()
  for (const e of listed) {
    if (parseNoteKey(e.key)) keys.add(e.key)
  }

  const unionIds = unionOf(index, baselines, keys)
  const localObs = await buildLocalObservations(notes, index, unionIds)
  const remoteObs = await buildRemoteObservations(store, keys, unionIds, signal)
  const blockedPaths = pathConflicts(localObs, remoteObs)

  const decisions = []
  for (const id of unionIds) {
    decisions.push(await decideNote({
      local: localObs[id],
      remote: remoteObs[id],
      baseline: baselines[id] || null,
      pathConflict: blockedPaths.has(id),
    }))
  }
  return { identity, connection, repo, index, baselines, unionIds, decisions }
}

// unionOf returns the sorted set of Sync IDs in this cycle: everything in the
// index, every snapshot baseline, and every listed remote note key.
function unionOf(index, baselines, keys) {
  const set = new Set()
  for (const id of Object.keys(index)) set.add(id)
  for (const id of Object.keys(baselines)) set.add(id)
  for (const key of keys) {
    const id = parseNoteKey(key)
    if (id) set.add(id)
  }
  return [...set].sort()
}

// buildLocalObservations derives the immutable local observation per union Sync
// ID from the note records and the index. An unindexed id is absent; an
// unrepresentable path is unknown (never absent); a present indexed note is
// read fresh and given its canonical content hash over LF-normalized Markdown.
async function buildLocalObservations(notes, index, unionIds) {
  const byPath = new Map()
  for (const n of notes) byPath.set(n.path, n)
  const obs = {}
  for (const id of unionIds) {
    const path = index[id]
    if (path === undefined) {
      obs[id] = { syncId: id, state: LocalState.ABSENT }
      continue
    }
    if (!validNotePath(path)) {
      obs[id] = { syncId: id, state: LocalState.UNKNOWN, path }
      continue
    }
    const rec = byPath.get(path)
    if (!rec) {
      obs[id] = { syncId: id, state: LocalState.ABSENT, path }
      continue
    }
    const markdown = normalizeMarkdown(rec.markdown)
    const contentHash = await computeContentHash({ schemaVersion: NOTE_SCHEMA_VERSION, syncId: id, path, markdown, deleted: false })
    obs[id] = { syncId: id, state: LocalState.LIVE, path, markdown, contentHash, revision: rec.revision }
  }
  return obs
}

// buildRemoteObservations reads and parses every listed note record. A key
// absent from the listing is missing — never a tombstone. A retryable read
// error becomes a retryable invalid observation for that note only; every other
// failure stops the cycle before any execution.
async function buildRemoteObservations(store, keys, unionIds, signal) {
  const obs = {}
  for (const id of unionIds) {
    const key = noteKey(id)
    if (!keys.has(key)) {
      obs[id] = { syncId: id, state: RemoteState.MISSING }
      continue
    }
    let data, version
    try {
      ;({ data, version } = await store.read(key, { signal }))
    } catch (e) {
      if (isRetryableStoreError(e)) {
        obs[id] = { syncId: id, state: RemoteState.INVALID, retryable: true }
        continue
      }
      throw e
    }
    let rec
    try {
      rec = parseNoteRecord(data)
    } catch (e) {
      throw new Error(`invalid remote record ${key}: ${e.message}`)
    }
    if (rec.syncId !== id) throw new Error(`remote record ${key} declares syncId "${rec.syncId}"`)
    const contentHash = await computeContentHash(rec)
    obs[id] = {
      syncId: id,
      state: rec.deleted ? RemoteState.TOMBSTONE : RemoteState.LIVE,
      path: rec.path,
      markdown: rec.markdown,
      contentHash,
      version,
    }
  }
  return obs
}

// pathConflicts returns the Sync IDs blocked by a portable path collision: two
// LIVE notes with DIFFERENT Sync IDs whose portable paths collide. The same Sync
// ID appearing both locally and remotely is one note, not a conflict.
function pathConflicts(localObs, remoteObs) {
  const byKey = new Map()
  const add = (id, path) => {
    if (!path) return
    const key = portablePathKey(path)
    if (!byKey.has(key)) byKey.set(key, new Set())
    byKey.get(key).add(id)
  }
  for (const [id, l] of Object.entries(localObs)) {
    if (l.state === LocalState.LIVE) add(id, l.path)
  }
  for (const [id, r] of Object.entries(remoteObs)) {
    if (r.state === RemoteState.LIVE) add(id, r.path)
  }
  const blocked = new Set()
  for (const ids of byKey.values()) {
    if (ids.size > 1) for (const id of ids) blocked.add(id)
  }
  return blocked
}

// executeDecisions applies the plan serially. baselines is mutated in place for
// final known-equal states and returned so the caller can commit the snapshot.
// The returned deferred count is the number of notes not converged (a raced
// local write, a stale remote CAS, a retryable failure).
export async function executeDecisions(store, decisions, baselines, { index, signal } = {}) {
  const ctx = { index }
  let deferred = 0
  for (const d of decisions) {
    if (signal && signal.aborted) throw abortError()
    if (d.kind === 'block' || d.kind === 'retry') continue
    deferred += await executeOne(store, d, baselines, ctx, signal)
  }
  return { deferred, baselines }
}

async function executeOne(store, d, baselines, ctx, signal) {
  switch (d.kind) {
    case 'noop':
      if (isConvergedDeletion(d)) await removeIndexEntry(d.syncId)
      return 0
    case 'establish_baseline':
      baselines[d.syncId] = { contentHash: d.contentHash, deleted: d.deleted, remoteVersion: d.version }
      return 0
    case 'push_live':
      return (await pushLive(store, d, baselines, signal)) ? 0 : 1
    case 'pull_live':
      return (await pullLive(d, baselines, ctx)) ? 0 : 1
    case 'push_tombstone':
      return (await pushTombstone(store, d, baselines, signal)) ? 0 : 1
    case 'apply_tombstone':
      return (await applyTombstone(d, baselines, ctx)) ? 0 : 1
    case 'preserve_local_then_pull':
    case 'preserve_local_then_delete':
    case 'preserve_remote_then_tombstone':
      return executeConflict(store, d, baselines, ctx, signal)
    default:
      return 0
  }
}

// pushLive conditionally uploads the local note (create-if-absent or replace at
// the current version). On any precondition/retryable failure the key is
// re-read so the outcome is never guessed: an identical landed write is
// established at the actual version; anything else defers. A fatal store error
// stops the cycle.
async function pushLive(store, d, baselines, signal) {
  const data = utf8Bytes(serializeNoteRecord({
    schemaVersion: NOTE_SCHEMA_VERSION, syncId: d.syncId, path: d.path, markdown: d.markdown, deleted: false,
  }))
  const key = noteKey(d.syncId)
  let version
  try {
    version = d.version === ''
      ? await store.create(key, data, { signal })
      : await store.replace(key, data, d.version, { signal })
  } catch (e) {
    if (e instanceof StoreError && (e.kind === 'precondition-failed' || isRetryableStoreError(e))) {
      return confirmLiveWrite(store, key, d, baselines, signal)
    }
    throw e
  }
  baselines[d.syncId] = { contentHash: d.contentHash, deleted: false, remoteVersion: version }
  return true
}

async function confirmLiveWrite(store, key, d, baselines, signal) {
  const landed = await confirmWrite(store, key, signal, (rec) => !rec.deleted && rec.path === d.path && rec.markdown === d.markdown)
  if (landed == null) return false
  baselines[d.syncId] = { contentHash: d.contentHash, deleted: false, remoteVersion: landed }
  return true
}

// pushTombstone replaces a remote live note with a tombstone at the current
// version CAS, re-reading to learn the true outcome.
async function pushTombstone(store, d, baselines, signal) {
  const data = utf8Bytes(serializeNoteRecord({
    schemaVersion: NOTE_SCHEMA_VERSION, syncId: d.syncId, path: d.path, deleted: true,
  }))
  const key = noteKey(d.syncId)
  let version
  try {
    version = await store.replace(key, data, d.version, { signal })
  } catch (e) {
    if (e instanceof StoreError && (e.kind === 'precondition-failed' || isRetryableStoreError(e))) {
      const landed = await confirmWrite(store, key, signal, (rec) => rec.deleted && rec.path === d.path)
      if (landed == null) return false
      version = landed
    } else {
      throw e
    }
  }
  baselines[d.syncId] = { contentHash: d.contentHash, deleted: true, remoteVersion: version }
  return true
}

// confirmWrite re-reads a key after a conditional-write failure to learn the
// outcome without guessing. A record matching want is an idempotent success at
// the actual version (returned); a missing key, a retryable transport error, or
// a non-matching landed state defers (null); a fatal error, malformed record, or
// syncId mismatch stops the cycle.
async function confirmWrite(store, key, signal, want) {
  let data, version
  try {
    ;({ data, version } = await store.read(key, { signal }))
  } catch (e) {
    if (e instanceof StoreError && e.kind === 'not-found') return null
    if (e instanceof StoreError && isRetryableStoreError(e)) return null
    throw e
  }
  let rec
  try {
    rec = parseNoteRecord(data)
  } catch (e) {
    throw e
  }
  const id = parseNoteKey(key)
  if (rec.syncId !== id) throw new Error(`remote record ${key} declares syncId "${rec.syncId}"`)
  return want(rec) ? version : null
}

// pullLive materializes the remote note locally with the observed local
// revision CAS (create-if-absent when the note is remote-only or locally
// absent). An in-app path change only applies when a LIVE local note sits at
// the indexed path and the remote moved it — deleting an already-absent note
// would otherwise hand applyNoteMutation an empty revision. An editor racing
// the pull wins: the CAS failure defers the note.
async function pullLive(d, baselines, ctx) {
  const indexedPath = ctx.index[d.syncId]
  const localRec = indexedPath ? await getNoteRec(indexedPath) : null
  const moving = Boolean(indexedPath && indexedPath !== d.path && localRec)
  const res = moving
    ? await applyNoteMutation({
        mode: 'path-change', syncId: d.syncId, oldPath: indexedPath, path: d.path,
        markdown: d.markdown, expectedRevision: d.localRevision,
      })
    : await applyNoteMutation({
        mode: 'pull', syncId: d.syncId, path: d.path, markdown: d.markdown,
        expectedRevision: indexedPath ? d.localRevision : '',
      })
  if (!res.applied) return false
  baselines[d.syncId] = { contentHash: d.contentHash, deleted: false, remoteVersion: d.version }
  return true
}

// applyTombstone writes a durable recovery copy of the local note BEFORE the
// local revision-CAS delete. A recovery failure or a stale revision leaves the
// note intact and its baseline unchanged.
async function applyTombstone(d, baselines, ctx) {
  const path = ctx.index[d.syncId]
  if (!path) return false
  const rec = await getNoteRec(path)
  if (!rec) {
    // Already gone locally; the tombstone is converged at the note level.
    baselines[d.syncId] = { contentHash: d.contentHash, deleted: true, remoteVersion: d.version }
    return true
  }
  const markdown = normalizeMarkdown(rec.markdown)
  const contentHash = await computeContentHash({ schemaVersion: NOTE_SCHEMA_VERSION, syncId: d.syncId, path, markdown, deleted: false })
  await writeRecovery(d.syncId, await stateHash(contentHash, false), path, rec.markdown)
  const res = await applyNoteMutation({ mode: 'delete', syncId: d.syncId, path, expectedRevision: d.localRevision })
  if (!res.applied) return false
  baselines[d.syncId] = { contentHash: d.contentHash, deleted: true, remoteVersion: d.version }
  return true
}

// executeConflict applies one compound preservation decision in the fixed
// spec-§8 order: reserve and durably save the conflict ID/path, create and
// verify the local conflict note, create and verify the remote conflict record,
// and only then act on the original. The returned count is the number of
// original-path outcomes deferred to the next cycle.
async function executeConflict(store, d, baselines, ctx, signal) {
  const conf = d.conflict
  if (!conf || !conf.conflictSyncId || !conf.conflictPath) {
    throw new Error(`note ${d.syncId}: missing conflict plan`)
  }

  // 1. Reserve the conflict identity/path (replay-safe).
  await reserveConflictNote(conf.conflictSyncId, conf.conflictPath, conf.conflictMarkdown)

  // 2. Create/verify the remote conflict record (create-if-absent, idempotent).
  const rec = {
    schemaVersion: NOTE_SCHEMA_VERSION, syncId: conf.conflictSyncId,
    path: conf.conflictPath, markdown: conf.conflictMarkdown, deleted: false,
  }
  const data = utf8Bytes(serializeNoteRecord(rec))
  const key = noteKey(conf.conflictSyncId)
  let conflictVersion
  try {
    conflictVersion = await store.create(key, data, { signal })
  } catch (e) {
    if (e instanceof StoreError && e.kind === 'precondition-failed') {
      // Re-read to learn the true outcome: an identical conflict record is an
      // idempotent success; an unavailable or unlanded state defers; a record
      // with a different identity/state is a hard collision.
      let existing, version
      try {
        ;({ data: existing, version } = await store.read(key, { signal }))
      } catch (e2) {
        if (e2 instanceof StoreError && (e2.kind === 'not-found' || isRetryableStoreError(e2))) return 1
        throw e2
      }
      let parsed
      try {
        parsed = parseNoteRecord(existing)
      } catch (e2) {
        throw e2
      }
      if (parsed.syncId !== conf.conflictSyncId || parsed.path !== conf.conflictPath ||
          parsed.markdown !== conf.conflictMarkdown || parsed.deleted) {
        throw new Error(`remote conflict collision at ${key}`)
      }
      conflictVersion = version
    } else if (e instanceof StoreError && isRetryableStoreError(e)) {
      return 1
    } else {
      throw e
    }
  }

  // 3. Only now act on the original; each helper sets the baseline on success.
  let deferred = 0
  if (d.kind === 'preserve_local_then_pull') {
    if (!(await pullLive(d, baselines, ctx))) deferred++
  } else if (d.kind === 'preserve_local_then_delete') {
    if (!(await applyTombstone(d, baselines, ctx))) deferred++
  } else if (d.kind === 'preserve_remote_then_tombstone') {
    if (!(await pushTombstone(store, d, baselines, signal))) deferred++
  }

  // 4. The conflict note is now known equal locally and remotely.
  baselines[conf.conflictSyncId] = {
    contentHash: await computeContentHash({
      schemaVersion: NOTE_SCHEMA_VERSION, syncId: conf.conflictSyncId,
      path: conf.conflictPath, markdown: conf.conflictMarkdown, deleted: false,
    }),
    deleted: false,
    remoteVersion: conflictVersion,
  }
  return deferred
}

// commitSnapshot atomically installs the consolidated snapshot once per cycle.
async function commitSnapshot({ identity, connection, repo, baselines }) {
  await replaceSnapshot({
    schemaVersion: SNAPSHOT_SCHEMA_VERSION,
    vaultId: identity.vaultId,
    replicaId: identity.replicaId,
    repositoryId: repo.repositoryId,
    providerProfile: connection.providerProfile,
    notes: baselines,
  })
}

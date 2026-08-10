// R6.2 IndexedDB sync identity, snapshot, and recovery storage.
//
// DB v3 adds four object stores beside the existing notes/folders:
//   - syncIndex      Sync ID -> last known Markdown path. It survives local
//                    note deletion so the next cycle can emit a tombstone; the
//                    coordinator removes the mapping only once that deletion is
//                    known converged.
//   - syncState      one record each for Vault/Replica identity, the strict
//                    connection pin (provider profile + Repository ID + the
//                    Disable flag), and one disposable schema-v2 snapshot.
//   - recovery       recovery-copy metadata (id = "syncId:stateHash", original
//                    path). No Markdown, so listing never loads document bodies.
//   - recoveryContent  the recovered Markdown for a recovery-copy id.
//
// Live note records optionally mirror their assigned `syncId`; note and index
// updates always share one transaction. Disable flips only the connection's
// `connected` flag; Reset clears the connection pin and the disposable snapshot
// while preserving notes, assigned Sync IDs, and recovery copies. A corrupt
// identity or connection pin stops sync (SyncStateError) and requires Reset;
// a missing or corrupt snapshot only triggers conservative onboarding.
//
// This mirrors internal/syncindex + internal/syncstate for the browser.

import { openDB, allOf, getRec } from './localVaultDb'
import { sha256Hex } from './sha256'
import { parseDocument } from './frontmatter'
import { isUUIDv4, isSyncID, newUUIDv4 } from '../sync/uuid'
import { validNotePath } from '../sync/note'

export const SNAPSHOT_SCHEMA_VERSION = 2

const IDENTITY_KEY = 'identity'
const CONNECTION_KEY = 'connection'
const SNAPSHOT_KEY = 'snapshot'
const HEX64_RE = /^[0-9a-f]{64}$/

// SyncStateError carries a stable code so the coordinator can decide whether
// sync must stop (identity/connection corruption) or merely onboard
// conservatively (a missing/corrupt snapshot). Codes: identity-corrupt,
// connection-corrupt, index-corrupt, invalid-snapshot, invalid-id,
// invalid-path, invalid-recovery, path-claimed, path-occupied, id-mapped,
// id-conflict, not-found.
export class SyncStateError extends Error {
  constructor(code, message) {
    super(message || code)
    this.code = code
  }
}

// runTx executes fn(t, reqP) inside one readwrite transaction and resolves with
// its result once the transaction commits. A thrown error aborts the
// transaction, so a multi-store mutation (note + index, recovery metadata +
// content) is all-or-nothing.
function runTx(stores, fn) {
  return openDB().then((db) => new Promise((resolve, reject) => {
    const t = db.transaction(stores, 'readwrite')
    const reqP = (request) => new Promise((res, rej) => {
      request.onsuccess = () => res(request.result)
      request.onerror = () => rej(request.error)
    })
    ;(async () => {
      try {
        const result = await fn(t, reqP)
        t.oncomplete = () => resolve(result)
      } catch (e) {
        try { t.abort() } catch (_) {}
        reject(e)
      }
    })()
    t.onerror = () => reject(t.error)
    t.onabort = () => reject(t.error)
  }))
}

// ---- identity -----------------------------------------------------------

// loadIdentity reads the Vault/Replica identity. A missing record means sync
// has never been enabled. A present-but-invalid record is corruption: sync must
// stop and the user must Reset — it is never reinterpreted as a fresh vault.
export async function loadIdentity() {
  const rec = await getRec('syncState', IDENTITY_KEY)
  if (!rec) return null
  if (typeof rec.vaultId !== 'string' || !isUUIDv4(rec.vaultId) ||
      typeof rec.replicaId !== 'string' || !isUUIDv4(rec.replicaId)) {
    throw new SyncStateError('identity-corrupt', 'sync identity is corrupt; reset sync')
  }
  return { vaultId: rec.vaultId, replicaId: rec.replicaId }
}

// saveIdentity writes the Vault/Replica identity (Enable).
export async function saveIdentity(vaultId, replicaId) {
  if (!isUUIDv4(vaultId) || !isUUIDv4(replicaId)) {
    throw new SyncStateError('invalid-id', 'vault and replica IDs must be UUID v4')
  }
  await runTx(['syncState'], async (t) => {
    t.objectStore('syncState').put({ key: IDENTITY_KEY, vaultId, replicaId })
  })
}

// ---- connection pin ------------------------------------------------------

function isValidConnection(rec) {
  return rec &&
    typeof rec.providerProfile === 'string' && HEX64_RE.test(rec.providerProfile) &&
    typeof rec.repositoryId === 'string' && isUUIDv4(rec.repositoryId) &&
    typeof rec.connected === 'boolean'
}

// loadConnection reads the strict connection pin. A missing record means sync
// is not connected; a present-but-invalid record stops sync and requires Reset.
export async function loadConnection() {
  const rec = await getRec('syncState', CONNECTION_KEY)
  if (!rec) return null
  if (!isValidConnection(rec)) {
    throw new SyncStateError('connection-corrupt', 'sync connection is corrupt; reset sync')
  }
  return { connected: rec.connected, providerProfile: rec.providerProfile, repositoryId: rec.repositoryId }
}

// setConnected flips ONLY the connected flag (Disable/Enable); the pin is kept.
// A missing connection record is a no-op.
export async function setConnected(connected) {
  if (typeof connected !== 'boolean') throw new SyncStateError('invalid-connection', 'connected must be a boolean')
  await runTx(['syncState'], async (t, reqP) => {
    const store = t.objectStore('syncState')
    const rec = await reqP(store.get(CONNECTION_KEY))
    if (rec) store.put({ ...rec, connected })
  })
}

// setConnection writes the connection pin (Enable, after validating the remote
// repository).
export async function setConnection({ providerProfile, repositoryId, connected }) {
  if (typeof providerProfile !== 'string' || !HEX64_RE.test(providerProfile) ||
      typeof repositoryId !== 'string' || !isUUIDv4(repositoryId) ||
      typeof connected !== 'boolean') {
    throw new SyncStateError('invalid-connection', 'invalid connection pin')
  }
  await runTx(['syncState'], async (t) => {
    t.objectStore('syncState').put({ key: CONNECTION_KEY, providerProfile, repositoryId, connected })
  })
}

// ---- disposable snapshot -------------------------------------------------

// validateSnapshot returns the name of the first invalid field, or null. It
// mirrors Go's SnapshotV2.Validate: exact schema, UUID v4 identities, a
// lowercase 64-hex provider fingerprint, and a non-null notes map whose entries
// carry a hex64 content hash, a non-empty remote version, and a deleted bit.
function validateSnapshot(s) {
  if (!s || s.schemaVersion !== SNAPSHOT_SCHEMA_VERSION) return 'schema'
  if (typeof s.vaultId !== 'string' || !isUUIDv4(s.vaultId)) return 'vaultId'
  if (typeof s.replicaId !== 'string' || !isUUIDv4(s.replicaId)) return 'replicaId'
  if (typeof s.repositoryId !== 'string' || !isUUIDv4(s.repositoryId)) return 'repositoryId'
  if (typeof s.providerProfile !== 'string' || !HEX64_RE.test(s.providerProfile)) return 'providerProfile'
  if (!s.notes || typeof s.notes !== 'object' || Array.isArray(s.notes)) return 'notes'
  for (const [syncId, e] of Object.entries(s.notes)) {
    if (!isSyncID(syncId)) return 'syncId'
    if (!e || typeof e !== 'object') return 'note'
    if (typeof e.contentHash !== 'string' || !HEX64_RE.test(e.contentHash)) return 'contentHash'
    if (typeof e.remoteVersion !== 'string' || e.remoteVersion === '') return 'remoteVersion'
    if (typeof e.deleted !== 'boolean') return 'deleted'
  }
  return null
}

// loadSnapshot reads the disposable snapshot against the expected identity.
// It never throws for a missing/corrupt snapshot — those map to a discard
// reason so the coordinator performs conservative onboarding — but the call is
// still subject to real IndexedDB I/O errors.
export async function loadSnapshot(expected) {
  const rec = await getRec('syncState', SNAPSHOT_KEY)
  if (!rec) return { reason: 'missing', snapshot: null }
  if (validateSnapshot(rec) !== null) return { reason: 'corrupt', snapshot: null }
  if (rec.vaultId !== expected.vaultId || rec.replicaId !== expected.replicaId) {
    return { reason: 'corrupt', snapshot: null }
  }
  if (rec.providerProfile !== expected.providerProfile) {
    return { reason: 'provider-profile-mismatch', snapshot: null }
  }
  if (rec.repositoryId !== expected.repositoryId) {
    return { reason: 'repository-id-mismatch', snapshot: null }
  }
  const { key, ...snap } = rec
  return { reason: 'usable', snapshot: snap }
}

// replaceSnapshot atomically installs a snapshot in one transaction (the single
// commit point per cycle). An invalid snapshot is rejected before any write, so
// the previous snapshot always remains loadable.
export async function replaceSnapshot(snapshot) {
  const bad = validateSnapshot(snapshot)
  if (bad !== null) throw new SyncStateError('invalid-snapshot', `snapshot field "${bad}" is invalid`)
  await runTx(['syncState'], async (t) => {
    t.objectStore('syncState').put({ key: SNAPSHOT_KEY, ...snapshot })
  })
}

// ---- sync index ----------------------------------------------------------

// loadSyncIndex returns the full Sync ID -> path mapping. Every entry's Sync ID
// key is validated and duplicate valid paths are rejected (structural
// corruption). An entry with an UNREPRESENTABLE path is not corruption: it is an
// unsyncable note the coordinator must observe as UNKNOWN (never absent, which
// would authorize a tombstone), so the entry is returned as-is for the
// observation layer to classify.
export async function loadSyncIndex() {
  const entries = await allOf('syncIndex')
  const index = {}
  const byPath = new Map()
  for (const e of entries) {
    if (!isSyncID(e.syncId)) {
      throw new SyncStateError('index-corrupt', 'sync index holds an invalid Sync ID')
    }
    if (byPath.has(e.path) && validNotePath(e.path)) {
      throw new SyncStateError('index-corrupt', `sync index maps two Sync IDs to "${e.path}"`)
    }
    index[e.syncId] = e.path
    byPath.set(e.path, e.syncId)
  }
  return index
}

// removeIndexEntry drops one Sync ID from the index (the converged-deletion
// cleanup: only the coordinator calls it once a tombstone is known converged).
export async function removeIndexEntry(syncId) {
  if (!isSyncID(syncId)) throw new SyncStateError('invalid-id', `invalid syncId`)
  await runTx(['syncIndex'], async (t) => {
    t.objectStore('syncIndex').delete(syncId)
  })
}

// assignMissingSyncIds assigns a stable UUID v4 to every live note that does
// not yet mirror one, in ONE transaction over notes + syncIndex. A path that is
// still indexed (a recreated note, or an Enable re-run) reuses its existing ID;
// a fresh path gets a new ID. Notes with an UNREPRESENTABLE path are never
// indexed (assigning one would poison the index) — they stay unsyncable and the
// coordinator observes them as UNKNOWN. The transaction's full updated notes
// snapshot is returned so a cycle enumerates IndexedDB exactly once.
export async function assignMissingSyncIds() {
  return runTx(['notes', 'syncIndex'], async (t, reqP) => {
    const notesStore = t.objectStore('notes')
    const indexStore = t.objectStore('syncIndex')
    const [notes, entries] = await Promise.all([
      reqP(notesStore.getAll()),
      reqP(indexStore.getAll()),
    ])
    const byPath = new Map()
    for (const e of entries) byPath.set(e.path, e.syncId)
    const assigned = []
    const resultNotes = []
    for (const note of notes) {
      let updated = note
      if (validNotePath(note.path)) {
        const mapped = byPath.get(note.path)
        if (note.syncId) {
          if (mapped && mapped !== note.syncId) {
            throw new SyncStateError('index-corrupt', `note "${note.path}" disagrees with its index mapping`)
          }
          if (!mapped) {
            indexStore.put({ syncId: note.syncId, path: note.path })
            byPath.set(note.path, note.syncId)
          }
        } else {
          let syncId = mapped
          if (!syncId) {
            syncId = newUUIDv4()
            indexStore.put({ syncId, path: note.path })
            byPath.set(note.path, syncId)
          }
          updated = { ...note, syncId }
          notesStore.put(updated)
          assigned.push({ syncId, path: note.path })
        }
      }
      resultNotes.push(updated)
    }
    return { assigned, notes: resultNotes }
  })
}

// reserveConflictNote durably reserves a deterministic conflict ID/path BEFORE
// the original note is changed (spec §8). It creates the conflict note and the
// index mapping in one transaction. A replay with the same ID, path, and
// content is an idempotent no-op; a path claimed by another Sync ID, an
// occupied path with different content, or a conflict ID already mapped
// elsewhere is a block (throws), never an overwrite.
export async function reserveConflictNote(syncId, path, markdown) {
  if (!isSyncID(syncId)) throw new SyncStateError('invalid-id', `invalid conflict syncId "${syncId}"`)
  if (!validNotePath(path)) throw new SyncStateError('invalid-path', `unsafe conflict path "${path}"`)
  if (typeof markdown !== 'string') throw new SyncStateError('invalid-recovery', 'conflict markdown must be a string')
  return runTx(['notes', 'syncIndex'], async (t, reqP) => {
    const notesStore = t.objectStore('notes')
    const indexStore = t.objectStore('syncIndex')
    const [note, idEntry] = await Promise.all([
      reqP(notesStore.get(path)),
      reqP(indexStore.get(syncId)),
    ])
    if (idEntry && idEntry.path !== path) {
      throw new SyncStateError('id-mapped', `conflict ${syncId} is already mapped to "${idEntry.path}"`)
    }
    const entries = await reqP(indexStore.getAll())
    for (const e of entries) {
      if (e.path === path && e.syncId !== syncId) {
        throw new SyncStateError('path-claimed', `path "${path}" is claimed by ${e.syncId}`)
      }
    }
    if (note) {
      if (note.syncId === syncId && note.markdown === markdown) {
        return { ok: true, created: false }
      }
      throw new SyncStateError('path-occupied', `conflict path "${path}" already holds a different note`)
    }
    notesStore.put(makeNoteRecord({ path, markdown, syncId }))
    indexStore.put({ syncId, path })
    return { ok: true, created: true }
  })
}

// ---- applying remote mutations with the local revision CAS ---------------

function makeNoteRecord({ path, markdown, syncId }) {
  const doc = parseDocument(markdown)
  const now = Date.now()
  return {
    path,
    markdown,
    syncId,
    content: doc.body,
    tags: doc.tags,
    revision: sha256Hex(markdown),
    modTime: now,
    created: now,
  }
}

// applyNoteMutation materializes one sync-cycle local mutation inside a single
// transaction, using the observed local revision as the optimistic-concurrency
// CAS token ('' = create-if-absent). Modes:
//   { mode: 'pull', syncId, path, markdown, expectedRevision }     create or
//     CAS-replace; a raced local write or an occupied/claimed path defers.
//   { mode: 'path-change', syncId, oldPath, path, markdown, expectedRevision }
//     delete the old location with its CAS, remap the Sync ID, create the new
//     note — one transaction, so a crash can never leave the new path unindexed.
//   { mode: 'delete', syncId, path, expectedRevision }              CAS delete.
//     The index mapping SURVIVES a local delete so the next cycle can emit the
//     tombstone; only the coordinator removes it once converged.
// A CAS race or a blocked path returns { applied: false, reason } and never
// clobbers — the coordinator defers such a note to the next cycle. delete and
// path-change are destructive and REQUIRE the observed local revision: an empty
// expectedRevision is rejected (never an unconditional delete/move), and the
// current revision is always compared.
export async function applyNoteMutation(m) {
  if (!isSyncID(m.syncId)) throw new SyncStateError('invalid-id', `invalid syncId "${m.syncId}"`)
  if (m.mode === 'delete' || m.mode === 'pull') {
    if (!validNotePath(m.path)) throw new SyncStateError('invalid-path', `unsafe path "${m.path}"`)
  } else if (m.mode === 'path-change') {
    if (!validNotePath(m.oldPath) || !validNotePath(m.path)) {
      throw new SyncStateError('invalid-path', 'unsafe path in path-change')
    }
  } else {
    throw new SyncStateError('invalid-mutation', `unknown mutation mode "${m.mode}"`)
  }
  if (m.mode === 'delete' || m.mode === 'path-change') {
    if (typeof m.expectedRevision !== 'string' || m.expectedRevision === '') {
      throw new SyncStateError('invalid-mutation', `${m.mode} requires a non-empty expectedRevision`)
    }
  }
  return runTx(['notes', 'syncIndex'], async (t, reqP) => {
    const notesStore = t.objectStore('notes')
    const indexStore = t.objectStore('syncIndex')
    const entries = await reqP(indexStore.getAll())
    const byPath = new Map()
    for (const e of entries) byPath.set(e.path, e.syncId)

    if (m.mode === 'delete') {
      const rec = await reqP(notesStore.get(m.path))
      if (!rec) return { applied: true } // already gone; idempotent
      if (rec.syncId && rec.syncId !== m.syncId) return { applied: false, reason: 'id-conflict' }
      if (rec.revision !== m.expectedRevision) {
        return { applied: false, reason: 'revision-conflict' }
      }
      notesStore.delete(m.path)
      return { applied: true }
    }

    if (m.mode === 'path-change') {
      const oldRec = await reqP(notesStore.get(m.oldPath))
      if (!oldRec) return { applied: false, reason: 'not-found' }
      if (oldRec.syncId && oldRec.syncId !== m.syncId) return { applied: false, reason: 'id-conflict' }
      if (oldRec.revision !== m.expectedRevision) {
        return { applied: false, reason: 'revision-conflict' }
      }
      const claimed = byPath.get(m.path)
      if (claimed && claimed !== m.syncId) return { applied: false, reason: 'id-conflict' }
      const newRec = await reqP(notesStore.get(m.path))
      if (newRec && !(newRec.syncId === m.syncId && newRec.markdown === m.markdown)) {
        return { applied: false, reason: 'path-occupied' }
      }
      notesStore.delete(m.oldPath)
      if (!newRec) notesStore.put(makeNoteRecord({ path: m.path, markdown: m.markdown, syncId: m.syncId }))
      indexStore.put({ syncId: m.syncId, path: m.path })
      return { applied: true }
    }

    // pull
    const rec = await reqP(notesStore.get(m.path))
    if (rec) {
      if (rec.syncId && rec.syncId !== m.syncId) return { applied: false, reason: 'id-conflict' }
      if (rec.markdown === m.markdown) {
        if (!rec.syncId) notesStore.put({ ...rec, syncId: m.syncId })
        indexStore.put({ syncId: m.syncId, path: m.path })
        return { applied: true }
      }
      if (m.expectedRevision && rec.revision !== m.expectedRevision) {
        return { applied: false, reason: 'revision-conflict' }
      }
      if (!m.expectedRevision) return { applied: false, reason: 'revision-conflict' }
      const claimed = byPath.get(m.path)
      if (claimed && claimed !== m.syncId) return { applied: false, reason: 'id-conflict' }
      const doc = parseDocument(m.markdown)
      const now = Date.now()
      notesStore.put({
        ...rec,
        markdown: m.markdown,
        syncId: m.syncId,
        content: doc.body,
        tags: doc.tags,
        revision: sha256Hex(m.markdown),
        modTime: now,
      })
      indexStore.put({ syncId: m.syncId, path: m.path })
      return { applied: true }
    }
    const claimed = byPath.get(m.path)
    if (claimed && claimed !== m.syncId) return { applied: false, reason: 'id-conflict' }
    notesStore.put(makeNoteRecord({ path: m.path, markdown: m.markdown, syncId: m.syncId }))
    indexStore.put({ syncId: m.syncId, path: m.path })
    return { applied: true }
  })
}

// ---- recovery store ------------------------------------------------------

function recoveryId(syncId, stateHash) {
  return `${syncId}:${stateHash}`
}

// writeRecovery durably stores a complete recovered Markdown document with its
// original path, keyed by (syncId, stateHash). The metadata and the content are
// written in one transaction; the content write is skipped when the copy is
// already identical. Recovery failure prevents deletion — the coordinator never
// deletes before this call succeeds.
export async function writeRecovery(syncId, stateHash, path, markdown) {
  if (!isSyncID(syncId)) throw new SyncStateError('invalid-id', `invalid syncId "${syncId}"`)
  if (!HEX64_RE.test(stateHash)) throw new SyncStateError('invalid-state-hash', `invalid state hash "${stateHash}"`)
  if (typeof markdown !== 'string') throw new SyncStateError('invalid-recovery', 'recovery markdown must be a string')
  const id = recoveryId(syncId, stateHash)
  return runTx(['recovery', 'recoveryContent'], async (t, reqP) => {
    const meta = t.objectStore('recovery')
    const content = t.objectStore('recoveryContent')
    const existing = await reqP(content.get(id))
    if (existing && existing.markdown === markdown) {
      const m = await reqP(meta.get(id))
      if (!m || m.path !== path || m.syncId !== syncId || m.stateHash !== stateHash) {
        meta.put({ id, syncId, stateHash, path })
      }
      return { ok: true }
    }
    meta.put({ id, syncId, stateHash, path })
    content.put({ id, markdown })
    return { ok: true }
  })
}

// listRecovery returns recovery-copy METADATA only (Sync ID, state hash,
// original path) without loading any Markdown, deterministically ordered.
export async function listRecovery() {
  const meta = await allOf('recovery')
  const out = meta.map((m) => ({ syncId: m.syncId, stateHash: m.stateHash, path: m.path || '' }))
  out.sort((a, b) => (a.syncId === b.syncId ? a.stateHash.localeCompare(b.stateHash) : a.syncId.localeCompare(b.syncId)))
  return out
}

// readRecovery loads one recovery copy (metadata + Markdown), or null.
export async function readRecovery(syncId, stateHash) {
  const id = recoveryId(syncId, stateHash)
  const [meta, content] = await Promise.all([
    getRec('recovery', id),
    getRec('recoveryContent', id),
  ])
  if (!meta || !content) return null
  return { syncId, stateHash, path: meta.path || '', markdown: content.markdown }
}

// restoreRecovery safely restores one recovery copy as a live note: it writes
// the recovered Markdown at the original path (or an explicit target), reuses
// the Sync ID, and repairs the index mapping — all in one transaction. A record
// at the target that is not this exact copy blocks the restore (never
// overwritten); an identical record is an idempotent no-op.
export async function restoreRecovery(syncId, stateHash, targetPath) {
  const copy = await readRecovery(syncId, stateHash)
  if (!copy) throw new SyncStateError('not-found', `no recovery copy for ${syncId}:${stateHash}`)
  const path = targetPath || copy.path
  if (!validNotePath(path)) throw new SyncStateError('invalid-path', `unsafe restore path "${path}"`)
  return runTx(['notes', 'syncIndex'], async (t, reqP) => {
    const notesStore = t.objectStore('notes')
    const indexStore = t.objectStore('syncIndex')
    const [rec, idEntry] = await Promise.all([
      reqP(notesStore.get(path)),
      reqP(indexStore.get(syncId)),
    ])
    // The Sync ID must not already live somewhere else: restoring a copy over a
    // still-live note would mint a second note with the same identity and
    // silently drop the index mapping. The copy is a deleted version; blocking
    // is the safe interpretation.
    if (idEntry && idEntry.path !== path) {
      throw new SyncStateError('id-mapped', `syncId ${syncId} is already mapped to "${idEntry.path}"`)
    }
    const entries = await reqP(indexStore.getAll())
    for (const e of entries) {
      if (e.path === path && e.syncId !== syncId) {
        throw new SyncStateError('path-claimed', `path "${path}" is claimed by ${e.syncId}`)
      }
    }
    if (rec) {
      if (rec.syncId === syncId && rec.markdown === copy.markdown) {
        indexStore.put({ syncId, path })
        return { ok: true, created: false }
      }
      throw new SyncStateError('path-occupied', `cannot restore over "${path}"`)
    }
    notesStore.put(makeNoteRecord({ path, markdown: copy.markdown, syncId }))
    indexStore.put({ syncId, path })
    return { ok: true, created: true }
  })
}

// ---- Reset ---------------------------------------------------------------

// resetSyncState clears the connection pin and the disposable snapshot in one
// transaction. Notes, assigned Sync IDs, the sync index, recovery copies, and
// the Vault/Replica identity are all preserved (spec R6.2).
export async function resetSyncState() {
  await runTx(['syncState'], async (t) => {
    const store = t.objectStore('syncState')
    store.delete(CONNECTION_KEY)
    store.delete(SNAPSHOT_KEY)
  })
}

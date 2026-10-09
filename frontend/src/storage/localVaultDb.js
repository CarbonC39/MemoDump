// Shared IndexedDB open/migrate/transaction boundary for the pure-frontend
// build (VITE_LOCAL=1). The memodump database used to store only {content,
// tags} per note; since version 2 every note also carries its canonical full
// Markdown and a local revision (the optimistic-concurrency CAS token), so
// unknown front-matter keys survive tag edits exactly as on the Go server.
//
// The record-shape migration runs once per database open, in a single
// readwrite transaction, before openDB() resolves. It is idempotent: records
// that already carry `markdown` are left untouched.

import { serializeTags, parseDocument } from './frontmatter'
import { sha256Hex } from './sha256'

// Since version 3 the database also carries the note-sync state the R6 browser
// engine needs, each in its own object store: `syncIndex` (Sync ID -> last
// known Markdown path), `syncState` (Vault/Replica identity, the strict
// connection pin, and one disposable schema-v2 snapshot), and the split
// `recovery` / `recoveryContent` stores (metadata without Markdown so a
// recovery list never loads document bodies). Live note records optionally
// mirror their assigned `syncId`; the note and index stores are always updated
// together (see the write helpers below), never in separate transactions.

const DB_NAME = 'memodump'
const DB_VERSION = 3

let dbPromise = null

export function idb() {
  return globalThis.indexedDB
}

export function openDB() {
  if (dbPromise) return dbPromise
  dbPromise = new Promise((resolve, reject) => {
    const req = idb().open(DB_NAME, DB_VERSION)
    req.onupgradeneeded = () => {
      const db = req.result
      if (!db.objectStoreNames.contains('notes')) db.createObjectStore('notes', { keyPath: 'path' })
      if (!db.objectStoreNames.contains('folders')) db.createObjectStore('folders', { keyPath: 'path' })
      if (!db.objectStoreNames.contains('syncIndex')) db.createObjectStore('syncIndex', { keyPath: 'syncId' })
      if (!db.objectStoreNames.contains('syncState')) db.createObjectStore('syncState', { keyPath: 'key' })
      if (!db.objectStoreNames.contains('recovery')) db.createObjectStore('recovery', { keyPath: 'id' })
      if (!db.objectStoreNames.contains('recoveryContent')) db.createObjectStore('recoveryContent', { keyPath: 'id' })
    }
    req.onsuccess = () => {
      const db = req.result
      migrateLegacyNotes(db)
        .then(() => resolve(db))
        .catch((e) => {
          db.close()
          reject(e)
        })
    }
    req.onerror = () => reject(req.error)
  })
  return dbPromise
}

// Rebuild the canonical Markdown a pre-v2 record implied: front matter built
// from its tags plus its body. This is the lossless reconstruction the upgrade
// uses (unknown front-matter keys cannot exist in v1 records — they were never
// stored).
function migrateMarkdown(rec) {
  const content = typeof rec.content === 'string' ? rec.content : ''
  const tags = Array.isArray(rec.tags) ? rec.tags.map(String) : []
  return (tags.length ? `---\n${serializeTags(tags)}\n---\n` : '') + content
}

// One readwrite transaction over the notes store: every record missing
// `markdown` gets it (rebuilt from tags + body) plus its `revision`.
function migrateLegacyNotes(db) {
  return new Promise((resolve, reject) => {
    const tx = db.transaction('notes', 'readwrite')
    const store = tx.objectStore('notes')
    const scan = store.openCursor()
    scan.onsuccess = () => {
      const cursor = scan.result
      if (!cursor) return
      const rec = cursor.value
      if (rec && typeof rec.markdown !== 'string') {
        const markdown = migrateMarkdown(rec)
        cursor.update({ ...rec, markdown, revision: sha256Hex(markdown) })
      }
      cursor.continue()
    }
    scan.onerror = () => reject(scan.error)
    tx.onerror = () => reject(tx.error)
    tx.onabort = () => reject(tx.error)
    tx.oncomplete = () => resolve()
  })
}

function reqP(request) {
  return new Promise((resolve, reject) => {
    request.onsuccess = () => resolve(request.result)
    request.onerror = () => reject(request.error)
  })
}

/** Runs fn(notesStore, foldersStore) in one readwrite transaction. */
export async function write(fn) {
  const db = await openDB()
  return new Promise((resolve, reject) => {
    const t = db.transaction(['notes', 'folders'], 'readwrite')
    try {
      fn(t.objectStore('notes'), t.objectStore('folders'))
    } catch (e) {
      try { t.abort() } catch (_) {}
      reject(e)
      return
    }
    t.oncomplete = () => resolve()
    t.onerror = () => reject(t.error)
    t.onabort = () => reject(t.error)
  })
}

export async function allOf(store) {
  const db = await openDB()
  return reqP(db.transaction(store).objectStore(store).getAll())
}

export async function getRec(store, key) {
  const db = await openDB()
  return reqP(db.transaction(store).objectStore(store).get(key))
}

export async function getNoteRec(path) {
  return getRec('notes', path)
}

/**
 * Runs fn inside ONE readwrite transaction that reads the CURRENT notes and
 * folders within that transaction, so a folder rename/move/delete can check the
 * destination and rewrite path prefixes without ever overwriting a concurrent
 * note update with a stale snapshot. fn(notes, folders) is async and returns:
 *   { type: 'apply', writes } – [{ store: 'notes'|'folders', delete?: path, put?: rec }]
 *   { type: 'error', status, code, message }
 * A rewritten note record that carries a `syncId` also updates the sync index in
 * the same transaction, so an in-app rename/move preserves the note's Sync ID
 * while atomically changing its indexed path. Locally deleted notes keep their
 * index mapping on purpose: it must survive until the tombstone converges.
 */
export function atomicFolderWrite(fn) {
  return openDB().then((db) => new Promise((resolve, reject) => {
    const t = db.transaction(['notes', 'folders', 'syncIndex'], 'readwrite')
    const notes = t.objectStore('notes')
    const folders = t.objectStore('folders')
    const syncIndex = t.objectStore('syncIndex')
    const reqP = (request) => new Promise((res, rej) => {
      request.onsuccess = () => res(request.result)
      request.onerror = () => rej(request.error)
    })
    const fail = (err) => { try { t.abort() } catch (_) {}; reject(err) }
    ;(async () => {
      try {
        const [allNotes, allFolders] = await Promise.all([
          reqP(notes.getAll()),
          reqP(folders.getAll()),
        ])
        const outcome = await fn(allNotes, allFolders)
        if (outcome.type === 'error') {
          fail({ response: { status: outcome.status, data: { error: { code: outcome.code, message: outcome.message } } } })
          return
        }
        // Read the sync index ONCE, not once per synced note: a folder move over
        // F notes must not become O(F x S) IndexedDB reads/clones. The maps are
        // kept in sync as claims are consumed so a later write in the same batch
        // sees the updated ownership.
        const entries = await reqP(syncIndex.getAll())
        const claimedByPath = new Map()
        const claimedBySync = new Map()
        for (const e of entries) {
          claimedByPath.set(e.path, e.syncId)
          claimedBySync.set(e.syncId, e.path)
        }
        for (const w of outcome.writes) {
          const store = w.store === 'folders' ? folders : notes
          if (w.delete) store.delete(w.delete)
          if (w.put) {
            if (w.store === 'notes' && w.put.syncId) {
              const syncId = w.put.syncId
              // Drop this note's own stale claim (its old path) first, then
              // reject a move into a path still claimed by a DIFFERENT Sync ID:
              // a tombstone-pending deletion keeps its mapping, and letting two
              // IDs point at one path is index corruption. The whole transaction
              // aborts instead of writing it.
              const oldPath = claimedBySync.get(syncId)
              if (oldPath) claimedByPath.delete(oldPath)
              const at = claimedByPath.get(w.put.path)
              if (at !== undefined && at !== syncId) {
                fail({ response: { status: 409, data: { error: { code: 'sync_path_conflict', message: `path "${w.put.path}" is already claimed by another synced note` } } } })
                return
              }
              claimedByPath.set(w.put.path, syncId)
              claimedBySync.set(syncId, w.put.path)
              syncIndex.put({ syncId, path: w.put.path })
            }
            store.put(w.put)
          }
        }
        t.oncomplete = () => resolve(outcome)
      } catch (e) {
        fail(e)
      }
    })()
    t.onerror = () => reject(t.error)
    t.onabort = () => reject(t.error)
  }))
}

/**
 * Runs fn inside ONE readwrite transaction that also reads the note at `path`,
 * so the CAS check and the write are atomic across tabs (IndexedDB serializes
 * readwrite transactions over the same store). fn(rec, notes, folders, reqP)
 * is async and may issue further reads via reqP; it returns one of:
 *   { type: 'put', rec, deleteOld? } – put rec; deleteOld (default false) also
 *                                     deletes `path` when rec.path differs (rename)
 *   { type: 'delete' }       – delete `path`
 *   { type: 'noop', rec }    – nothing to write
 *   { type: 'error', status, code, message } – reject with that API error
 * Resolves with the returned outcome once the transaction commits.
 * When the written record carries a `syncId` its index mapping is upserted to
 * `rec.path` in the same transaction (in-app rename/move preservation). A local
 * delete never touches the index: the mapping survives until the sync cycle
 * knows the deletion converged.
 */
export function atomicNoteWrite(path, fn) {
  return openDB().then((db) => new Promise((resolve, reject) => {
    const t = db.transaction(['notes', 'folders', 'syncIndex'], 'readwrite')
    const notes = t.objectStore('notes')
    const folders = t.objectStore('folders')
    const syncIndex = t.objectStore('syncIndex')
    const reqP = (request) => new Promise((res, rej) => {
      request.onsuccess = () => res(request.result)
      request.onerror = () => rej(request.error)
    })
    const fail = (err) => { try { t.abort() } catch (_) {}; reject(err) }
    const apiError = (status, code, message) => {
      fail({ response: { status, data: { error: { code, message } } } })
    }

    ;(async () => {
      try {
        const rec = await reqP(notes.get(path))
        const outcome = await fn(rec, notes, folders, reqP)
        if (outcome.type === 'error') {
          apiError(outcome.status, outcome.code, outcome.message)
          return
        }
        if (outcome.type === 'put') {
          if (outcome.rec.syncId) {
            // Reject a move into a path still claimed by a different Sync ID
            // before writing anything: the whole transaction aborts, so the
            // note and the index can never diverge onto one path.
            const entries = await reqP(syncIndex.getAll())
            for (const e of entries) {
              if (e.path === outcome.rec.path && e.syncId !== outcome.rec.syncId) {
                apiError(409, 'sync_path_conflict', `path "${outcome.rec.path}" is already claimed by another synced note`)
                return
              }
            }
          }
          if (outcome.deleteOld && outcome.rec.path !== path) notes.delete(path)
          notes.put(outcome.rec)
          if (outcome.rec.syncId) {
            syncIndex.put({ syncId: outcome.rec.syncId, path: outcome.rec.path })
          }
        } else if (outcome.type === 'delete') {
          notes.delete(path)
        }
        t.oncomplete = () => resolve(outcome)
      } catch (e) {
        fail(e)
      }
    })()
    t.onerror = () => reject(t.error)
    t.onabort = () => reject(t.error)
  }))
}

// Test-only: close and reset the cached connection so suites can start from a
// pristine database (deleteDatabase blocks while a connection is open).
export async function _close() {
  if (dbPromise) {
    const db = await dbPromise
    db.close()
    dbPromise = null
  }
}

export { parseDocument }

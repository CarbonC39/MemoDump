// Shared IndexedDB open/migrate/transaction boundary for the pure-frontend
// build (VITE_LOCAL=1). The memodump database used to store only {content,
// tags} per note; since version 2 every note also carries its canonical full
// Markdown and a local revision (the optimistic-concurrency CAS token), so
// unknown front-matter keys survive tag edits exactly as on the Go server.
//
// The record-shape migration runs once per database open, in a single
// readwrite transaction, before openDB() resolves. It is idempotent: records
// that already carry `markdown` are left untouched.

import { frontMatterPartWithTags, parseDocument } from './frontmatter'
import { sha256Hex } from './sha256'

const DB_NAME = 'memodump'
const DB_VERSION = 2

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
  return frontMatterPartWithTags('', tags) + content
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

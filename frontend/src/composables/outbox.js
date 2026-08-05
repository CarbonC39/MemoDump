// frontend/src/composables/outbox.js
// IndexedDB-backed outbox for offline note writes. When a save can't reach the
// server (network down / server unreachable), the intended write is enqueued
// here and replayed once connectivity returns. One note == one coalesced entry
// keyed by its path (existing note) or a client id (new note).
import { ref } from 'vue'

const DB_NAME = 'memodump-outbox'
const STORE = 'outbox'
const DB_VERSION = 1

// Reactive count of pending entries — drives the 'offline' save-status.
export const outboxCount = ref(0)

let _dbPromise = null
function openDB() {
  if (_dbPromise) return _dbPromise
  _dbPromise = new Promise((resolve, reject) => {
    const idb = globalThis.indexedDB
    if (!idb) { reject(new Error('indexeddb unavailable')); return }
    const req = idb.open(DB_NAME, DB_VERSION)
    req.onupgradeneeded = () => {
      const db = req.result
      if (!db.objectStoreNames.contains(STORE)) db.createObjectStore(STORE, { keyPath: 'key' })
    }
    req.onsuccess = () => resolve(req.result)
    req.onerror = () => reject(req.error)
  })
  return _dbPromise
}

function store(mode) {
  return openDB().then(db => db.transaction(STORE, mode).objectStore(STORE))
}

function asPromise(request, after) {
  return new Promise((resolve, reject) => {
    request.onsuccess = () => {
      const done = () => resolve(request.result)
      if (after) { const p = after(); if (p && typeof p.then === 'function') p.then(done); else done() }
      else done()
    }
    request.onerror = () => reject(request.error)
  })
}

export async function outboxPut(entry) {
  const s = await store('readwrite')
  const existing = await asPromise(s.get(entry.key))
  let merged = entry
  if (existing) {
    // Coalescing: consecutive offline edits to the same note keep the earliest
    // baseRevision (the baseline they diverge from) and the latest content. A
    // delete supersedes earlier create/update work for that key.
    merged = {
      ...entry,
      baseRevision: existing.baseRevision || entry.baseRevision,
      clientId: existing.clientId || entry.clientId,
      originalName: existing.originalName ?? entry.originalName,
      op: existing.op === 'delete' || entry.op === 'delete'
        ? 'delete'
        : (existing.op === 'create' ? 'create' : (entry.op || 'update')),
      conflict: Boolean(existing.conflict || entry.conflict),
    }
  }
  await asPromise(s.put(merged), refreshCount)
}

export async function outboxDelete(key) {
  const s = await store('readwrite')
  await asPromise(s.delete(key), refreshCount)
}

export async function outboxAll() {
  const s = await store('readonly')
  const all = await asPromise(s.getAll())
  return all.slice().sort((a, b) => a.ts - b.ts)
}

export async function outboxClear() {
  const s = await store('readwrite')
  await asPromise(s.clear(), () => { outboxCount.value = 0 })
}

async function refreshCount() {
  try {
    const s = await store('readonly')
    outboxCount.value = await asPromise(s.count())
  } catch (_) { /* best-effort */ }
}

// Build a coalesced outbox entry from the live editor refs. baseRevision is
// the local CAS baseline the offline edit diverges from; replay sends it so an
// offline change never bypasses optimistic concurrency.
export function buildEntry({ editingNote, editContent, editName, editTags, editFolder }) {
  const n = editingNote.value || {}
  const clientId = n.clientId || null
  return {
    key: n.path || ('new::' + clientId),
    clientId,
    path: n.path || '',
    originalName: n.name || '',
    content: editContent.value,
    name: editName.value,
    tags: [...(editTags.value || [])],
    folder: editFolder.value,
    op: n.path ? 'update' : 'create',
    baseRevision: n.revision || '',
    ts: Date.now(),
  }
}

// Build an outbox entry for an offline delete, carrying the CAS baseline so a
// stale delete (the note changed since it was read) is rejected on replay.
export function buildDeleteEntry({ editingNote }) {
  const n = editingNote.value || {}
  return {
    key: n.path,
    clientId: n.clientId || null,
    path: n.path || '',
    originalName: n.name || '',
    content: n.content || '',
    name: n.name || '',
    tags: [...(n.tags || [])],
    folder: '',
    op: 'delete',
    baseRevision: n.revision || '',
    ts: Date.now(),
  }
}

refreshCount().catch(() => {})

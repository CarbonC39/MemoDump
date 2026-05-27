// Browser-local backend for MemoDump.
//
// Implements the same method surface as the axios-based `api.js` (each method
// resolves to an axios-shaped `{ data }` and rejects with `{ response: { status,
// data: { error } } }`), but stores notes/folders in IndexedDB instead of
// talking to the Go server. This powers the no-server public/demo build
// (VITE_LOCAL=1). It deliberately mirrors the semantics of api.go: notes are
// addressed by a slash-relative path, the name is the basename without `.md`,
// tags live alongside the body, folders are tracked explicitly so empty ones
// survive, and moves/renames rewrite path prefixes.

const DB_NAME = 'memodump'
const DB_VERSION = 1
const PREVIEW_LIMIT = 1000
const UPLOAD_LIMIT = 1 << 20 // 1 MB

let dbPromise = null

function idb() {
  return globalThis.indexedDB
}

function openDB() {
  if (dbPromise) return dbPromise
  dbPromise = new Promise((resolve, reject) => {
    const req = idb().open(DB_NAME, DB_VERSION)
    req.onupgradeneeded = () => {
      const db = req.result
      if (!db.objectStoreNames.contains('notes')) db.createObjectStore('notes', { keyPath: 'path' })
      if (!db.objectStoreNames.contains('folders')) db.createObjectStore('folders', { keyPath: 'path' })
    }
    req.onsuccess = () => resolve(req.result)
    req.onerror = () => reject(req.error)
  })
  return dbPromise
}

function reqP(request) {
  return new Promise((resolve, reject) => {
    request.onsuccess = () => resolve(request.result)
    request.onerror = () => reject(request.error)
  })
}

async function allOf(store) {
  const db = await openDB()
  return reqP(db.transaction(store).objectStore(store).getAll())
}

async function getNoteRec(path) {
  const db = await openDB()
  return reqP(db.transaction('notes').objectStore('notes').get(path))
}

// Run a readwrite transaction over both stores. `fn(notes, folders)` issues
// put/delete requests; the returned promise resolves when the txn commits.
async function write(fn) {
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

function apiError(status, error) {
  return Promise.reject({ response: { status, data: { error } } })
}

// ---- path helpers ----
function dirname(p) {
  const i = p.lastIndexOf('/')
  return i < 0 ? '' : p.slice(0, i)
}
function basename(p) {
  const i = p.lastIndexOf('/')
  return i < 0 ? p : p.slice(i + 1)
}
function noteName(p) {
  return basename(p).replace(/\.md$/, '')
}
function isUnder(path, folder) {
  return path === folder || path.startsWith(folder + '/')
}
function ancestors(dir) {
  const out = []
  if (!dir) return out
  const parts = dir.split('/')
  let cur = ''
  for (const part of parts) {
    cur = cur ? cur + '/' + part : part
    out.push(cur)
  }
  return out
}
function pad(n) {
  return String(n).padStart(2, '0')
}
function timestampName(d = new Date()) {
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}_${pad(d.getHours())}${pad(d.getMinutes())}${pad(d.getSeconds())}`
}

// ---- front matter (mirrors api.go parseFrontMatter / buildFrontMatter) ----
const FM_RE = /^---\n([\s\S]*?)\n---\n?/
const TAG_RE = /^tags:\s*\[([^\]]*)\]/m

export function parseFrontMatter(content) {
  const m = FM_RE.exec(content)
  if (!m) return { tags: [], body: content }
  const body = content.slice(m[0].length)
  const tags = []
  const tm = TAG_RE.exec(m[1])
  if (tm) {
    for (const raw of tm[1].split(',')) {
      const t = raw.trim()
      if (t) tags.push(t)
    }
  }
  return { tags, body }
}

function sanitizeName(name) {
  let out = ''
  for (const ch of basename(name)) {
    const c = ch.codePointAt(0)
    if (c < 0x20 || c === 0x7f || '/\\:*?"<>|'.includes(ch)) out += '_'
    else out += ch
  }
  out = out.replace(/^[ .]+|[ .]+$/g, '')
  return out.length > 200 ? out.slice(0, 200) : out
}

// ---- shaping ----
function toMeta(rec) {
  const body = rec.content || ''
  let preview = body.trim()
  if (preview.length > PREVIEW_LIMIT) preview = preview.slice(0, PREVIEW_LIMIT) + '...'
  return { path: rec.path, name: noteName(rec.path), tags: rec.tags || [], modTime: rec.modTime || 0, preview }
}
function toFull(rec) {
  return { path: rec.path, name: noteName(rec.path), tags: rec.tags || [], modTime: rec.modTime || 0, content: rec.content || '' }
}
function byModDesc(a, b) {
  return (b.modTime || 0) - (a.modTime || 0)
}

// Ensure a folder and all its ancestors exist (idempotent), like os.MkdirAll.
function ensureFolders(foldersStore, dir) {
  for (const a of ancestors(dir)) foldersStore.put({ path: a })
}

// Persist a brand-new note, avoiding clobbering an existing path. Returns the rec.
async function createNoteRec({ name, folder, content, tags }) {
  let filename = (name || '').trim() || timestampName()
  if (!filename.endsWith('.md')) filename += '.md'
  let path = folder ? folder + '/' + filename : filename
  if (await getNoteRec(path)) {
    filename = timestampName() + '_' + filename
    path = folder ? folder + '/' + filename : filename
  }
  const now = Date.now()
  const rec = { path, content: content || '', tags: tags || [], modTime: now, created: now }
  await write((notes, folders) => {
    notes.put(rec)
    ensureFolders(folders, folder)
  })
  return rec
}

const localApi = {
  // No server, no sessions: auth is a no-op in local mode.
  config() {
    return Promise.resolve({ data: { noAuth: true } })
  },
  login() {
    return Promise.resolve({ data: { status: 'ok' } })
  },
  logout() {
    return Promise.resolve({ data: { status: 'ok' } })
  },
  ping() {
    return Promise.resolve({ data: { status: 'ok' } })
  },

  async listNotes(folder) {
    const target = folder || ''
    const notes = await allOf('notes')
    const out = notes.filter(n => dirname(n.path) === target).map(toMeta)
    out.sort(byModDesc)
    return { data: out }
  },

  async getNote(path) {
    if (!path) return apiError(400, 'Path is illegal')
    const rec = await getNoteRec(path)
    if (!rec) return apiError(404, 'File not found')
    return { data: toFull(rec) }
  },

  async createNote(data) {
    const rec = await createNoteRec({
      name: data.name,
      folder: data.folder || '',
      content: data.content || '',
      tags: data.tags || [],
    })
    return { data: toFull(rec) }
  },

  async updateNote(path, data) {
    const rec = await getNoteRec(path)
    if (!rec) return apiError(404, 'File not found')

    if (data.content != null) rec.content = data.content
    rec.tags = data.tags || []
    rec.modTime = Date.now()

    let targetPath = path
    if (data.rename != null) {
      let newName = (data.rename || '').trim() || timestampName()
      if (!newName.endsWith('.md')) newName += '.md'
      const dir = dirname(path)
      targetPath = dir ? dir + '/' + newName : newName
    }

    if (targetPath !== path) {
      const moved = { ...rec, path: targetPath }
      await write((notes) => {
        notes.delete(path)
        notes.put(moved)
      })
      return { data: toFull(moved) }
    }
    await write((notes) => notes.put(rec))
    return { data: toFull(rec) }
  },

  async deleteNote(path) {
    await write((notes) => notes.delete(path))
    return { data: { status: 'ok' } }
  },

  async moveNote(path, destination) {
    const rec = await getNoteRec(path)
    if (!rec) return apiError(404, 'File not found')
    const dest = destination || ''
    const newPath = dest ? dest + '/' + basename(path) : basename(path)
    if (newPath === path) return { data: toMeta(rec) }
    if (await getNoteRec(newPath)) {
      return apiError(409, 'A note with that name already exists in the destination')
    }
    const moved = { ...rec, path: newPath }
    await write((notes, folders) => {
      notes.delete(path)
      notes.put(moved)
      ensureFolders(folders, dest)
    })
    return { data: toMeta(moved) }
  },

  async listFolders() {
    const [notes, folders] = [await allOf('notes'), await allOf('folders')]
    const dirSet = new Set()
    for (const f of folders) for (const a of ancestors(f.path)) dirSet.add(a)
    for (const n of notes) for (const a of ancestors(dirname(n.path))) dirSet.add(a)

    const nodes = new Map()
    for (const p of dirSet) nodes.set(p, { name: basename(p), path: p, children: [], notes: [] })
    for (const n of notes) {
      const d = dirname(n.path)
      if (d && nodes.has(d)) nodes.get(d).notes.push(toMeta(n))
    }
    const roots = []
    for (const [p, node] of nodes) {
      const parent = dirname(p)
      if (parent && nodes.has(parent)) nodes.get(parent).children.push(node)
      else roots.push(node)
    }
    const sortNode = (node) => {
      node.notes.sort(byModDesc)
      node.children.sort((a, b) => a.name.localeCompare(b.name))
      node.children.forEach(sortNode)
    }
    roots.sort((a, b) => a.name.localeCompare(b.name))
    roots.forEach(sortNode)
    return { data: roots }
  },

  async createFolder(path) {
    if (!path) return apiError(400, 'Path is illegal')
    await write((notes, folders) => ensureFolders(folders, path))
    return { data: { status: 'ok', path } }
  },

  async renameFolder(path, newName) {
    const parent = dirname(path)
    const newPath = parent ? parent + '/' + newName : newName
    if (newPath === path) return { data: { status: 'ok' } }

    const [notes, folders] = [await allOf('notes'), await allOf('folders')]
    if (folders.some(f => f.path === newPath)) {
      return apiError(409, 'A folder with that name already exists')
    }
    const rewrite = (p) => newPath + p.slice(path.length)
    await write((notesStore, foldersStore) => {
      for (const f of folders) {
        if (isUnder(f.path, path)) {
          foldersStore.delete(f.path)
          foldersStore.put({ path: rewrite(f.path) })
        }
      }
      for (const n of notes) {
        if (isUnder(n.path, path)) {
          notesStore.delete(n.path)
          notesStore.put({ ...n, path: rewrite(n.path) })
        }
      }
    })
    return { data: { status: 'ok' } }
  },

  async deleteFolder(path) {
    const [notes, folders] = [await allOf('notes'), await allOf('folders')]
    await write((notesStore, foldersStore) => {
      for (const f of folders) if (isUnder(f.path, path)) foldersStore.delete(f.path)
      for (const n of notes) if (isUnder(n.path, path)) notesStore.delete(n.path)
    })
    return { data: { status: 'ok' } }
  },

  async moveFolder(path, destination) {
    const dest = destination || ''
    const name = basename(path)
    const newPath = dest ? dest + '/' + name : name
    if (newPath === path) return { data: { status: 'ok' } }
    if (isUnder(newPath, path)) return apiError(400, 'Cannot move folder into itself')

    const [notes, folders] = [await allOf('notes'), await allOf('folders')]
    if (folders.some(f => f.path === newPath)) {
      return apiError(409, 'A folder with that name already exists in the destination')
    }
    const rewrite = (p) => newPath + p.slice(path.length)
    await write((notesStore, foldersStore) => {
      ensureFolders(foldersStore, dest)
      for (const f of folders) {
        if (isUnder(f.path, path)) {
          foldersStore.delete(f.path)
          foldersStore.put({ path: rewrite(f.path) })
        }
      }
      for (const n of notes) {
        if (isUnder(n.path, path)) {
          notesStore.delete(n.path)
          notesStore.put({ ...n, path: rewrite(n.path) })
        }
      }
    })
    return { data: { status: 'ok' } }
  },

  async search(q, tag) {
    const query = (q || '').toLowerCase()
    const wantTag = (tag || '').toLowerCase()
    const notes = await allOf('notes')
    const out = []
    for (const n of notes) {
      const matchQuery = !query || (n.content || '').toLowerCase().includes(query)
      let matchTag = !wantTag
      if (!matchTag) matchTag = (n.tags || []).some(t => t.toLowerCase() === wantTag)
      if (matchQuery && matchTag) out.push(toMeta(n))
    }
    out.sort(byModDesc)
    return { data: out }
  },

  async uploadNote(formData, folder = '') {
    const file = formData.get('file')
    if (!file) return apiError(400, 'No file provided')
    const fname = basename(file.name || '')
    const dot = fname.lastIndexOf('.')
    const ext = dot >= 0 ? fname.slice(dot).toLowerCase() : ''
    if (ext !== '.md' && ext !== '.txt') {
      return apiError(400, 'Only .md and .txt files are accepted')
    }
    if (file.size > UPLOAD_LIMIT) {
      return apiError(413, 'File too large (max 1 MB)')
    }
    const text = await file.text()
    if (/\x00/.test(text)) return apiError(400, "File contains binary data")
    const { tags, body } = parseFrontMatter(text)
    const base = sanitizeName(dot >= 0 ? fname.slice(0, dot) : fname)
    const rec = await createNoteRec({ name: base, folder: folder || '', content: body, tags })
    return { data: toFull(rec) }
  },
}

// Test-only: wipe all data so suites start clean.
export async function _clear() {
  await write((notes, folders) => {
    notes.clear()
    folders.clear()
  })
}

export default localApi

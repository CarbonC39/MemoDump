// Browser-local backend for MemoDump.
//
// Implements the same method surface as the axios-based `api.js` (each method
// resolves to an axios-shaped `{ data }` and rejects with `{ response: { status,
// data: { error } } }`), but stores notes/folders in IndexedDB instead of
// talking to the Go server. This powers the no-server public/demo build
// (VITE_LOCAL=1). It deliberately mirrors the semantics of api.go / vaultfs:
// notes are addressed by a slash-relative path, the name is the basename
// without `.md`, tags live alongside the body, folders are tracked explicitly
// so empty ones survive, and moves/renames rewrite path prefixes.
//
// Since DB version 2 each note record also stores its canonical full Markdown
// and a `revision` digest. updateNote/deleteNote accept an optional
// `baseRevision`; a stale one is rejected with `409 local_revision_conflict`
// without touching the record. Every note mutation runs inside ONE readwrite
// IndexedDB transaction (read current -> compare revision -> check destination
// -> put/delete), so two tabs cannot pass the same CAS check.

import { atomicNoteWrite, atomicFolderWrite, getNoteRec, write, allOf } from './storage/localVaultDb'
import { frontMatterPartWithTags, parseDocument, serializeTags } from './storage/frontmatter'
import { sha256Hex } from './storage/sha256'

const PREVIEW_LIMIT = 1000
const UPLOAD_LIMIT = 1 << 20 // 1 MB

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

function encodeCursor(value) {
  const bytes = new TextEncoder().encode(JSON.stringify(value))
  let binary = ''
  for (const byte of bytes) binary += String.fromCharCode(byte)
  return btoa(binary).replaceAll('+', '-').replaceAll('/', '_').replace(/=+$/, '')
}

function decodeCursor(value) {
  const padded = value.replaceAll('-', '+').replaceAll('_', '/') + '='.repeat((4 - value.length % 4) % 4)
  const binary = atob(padded)
  const bytes = Uint8Array.from(binary, ch => ch.charCodeAt(0))
  return JSON.parse(new TextDecoder().decode(bytes))
}

export { parseFrontMatter } from './storage/frontmatter'

function sanitizeName(name) {
  const portableBase = basename(String(name).replaceAll('\\', '/'))
  let out = ''
  for (const ch of portableBase) {
    const c = ch.codePointAt(0)
    if (c < 0x20 || c === 0x7f || '/\\:*?"<>|'.includes(ch)) out += '_'
    else out += ch
  }
  out = out.replace(/^[ .]+|[ .]+$/g, '')
  out = Array.from(out).slice(0, 200).join('')
  if (!out) return ''
  const dot = out.lastIndexOf('.')
  const stem = (dot > 0 ? out.slice(0, dot) : out).toUpperCase()
  if (/^(CON|PRN|AUX|NUL|COM[1-9]|LPT[1-9])$/.test(stem)) out = '_' + out
  return out
}

export const _sanitizeName = sanitizeName

// Strip reactivity: callers (Vue) pass reactive Proxy arrays, which IndexedDB's
// structured-clone cannot serialise ("Proxy object could not be cloned"). Map to
// a fresh plain array of strings so every record we put() is clone-safe.
function plainTags(tags) {
  return Array.isArray(tags) ? tags.map(t => String(t)) : []
}

function apiError(status, error) {
  return Promise.reject({ response: { status, data: { error } } })
}

// ---- shaping ----
function toMeta(rec) {
  const body = rec.content || ''
  let preview = body.trim()
  if (preview.length > PREVIEW_LIMIT) preview = preview.slice(0, PREVIEW_LIMIT) + '...'
  return { path: rec.path, name: noteName(rec.path), tags: rec.tags || [], modTime: rec.modTime || 0, preview }
}
function toFull(rec) {
  return {
    path: rec.path,
    name: noteName(rec.path),
    tags: rec.tags || [],
    modTime: rec.modTime || 0,
    content: rec.content || '',
    revision: rec.revision || '',
  }
}
function byModDesc(a, b) {
  return (b.modTime || 0) - (a.modTime || 0)
}

// Ensure a folder and all its ancestors exist (idempotent), like os.MkdirAll.
function ensureFolders(foldersStore, dir) {
  for (const a of ancestors(dir)) foldersStore.put({ path: a })
}

// Build the canonical document from body + tags (the create path).
function buildMarkdown(content, tags) {
  const t = plainTags(tags)
  return (t.length ? `---\n${serializeTags(t)}\n---\n` : '') + (content || '')
}

function buildPath(name, folder) {
  let filename = sanitizeName(name || '')
  if (!filename) filename = timestampName()
  if (!filename.endsWith('.md')) filename += '.md'
  return folder ? folder + '/' + filename : filename
}

const localApi = {
  // No server, no sessions: auth is a no-op in local mode.
  config() {
    let image = { provider: 'off', configured: false, editable: true }
    try {
      const raw = localStorage.getItem('memodump_image_config')
      if (raw) {
        const cfg = JSON.parse(raw)
        if (cfg.provider === 's3') {
          image = {
            provider: 's3',
            bucket: cfg.bucket || '',
            publicBaseUrl: cfg.publicBaseUrl || '',
            prefix: cfg.prefix || '',
            configured: true,
            editable: true,
          }
        }
      }
    } catch (_) {}
    return Promise.resolve({ data: { noAuth: true, image } })
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
    const markdown = buildMarkdown(data.content || '', data.tags || [])
    const rec = await createMarkdownRec({ name: data.name, folder: data.folder || '', markdown })
    return { data: toFull(rec) }
  },

  async updateNote(path, data) {
    if (!path) return apiError(400, 'Path is illegal')
    return atomicNoteWrite(path, async (rec, notes, folders, reqP) => {
      if (!rec) {
        return { type: 'error', status: 404, code: 'note_not_found', message: 'File not found' }
      }
      if (data.baseRevision && rec.revision !== data.baseRevision) {
        return { type: 'error', status: 409, code: 'local_revision_conflict', message: 'the note changed since it was read' }
      }

      const newTags = Array.isArray(data.tags) ? plainTags(data.tags) : rec.tags
      const newBody = data.content != null ? data.content : rec.content
      let newMarkdown
      try {
        const doc = parseDocument(rec.markdown || buildMarkdown(rec.content, rec.tags))
        newMarkdown = frontMatterPartWithTags(doc, newTags) + newBody
      } catch (e) {
        if (e && e.name === 'FrontMatterNotEditable') {
          return { type: 'error', status: 400, code: 'front_matter_not_editable', message: 'front matter cannot be edited safely' }
        }
        throw e
      }

      let targetPath = path
      if (data.rename != null) {
        let newName = sanitizeName((data.rename || '').trim()) || timestampName()
        if (!newName.endsWith('.md')) newName += '.md'
        targetPath = dirname(path) ? dirname(path) + '/' + newName : newName
      }
      if (data.destination != null) {
        targetPath = (data.destination ? data.destination + '/' : '') + basename(targetPath)
      }

      const moving = targetPath !== path
      if (!moving && newMarkdown === rec.markdown) {
        // No semantic change and no rename/move: leave the record untouched.
        return { type: 'noop', rec }
      }
      if (moving) {
        const target = await reqP(notes.get(targetPath))
        if (target) {
          return { type: 'error', status: 409, code: 'note_name_conflict', message: 'A note with that name already exists' }
        }
      }
      const now = Date.now()
      const updated = {
        ...rec,
        path: targetPath,
        markdown: newMarkdown,
        content: newBody,
        tags: newTags,
        revision: sha256Hex(newMarkdown),
        modTime: now,
      }
      return { type: 'put', rec: updated, deleteOld: moving }
    }).then((outcome) => ({ data: toFull(outcome.rec) }))
  },

  async deleteNote(path, baseRevision) {
    if (!path) return apiError(400, 'Path is illegal')
    return atomicNoteWrite(path, (rec) => {
      if (!rec) {
        return { type: 'error', status: 404, code: 'note_not_found', message: 'File not found' }
      }
      if (baseRevision && rec.revision !== baseRevision) {
        return { type: 'error', status: 409, code: 'local_revision_conflict', message: 'the note changed since it was read' }
      }
      return { type: 'delete' }
    }).then(() => ({ data: { status: 'ok' } }))
  },

  async duplicateNote(path) {
    if (!path) return apiError(400, 'Path is illegal')
    return atomicNoteWrite(path, async (src, notes, folders, reqP) => {
      if (!src) {
        return { type: 'error', status: 404, code: 'note_not_found', message: 'File not found' }
      }
      const dir = dirname(path)
      const base = noteName(path)
      let filename = `${base} (copy).md`
      let i = 2
      while (await reqP(notes.get(dir ? dir + '/' + filename : filename))) {
        filename = `${base} (copy ${i}).md`
        i++
      }
      const newPath = dir ? dir + '/' + filename : filename
      const now = Date.now()
      // A duplicate is a NEW local note: strip the source's syncId so the next
      // sync cycle assigns it a fresh identity instead of mirroring the source.
      const rec = { ...src, path: newPath, modTime: now, created: now }
      delete rec.syncId
      return { type: 'put', rec, deleteOld: false }
    }).then((outcome) => ({ data: toFull(outcome.rec) }))
  },

  async moveNote(path, destination) {
    if (!path) return apiError(400, 'Path is illegal')
    return atomicNoteWrite(path, async (rec, notes, folders, reqP) => {
      if (!rec) {
        return { type: 'error', status: 404, code: 'note_not_found', message: 'File not found' }
      }
      const dest = destination || ''
      const newPath = dest ? dest + '/' + basename(path) : basename(path)
      if (newPath === path) return { type: 'noop', rec }
      const target = await reqP(notes.get(newPath))
      if (target) {
        return { type: 'error', status: 409, code: 'note_name_conflict', message: 'A note with that name already exists in the destination' }
      }
      ensureFolders(folders, dest)
      return { type: 'put', rec: { ...rec, path: newPath }, deleteOld: true }
    }).then((outcome) => ({ data: toFull(outcome.rec) }))
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

  async listNotesV2(parent = '', { cursor = '', limit = 50, sort = 'modified-desc' } = {}) {
    if (sort !== 'modified-desc' && sort !== 'modified-asc') {
      return apiError(400, { code: 'invalid_sort', message: 'sort must be modified-desc or modified-asc' })
    }
    const notes = (await this.listNotes(parent)).data.map(n => ({
      id: n.path,
      name: n.name,
      parentId: parent,
      tags: n.tags || [],
      modifiedAt: n.modTime || 0,
      preview: n.preview || '',
    }))
    if (sort === 'modified-asc') {
      notes.sort((a, b) => a.modifiedAt - b.modifiedAt || a.id.localeCompare(b.id))
    }
    let start = 0
    if (cursor) {
      const decoded = decodeCursor(cursor)
      start = notes.findIndex(n =>
        (sort === 'modified-desc' && n.modifiedAt < decoded.modifiedAt) ||
        (sort === 'modified-asc' && n.modifiedAt > decoded.modifiedAt) ||
        (n.modifiedAt === decoded.modifiedAt && n.id > decoded.id))
      if (start < 0) start = notes.length
    }
    const size = Math.min(200, Math.max(1, Number(limit) || 50))
    const items = notes.slice(start, start + size)
    const last = items.at(-1)
    const next = start + size < notes.length && last
      ? encodeCursor({ modifiedAt: last.modifiedAt, id: last.id })
      : null
    return { data: { items, nextCursor: next } }
  },

  async listFoldersV2(parent = '') {
    const folders = await allOf('folders')
    const notes = await allOf('notes')
    const ids = new Set()
    for (const folder of folders) for (const ancestor of ancestors(folder.path)) ids.add(ancestor)
    for (const note of notes) for (const ancestor of ancestors(dirname(note.path))) ids.add(ancestor)
    const items = [...ids]
      .filter(id => dirname(id) === parent)
      .sort((a, b) => basename(a).localeCompare(basename(b)))
      .map(id => ({
        id,
        name: basename(id),
        parentId: parent,
        hasChildren: [...ids].some(candidate => dirname(candidate) === id),
      }))
    return { data: { items } }
  },

  async searchV2(q, tag, { cursor = '', limit = 50, sort = 'modified-desc' } = {}) {
    if (sort !== 'modified-desc' && sort !== 'modified-asc') {
      return apiError(400, { code: 'invalid_sort', message: 'sort must be modified-desc or modified-asc' })
    }
    const legacy = (await this.search(q, tag)).data
    const notes = legacy.map(n => ({
      id: n.path,
      name: n.name,
      parentId: dirname(n.path),
      tags: n.tags || [],
      modifiedAt: n.modTime || 0,
      preview: n.preview || '',
    }))
    if (sort === 'modified-asc') {
      notes.sort((a, b) => a.modifiedAt - b.modifiedAt || a.id.localeCompare(b.id))
    }
    let start = 0
    if (cursor) {
      const decoded = decodeCursor(cursor)
      start = notes.findIndex(n =>
        (sort === 'modified-desc' && n.modifiedAt < decoded.modifiedAt) ||
        (sort === 'modified-asc' && n.modifiedAt > decoded.modifiedAt) ||
        (n.modifiedAt === decoded.modifiedAt && n.id > decoded.id))
      if (start < 0) start = notes.length
    }
    const size = Math.min(200, Math.max(1, Number(limit) || 50))
    const items = notes.slice(start, start + size)
    const last = items.at(-1)
    const nextCursor = start + size < notes.length && last
      ? encodeCursor({ modifiedAt: last.modifiedAt, id: last.id })
      : null
    return { data: { items, nextCursor } }
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
    // Read + collision check + path rewrite + write share one transaction, so a
    // concurrent update in another tab is never overwritten by a stale snapshot.
    return atomicFolderWrite(async (notes, folders) => {
      if (folders.some(f => f.path === newPath)) {
        return { type: 'error', status: 409, code: 'folder_name_conflict', message: 'A folder with that name already exists' }
      }
      const rewrite = (p) => newPath + p.slice(path.length)
      const writes = []
      for (const f of folders) if (isUnder(f.path, path)) writes.push({ store: 'folders', delete: f.path, put: { path: rewrite(f.path) } })
      for (const n of notes) if (isUnder(n.path, path)) writes.push({ store: 'notes', delete: n.path, put: { ...n, path: rewrite(n.path) } })
      return { type: 'apply', writes }
    }).then(() => ({ data: { status: 'ok' } }))
  },

  async deleteFolder(path) {
    return atomicFolderWrite(async (notes, folders) => {
      const writes = []
      for (const f of folders) if (isUnder(f.path, path)) writes.push({ store: 'folders', delete: f.path })
      for (const n of notes) if (isUnder(n.path, path)) writes.push({ store: 'notes', delete: n.path })
      return { type: 'apply', writes }
    }).then(() => ({ data: { status: 'ok' } }))
  },

  async moveFolder(path, destination) {
    const dest = destination || ''
    const name = basename(path)
    const newPath = dest ? dest + '/' + name : name
    if (newPath === path) return { data: { status: 'ok' } }
    if (isUnder(newPath, path)) return apiError(400, 'Cannot move folder into itself')
    return atomicFolderWrite(async (notes, folders) => {
      if (folders.some(f => f.path === newPath)) {
        return { type: 'error', status: 409, code: 'folder_name_conflict', message: 'A folder with that name already exists in the destination' }
      }
      const rewrite = (p) => newPath + p.slice(path.length)
      const writes = []
      for (const a of ancestors(dest)) writes.push({ store: 'folders', put: { path: a } })
      for (const f of folders) if (isUnder(f.path, path)) writes.push({ store: 'folders', delete: f.path, put: { path: rewrite(f.path) } })
      for (const n of notes) if (isUnder(n.path, path)) writes.push({ store: 'notes', delete: n.path, put: { ...n, path: rewrite(n.path) } })
      return { type: 'apply', writes }
    }).then(() => ({ data: { status: 'ok' } }))
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
    const base = sanitizeName(dot >= 0 ? fname.slice(0, dot) : fname)
    const rec = await createMarkdownRec({ name: base, folder: folder || '', markdown: text })
    return { data: toFull(rec) }
  },
  // Cloud sync is a server feature; the pure-frontend build has no provider.
  syncStatus() {
    return Promise.resolve({ data: { enabled: false, connected: false, experimental: false, noE2EE: false, recoveryCount: 0 } })
  },
  syncEnable() { return Promise.reject(apiError(400, "Cloud sync requires the server build")) },
  syncRun() { return Promise.reject(apiError(400, "Cloud sync requires the server build")) },
  syncDisable() { return Promise.reject(apiError(400, "Cloud sync requires the server build")) },
  syncReset() { return Promise.reject(apiError(400, "Cloud sync requires the server build")) },
  syncTest() { return Promise.reject(apiError(400, "Cloud sync requires the server build")) },
  syncRecovery() { return Promise.resolve({ data: { recovery: [] } }) },
}

// Persist a brand-new note, avoiding clobbering an existing path. The full
// Markdown document is stored verbatim; content/tags are its projection. The
// existence check and the write share one transaction, so two tabs creating the
// same name cannot pick the same de-collided path.
async function createMarkdownRec({ name, folder, markdown }) {
  const doc = parseDocument(markdown)
  const now = Date.now()
  const initial = buildPath(name, folder)
  return atomicNoteWrite(initial, async (existing, notes, folders, reqP) => {
    let path = initial
    while (await reqP(notes.get(path))) {
      path = buildPath(timestampName() + '_' + basename(path), folder)
    }
    ensureFolders(folders, folder)
    const rec = {
      path,
      markdown,
      content: doc.body,
      tags: doc.tags,
      revision: sha256Hex(markdown),
      modTime: now,
      created: now,
    }
    return { type: 'put', rec, deleteOld: false }
  }).then((outcome) => outcome.rec)
}

// Test-only: wipe all data so suites start clean.
export async function _clear() {
  await write((notes, folders) => {
    notes.clear()
    folders.clear()
  })
}

export default localApi

import 'fake-indexeddb/auto'
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import localApi, { _clear, parseFrontMatter } from './localApi'

beforeEach(async () => {
  await _clear()
})

afterEach(() => {
  vi.restoreAllMocks()
})

describe('auth no-ops', () => {
  it('config reports no-auth', async () => {
    expect((await localApi.config()).data).toEqual({ noAuth: true })
  })
  it('login/logout/ping resolve', async () => {
    expect((await localApi.login('a', 'b')).data.status).toBe('ok')
    expect((await localApi.logout()).data.status).toBe('ok')
    expect((await localApi.ping()).data.status).toBe('ok')
  })
})

describe('note CRUD', () => {
  it('creates and reads a note round-trip', async () => {
    const created = (await localApi.createNote({ name: 'hello', content: '# Hi\nbody', tags: ['x', 'y'] })).data
    expect(created.path).toBe('hello.md')
    expect(created.name).toBe('hello')
    expect(created.content).toBe('# Hi\nbody')
    expect(created.tags).toEqual(['x', 'y'])

    const got = (await localApi.getNote('hello.md')).data
    expect(got.content).toBe('# Hi\nbody')
    expect(got.tags).toEqual(['x', 'y'])
  })

  it('auto-names with a timestamp when no name given', async () => {
    const created = (await localApi.createNote({ content: 'x' })).data
    expect(created.path).toMatch(/^\d{4}-\d{2}-\d{2}_\d{6}\.md$/)
  })

  it('avoids clobbering an existing path', async () => {
    const a = (await localApi.createNote({ name: 'dup', content: '1' })).data
    const b = (await localApi.createNote({ name: 'dup', content: '2' })).data
    expect(a.path).toBe('dup.md')
    expect(b.path).not.toBe('dup.md')
    expect((await localApi.getNote(a.path)).data.content).toBe('1')
    expect((await localApi.getNote(b.path)).data.content).toBe('2')
  })

  it('getNote 404s for missing path', async () => {
    await expect(localApi.getNote('nope.md')).rejects.toMatchObject({ response: { status: 404 } })
  })

  it('updates content and tags', async () => {
    await localApi.createNote({ name: 'n', content: 'old', tags: ['a'] })
    const upd = (await localApi.updateNote('n.md', { content: 'new', tags: ['b', 'c'] })).data
    expect(upd.content).toBe('new')
    expect(upd.tags).toEqual(['b', 'c'])
  })

  it('renames via update, dropping the old path', async () => {
    await localApi.createNote({ name: 'old', content: 'body', tags: ['t'] })
    const upd = (await localApi.updateNote('old.md', { content: 'body', tags: ['t'], rename: 'fresh' })).data
    expect(upd.path).toBe('fresh.md')
    await expect(localApi.getNote('old.md')).rejects.toMatchObject({ response: { status: 404 } })
    expect((await localApi.getNote('fresh.md')).data.content).toBe('body')
  })

  it('deletes a note', async () => {
    await localApi.createNote({ name: 'gone', content: 'x' })
    await localApi.deleteNote('gone.md')
    await expect(localApi.getNote('gone.md')).rejects.toMatchObject({ response: { status: 404 } })
  })

  it('empty note yields an empty preview (placeholder case)', async () => {
    await localApi.createNote({ name: 'blank', content: '' })
    const list = (await localApi.listNotes('')).data
    const blank = list.find(n => n.path === 'blank.md')
    expect(blank.preview).toBe('')
  })
})

describe('reactive (Proxy) inputs', () => {
  // Vue hands the adapter reactive arrays/objects, which are Proxies. IndexedDB
  // put() runs structured-clone, which throws "Proxy object could not be cloned"
  // on any Proxy. The adapter must normalise inputs to plain values before put.
  it('creates a note whose tags are a Proxy without DataCloneError', async () => {
    const reactiveTags = new Proxy(['a', 'b'], {})
    const created = (await localApi.createNote({ name: 'rx', content: 'body', tags: reactiveTags })).data
    expect(created.tags).toEqual(['a', 'b'])
    // and it actually round-trips back out of the store
    expect((await localApi.getNote('rx.md')).data.tags).toEqual(['a', 'b'])
  })

  it('updates a note with Proxy tags without DataCloneError', async () => {
    await localApi.createNote({ name: 'u', content: 'x', tags: ['old'] })
    const upd = (await localApi.updateNote('u.md', { content: 'x', tags: new Proxy(['new'], {}) })).data
    expect(upd.tags).toEqual(['new'])
    expect((await localApi.getNote('u.md')).data.tags).toEqual(['new'])
  })
})

describe('listNotes scoping & sort', () => {
  it('lists only notes in the requested folder', async () => {
    await localApi.createNote({ name: 'root', content: 'r' })
    await localApi.createNote({ name: 'inner', folder: 'docs', content: 'd' })
    const root = (await localApi.listNotes('')).data
    expect(root.map(n => n.path)).toEqual(['root.md'])
    const docs = (await localApi.listNotes('docs')).data
    expect(docs.map(n => n.path)).toEqual(['docs/inner.md'])
  })

  it('sorts by modTime descending', async () => {
    // Stub Date.now (not the timers — fake timers break fake-indexeddb's
    // async request callbacks) to give the two notes distinct modTimes.
    const now = vi.spyOn(Date, 'now')
    now.mockReturnValue(1000)
    await localApi.createNote({ name: 'first', content: '1' })
    now.mockReturnValue(2000)
    await localApi.createNote({ name: 'second', content: '2' })
    now.mockRestore()
    const list = (await localApi.listNotes('')).data
    expect(list.map(n => n.name)).toEqual(['second', 'first'])
  })
})

describe('moveNote', () => {
  it('moves a note into a folder', async () => {
    await localApi.createNote({ name: 'm', content: 'x' })
    const moved = (await localApi.moveNote('m.md', 'box')).data
    expect(moved.path).toBe('box/m.md')
    await expect(localApi.getNote('m.md')).rejects.toMatchObject({ response: { status: 404 } })
    expect((await localApi.getNote('box/m.md')).data.content).toBe('x')
  })

  it('409s on destination name collision', async () => {
    await localApi.createNote({ name: 'c', content: '1' })
    await localApi.createNote({ name: 'c', folder: 'box', content: '2' })
    await expect(localApi.moveNote('c.md', 'box')).rejects.toMatchObject({ response: { status: 409 } })
  })
})

describe('duplicateNote', () => {
  it('creates a (copy) in the same folder with same content and tags', async () => {
    const src = (await localApi.createNote({ name: 'orig', content: 'body', tags: ['a', 'b'] })).data
    const dup = (await localApi.duplicateNote(src.path)).data
    expect(dup.path).toBe('orig (copy).md')
    expect(dup.content).toBe('body')
    expect(dup.tags).toEqual(['a', 'b'])
    // original is untouched
    expect((await localApi.getNote('orig.md')).data.content).toBe('body')
  })

  it('de-collides with (copy 2), (copy 3)', async () => {
    const src = (await localApi.createNote({ name: 'note', content: 'x' })).data
    const d1 = (await localApi.duplicateNote(src.path)).data
    const d2 = (await localApi.duplicateNote(src.path)).data
    expect(d1.path).toBe('note (copy).md')
    expect(d2.path).toBe('note (copy 2).md')
  })

  it('duplicates into the same subfolder', async () => {
    await localApi.createFolder('docs')
    const src = (await localApi.createNote({ name: 'infolder', folder: 'docs', content: 'hi' })).data
    const dup = (await localApi.duplicateNote(src.path)).data
    expect(dup.path).toBe('docs/infolder (copy).md')
  })

  it('404s for a missing source', async () => {
    await expect(localApi.duplicateNote('nope.md')).rejects.toMatchObject({ response: { status: 404 } })
  })
})

describe('folder tree', () => {
  it('nests children and notes, excludes root notes', async () => {
    await localApi.createNote({ name: 'rootnote', content: 'r' })
    await localApi.createNote({ name: 'deep', folder: 'a/b', content: 'd' })
    const roots = (await localApi.listFolders()).data
    expect(roots.map(f => f.path)).toEqual(['a'])
    const a = roots[0]
    expect(a.children.map(c => c.path)).toEqual(['a/b'])
    expect(a.children[0].notes.map(n => n.path)).toEqual(['a/b/deep.md'])
    // root note is not part of the folder tree
    const allNoteNames = JSON.stringify(roots)
    expect(allNoteNames).not.toContain('rootnote')
  })

  it('keeps explicitly-created empty folders', async () => {
    await localApi.createFolder('empty')
    const roots = (await localApi.listFolders()).data
    expect(roots.map(f => f.path)).toContain('empty')
    expect(roots.find(f => f.path === 'empty').notes).toEqual([])
  })

  it('creates ancestor folders', async () => {
    await localApi.createFolder('x/y/z')
    const roots = (await localApi.listFolders()).data
    expect(roots[0].path).toBe('x')
    expect(roots[0].children[0].path).toBe('x/y')
    expect(roots[0].children[0].children[0].path).toBe('x/y/z')
  })
})

describe('renameFolder / deleteFolder', () => {
  it('rewrites descendant notes and subfolders on rename', async () => {
    await localApi.createNote({ name: 'n', folder: 'a', content: 'x' })
    await localApi.createNote({ name: 'm', folder: 'a/sub', content: 'y' })
    await localApi.renameFolder('a', 'z')
    await expect(localApi.getNote('a/n.md')).rejects.toMatchObject({ response: { status: 404 } })
    expect((await localApi.getNote('z/n.md')).data.content).toBe('x')
    expect((await localApi.getNote('z/sub/m.md')).data.content).toBe('y')
    const roots = (await localApi.listFolders()).data
    expect(roots.map(f => f.path)).toEqual(['z'])
    expect(roots[0].children.map(c => c.path)).toEqual(['z/sub'])
  })

  it('deletes a folder and everything under it', async () => {
    await localApi.createNote({ name: 'n', folder: 'a', content: 'x' })
    await localApi.createNote({ name: 'm', folder: 'a/sub', content: 'y' })
    await localApi.deleteFolder('a')
    await expect(localApi.getNote('a/n.md')).rejects.toMatchObject({ response: { status: 404 } })
    await expect(localApi.getNote('a/sub/m.md')).rejects.toMatchObject({ response: { status: 404 } })
    expect((await localApi.listFolders()).data).toEqual([])
  })
})

describe('moveFolder', () => {
  it('moves a folder subtree into another folder', async () => {
    await localApi.createNote({ name: 'x', folder: 'a', content: '1' })
    await localApi.createFolder('b')
    await localApi.moveFolder('a', 'b')
    expect((await localApi.getNote('b/a/x.md')).data.content).toBe('1')
    await expect(localApi.getNote('a/x.md')).rejects.toMatchObject({ response: { status: 404 } })
  })

  it('400s when moving a folder into itself', async () => {
    await localApi.createFolder('a/sub')
    await expect(localApi.moveFolder('a', 'a/sub')).rejects.toMatchObject({ response: { status: 400 } })
  })

  it('409s on destination collision', async () => {
    await localApi.createFolder('a')
    await localApi.createFolder('b/a')
    await expect(localApi.moveFolder('a', 'b')).rejects.toMatchObject({ response: { status: 409 } })
  })
})

describe('search', () => {
  beforeEach(async () => {
    await localApi.createNote({ name: 'apple', content: 'red fruit', tags: ['food'] })
    await localApi.createNote({ name: 'sky', content: 'blue thing', tags: ['nature'] })
  })

  it('matches body substring case-insensitively', async () => {
    const r = (await localApi.search('RED', '')).data
    expect(r.map(n => n.name)).toEqual(['apple'])
  })

  it('matches by tag', async () => {
    const r = (await localApi.search('', 'nature')).data
    expect(r.map(n => n.name)).toEqual(['sky'])
  })

  it('ANDs query and tag', async () => {
    expect((await localApi.search('blue', 'food')).data).toEqual([])
    expect((await localApi.search('blue', 'nature')).data.map(n => n.name)).toEqual(['sky'])
  })
})

describe('uploadNote', () => {
  it('imports a .md file, parsing front matter into tags', async () => {
    const file = new File(['---\ntags: [a, b]\n---\nhello body'], 'imported.md', { type: 'text/markdown' })
    const fd = new FormData()
    fd.append('file', file)
    const created = (await localApi.uploadNote(fd, '')).data
    expect(created.name).toBe('imported')
    expect(created.content).toBe('hello body')
    expect(created.tags).toEqual(['a', 'b'])
  })

  it('rejects non-md/txt extensions', async () => {
    const fd = new FormData()
    fd.append('file', new File(['x'], 'pic.png'))
    await expect(localApi.uploadNote(fd, '')).rejects.toMatchObject({ response: { status: 400 } })
  })

  it('rejects files over 1 MB', async () => {
    const fd = new FormData()
    fd.append('file', new File(['x'.repeat((1 << 20) + 1)], 'big.md'))
    await expect(localApi.uploadNote(fd, '')).rejects.toMatchObject({ response: { status: 413 } })
  })

  it('rejects binary (null-byte) content', async () => {
    const fd = new FormData()
    fd.append('file', new File([new Uint8Array([97, 0, 98])], 'bin.md'))
    await expect(localApi.uploadNote(fd, '')).rejects.toMatchObject({ response: { status: 400 } })
  })
})

describe('parseFrontMatter', () => {
  it('splits front matter tags from body', () => {
    expect(parseFrontMatter('---\ntags: [x, y]\n---\nbody')).toEqual({ tags: ['x', 'y'], body: 'body' })
  })
  it('returns whole content when no front matter', () => {
    expect(parseFrontMatter('# just text')).toEqual({ tags: [], body: '# just text' })
  })
})

import 'fake-indexeddb/auto'
import { describe, it, expect, beforeEach } from 'vitest'
import { openDB, allOf, write, _close } from './localVaultDb'
import { sha256Hex } from './sha256'
import { frontMatterPartWithTags } from './frontmatter'

// Start each test from a pristine database: close the cached connection first
// so deleteDatabase does not block on it.
beforeEach(async () => {
  await _close()
  const req = indexedDB.deleteDatabase('memodump')
  await new Promise((resolve, reject) => {
    req.onsuccess = () => resolve()
    req.onerror = () => reject(req.error)
  })
})

// Open the database with a v1 schema, mirroring the pre-migration layout.
async function openV1() {
  return new Promise((resolve, reject) => {
    const req = indexedDB.open('memodump', 1)
    req.onupgradeneeded = () => {
      const db = req.result
      if (!db.objectStoreNames.contains('notes')) db.createObjectStore('notes', { keyPath: 'path' })
      if (!db.objectStoreNames.contains('folders')) db.createObjectStore('folders', { keyPath: 'path' })
    }
    req.onsuccess = () => resolve(req.result)
    req.onerror = () => reject(req.error)
  })
}

describe('v1 -> v2 note-record migration', () => {
  it('adds canonical Markdown and a revision to every v1 record', async () => {
    const db = await openV1()
    const tx = db.transaction('notes', 'readwrite')
    tx.objectStore('notes').put({ path: 'a.md', content: 'hello body', tags: ['x', 'y'], modTime: 1, created: 1 })
    tx.objectStore('notes').put({ path: 'b.md', content: 'no tags', tags: [], modTime: 2, created: 2 })
    await new Promise((resolve, reject) => {
      tx.oncomplete = resolve
      tx.onerror = () => reject(tx.error)
    })
    db.close()

    await openDB() // opens at v2, migration runs

    const notes = await allOf('notes')
    const a = notes.find(n => n.path === 'a.md')
    const b = notes.find(n => n.path === 'b.md')

    expect(a.markdown).toBe('---\ntags: ["x", "y"]\n---\nhello body')
    expect(a.revision).toBe(sha256Hex(a.markdown))
    // the API projection is preserved
    expect(a.content).toBe('hello body')
    expect(a.tags).toEqual(['x', 'y'])

    expect(b.markdown).toBe('no tags')
    expect(b.revision).toBe(sha256Hex('no tags'))
    expect(b.tags).toEqual([])
  })

  it('leaves already-migrated records untouched', async () => {
    const db = await openV1()
    const tx = db.transaction('notes', 'readwrite')
    const existing = {
      path: 'c.md',
      markdown: '---\ncreated: 2024\ntags: ["a"]\n---\nbody',
      content: 'body',
      tags: ['a'],
      revision: sha256Hex('---\ncreated: 2024\ntags: ["a"]\n---\nbody'),
      modTime: 3,
    }
    tx.objectStore('notes').put(existing)
    await new Promise((resolve, reject) => {
      tx.oncomplete = resolve
      tx.onerror = () => reject(tx.error)
    })
    db.close()

    await openDB()
    const [rec] = await allOf('notes')
    expect(rec).toEqual(existing)
  })

  it('migration result matches frontMatterPartWithTags', async () => {
    const db = await openV1()
    const tx = db.transaction('notes', 'readwrite')
    tx.objectStore('notes').put({ path: 'd.md', content: 'body', tags: ['a', 'b'], modTime: 4 })
    await new Promise((resolve, reject) => {
      tx.oncomplete = resolve
      tx.onerror = () => reject(tx.error)
    })
    db.close()

    await openDB()
    const [rec] = await allOf('notes')
    const expectMarkdown = frontMatterPartWithTags('', ['a', 'b']) + 'body'
    expect(rec.markdown).toBe(expectMarkdown)
  })
})

describe('write / allOf helpers', () => {
  it('commit within one transaction', async () => {
    await write((notes) => notes.put({ path: 'x.md', markdown: 'x', revision: 'r', modTime: 1 }))
    const notes = await allOf('notes')
    expect(notes.map(n => n.path)).toEqual(['x.md'])
  })
})

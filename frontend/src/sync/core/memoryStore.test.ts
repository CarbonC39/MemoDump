import { describe, it, expect } from 'vitest'
import { MemoryStore } from './memoryStore'
import { StoreError } from './remoteStore'

const enc = new TextEncoder()
const dec = new TextDecoder()

describe('MemoryStore create/replace semantics', () => {
  it('implements the provider contract', async () => {
    const s = new MemoryStore()

    const { version: v1 } = await s.create('a', enc.encode('x'))
    expect(v1).toBeTruthy()

    await expect(s.create('a', enc.encode('y'))).rejects.toMatchObject({ kind: 'precondition-failed' })

    const { bytes, version } = await s.read('a')
    expect(dec.decode(bytes)).toBe('x')
    expect(version).toBe(v1)

    await expect(s.replace('a', enc.encode('z'), 'stale')).rejects.toMatchObject({ kind: 'precondition-failed' })
    const { version: v2 } = await s.replace('a', enc.encode('z'), v1)
    expect(v2).not.toBe(v1)

    await expect(s.read('missing')).rejects.toMatchObject({ kind: 'not-found' })
    await expect(s.replace('missing', enc.encode('z'), 'x')).rejects.toMatchObject({ kind: 'not-found' })
    await expect(s.remove('missing')).rejects.toMatchObject({ kind: 'not-found' })
  })
})

describe('MemoryStore delta cursor', () => {
  it('baseline, incremental resume, update and delete changes', async () => {
    const s = new MemoryStore()
    await s.create('a/1', enc.encode('1'))
    await s.create('b/1', enc.encode('x'))

    // Full baseline: current keys under prefix, all created.
    const page = await s.list('a')
    expect(page.changes.map(c => c.key)).toEqual(['a/1'])
    expect(page.changes[0].type).toBe('created')
    expect(page.syncCursor).toBeTruthy()

    // Resuming with the sync cursor returns no new changes.
    const again = await s.list('a', page.syncCursor)
    expect(again.changes).toHaveLength(0)

    // A new create, an update, and a removal appear as a typed delta.
    await s.create('a/2', enc.encode('2'))
    await s.replace('a/1', enc.encode('1b'), page.changes[0].version)
    await s.remove('b/1')
    const delta = await s.list('a', page.syncCursor)
    expect(delta.changes).toHaveLength(2)
    const seen = Object.fromEntries(delta.changes.map(c => [c.key, c.type]))
    expect(seen['a/2']).toBe('created')
    expect(seen['a/1']).toBe('updated')

    // The removed b/1 is visible under its own prefix as deleted.
    const deleted = await s.list('b', page.syncCursor)
    expect(deleted.changes).toHaveLength(1)
    expect(deleted.changes[0].type).toBe('deleted')
  })

  it('resets to a full baseline on an invalid cursor', async () => {
    const s = new MemoryStore()
    await s.create('a/1', enc.encode('1'))
    await s.create('a/2', enc.encode('2'))
    const reset = await s.list('a', 'garbage-cursor')
    expect(reset.changes.map(c => c.key)).toEqual(['a/1', 'a/2'])
    expect(reset.changes.every(c => c.type === 'created')).toBe(true)
  })
})

describe('MemoryStore fault injection', () => {
  it('fails exactly the next matching operation', async () => {
    const s = new MemoryStore()
    await s.create('k', enc.encode('v'))

    s.armFault('read', new StoreError('retryable-transport', 'flaky'))
    await expect(s.read('k')).rejects.toMatchObject({ kind: 'retryable-transport' })
    await expect(s.read('k')).resolves.toBeTruthy()

    s.armFault('create', new StoreError('rate-limit', 'slow down'))
    await expect(s.create('k2', enc.encode('v'))).rejects.toMatchObject({ kind: 'rate-limit' })
    await expect(s.create('k2', enc.encode('v'))).resolves.toBeTruthy()
  })
})

describe('MemoryStore test()', () => {
  it('reports capabilities asynchronously', async () => {
    const caps = await new MemoryStore().test()
    expect(caps).toEqual({ conditionalWrites: true, pagedListing: true, deltaCursor: true })
  })
})

describe('baseline pagination watermark', () => {
  it('keeps a first-scan watermark so a mid-scan modification is not lost', async () => {
    const s = new MemoryStore()
    for (let i = 0; i < 105; i++) {
      await s.create(`a/${String(i).padStart(3, '0')}`, enc.encode('x'))
    }
    const p1 = await s.list('a')
    expect(p1.changes).toHaveLength(100)
    expect(p1.nextCursor).toBeTruthy()
    const watermark = p1.syncCursor

    // Modify a key already scanned in page 1.
    await s.replace('a/000', enc.encode('changed'), p1.changes[0].version)

    // The continuation keeps the first scan's watermark and never reports the
    // modified key with its new version.
    const p2 = await s.list('a', p1.nextCursor)
    expect(p2.syncCursor).toBe(watermark)
    expect(p2.changes).toHaveLength(5)
    expect(p2.changes.some(c => c.key === 'a/000')).toBe(false)

    // A delta from the watermark reports the modification.
    const delta = await s.list('a', watermark)
    expect(delta.changes.some(c => c.key === 'a/000' && c.type === 'updated')).toBe(true)
  })

  it('resets when the delta cursor is beyond the current seq', async () => {
    const s = new MemoryStore()
    await s.create('a/1', enc.encode('1'))
    const page = await s.list('a', '999999999')
    expect(page.changes.map(c => c.key)).toEqual(['a/1'])
    expect(page.changes[0].type).toBe('created')
  })
})

describe('baseline cursor validation', () => {
  it('resets on an out-of-range watermark, an absent continuation key, or garbage', async () => {
    const s = new MemoryStore()
    for (let i = 0; i < 105; i++) {
      await s.create(`a/${String(i).padStart(3, '0')}`, enc.encode('x'))
    }
    const p1 = await s.list('a')
    const current = p1.syncCursor

    for (const bad of ['base:999999999:a/099', `base:${current}:nonexistent`, 'base:garbage:key']) {
      const reset = await s.list('a', bad)
      expect(reset.syncCursor).toBe(current)
      expect(reset.changes).toHaveLength(100) // first page of the reset baseline
    }
  })
})

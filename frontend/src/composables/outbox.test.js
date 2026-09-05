import 'fake-indexeddb/auto'
import { describe, it, expect, beforeEach } from 'vitest'
import { buildEntry, buildDeleteEntry, outboxPut, outboxDelete, outboxAll, outboxClear } from './outbox'

beforeEach(async () => {
  await outboxClear()
})

describe('outbox revision contract (Phase 0)', () => {
  it('captures baseRevision in a queued update', () => {
    const entry = buildEntry({
      editingNote: { value: { path: 'a.md', revision: 'r1', name: 'a', clientId: null } },
      editContent: { value: 'body' },
      editName: { value: 'a' },
      editTags: { value: [] },
      editFolder: { value: '' },
    })
    expect(entry.baseRevision).toBe('r1')
    expect(entry.op).toBe('update')
  })

  it('captures baseRevision in a queued delete', () => {
    const entry = buildDeleteEntry({ editingNote: { value: { path: 'a.md', revision: 'r1', name: 'a' } } })
    expect(entry.baseRevision).toBe('r1')
    expect(entry.op).toBe('delete')
  })

  it('coalesces consecutive offline edits keeping the earliest baseRevision and latest content', async () => {
    await outboxPut({ key: 'a.md', path: 'a.md', baseRevision: 'r0', content: 'v1', op: 'update', ts: 1 })
    await outboxPut({ key: 'a.md', path: 'a.md', baseRevision: 'r0', content: 'v2', op: 'update', ts: 2 })
    const all = await outboxAll()
    expect(all).toHaveLength(1)
    expect(all[0].content).toBe('v2')
    expect(all[0].baseRevision).toBe('r0')
  })

  it('a delete supersedes earlier edits for the same key', async () => {
    await outboxPut({ key: 'a.md', path: 'a.md', baseRevision: 'r0', content: 'v1', op: 'update', ts: 1 })
    await outboxPut(buildDeleteEntry({ editingNote: { value: { path: 'a.md', revision: 'r0', name: 'a' } } }))
    const all = await outboxAll()
    expect(all).toHaveLength(1)
    expect(all[0].op).toBe('delete')
  })

  it('keeps the conflict flag through coalescing', async () => {
    await outboxPut({ key: 'a.md', path: 'a.md', baseRevision: 'r0', content: 'v1', op: 'update', conflict: true, ts: 1 })
    await outboxPut({ key: 'a.md', path: 'a.md', baseRevision: 'r0', content: 'v2', op: 'update', ts: 2 })
    const [entry] = await outboxAll()
    expect(entry.conflict).toBe(true)
  })
})

describe('outbox conflict derivation (restart-safe)', () => {
  it('derives outboxHasConflict from persisted conflicted entries', async () => {
    const { outboxHasConflict } = await import('./outbox')
    expect(outboxHasConflict.value).toBe(false)
    await outboxPut({ key: 'a.md', path: 'a.md', baseRevision: 'r0', content: 'v1', op: 'update', conflict: true, ts: 1 })
    await new Promise(r => setTimeout(r, 0))
    expect(outboxHasConflict.value).toBe(true)
    await outboxDelete('a.md')
    await new Promise(r => setTimeout(r, 0))
    expect(outboxHasConflict.value).toBe(false)
  })
})

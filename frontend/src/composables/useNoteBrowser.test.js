import { describe, expect, it, vi } from 'vitest'
import { presentV2Note, useNoteBrowser } from './useNoteBrowser'

function page(items, nextCursor = null) {
  return { data: { items, nextCursor } }
}

describe('useNoteBrowser', () => {
  it('maps v2 notes into stable presentation fields', () => {
    expect(presentV2Note({
      id: 'notes/a.md',
      name: 'a',
      tags: ['x'],
      modifiedAt: 12,
      preview: '# Hello',
    })).toEqual({
      path: 'notes/a.md',
      name: 'a',
      tags: ['x'],
      modTime: 12,
      preview: '# Hello',
      hasCustomName: true,
      plainPreview: 'Hello',
    })
  })

  it('loads, sorts and persists the current page', async () => {
    const api = {
      listNotesV2: vi.fn().mockResolvedValue(page([
        { id: 'old.md', name: 'old', modifiedAt: 1, preview: '' },
        { id: 'new.md', name: 'new', modifiedAt: 2, preview: '' },
      ], 'next')),
      listFoldersV2: vi.fn().mockResolvedValue(page([])),
    }
    const storage = { getItem: vi.fn(), setItem: vi.fn() }
    const browser = useNoteBrowser({ api, storage })

    await browser.loadAll()
    expect(browser.sortedDisplayNotes.value.map(note => note.path)).toEqual(['new.md', 'old.md'])
    expect(browser.nextNotesCursor.value).toBe('next')

    await browser.setSort('modified-asc')
    expect(browser.sortedDisplayNotes.value.map(note => note.path)).toEqual(['old.md', 'new.md'])
    expect(storage.setItem).toHaveBeenCalledWith('memodump_sort', 'modified-asc')
    expect(api.listNotesV2).toHaveBeenLastCalledWith('', { sort: 'modified-asc' })
  })

  it('appends the next page using the opaque cursor', async () => {
    const api = {
      listNotesV2: vi.fn()
        .mockResolvedValueOnce(page([{ id: 'a.md', name: 'a' }], 'cursor-1'))
        .mockResolvedValueOnce(page([{ id: 'b.md', name: 'b' }])),
      listFoldersV2: vi.fn().mockResolvedValue(page([])),
    }
    const browser = useNoteBrowser({ api, storage: null })

    await browser.loadAll()
    await browser.loadMoreNotes()

    expect(api.listNotesV2).toHaveBeenLastCalledWith('', {
      cursor: 'cursor-1',
      sort: 'modified-desc',
    })
    expect(browser.displayNotes.value.map(note => note.path)).toEqual(['a.md', 'b.md'])
    expect(browser.nextNotesCursor.value).toBeNull()
  })

  it('loads direct folder children only when expanded', async () => {
    const api = {
      listNotesV2: vi.fn().mockResolvedValue(page([{ id: 'work/a.md', name: 'a' }])),
      listFoldersV2: vi.fn()
        .mockResolvedValueOnce(page([{ id: 'work', name: 'work', hasChildren: true }]))
        .mockResolvedValueOnce(page([{ id: 'work/sub', name: 'sub', hasChildren: false }])),
    }
    const browser = useNoteBrowser({ api, storage: null })

    await browser.loadAll()
    await browser.loadFolderNode('work')

    expect(browser.folders.value[0].children[0].path).toBe('work/sub')
    expect(browser.folders.value[0].notes[0].path).toBe('work/a.md')
    expect(browser.folders.value[0].loaded).toBe(true)
  })

  it('loads all folder destinations on demand for the picker', async () => {
    const api = {
      listNotesV2: vi.fn().mockResolvedValue(page([])),
      listFoldersV2: vi.fn()
        .mockResolvedValueOnce(page([{ id: 'work', name: 'work', hasChildren: true }]))
        .mockResolvedValueOnce(page([{ id: 'work/deep', name: 'deep', hasChildren: false }])),
    }
    const browser = useNoteBrowser({ api, storage: null })

    await browser.loadAll()
    await browser.loadFolderTreeForPicker()

    expect(browser.flatFoldersForPicker.value.map(folder => folder.path))
      .toEqual(['work', 'work/deep'])
  })
})

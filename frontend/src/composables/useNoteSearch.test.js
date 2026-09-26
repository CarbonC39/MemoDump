import { afterEach, describe, expect, it, vi } from 'vitest'
import { useNoteSearch } from './useNoteSearch'

afterEach(() => {
  vi.useRealTimers()
})

describe('useNoteSearch', () => {
  it('debounces queries and maps results', async () => {
    vi.useFakeTimers()
    const api = {
      searchV2: vi.fn().mockResolvedValue({
        data: { items: [{ id: 'a.md', name: 'a', preview: '**body**' }] },
      }),
    }
    const search = useNoteSearch({ api })
    search.searchQuery.value = 'body'
    search.doSearch()

    expect(api.searchV2).not.toHaveBeenCalled()
    await vi.advanceTimersByTimeAsync(300)

    expect(api.searchV2).toHaveBeenCalledWith('body', '')
    expect(search.searchResults.value[0].plainPreview).toBe('body')
  })

  it('ignores a stale response after a newer query', async () => {
    vi.useFakeTimers()
    let resolveFirst
    const first = new Promise(resolve => { resolveFirst = resolve })
    const api = {
      searchV2: vi.fn()
        .mockReturnValueOnce(first)
        .mockResolvedValueOnce({ data: { items: [{ id: 'new.md', name: 'new' }] } }),
    }
    const search = useNoteSearch({ api, debounceMs: 1 })

    search.searchQuery.value = 'old'
    search.doSearch()
    await vi.advanceTimersByTimeAsync(1)
    search.searchQuery.value = 'new'
    search.doSearch()
    await vi.advanceTimersByTimeAsync(1)

    resolveFirst({ data: { items: [{ id: 'old.md', name: 'old' }] } })
    await Promise.resolve()
    expect(search.searchResults.value.map(note => note.path)).toEqual(['new.md'])
  })

  it('clears results and invalidates pending work for an empty query', async () => {
    vi.useFakeTimers()
    const api = { searchV2: vi.fn() }
    const search = useNoteSearch({ api })
    search.searchResults.value = [{ path: 'old.md' }]
    search.searchQuery.value = 'pending'
    search.doSearch()
    search.searchQuery.value = ''
    search.doSearch()
    await vi.runAllTimersAsync()

    expect(api.searchV2).not.toHaveBeenCalled()
    expect(search.searchResults.value).toEqual([])
  })
})

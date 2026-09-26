import { onBeforeUnmount, ref } from 'vue'
import { presentV2Note } from './useNoteBrowser'

export function useNoteSearch({ api, debounceMs = 300 } = {}) {
  if (!api) throw new Error('useNoteSearch requires an API implementation')
  const searchOpen = ref(false)
  const searchResults = ref([])
  const searchQuery = ref('')
  const searchTag = ref('')
  let timer = null
  let generation = 0

  function cancelPending() {
    generation++
    if (timer) clearTimeout(timer)
    timer = null
  }

  function doSearch() {
    cancelPending()
    if (!searchQuery.value && !searchTag.value) {
      searchResults.value = []
      return
    }

    const requestGeneration = generation
    timer = setTimeout(async () => {
      timer = null
      try {
        const response = await api.searchV2(searchQuery.value, searchTag.value)
        if (requestGeneration !== generation) return
        searchResults.value = response.data.items.map(presentV2Note)
      } catch (_) {
        if (requestGeneration === generation) searchResults.value = []
      }
    }, debounceMs)
  }

  onBeforeUnmount(cancelPending)

  return {
    searchOpen,
    searchResults,
    searchQuery,
    searchTag,
    doSearch,
    cancelPending,
  }
}

import { ref, watch, onMounted, onBeforeUnmount } from 'vue'

// Autosave + localStorage-draft fallback. iOS Safari/PWA frequently suspends
// or evicts a backgrounded tab without ever firing beforeunload, so edits are
// saved debounced after each change and immediately when the tab is hidden or
// torn down. If a save fails (e.g. backgrounded PWA with no connectivity) the
// draft is persisted to localStorage so the next launch can restore it.
export function useAutosave({ editingNote, isDirty, editContent, editName, editTags, editFolder, saveNote }) {
  // Draft restored banner
  const showDraftRestoredBanner = ref(false)

  let autosaveTimer = null
  let autosaving = false

  function scheduleAutosave() {
    if (autosaveTimer) clearTimeout(autosaveTimer)
    autosaveTimer = setTimeout(() => {
      if (isDirty.value && editingNote.value && !autosaving) {
        runAutosave()
      }
    }, 3000)
  }

  async function runAutosave() {
    autosaving = true
    try {
      await saveNote()
    } finally {
      autosaving = false
    }
  }

  watch(isDirty, (dirty) => {
    if (dirty) scheduleAutosave()
  })

  function persistDraftToLocalStorage() {
    try {
      localStorage.setItem('memodump_draft', JSON.stringify({
        content: editContent.value,
        name: editName.value,
        tags: editTags.value,
        folder: editFolder.value,
        path: editingNote.value?.path || '',
      }))
    } catch (_) {}
  }

  async function flushSaveOrFallback() {
    if (!isDirty.value || !editingNote.value || autosaving) return
    autosaving = true
    try {
      await saveNote({ silent: true })
    } catch (_) {
      // Network unavailable — fall back to the localStorage draft so the next
      // launch can restore it.
      persistDraftToLocalStorage()
    } finally {
      autosaving = false
    }
  }

  function handleVisibilityChange() {
    if (document.hidden) flushSaveOrFallback()
  }

  function handlePageHide() {
    flushSaveOrFallback()
  }

  function handleBeforeUnload(e) {
    if (isDirty.value) {
      e.preventDefault()
      e.returnValue = ''
    }
  }

  onMounted(() => {
    window.addEventListener('beforeunload', handleBeforeUnload)
    document.addEventListener('visibilitychange', handleVisibilityChange)
    window.addEventListener('pagehide', handlePageHide)
  })

  onBeforeUnmount(() => {
    window.removeEventListener('beforeunload', handleBeforeUnload)
    document.removeEventListener('visibilitychange', handleVisibilityChange)
    window.removeEventListener('pagehide', handlePageHide)
    if (autosaveTimer) clearTimeout(autosaveTimer)
  })

  return { showDraftRestoredBanner }
}

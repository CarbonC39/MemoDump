import { ref, computed, watch, onMounted, onBeforeUnmount } from 'vue'
import { outboxPut, outboxAll, outboxDelete, outboxCount, buildEntry } from './outbox.js'

const AUTOSAVE_DELAY_MS = 5000
const REPLAY_GAP_MS = 50
const PING_INTERVAL_MS = 30000

// Autosave with an offline-resilient outbox. Online edits are pushed to the
// server debounced and silently. When a save can't reach the server the write
// is queued in IndexedDB and replayed on reconnect. The UI shows a calm static
// status icon instead of any alert/animation.
export function useAutosave({ editingNote, isDirty, editContent, editName, editTags, editFolder, saveNote, reload, ping }) {
  const showDraftRestoredBanner = ref(false)
  const online = ref(typeof navigator === 'undefined' ? true : navigator.onLine)
  const saveError = ref(null)

  const saveStatus = computed(() => {
    if (outboxCount.value > 0 || !online.value) return 'offline'
    if (saveError.value) return 'error'
    if (isDirty.value) return 'dirty'
    return 'synced'
  })

  let autosaveTimer = null
  let autosaving = false
  let replaying = false

  async function enqueueCurrent() {
    try { await outboxPut(buildEntry({ editingNote, editContent, editName, editTags, editFolder })) } catch (_) {}
  }

  function scheduleAutosave() {
    if (autosaveTimer) clearTimeout(autosaveTimer)
    autosaveTimer = setTimeout(() => {
      if (isDirty.value && editingNote.value && !autosaving) runAutosave()
    }, AUTOSAVE_DELAY_MS)
  }

  async function runAutosave() {
    autosaving = true
    try {
      // Offline or already have pending writes -> just update the queue, no network spam.
      if (!online.value || outboxCount.value > 0) { await enqueueCurrent(); return }
      await saveNote()
    } finally {
      autosaving = false
    }
  }

  watch(isDirty, (dirty) => { if (dirty) { saveError.value = null; scheduleAutosave() } })

  async function flushSaveOrFallback() {
    if (!isDirty.value || !editingNote.value || autosaving) return
    autosaving = true
    try {
      if (!online.value) { await enqueueCurrent(); return }
      await saveNote({ silent: true })
    } catch (e) {
      if (!e.response) await enqueueCurrent()
    } finally {
      autosaving = false
    }
  }

  function delay(ms) { return new Promise(r => setTimeout(r, ms)) }

  async function replayAll() {
    if (replaying) return
    if (typeof navigator !== 'undefined' && !navigator.onLine) return
    let entries
    try { entries = await outboxAll() } catch (_) { return }
    if (!entries.length) return
    replaying = true
    try {
      for (const entry of entries) {
        try {
          await saveNote({ replay: entry, skipReload: true })
        } catch (e) {
          if (e?.response?.status === 401) return   // session expired — stop, login flow takes over
          break                                    // still offline/unreachable — retry later
        }
        try { await outboxDelete(entry.key) } catch (_) {}
        await delay(REPLAY_GAP_MS)
      }
      try { await reload() } catch (_) {}
      saveError.value = null
    } finally {
      replaying = false
    }
  }

  function onWindowOnline() { online.value = true; replayAll() }
  function onWindowOffline() { online.value = false }
  let pingTimer = null

  function handleVisibilityChange() { if (document.hidden) flushSaveOrFallback() }
  function handlePageHide() { flushSaveOrFallback() }
  function handleBeforeUnload(e) {
    if (isDirty.value) { e.preventDefault(); e.returnValue = '' }
  }

  onMounted(() => {
    window.addEventListener('beforeunload', handleBeforeUnload)
    document.addEventListener('visibilitychange', handleVisibilityChange)
    window.addEventListener('pagehide', handlePageHide)
    window.addEventListener('online', onWindowOnline)
    window.addEventListener('offline', onWindowOffline)
    pingTimer = setInterval(async () => {
      if (outboxCount.value > 0 && navigator.onLine) {
        try { await ping(); replayAll() } catch (_) {}
      }
    }, PING_INTERVAL_MS)
  })

  onBeforeUnmount(() => {
    window.removeEventListener('beforeunload', handleBeforeUnload)
    document.removeEventListener('visibilitychange', handleVisibilityChange)
    window.removeEventListener('pagehide', handlePageHide)
    window.removeEventListener('online', onWindowOnline)
    window.removeEventListener('offline', onWindowOffline)
    if (autosaveTimer) clearTimeout(autosaveTimer)
    if (pingTimer) clearInterval(pingTimer)
  })

  return { showDraftRestoredBanner, saveStatus, saveError, replayAll }
}

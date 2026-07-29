import { ref, computed, onMounted, onBeforeUnmount } from 'vue'
import { outboxCount, outboxAll, outboxDelete } from './outbox.js'

const PING_INTERVAL_MS = 30000

// Manual-save-only mode: no debounced autosave, no visibility-change flush.
// The outbox is the safety net — writes that fail (network down / server
// unreachable) are queued in IndexedDB and replayed on reconnect via the
// periodic ping timer. beforeunload warns if there are unsaved changes.
export function useAutosave({ editingNote, isDirty, saveNote, reload, ping, saveError = ref(null) }) {
  const showDraftRestoredBanner = ref(false)
  const online = ref(typeof navigator === 'undefined' ? true : navigator.onLine)
  const saveStatus = computed(() => {
    if (outboxCount.value > 0 || !online.value) return 'offline'
    if (saveError.value) return 'error'
    if (isDirty.value) return 'dirty'
    return 'synced'
  })

  let replaying = false

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
          await saveNote({ replay: entry })
        } catch (e) {
          if (e?.response?.status === 401) return
          break
        }
        try { await outboxDelete(entry.key) } catch (_) {}
        await delay(50)
      }
      try { await reload() } catch (_) {}
      saveError.value = null
    } finally {
      replaying = false
    }
  }

  // Browser close warning — pure client-side, no network request
  function handleBeforeUnload(e) {
    if (isDirty.value) { e.preventDefault(); e.returnValue = '' }
  }

  // Online/offline tracking for saveStatus display
  function onOnline() { online.value = true }
  function onOffline() { online.value = false }

  let pingTimer = null

  onMounted(() => {
    window.addEventListener('beforeunload', handleBeforeUnload)
    window.addEventListener('online', onOnline)
    window.addEventListener('offline', onOffline)
    pingTimer = setInterval(async () => {
      if (outboxCount.value > 0 && navigator.onLine) {
        try { await ping(); replayAll() } catch (_) {}
      }
    }, PING_INTERVAL_MS)
  })

  onBeforeUnmount(() => {
    window.removeEventListener('beforeunload', handleBeforeUnload)
    window.removeEventListener('online', onOnline)
    window.removeEventListener('offline', onOffline)
    if (pingTimer) clearInterval(pingTimer)
  })

  return { showDraftRestoredBanner, saveStatus, saveError, replayAll }
}

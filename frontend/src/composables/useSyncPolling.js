// Lightweight automatic-sync status poller (R5.4). The backend can change local
// Markdown without a frontend request, so a connected server/Wails build polls
// the redacted /api/sync/status every 30 seconds while the document is visible.
// It never calls /api/sync/run, never fetches recovery content on a poll (the
// status carries only the count), and does not exist in the local/IndexedDB
// build. When an automatic attempt completes, the visible list is refreshed and
// the open note is re-read: a clean buffer adopts the new content, a deleted
// note closes, and a dirty buffer is never replaced — only a notice is shown.
import { ref } from 'vue'
import { applyLightweightStatus } from './useSyncSettings'

const DEFAULT_INTERVAL_MS = 30000
const AUTO_TRIGGERS = ['startup', 'periodic', 'retry', 'enable']

export function useSyncPolling({
  api,
  editor,
  isLocalBuild = false,
  intervalMs = DEFAULT_INTERVAL_MS,
  isVisible = () => !document.hidden,
  setIntervalFn = setInterval,
  clearIntervalFn = clearInterval,
  addVisibilityListener = (fn) => document.addEventListener('visibilitychange', fn),
  removeVisibilityListener = (fn) => document.removeEventListener('visibilitychange', fn),
  onAutoSync = () => {},
  onNoteClosed = () => {},
  onNotice = () => {},
  onRecoveryChanged = () => {},
} = {}) {
  const { editingNote, isDirty, isSaving, loadDocument, clearDocument } = editor
  const lastCompleted = ref(null)
  let timer = null
  let started = false
  let running = false
  let recoveryCount = null

  function startTimer() {
    if (timer !== null) return
    timer = setIntervalFn(poll, intervalMs)
  }

  function stopTimer() {
    if (timer !== null) {
      clearIntervalFn(timer)
      timer = null
    }
  }

  function onVisibilityChange() {
    if (isVisible()) {
      startTimer()
      poll() // refresh once when visibility returns
    } else {
      stopTimer()
    }
  }

  // start begins polling: it registers the visibility listener and polls while
  // the document is visible. It is idempotent.
  function start() {
    if (isLocalBuild || started) return
    started = true
    addVisibilityListener(onVisibilityChange)
    if (isVisible()) {
      startTimer()
    }
  }

  // stop cancels the timer and removes the visibility listener.
  function stop() {
    if (!started) return
    started = false
    stopTimer()
    removeVisibilityListener(onVisibilityChange)
  }

  async function poll() {
    if (isLocalBuild || running) return
    running = true
    try {
      const resp = await api.syncStatus()
      const d = resp.data || {}
      // Keep the settings panel fresh (running/next-run/paused) without
      // downloading recovery content on every poll.
      applyLightweightStatus(d)
      const completed = d.lastCompleted
      const autoTrigger = AUTO_TRIGGERS.includes(d.lastTrigger)
      if (completed && completed !== lastCompleted.value) {
        lastCompleted.value = completed
        if (autoTrigger) {
          onAutoSync()
          await refreshOpenNote()
        }
      }
      const rc = d.recoveryCount
      if (rc !== undefined && rc !== recoveryCount) {
        const increased = recoveryCount !== null && rc > recoveryCount
        recoveryCount = rc
        if (increased) onRecoveryChanged(rc)
      }
    } catch (_) {
      // A transient poll failure is harmless; the next interval retries.
    } finally {
      running = false
    }
  }

  // refreshOpenNote re-reads the open note. A clean buffer adopts the fetched
  // revision/content; a deleted note closes; a dirty/saving/conflicting buffer
  // is never replaced or closed — only a notice is shown, and the existing
  // revision CAS prevents overwrite. If the user switches notes while the
  // request is in flight, the stale response is discarded so note A's content
  // can never be loaded into note B.
  async function refreshOpenNote() {
    const instance = editingNote.value
    if (!instance || !instance.path) return
    const path = instance.path
    let fetched
    try {
      const resp = await api.getNote(path)
      fetched = resp.data
    } catch (e) {
      if (e?.response?.status === 404) {
        if (editingNote.value !== instance) return // user switched away
        if (isDirty.value || isSaving.value) {
          onNotice('changed')
        } else {
          clearDocument()
          onNoteClosed(path)
        }
      }
      return
    }
    if (editingNote.value !== instance) return // user switched during the request
    if (!fetched || fetched.revision === instance.revision) return
    if (isDirty.value || isSaving.value) {
      onNotice('changed')
      return
    }
    loadDocument(fetched)
  }

  return { poll, start, stop, refreshOpenNote, lastCompleted }
}

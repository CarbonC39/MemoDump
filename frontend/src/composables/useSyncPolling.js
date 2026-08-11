// Lightweight automatic-sync status poller (R5.4). The Wails backend can change
// local Markdown without a frontend request, so a connected Wails runtime polls
// the redacted /api/sync/status every 30 seconds while the document is visible.
// The Pure frontend/PWA build polls the same redacted shape, served in-page by
// the R6.5 browser service — never an HTTP call to MemoDump itself. It never
// calls /api/sync/run, never fetches recovery content on a poll (the status
// carries only the count), and only runs when the runtime reports cloud sync
// available (R6.0/R6.5) — never on the CLI Web server. When an automatic
// attempt completes, the visible list is refreshed and the open note is re-read:
// a clean buffer adopts the new content, a deleted note closes, and a dirty
// buffer is never replaced — only a notice is shown.
import { ref } from 'vue'
import { applyLightweightStatus, getSyncSettings } from './useSyncSettings'

const DEFAULT_INTERVAL_MS = 30000
const AUTO_TRIGGERS = ['startup', 'periodic', 'retry', 'enable']

export function useSyncPolling({
  api,
  editor,
  available = false,
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
  const { editingNote, isDirty, isSaving, loadDocument, clearDocument, isOffline, isConflict } = editor
  const lastCompleted = ref(null)
  let timer = null
  let started = false
  let running = false

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
  // the document is visible. It is idempotent and does nothing when the runtime
  // has no cloud-sync surface (CLI Web server) or none yet (Pure frontend/PWA).
  function start() {
    if (!available || started) return
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
    if (!available || running) return
    running = true
    try {
      const resp = await api.syncStatus()
      const d = resp.data || {}
      // Capture the count BEFORE the lightweight status write overwrites it, so
      // the very first background addition (e.g. the 10s startup run creating a
      // recovery copy seen by the first 30s poll) still triggers a detail
      // refresh instead of being treated as a baseline.
      const beforeRecoveryCount = getSyncSettings().recoveryCount
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
      if (rc !== undefined && rc > beforeRecoveryCount) {
        onRecoveryChanged(rc)
      }
    } catch (_) {
      // A transient poll failure is harmless; the next interval retries.
    } finally {
      running = false
    }
  }

  // refreshOpenNote re-reads the open note. A clean buffer adopts the fetched
  // revision/content; a deleted note closes. A dirty, saving, offline (unsaved
  // outbox) or conflicting buffer is never replaced or closed — only a notice
  // is shown, and the existing revision CAS prevents overwrite. If the user
  // switches notes while the request is in flight, the stale response is
  // discarded so note A's content can never be loaded into note B.
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
        if (isProtectedBuffer()) {
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
    if (isProtectedBuffer()) {
      onNotice('changed')
      return
    }
    loadDocument(fetched)
  }

  // isProtectedBuffer reports whether the open buffer holds unsaved work that a
  // sync refresh must never replace or close: a dirty or in-flight save, or an
  // offline outbox / conflict state even when the editor looks clean.
  function isProtectedBuffer() {
    if (isDirty.value || isSaving.value) return true
    if (isOffline && isOffline.value) return true
    if (isConflict && isConflict.value) return true
    return false
  }

  return { poll, start, stop, refreshOpenNote, lastCompleted }
}

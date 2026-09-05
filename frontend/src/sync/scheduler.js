// R6.6 page-lifetime sync scheduler for the Pure frontend/PWA build. One
// in-memory scheduler owned by the app instance, mirroring the reviewed R5 Go
// behavior without its goroutines: a connected startup attempt 10s after start,
// an immediate coalesced attempt on a successful Enable, the next ordinary run
// five minutes after completion, the exact transient backoff 1m/2m/5m/10m/30m
// (honoring a larger provider Retry-After), and a permanent pause on
// configuration/auth/quota/corruption/mismatch failures until a successful
// manual run, a successful Enable, or a page restart.
//
// The scheduler owns ONE resettable timeout and ONE in-process single-flight
// attempt; the Web Lock supplies cross-tab exclusion. When the document is
// hidden the timeout is cancelled but the in-memory due time is retained; when
// visible again the attempt runs immediately if overdue or re-arms the remaining
// delay. stop() cancels the timeout and the in-flight attempt via AbortSignal
// and waits for the promise — no work is promised after page/PWA closure.
//
// Time sources, timers, visibility, and the run function are injected so tests
// drive everything deterministically (fake timers, a fake run, a switchable
// visibility flag) without time.Sleep.

export const STARTUP_DELAY_MS = 10 * 1000
export const PERIODIC_EVERY_MS = 5 * 60 * 1000
export const BACKOFF_MS = [
  1 * 60 * 1000, 2 * 60 * 1000, 5 * 60 * 1000, 10 * 60 * 1000, 30 * 60 * 1000,
]

const TRIGGER_STARTUP = 'startup'
const TRIGGER_PERIODIC = 'periodic'
const TRIGGER_RETRY = 'retry'
const TRIGGER_ENABLE = 'enable'

function defaultVisible() {
  return typeof document === 'undefined' ? true : !document.hidden
}

// createSyncScheduler builds one scheduler. run(trigger, signal) must resolve
// with a classification: { kind: 'disabled' } (not connected -> idle),
// { kind: 'permanent', pauseReason } (pause until manual/Enable/restart),
// { kind: 'retryable', retryAfter } (backoff in seconds), or
// { kind: 'success' } (next ordinary interval).
export function createSyncScheduler({
  run,
  now = () => Date.now(),
  setTimeoutFn = setTimeout,
  clearTimeoutFn = clearTimeout,
  isVisible = defaultVisible,
  addVisibilityListener = (fn) => {
    if (typeof document !== 'undefined') document.addEventListener('visibilitychange', fn)
  },
  removeVisibilityListener = (fn) => {
    if (typeof document !== 'undefined') document.removeEventListener('visibilitychange', fn)
  },
  onAttemptDone = () => {},
} = {}) {
  let timerId = null
  let due = 0 // 0 = idle (wait for a wake only)
  let trigger = TRIGGER_ENABLE
  let paused = false
  let pauseReason = ''
  let failures = 0
  let running = false
  let pendingWake = false
  let started = false
  let stopped = false
  let inflight = null
  const abort = new AbortController()

  function clearTimer() {
    if (timerId !== null) {
      clearTimeoutFn(timerId)
      timerId = null
    }
  }

  // arm resets the single timeout from the current state. It never fires a
  // timer while an attempt is in flight (the attempt's completion re-arms) or
  // while the document is hidden (the due time is retained in memory).
  function arm() {
    clearTimer()
    if (stopped || running) return
    if (pendingWake) {
      pendingWake = false
      fire(TRIGGER_ENABLE)
      return
    }
    if (paused || due === 0) return
    if (!isVisible()) return
    timerId = setTimeoutFn(() => {
      timerId = null
      fire(trigger)
    }, Math.max(0, due - now()))
  }

  // start arms the connected startup attempt. Idempotent.
  function start() {
    if (started || stopped) return
    started = true
    addVisibilityListener(onVisibility)
    due = now() + STARTUP_DELAY_MS
    trigger = TRIGGER_STARTUP
    arm()
  }

  // stop cancels the timeout, aborts the in-flight attempt, and waits for it.
  // Returns a promise that resolves once no work is pending.
  async function stop() {
    if (!started || stopped) return
    stopped = true
    clearTimer()
    removeVisibilityListener(onVisibility)
    abort.abort()
    if (inflight) {
      try {
        await inflight
      } catch (_) {}
    }
  }

  // wake requests an immediate coalesced attempt (a successful Enable): it
  // clears a permanent pause and runs right away (or immediately after the
  // current attempt, coalesced).
  function wake() {
    paused = false
    pauseReason = ''
    pendingWake = true
    arm()
  }

  // reset leaves the scheduler idle (Disable/Reset). A successful Enable wakes
  // it again.
  function reset() {
    pendingWake = false
    paused = false
    pauseReason = ''
    failures = 0
    due = 0
    trigger = TRIGGER_ENABLE
    arm()
  }

  function onVisibility() {
    if (isVisible()) arm() // overdue -> immediate; otherwise re-arm the remainder
    else clearTimer() // retain the in-memory due time
  }

  // noteAttempt feeds an attempt that ran OUTSIDE the scheduler (the enable
  // first cycle and manual runs run inline, under their own lock) into the
  // schedule so automatic timing, backoff, and pause stay consistent.
  function noteAttempt(classification) {
    apply(classification)
    arm()
  }

  // runNow runs one attempt immediately (manual Run). It first cancels any
  // armed startup/periodic/retry timeout so the manual request can never
  // collide with the timer firing and start a SECOND attempt; single-flight
  // then coalesces onto the in-flight promise. A stopped scheduler refuses.
  function runNow(triggerName) {
    if (stopped) return Promise.resolve({ kind: 'disabled' })
    clearTimer()
    if (running) return inflight
    return fire(triggerName)
  }

  function fire(triggerName) {
    // Defensive re-entry guard: a timer that fires mid-attempt (for example
    // across the manual request's own expiry point) must coalesce onto the
    // in-flight promise, never overwrite it with a second concurrent attempt.
    if (running) return inflight
    running = true
    inflight = (async () => {
      let classification
      try {
        classification = await run(triggerName, abort.signal)
      } catch (e) {
        // A rejected run must never become an unhandled rejection or a zero-delay
        // re-fire loop: classify it as a permanent 'error' pause.
        classification = { kind: 'permanent', pauseReason: 'error' }
      }
      apply(classification)
      return classification
    })().finally(() => {
      running = false
      inflight = null
      arm()
      // No UI work is promised after stop (page closure): the abort of an
      // in-flight attempt must not refresh an unmounted page.
      if (!stopped) onAttemptDone()
    })
    return inflight
  }

  function apply(classification) {
    if (!classification || classification.kind === 'disabled') {
      // Not connected: idle until a wake (Enable).
      paused = false
      pauseReason = ''
      due = 0
      trigger = TRIGGER_ENABLE
      return
    }
    if (classification.kind === 'permanent') {
      // Pause automatic attempts until a successful manual run or Enable.
      paused = true
      pauseReason = classification.pauseReason || 'error'
      due = 0
      return
    }
    const synced = !!(classification.result && classification.result.Synced)
    if (paused && !synced) {
      // During a permanent pause ONLY a genuinely synced result changes the
      // schedule. A retryable or locked manual outcome must leave the pause
      // intact — arming a backoff here would only produce a nextRun that arm()
      // (paused) never executes.
      return
    }
    if (classification.kind === 'retryable') {
      // Backoff, capped at 30m, honoring a larger provider Retry-After.
      let d = BACKOFF_MS[Math.min(failures, BACKOFF_MS.length - 1)]
      if (classification.retryAfter && classification.retryAfter * 1000 > d) {
        d = classification.retryAfter * 1000
      }
      failures++
      due = now() + d
      trigger = TRIGGER_RETRY
      return
    }
    // success: only a genuinely SYNCED run clears a permanent pause. A locked,
    // cancelled, or blocked outcome never unpauses — a user stuck on a
    // permission pause who hits Run now while another tab holds the lock must
    // stay paused, not have the pause silently dropped.
    if (synced) {
      paused = false
      pauseReason = ''
      failures = 0
    }
    if (!paused) {
      due = now() + PERIODIC_EVERY_MS
      trigger = TRIGGER_PERIODIC
    }
  }

  function status() {
    return {
      active: started && !stopped,
      paused,
      pauseReason,
      nextRun: !stopped && due > 0 ? new Date(due).toISOString() : null,
      syncRunning: running,
      failures,
    }
  }

  return { start, stop, wake, reset, runNow, noteAttempt, status }
}

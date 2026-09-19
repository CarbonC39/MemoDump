import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { createSyncScheduler, STARTUP_DELAY_MS, PERIODIC_EVERY_MS, BACKOFF_MS } from './scheduler'

const SUCCESS = { kind: 'success', result: { Synced: true } }
const DISABLED = { kind: 'disabled' }

function makeScheduler({ run, visible = true } = {}) {
  let visibleState = visible
  let visibilityHandler = null
  const onAttemptDone = vi.fn()
  const s = createSyncScheduler({
    run,
    now: () => Date.now(),
    setTimeoutFn: setTimeout,
    clearTimeoutFn: clearTimeout,
    isVisible: () => visibleState,
    addVisibilityListener: (fn) => { visibilityHandler = fn },
    removeVisibilityListener: (fn) => { visibilityHandler = null },
    onAttemptDone,
  })
  return {
    s,
    onAttemptDone,
    setVisible(v) { visibleState = v; if (visibilityHandler) visibilityHandler() },
  }
}

describe('R6.6 page-lifetime scheduler', () => {
  beforeEach(() => {
    vi.useFakeTimers()
  })
  afterEach(() => {
    vi.useRealTimers()
  })

  it('runs the connected startup attempt after 10s, then the ordinary interval', async () => {
    const run = vi.fn().mockResolvedValue(SUCCESS)
    const { s, onAttemptDone } = makeScheduler({ run })
    s.start()
    expect(run).not.toHaveBeenCalled()

    await vi.advanceTimersByTimeAsync(STARTUP_DELAY_MS)
    expect(run).toHaveBeenNthCalledWith(1, 'startup', expect.anything())
    expect(onAttemptDone).toHaveBeenCalledTimes(1)

    await vi.advanceTimersByTimeAsync(PERIODIC_EVERY_MS)
    expect(run).toHaveBeenNthCalledWith(2, 'periodic', expect.anything())
    expect(onAttemptDone).toHaveBeenCalledTimes(2)
  })

  it('goes idle when the startup attempt is not connected', async () => {
    const run = vi.fn().mockResolvedValue(DISABLED)
    const { s } = makeScheduler({ run })
    s.start()
    await vi.advanceTimersByTimeAsync(STARTUP_DELAY_MS)
    expect(run).toHaveBeenCalledTimes(1)
    // Idle: no periodic or retry timers afterwards.
    await vi.advanceTimersByTimeAsync(30 * 60 * 1000)
    expect(run).toHaveBeenCalledTimes(1)
    expect(s.status().nextRun).toBeNull()
  })

  it('a successful Enable wakes an immediate coalesced attempt and clears any pause', async () => {
    let mode = 'fail'
    const run = vi.fn().mockImplementation(async () => (mode === 'fail' ? { kind: 'permanent', pauseReason: 'permission' } : SUCCESS))
    const { s } = makeScheduler({ run })
    s.start()
    await vi.advanceTimersByTimeAsync(STARTUP_DELAY_MS) // permanent pause
    expect(s.status().paused).toBe(true)

    mode = 'ok'
    s.wake() // a successful Enable
    await vi.advanceTimersByTimeAsync(0)
    expect(run).toHaveBeenLastCalledWith('enable', expect.anything())
    expect(s.status().paused).toBe(false)
    expect(s.status().nextRun).not.toBeNull()
  })

  it('backs off 1m/2m/5m/10m/30m on retryable failures, capped at 30m', async () => {
    const run = vi.fn().mockResolvedValue({ kind: 'retryable' })
    const { s } = makeScheduler({ run })
    s.start()
    await vi.advanceTimersByTimeAsync(STARTUP_DELAY_MS)
    expect(run).toHaveBeenCalledTimes(1)

    for (const delay of [60_000, 120_000, 300_000, 600_000, 1_800_000]) {
      await vi.advanceTimersByTimeAsync(delay)
      expect(run).toHaveBeenCalled()
    }
    expect(run).toHaveBeenCalledTimes(6)
    // Capped: another 30m step does not shorten.
    await vi.advanceTimersByTimeAsync(1_800_000)
    expect(run).toHaveBeenCalledTimes(7)
    expect(s.status().failures).toBe(7)
  })

  it('honors a larger provider Retry-After instead of the fixed backoff', async () => {
    const run = vi.fn().mockResolvedValue({ kind: 'retryable', retryAfter: 120 })
    const { s } = makeScheduler({ run })
    s.start()
    await vi.advanceTimersByTimeAsync(STARTUP_DELAY_MS)
    expect(run).toHaveBeenCalledTimes(1)

    // At the fixed 60s the retry must NOT fire.
    await vi.advanceTimersByTimeAsync(60_000)
    expect(run).toHaveBeenCalledTimes(1)
    // It fires at the honored 120s.
    await vi.advanceTimersByTimeAsync(60_000)
    expect(run).toHaveBeenCalledTimes(2)
  })

  it('a permanent failure pauses until a successful manual run or Enable', async () => {
    let mode = 'fail'
    const run = vi.fn().mockImplementation(async () => (mode === 'fail' ? { kind: 'permanent', pauseReason: 'quota' } : SUCCESS))
    const { s } = makeScheduler({ run })
    s.start()
    await vi.advanceTimersByTimeAsync(STARTUP_DELAY_MS)
    expect(s.status()).toMatchObject({ paused: true, pauseReason: 'quota' })
    expect(s.status().nextRun).toBeNull()

    await vi.advanceTimersByTimeAsync(60 * 60 * 1000) // paused: no further runs
    expect(run).toHaveBeenCalledTimes(1)

    mode = 'ok'
    await s.runNow('manual') // a successful manual run clears the pause
    expect(s.status().paused).toBe(false)
    expect(s.status().nextRun).not.toBeNull()
  })

  it('a locked/blocked manual run never clears a permanent pause', async () => {
    let mode = 'fail'
    const run = vi.fn().mockImplementation(async () => {
      if (mode === 'fail') return { kind: 'permanent', pauseReason: 'permission' }
      // mode 'locked': a success-kind outcome WITHOUT a Synced result (another
      // tab holding the Web Lock, a cancelled or blocked cycle).
      return { kind: 'success' }
    })
    const { s } = makeScheduler({ run })
    s.start()
    await vi.advanceTimersByTimeAsync(STARTUP_DELAY_MS)
    expect(s.status().paused).toBe(true)

    mode = 'locked'
    await s.runNow('manual') // Run now while another tab holds the lock
    expect(s.status().paused).toBe(true) // the permission pause survives
    expect(s.status().nextRun).toBeNull()
  })

  it('reset leaves the scheduler idle and wake re-arms it', async () => {
    const run = vi.fn().mockResolvedValue(SUCCESS)
    const { s } = makeScheduler({ run })
    s.start()
    await vi.advanceTimersByTimeAsync(STARTUP_DELAY_MS)
    expect(s.status().nextRun).not.toBeNull()

    s.reset() // Disable/Reset
    expect(s.status()).toMatchObject({ paused: false, nextRun: null, failures: 0 })
    await vi.advanceTimersByTimeAsync(60 * 60 * 1000)
    expect(run).toHaveBeenCalledTimes(1)

    s.wake() // Enable again
    await vi.advanceTimersByTimeAsync(0)
    expect(run).toHaveBeenCalledTimes(2)
  })

  it('hides the timer while the document is hidden and catches up when visible again', async () => {
    const run = vi.fn().mockResolvedValue(SUCCESS)
    const { s, setVisible } = makeScheduler({ run })
    s.start()
    setVisible(false) // hide before the startup delay elapses
    await vi.advanceTimersByTimeAsync(STARTUP_DELAY_MS * 2)
    expect(run).not.toHaveBeenCalled() // due retained, timer cancelled

    setVisible(true) // overdue -> runs immediately
    await vi.advanceTimersByTimeAsync(0)
    expect(run).toHaveBeenCalledWith('startup', expect.anything())

    // Hide again mid-period; the due time survives and re-arms on return.
    await vi.advanceTimersByTimeAsync(PERIODIC_EVERY_MS / 2)
    const calls = run.mock.calls.length
    setVisible(false)
    await vi.advanceTimersByTimeAsync(PERIODIC_EVERY_MS)
    expect(run.mock.calls.length).toBe(calls) // nothing while hidden
    setVisible(true) // not overdue yet -> re-arms the remaining half
    await vi.advanceTimersByTimeAsync(PERIODIC_EVERY_MS / 2)
    expect(run.mock.calls.length).toBe(calls + 1)
  })

  it('stop cancels the timer, aborts the in-flight attempt, and waits for it', async () => {
    let resolveRun
    const run = vi.fn().mockImplementation((trigger, signal) => new Promise((resolve) => {
      signal.addEventListener('abort', () => resolve(SUCCESS))
      resolveRun = resolve
    }))
    const { s, onAttemptDone } = makeScheduler({ run })
    s.start()
    await vi.advanceTimersByTimeAsync(STARTUP_DELAY_MS) // attempt in flight
    expect(run).toHaveBeenCalledTimes(1)

    const stopped = s.stop()
    await stopped
    expect(s.status().syncRunning).toBe(false)
    expect(s.status().active).toBe(false)
    expect(s.status().nextRun).toBeNull()
    // The aborted in-flight attempt must NOT refresh the UI after shutdown.
    expect(onAttemptDone).not.toHaveBeenCalled()

    // No further work is scheduled after stop.
    await vi.advanceTimersByTimeAsync(PERIODIC_EVERY_MS)
    expect(run).toHaveBeenCalledTimes(1)
    resolveRun?.(SUCCESS) // keep the promise settled for the suite
  })

  it('a manual run cancels the armed timeout instead of double-firing across it', async () => {
    const run = vi.fn().mockResolvedValue(SUCCESS)
    const { s } = makeScheduler({ run })
    s.start()
    await vi.advanceTimersByTimeAsync(STARTUP_DELAY_MS) // startup; periodic armed at +5m
    await vi.advanceTimersByTimeAsync(PERIODIC_EVERY_MS - 1000) // 1s before the periodic
    expect(run).toHaveBeenCalledTimes(1)

    await s.runNow('manual') // must cancel the pending periodic timer
    expect(run).toHaveBeenCalledTimes(2)

    // Crossing the original periodic expiry must NOT start a second attempt:
    // the manual result re-armed 5m from now instead.
    await vi.advanceTimersByTimeAsync(5000)
    expect(run).toHaveBeenCalledTimes(2)
  })

  it('a retryable manual run while paused keeps the pause and never arms a phantom nextRun', async () => {
    let mode = 'fail'
    const run = vi.fn().mockImplementation(async () => {
      if (mode === 'fail') return { kind: 'permanent', pauseReason: 'permission' }
      if (mode === 'retryable') return { kind: 'retryable', retryAfter: 60 }
      return SUCCESS
    })
    const { s } = makeScheduler({ run })
    s.start()
    await vi.advanceTimersByTimeAsync(STARTUP_DELAY_MS)
    expect(s.status().paused).toBe(true)

    mode = 'retryable'
    await s.runNow('manual')
    expect(s.status().paused).toBe(true)
    // No never-executing backoff: arm() is paused, so no phantom nextRun.
    expect(s.status().nextRun).toBeNull()
  })

  it('runNow refuses a stopped scheduler instead of firing', async () => {
    const run = vi.fn().mockResolvedValue(SUCCESS)
    const { s } = makeScheduler({ run })
    s.start()
    await s.stop()
    const classification = await s.runNow('manual')
    expect(classification.kind).toBe('disabled')
    expect(run).not.toHaveBeenCalled()
  })

  it('a rejected run becomes a permanent pause, never an unhandled rejection or a re-fire loop', async () => {
    const run = vi.fn().mockRejectedValue(new Error('boom'))
    const { s } = makeScheduler({ run })
    s.start()
    await vi.advanceTimersByTimeAsync(STARTUP_DELAY_MS)
    expect(run).toHaveBeenCalledTimes(1)
    expect(s.status()).toMatchObject({ paused: true, pauseReason: 'error' })
    expect(s.status().nextRun).toBeNull()
    // Paused: no zero-delay re-fire loop.
    await vi.advanceTimersByTimeAsync(10 * 60 * 1000)
    expect(run).toHaveBeenCalledTimes(1)
  })

  it('runNow is single-flight: a manual run coalesces onto the in-flight attempt', async () => {
    let resolveRun
    const run = vi.fn().mockImplementation(() => new Promise((resolve) => { resolveRun = resolve }))
    const { s } = makeScheduler({ run })
    s.start()
    await vi.advanceTimersByTimeAsync(STARTUP_DELAY_MS) // startup attempt in flight
    expect(run).toHaveBeenCalledTimes(1)

    const coalesced = s.runNow('manual')
    expect(run).toHaveBeenCalledTimes(1) // no second run while one is in flight

    resolveRun(SUCCESS)
    await coalesced
    expect(s.status().nextRun).not.toBeNull()
  })

  it('notifies the UI refresh hook after every attempt (local direct UI refresh)', async () => {
    const run = vi.fn().mockResolvedValue(SUCCESS)
    const { s, onAttemptDone } = makeScheduler({ run })
    s.start()
    await vi.advanceTimersByTimeAsync(STARTUP_DELAY_MS)
    await vi.advanceTimersByTimeAsync(PERIODIC_EVERY_MS)
    expect(onAttemptDone).toHaveBeenCalledTimes(2)
  })
})

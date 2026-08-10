import { describe, it, expect, vi, beforeEach } from 'vitest'
import { useNoteEditor } from './useNoteEditor'
import { useSyncPolling } from './useSyncPolling'
import { getSyncSettings } from './useSyncSettings'

// useSyncPolling imports useSyncSettings, which loads the real ../api (axios +
// router referencing window). Stub it so the module loads in a plain node
// environment; the poller under test uses its own injected api.
vi.mock('../api', () => ({ default: { syncStatus: vi.fn(), syncRecovery: vi.fn() } }))

function makeEditor() {
  const editor = useNoteEditor()
  editor.loadDocument({ path: 'a.md', name: 'a', content: '# old\n', tags: [], revision: 'r1' })
  return editor
}

function makePolling({ api, editor, visible = true, local = false, onAutoSync = vi.fn(), onNotice = vi.fn(), onNoteClosed = vi.fn(), onRecoveryChanged = vi.fn() }) {
  let visibilityHandler = null
  let visibleState = visible
  const p = useSyncPolling({
    api,
    editor,
    isLocalBuild: local,
    intervalMs: 30000,
    isVisible: () => visibleState,
    addVisibilityListener: (fn) => { visibilityHandler = fn },
    removeVisibilityListener: (fn) => { visibilityHandler = null },
    onAutoSync,
    onNotice,
    onNoteClosed,
    onRecoveryChanged,
  })
  return {
    p, api, editor, onAutoSync, onNotice, onNoteClosed, onRecoveryChanged,
    setVisible(v) { visibleState = v; if (visibilityHandler) visibilityHandler() },
  }
}

const baseStatus = (over = {}) => ({
  data: {
    enabled: true, connected: true, experimental: true, noE2EE: true,
    lastCompleted: '2026-08-09T00:05:00Z', lastTrigger: 'periodic',
    recoveryCount: 0, autoEnabled: true, autoIntervalSecs: 300, nextRun: null,
    ...over,
  },
})

describe('useSyncPolling', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    getSyncSettings().recoveryCount = 0
  })

  it('polls lightweight status without ever downloading recovery content', async () => {
    const api = { syncStatus: vi.fn().mockResolvedValue(baseStatus()), getNote: vi.fn().mockResolvedValue({ data: { revision: 'r1' } }) }
    const { p, api: a } = makePolling({ api, editor: makeEditor() })
    p.start()
    await vi.advanceTimersByTimeAsync(30000)
    expect(a.syncStatus).toHaveBeenCalledTimes(1)
    expect(a.syncRecovery).toBeUndefined()
    expect(a.getNote).toHaveBeenCalledWith('a.md')
    p.stop()
  })

  it('refreshes the list when an automatic attempt advances lastCompleted', async () => {
    const onAutoSync = vi.fn()
    const api = {
      syncStatus: vi.fn()
        .mockResolvedValueOnce(baseStatus({ lastCompleted: '2026-08-09T00:05:00Z' }))
        .mockResolvedValueOnce(baseStatus({ lastCompleted: '2026-08-09T00:05:00Z' }))
        .mockResolvedValueOnce(baseStatus({ lastCompleted: '2026-08-09T00:10:00Z' })),
      getNote: vi.fn().mockResolvedValue({ data: { revision: 'r1' } }),
    }
    const { p } = makePolling({ api, editor: makeEditor(), onAutoSync })
    p.start()
    await vi.advanceTimersByTimeAsync(30000) // first poll: initial value
    await vi.advanceTimersByTimeAsync(30000) // unchanged: no refresh
    await vi.advanceTimersByTimeAsync(30000) // advanced: refresh
    expect(onAutoSync).toHaveBeenCalledTimes(2)
    p.stop()
  })

  it('adopts a clean open note when its revision changed', async () => {
    const editor = makeEditor()
    const api = {
      syncStatus: vi.fn().mockResolvedValue(baseStatus({ lastCompleted: '2026-08-09T00:10:00Z' })),
      getNote: vi.fn().mockResolvedValue({ data: { path: 'a.md', name: 'a', content: '# new\n', tags: [], revision: 'r2' } }),
    }
    const { p } = makePolling({ api, editor })
    p.start()
    await vi.advanceTimersByTimeAsync(30000)
    expect(editor.editingNote.value.revision).toBe('r2')
    expect(editor.editContent.value).toBe('# new\n')
    expect(editor.isDirty.value).toBe(false)
    p.stop()
  })

  it('closes a clean open note that was deleted by sync', async () => {
    const onNoteClosed = vi.fn()
    const editor = makeEditor()
    const api = {
      syncStatus: vi.fn().mockResolvedValue(baseStatus({ lastCompleted: '2026-08-09T00:10:00Z' })),
      getNote: vi.fn().mockRejectedValue({ response: { status: 404 } }),
    }
    const { p } = makePolling({ api, editor, onNoteClosed })
    p.start()
    await vi.advanceTimersByTimeAsync(30000)
    expect(editor.editingNote.value).toBeNull()
    expect(onNoteClosed).toHaveBeenCalledWith('a.md')
    p.stop()
  })

  it('never replaces or closes a dirty editor buffer', async () => {
    const onNotice = vi.fn()
    const editor = makeEditor()
    editor.isDirty.value = true
    const api = {
      syncStatus: vi.fn().mockResolvedValue(baseStatus({ lastCompleted: '2026-08-09T00:10:00Z' })),
      getNote: vi.fn().mockResolvedValue({ data: { path: 'a.md', revision: 'r2', content: '# new\n' } }),
    }
    const { p } = makePolling({ api, editor, onNotice })
    p.start()
    await vi.advanceTimersByTimeAsync(30000)
    expect(editor.editingNote.value.revision).toBe('r1')
    expect(editor.editContent.value).toBe('# old\n')
    expect(onNotice).toHaveBeenCalledWith('changed')
    p.stop()
  })

  it('pauses polling when hidden and resumes on visibility return', async () => {
    const api = { syncStatus: vi.fn().mockResolvedValue(baseStatus()), getNote: vi.fn().mockResolvedValue({ data: { revision: 'r1' } }) }
    const { p, setVisible, api: a } = makePolling({ api, editor: makeEditor(), visible: true })
    p.start()
    await vi.advanceTimersByTimeAsync(30000)
    const calls = a.syncStatus.mock.calls.length
    setVisible(false)
    await vi.advanceTimersByTimeAsync(120000) // hidden: no polls
    expect(a.syncStatus.mock.calls.length).toBe(calls)
    setVisible(true)
    await vi.advanceTimersByTimeAsync(0) // immediate refresh on return
    expect(a.syncStatus.mock.calls.length).toBeGreaterThan(calls)
    await vi.advanceTimersByTimeAsync(30000)
    expect(a.syncStatus.mock.calls.length).toBeGreaterThan(calls + 1)
    p.stop()
  })

  it('discards a stale note response when the user switches notes mid-request', async () => {
    const editor = makeEditor() // open note A (a.md)
    let resolveGetNote
    const pending = new Promise((resolve) => { resolveGetNote = resolve })
    const api = {
      syncStatus: vi.fn().mockResolvedValue(baseStatus({ lastCompleted: '2026-08-09T00:10:00Z' })),
      getNote: vi.fn().mockReturnValue(pending),
    }
    const { p } = makePolling({ api, editor })
    p.start()
    await vi.advanceTimersByTimeAsync(30000) // poll fires; getNote('a.md') is pending

    // The user switches to note B while the request for A is in flight.
    editor.loadDocument({ path: 'b.md', name: 'b', content: '# B\n', tags: [], revision: 'b1' })
    // The stale response for A arrives afterwards.
    resolveGetNote({ data: { path: 'a.md', name: 'a', content: '# A new\n', tags: [], revision: 'a2' } })
    await vi.advanceTimersByTimeAsync(0)

    // Note A's content must never be loaded into note B.
    expect(editor.editingNote.value.path).toBe('b.md')
    expect(editor.editContent.value).toBe('# B\n')
    expect(editor.editingNote.value.revision).toBe('b1')
    p.stop()
  })

  it('does not close a newly opened note when a stale 404 for another note arrives', async () => {
    const editor = makeEditor() // note A
    let rejectGetNote
    const pending = new Promise((_, reject) => { rejectGetNote = reject })
    const api = {
      syncStatus: vi.fn().mockResolvedValue(baseStatus({ lastCompleted: '2026-08-09T00:10:00Z' })),
      getNote: vi.fn().mockReturnValue(pending),
    }
    const { p, onNoteClosed } = makePolling({ api, editor })
    p.start()
    await vi.advanceTimersByTimeAsync(30000) // poll fires; getNote('a.md') pending

    // The user switches to note B, then A's 404 arrives.
    editor.loadDocument({ path: 'b.md', name: 'b', content: '# B\n', tags: [], revision: 'b1' })
    rejectGetNote({ response: { status: 404 } })
    await vi.advanceTimersByTimeAsync(0)

    expect(editor.editingNote.value.path).toBe('b.md')
    expect(onNoteClosed).not.toHaveBeenCalled()
    p.stop()
  })

  it('updates the settings panel scheduling state on every poll', async () => {
    const api = {
      syncStatus: vi.fn().mockResolvedValue(baseStatus({
        syncRunning: true, nextRun: '2026-08-09T00:10:00Z',
        autoPaused: true, pauseReason: 'permission',
      })),
      getNote: vi.fn().mockResolvedValue({ data: { revision: 'r1' } }),
    }
    const { p } = makePolling({ api, editor: makeEditor() })
    p.start()
    await vi.advanceTimersByTimeAsync(30000)
    const s = getSyncSettings()
    expect(s.syncRunning).toBe(true)
    expect(s.nextRun).toBe('2026-08-09T00:10:00Z')
    expect(s.autoPaused).toBe(true)
    expect(s.pauseReason).toBe('permission')
    expect(s.lastTrigger).toBe('periodic')
    p.stop()
  })

  it('refreshes recovery details when the first background copy appears', async () => {
    const onRecoveryChanged = vi.fn()
    // The settings state baseline is 0; the first poll sees the recovery copy
    // the startup run created, so it must trigger a detail refresh — not treat
    // it as a baseline.
    const api = {
      syncStatus: vi.fn().mockResolvedValue(baseStatus({ recoveryCount: 1, lastCompleted: '2026-08-09T00:10:00Z' })),
      getNote: vi.fn().mockResolvedValue({ data: { revision: 'r1' } }),
    }
    const { p } = makePolling({ api, editor: makeEditor(), onRecoveryChanged })
    p.start()
    await vi.advanceTimersByTimeAsync(30000)
    expect(onRecoveryChanged).toHaveBeenCalledWith(1)
    // A repeated poll with the same count does not re-trigger.
    await vi.advanceTimersByTimeAsync(30000)
    expect(onRecoveryChanged).toHaveBeenCalledTimes(1)
    p.stop()
  })

  it('local build creates no timer and makes no request', async () => {
    const api = { syncStatus: vi.fn(), getNote: vi.fn() }
    const { p, api: a } = makePolling({ api, editor: makeEditor(), local: true })
    p.start()
    await vi.advanceTimersByTimeAsync(30000)
    expect(a.syncStatus).not.toHaveBeenCalled()
    expect(a.getNote).not.toHaveBeenCalled()
    p.stop()
  })
})

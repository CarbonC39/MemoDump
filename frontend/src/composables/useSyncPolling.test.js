import { describe, it, expect, vi, beforeEach } from 'vitest'
import { useNoteEditor } from './useNoteEditor'
import { useSyncPolling } from './useSyncPolling'

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
    p, api, editor,
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
  beforeEach(() => { vi.useFakeTimers() })

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

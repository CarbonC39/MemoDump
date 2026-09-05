import { describe, expect, it, vi } from 'vitest'
import { createEditorChangeBridge } from './editorChangeBridge'

function setup() {
  const tasks = []
  let markdown = ''
  const publishUpdate = vi.fn()
  const publishReady = vi.fn()
  const bridge = createEditorChangeBridge({
    readMarkdown: () => markdown,
    publishUpdate,
    publishReady,
    schedule: task => tasks.push(task),
  })
  return {
    bridge,
    tasks,
    publishUpdate,
    publishReady,
    setMarkdown: value => { markdown = value },
  }
}

describe('editor document change bridge', () => {
  it('publishes a user transaction immediately from the latest document', () => {
    const ctx = setup()
    ctx.setMarkdown('- [x] checked')
    ctx.bridge.changed(true)
    ctx.tasks.shift()()

    expect(ctx.publishUpdate).toHaveBeenCalledWith('- [x] checked')
    expect(ctx.publishReady).not.toHaveBeenCalled()
  })

  it('keeps programmatic replacement changes as a clean baseline', () => {
    const ctx = setup()
    ctx.setMarkdown('replacement')
    ctx.bridge.changed(false)
    ctx.tasks.shift()()

    expect(ctx.publishReady).toHaveBeenCalledWith('replacement')
    expect(ctx.publishUpdate).not.toHaveBeenCalled()
  })

  it('coalesces transactions and lets any user change win over clean changes', () => {
    const ctx = setup()
    ctx.bridge.changed(false)
    ctx.bridge.changed(true)
    ctx.setMarkdown('latest')
    ctx.tasks.shift()()

    expect(ctx.tasks).toHaveLength(0)
    expect(ctx.publishUpdate).toHaveBeenCalledTimes(1)
    expect(ctx.publishUpdate).toHaveBeenCalledWith('latest')
  })

  it('drops a queued callback when the active document is reset', () => {
    const ctx = setup()
    ctx.bridge.changed(true)
    const staleTask = ctx.tasks.shift()
    ctx.bridge.reset()
    staleTask()

    expect(ctx.publishUpdate).not.toHaveBeenCalled()
    expect(ctx.publishReady).not.toHaveBeenCalled()
  })
})

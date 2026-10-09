export function createEditorChangeBridge({
  readMarkdown,
  publishUpdate,
  publishReady,
  schedule = queueMicrotask,
}) {
  let generation = 0
  let queued = false
  let pendingUserChange = false
  let active = true

  function changed(userChange) {
    if (!active) return
    pendingUserChange ||= userChange
    if (queued) return

    queued = true
    const scheduledGeneration = generation
    schedule(() => {
      if (!active || scheduledGeneration !== generation) return
      queued = false
      const userChanged = pendingUserChange
      pendingUserChange = false
      const markdown = readMarkdown()
      if (userChanged) publishUpdate(markdown)
      else publishReady(markdown)
    })
  }

  function reset() {
    generation++
    queued = false
    pendingUserChange = false
  }

  function destroy() {
    active = false
    reset()
  }

  return { changed, reset, destroy }
}

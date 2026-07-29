let editorModulePromise = null

export function preloadMilkdownEditor() {
  if (!editorModulePromise) {
    editorModulePromise = import('./MilkdownEditor.vue').catch(error => {
      // A retry must issue a fresh chunk request instead of reusing a rejected promise.
      editorModulePromise = null
      throw error
    })
  }
  return editorModulePromise
}

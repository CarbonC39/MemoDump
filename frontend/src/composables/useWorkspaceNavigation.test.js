import { describe, expect, it, vi } from 'vitest'
import { reactive, ref } from 'vue'
import { useNoteEditor } from './useNoteEditor'
import { useWorkspaceNavigation } from './useWorkspaceNavigation'

function setup({ query = {}, confirmDiscard = vi.fn(() => true) } = {}) {
  const editor = useNoteEditor()
  const browser = {
    currentFolder: ref(''),
    displayNotes: ref([]),
    allNotes: ref([]),
    loadFolderPage: vi.fn().mockResolvedValue(undefined),
  }
  const router = { replace: vi.fn() }
  const navigation = useWorkspaceNavigation({
    router,
    route: { query },
    editor,
    browser,
    searchOpen: ref(false),
    showSettings: ref(false),
    mobileSidebar: ref(false),
    openSections: reactive({ storage: false }),
    openDocument: vi.fn().mockImplementation(async note => {
      editor.loadDocument({ path: note.path, name: 'note', tags: [], content: '' })
    }),
    deleteCurrent: vi.fn(),
    loadAll: vi.fn().mockResolvedValue(undefined),
    readOutbox: vi.fn().mockResolvedValue([]),
    replayAll: vi.fn(),
    showDraftRestoredBanner: ref(false),
    showConfirm: vi.fn().mockResolvedValue(true),
    t: key => key,
    confirmDiscard,
    notify: vi.fn(),
  })
  return { navigation, editor, browser, router }
}

describe('useWorkspaceNavigation', () => {
  it('cancels navigation when dirty changes are rejected', async () => {
    const confirmDiscard = vi.fn(() => false)
    const { navigation, editor, browser } = setup({ confirmDiscard })
    editor.createDocument('')
    editor.onEditorReady('')
    editor.onEditorUpdate('dirty')

    await navigation.selectFolder('work')

    expect(confirmDiscard).toHaveBeenCalled()
    expect(browser.loadFolderPage).not.toHaveBeenCalled()
    expect(editor.editingNote.value).not.toBeNull()
  })

  it('restores a folder route through the browser boundary', async () => {
    const { navigation, browser, router } = setup({ query: { folder: 'work' } })

    await navigation.restoreFromUrl()

    expect(browser.loadFolderPage).toHaveBeenCalledWith('work')
    expect(router.replace).not.toHaveBeenCalled()
  })

  it('encodes the active opaque note path without parsing it', async () => {
    const { navigation, editor, router } = setup()
    editor.loadDocument({ path: 'folder/note.md', name: 'note', tags: [], content: '' })

    navigation.updateUrl()

    expect(router.replace).toHaveBeenCalledWith({ query: { note: 'folder/note.md' } })
  })
})

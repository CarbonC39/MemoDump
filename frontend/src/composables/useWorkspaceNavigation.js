import { computed, reactive } from 'vue'

export function useWorkspaceNavigation({
  router,
  route,
  editor,
  browser,
  searchOpen,
  showSettings,
  mobileSidebar,
  openSections,
  openDocument,
  deleteCurrent,
  loadAll,
  readOutbox,
  replayAll,
  showDraftRestoredBanner,
  showConfirm,
  t,
  confirmDiscard = message => globalThis.confirm(message),
  notify = message => globalThis.alert(message),
} = {}) {
  const {
    editingNote, editFolder, isDirty,
    createDocument, restoreDraft,
  } = editor
  const {
    currentFolder, displayNotes, allNotes, loadFolderPage,
  } = browser

  const prevView = reactive({ folder: '', search: false })
  const hasPrevPage = computed(() => prevView.search || Boolean(prevView.folder))

  function updateUrl() {
    const query = {}
    if (editingNote.value?.path) query.note = editingNote.value.path
    else if (currentFolder.value) query.folder = currentFolder.value
    else if (searchOpen.value) query.search = '1'
    router.replace({ query })
  }

  function confirmLeave() {
    if (!isDirty.value) return true
    return confirmDiscard(t('modals.unsavedChanges'))
  }

  function forceNewNote() {
    showSettings.value = false
    prevView.folder = currentFolder.value
    prevView.search = searchOpen.value
    createDocument(currentFolder.value)
    searchOpen.value = false
    mobileSidebar.value = false
    updateUrl()
  }

  function newNote() {
    if (!confirmLeave()) return
    forceNewNote()
  }

  function createNewNoteIn(folderPath) {
    if (!confirmLeave()) return
    forceNewNote()
    editFolder.value = folderPath
    openSections.storage = true
  }

  async function openNote(note) {
    if (!confirmLeave()) return
    prevView.folder = currentFolder.value
    prevView.search = searchOpen.value
    try {
      await openDocument(note)
      searchOpen.value = false
      mobileSidebar.value = false
      updateUrl()
    } catch (error) {
      console.error('Failed to open note', error)
    }
  }

  async function selectFolder(folderPath) {
    if (!confirmLeave()) return
    showSettings.value = false
    editingNote.value = null
    isDirty.value = false
    searchOpen.value = false
    mobileSidebar.value = false
    try { await loadFolderPage(folderPath) } catch (_) {}
    updateUrl()
  }

  async function handleAllClick() {
    if (!confirmLeave()) return
    showSettings.value = false
    editingNote.value = null
    isDirty.value = false
    searchOpen.value = false
    currentFolder.value = ''
    updateUrl()
    await loadAll()
  }

  function openSearchPanel() {
    if (!confirmLeave()) return
    showSettings.value = false
    searchOpen.value = true
    editingNote.value = null
    isDirty.value = false
    updateUrl()
  }

  async function goBack() {
    if (!confirmLeave()) return
    editingNote.value = null
    isDirty.value = false
    if (prevView.search) {
      searchOpen.value = true
      currentFolder.value = ''
      updateUrl()
    } else if (prevView.folder) {
      await selectFolder(prevView.folder)
    } else {
      await handleAllClick()
    }
  }

  async function deleteCurrentNote() {
    const confirmed = await showConfirm({
      title: t('modals.deleteNote'),
      message: t('modals.deleteNoteMsg'),
      okLabel: t('modals.delete'),
      danger: true,
    })
    if (!confirmed) return
    try {
      await deleteCurrent()
      await loadAll()
      forceNewNote()
    } catch (_) {
      notify(t('errors.deleteFailed'))
    }
  }

  async function restoreFromUrl() {
    const { note, folder } = route.query
    if (note) {
      try {
        await openDocument({ path: note })
        searchOpen.value = false
        return
      } catch (_) {}
    }
    if (folder) {
      openSections.storage = true
      try {
        await loadFolderPage(folder)
      } catch (_) {
        displayNotes.value = allNotes.value
      }
      return
    }
    forceNewNote()
  }

  async function initialize({ onReady = () => {} } = {}) {
    const listLoad = loadAll()
    let restored = false
    try {
      const entries = await readOutbox()
      if (entries.length) {
        restoreDraft(entries[entries.length - 1])
        showDraftRestoredBanner.value = true
        restored = true
      }
    } catch (_) {}

    if (!restored) await restoreFromUrl()
    onReady()
    await listLoad
    if (typeof navigator === 'undefined' || navigator.onLine) replayAll()
  }

  return {
    prevView,
    hasPrevPage,
    updateUrl,
    confirmLeave,
    forceNewNote,
    newNote,
    createNewNoteIn,
    openNote,
    selectFolder,
    handleAllClick,
    openSearchPanel,
    goBack,
    deleteCurrentNote,
    restoreFromUrl,
    initialize,
  }
}

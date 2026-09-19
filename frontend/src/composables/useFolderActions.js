export function useFolderActions({
  api,
  currentFolder,
  loadAll,
  loadFolderNode,
  refreshRootFolders,
  showPrompt,
  showConfirm,
  t,
  updateUrl = () => {},
  notify = globalThis.alert,
} = {}) {
  if (!api) throw new Error('useFolderActions requires an API implementation')

  async function promptNewFolder(parentPath) {
    const name = await showPrompt(t('modals.folderName'))
    if (!name) return
    const path = parentPath ? `${parentPath}/${name}` : name
    try {
      await api.createFolder(path)
      if (parentPath) await loadFolderNode(parentPath, { force: true })
      else await refreshRootFolders()
    } catch (_) {
      notify(t('errors.failed'))
    }
  }

  async function promptRenameFolder(folderPath) {
    const currentName = folderPath.split('/').pop()
    const name = await showPrompt(t('modals.newName'), currentName)
    if (!name || name === currentName) return
    try {
      await api.renameFolder(folderPath, name)
      if (
        currentFolder.value === folderPath ||
        currentFolder.value.startsWith(`${folderPath}/`)
      ) {
        const parent = folderPath.substring(0, folderPath.lastIndexOf('/'))
        const renamedPath = parent ? `${parent}/${name}` : name
        currentFolder.value = currentFolder.value.replace(folderPath, renamedPath)
      }
      await loadAll()
      updateUrl()
    } catch (_) {
      notify(t('errors.failed'))
    }
  }

  async function deleteFolder(folderPath) {
    const confirmed = await showConfirm({
      title: t('modals.deleteFolder'),
      message: t('modals.deleteFolderMsg'),
      okLabel: t('modals.delete'),
      danger: true,
    })
    if (!confirmed) return
    try {
      await api.deleteFolder(folderPath)
      if (
        currentFolder.value === folderPath ||
        currentFolder.value.startsWith(`${folderPath}/`)
      ) {
        currentFolder.value = ''
      }
      await loadAll()
      updateUrl()
    } catch (_) {
      notify(t('errors.failed'))
    }
  }

  return {
    promptNewFolder,
    promptRenameFolder,
    deleteFolder,
  }
}

import { onBeforeUnmount, onMounted, ref } from 'vue'
import apiClient from '../api'
import { useI18n } from '../i18n'

// Card drag/drop (move notes/folders) + .md/.txt file import via drag/drop or
// the file picker. Folder moves update the current-folder ref + URL so the
// view follows a moved folder; imports open the last imported note.
export function useFileImport({ editFolder, currentFolder, loadAll, openNote, editingNote, updateUrl }) {
  const { t } = useI18n()
  const hoveredNotePath = ref(null)

  // ===== DRAG AND DROP =====
  const rootDropOver = ref(false)
  const isDragging = ref(false)

  // Drag sources live in both the main waterfall and the recursive sidebar.
  // Listen during capture so FolderNode's stop modifiers cannot hide the drag
  // lifecycle from us. External file drags do not dispatch dragstart in this
  // document, so this state represents MemoDump's own card/folder drags.
  function onDragStart() {
    isDragging.value = true
  }

  function onDragEnd() {
    isDragging.value = false
    rootDropOver.value = false
  }

  onMounted(() => {
    document.addEventListener('dragstart', onDragStart, true)
    document.addEventListener('dragend', onDragEnd, true)
  })

  onBeforeUnmount(() => {
    document.removeEventListener('dragstart', onDragStart, true)
    document.removeEventListener('dragend', onDragEnd, true)
  })

  function onNoteDragStart(e, note) {
    e.dataTransfer.effectAllowed = 'move'
    e.dataTransfer.setData('memodump-type', 'note')
    e.dataTransfer.setData('memodump-path', note.path)
  }

  async function onDropNote({ notePath, destFolder }) {
    try {
      await apiClient.moveNote(notePath, destFolder)
      await loadAll()
      if (editingNote.value && editingNote.value.path === notePath) {
        const filename = notePath.split('/').pop()
        const newPath = destFolder ? destFolder + '/' + filename : filename
        openNote({ path: newPath })
      }
    } catch (e) {
      if (e.response?.status === 409) alert(t('errors.nameExists'))
      else alert(t('errors.moveFailed'))
    }
  }

  async function onDropFolder({ folderPath, destFolder }) {
    try {
      await apiClient.moveFolder(folderPath, destFolder)
      if (currentFolder.value === folderPath || currentFolder.value.startsWith(folderPath + '/')) {
        const folderName = folderPath.split('/').pop()
        currentFolder.value = destFolder ? destFolder + '/' + folderName : folderName
      }
      await loadAll()
      updateUrl()
    } catch (e) {
      if (e.response?.status === 409) alert(t('errors.folderExists'))
      else if (e.response?.status === 400) alert(e.response.data?.error || t('errors.moveFailed'))
      else alert(t('errors.moveFailed'))
    }
  }

  async function onDropOnRoot(e) {
    rootDropOver.value = false
    const type = e.dataTransfer.getData('memodump-type')
    const path = e.dataTransfer.getData('memodump-path')
    if (!path) return
    if (type === 'note') {
      await onDropNote({ notePath: path, destFolder: '' })
    } else if (type === 'folder') {
      await onDropFolder({ folderPath: path, destFolder: '' })
    }
  }

  // ===== FILE UPLOAD =====
  const uploadingFiles = ref(false)
  const isFileDragOver = ref(false)
  let fileDragCounter = 0

  function onFileInputChange(e) {
    const files = Array.from(e.target.files || [])
    e.target.value = ''
    if (files.length) uploadFiles(files)
  }

  function onMainDragEnter(e) {
    if (!e.dataTransfer.types.includes('Files')) return
    fileDragCounter++
    isFileDragOver.value = true
    e.preventDefault()
  }

  function onMainDragLeave(e) {
    if (!e.dataTransfer.types.includes('Files')) return
    fileDragCounter--
    if (fileDragCounter <= 0) {
      fileDragCounter = 0
      isFileDragOver.value = false
    }
  }

  function onMainDragOver(e) {
    if (!e.dataTransfer.types.includes('Files')) return
    e.preventDefault()
    e.dataTransfer.dropEffect = 'copy'
  }

  function onMainDrop(e) {
    if (!e.dataTransfer.types.includes('Files')) return
    e.preventDefault()
    fileDragCounter = 0
    isFileDragOver.value = false
    const files = Array.from(e.dataTransfer.files)
    if (files.length) uploadFiles(files)
  }

  async function uploadFiles(files) {
    const allowed = files.filter(f => /\.(md|txt)$/i.test(f.name))
    if (!allowed.length) {
      alert(t('errors.fileTypeUnsupported'))
      return
    }
    uploadingFiles.value = true
    let lastOpened = null
    for (const file of allowed) {
      const fd = new FormData()
      fd.append('file', file)
      try {
        const res = await apiClient.uploadNote(fd, editFolder.value || currentFolder.value || '')
        lastOpened = res.data
      } catch (e) {
        const msg = e.response?.data?.error || e.message
        alert(t('errors.importFailed').replace('{name}', file.name).replace('{msg}', msg))
      }
    }
    uploadingFiles.value = false
    await loadAll()
    if (lastOpened) openNote(lastOpened)
  }

  return {
    rootDropOver, isDragging, hoveredNotePath, onNoteDragStart, onDropNote, onDropFolder, onDropOnRoot,
    uploadingFiles, isFileDragOver, onFileInputChange,
    onMainDragEnter, onMainDragLeave, onMainDragOver, onMainDrop, uploadFiles,
  }
}

import { reactive } from 'vue'
import apiClient from '../api'
import { useI18n } from '../i18n'

// Per-card context menu: edit/copy/duplicate/download/move/delete. The menu
// is positioned near the three-dot button and stays within the viewport.
export function useContextMenu({ openNote, isDirty, loadAll, editingNote, _forceNewNote, showConfirm, showFolderPicker, copyDialog }) {
  const { t } = useI18n()
  const contextMenu = reactive({
    visible: false,
    x: 0,
    y: 0,
    note: null
  })

  // Called from three-dot button — position near the button element
  function openContextMenuBtn(e, note) {
    e.stopPropagation()
    contextMenu.note = note
    contextMenu.visible = true
    // Use button bounding rect for reliable position on both desktop and mobile
    const rect = e.currentTarget.getBoundingClientRect()
    let x = rect.right
    let y = rect.bottom + 4
    // Keep menu within viewport
    const menuW = 160, menuH = 175
    if (x + menuW > window.innerWidth) x = rect.left - menuW
    if (y + menuH > window.innerHeight) y = rect.top - menuH
    if (x < 0) x = 4
    if (y < 0) y = 4
    contextMenu.x = x
    contextMenu.y = y
  }

  function closeContextMenu() {
    contextMenu.visible = false
    contextMenu.note = null
  }

  function menuEditNote() {
    if (contextMenu.note) openNote(contextMenu.note)
    closeContextMenu()
  }

  async function menuCopyContent() {
    const note = contextMenu.note
    closeContextMenu()
    if (!note) return
    try {
      const res = await apiClient.getNote(note.path)
      const content = res.data.content || ''

      // Modern clipboard API (works on iOS 16.4+ PWA even after async)
      if (navigator.clipboard?.writeText) {
        try {
          await navigator.clipboard.writeText(content)
          return
        } catch (_) {}
      }

      // Legacy fallback — setSelectionRange required for iOS (ta.select() is unreliable)
      const ta = document.createElement('textarea')
      ta.value = content
      ta.style.cssText = 'position:fixed;top:0;left:0;width:1px;height:1px;padding:0;border:none;outline:none;font-size:16px;opacity:0.01;'
      document.body.appendChild(ta)
      ta.focus({ preventScroll: true })
      ta.setSelectionRange(0, content.length)
      const ok = document.execCommand('copy')
      document.body.removeChild(ta)
      if (ok) return

      // Final fallback: show dialog so user can manually long-press → copy
      copyDialog.content = content
      copyDialog.visible = true
    } catch (e) {
      alert(t('errors.copyFailed'))
    }
  }

  async function menuDuplicateNote() {
    const note = contextMenu.note
    closeContextMenu()
    if (!note) return
    try {
      await apiClient.duplicateNote(note.path)
      await loadAll()
    } catch (e) { alert('Duplicate failed') }
  }

  async function menuDeleteNote() {
    const note = contextMenu.note
    closeContextMenu()
    if (!note) return
    if (!(await showConfirm({
      title: t('modals.deleteNote'),
      message: t('modals.deleteNoteMsg'),
      okLabel: t('modals.delete'),
      danger: true,
    }))) return
    try {
      await apiClient.deleteNote(note.path)
      isDirty.value = false
      await loadAll()
      if (editingNote.value && editingNote.value.path === note.path) _forceNewNote()
    } catch (e) { alert(t('errors.deleteFailed')) }
  }

  async function menuDownloadNote() {
    const note = contextMenu.note
    closeContextMenu()
    if (!note) return
    try {
      const res = await apiClient.getNote(note.path)
      const content = res.data.content || ''
      const blob = new Blob([content], { type: 'text/markdown;charset=utf-8' })
      const url = URL.createObjectURL(blob)
      const a = document.createElement('a')
      a.href = url
      a.download = note.name + '.md'
      document.body.appendChild(a)
      a.click()
      document.body.removeChild(a)
      URL.revokeObjectURL(url)
    } catch (e) {
      alert(t('errors.downloadFailed'))
    }
  }

  async function menuMoveNote() {
    const note = contextMenu.note
    closeContextMenu()
    if (!note) return
    const noteParts = note.path.split('/')
    const curFolder = noteParts.length > 1 ? noteParts.slice(0, -1).join('/') : ''
    const dest = await showFolderPicker(curFolder)
    if (dest === null) return
    try {
      await apiClient.moveNote(note.path, dest)
      await loadAll()
      if (editingNote.value && editingNote.value.path === note.path) {
        const newPath = dest ? dest + '/' + note.path.split('/').pop() : note.path.split('/').pop()
        openNote({ path: newPath })
      }
    } catch (e) { alert(t('errors.moveFailed')) }
  }

  return {
    contextMenu, openContextMenuBtn, closeContextMenu, menuEditNote, menuCopyContent,
    menuDuplicateNote, menuDeleteNote, menuDownloadNote, menuMoveNote,
  }
}

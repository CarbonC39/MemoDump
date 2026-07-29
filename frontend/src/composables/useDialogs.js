import { ref, reactive } from 'vue'
import apiClient from '../api'
import { useI18n } from '../i18n'

export function useDialogs({ folders }) {
  const { t } = useI18n()
  // ===== Confirm Modal =====
  const confirmDialog = reactive({
    visible: false,
    title: '',
    message: '',
    okLabel: 'Confirm',
    danger: false,
  })
  let confirmResolve = null

  function showConfirm({ title = 'Confirm', message = '', okLabel = 'Confirm', danger = false } = {}) {
    confirmDialog.title = title
    confirmDialog.message = message
    confirmDialog.okLabel = okLabel
    confirmDialog.danger = danger
    confirmDialog.visible = true
    return new Promise(resolve => { confirmResolve = resolve })
  }

  function acceptConfirm() {
    confirmDialog.visible = false
    if (confirmResolve) { confirmResolve(true); confirmResolve = null }
  }

  function cancelConfirm() {
    confirmDialog.visible = false
    if (confirmResolve) { confirmResolve(false); confirmResolve = null }
  }

  // ===== Prompt Modal =====
  const promptVisible = ref(false)
  const promptTitle = ref('')
  const promptValue = ref('')
  let promptResolve = null

  function showPrompt(title, defaultValue = '') {
    promptTitle.value = title
    promptValue.value = defaultValue
    promptVisible.value = true
    return new Promise(resolve => {
      promptResolve = resolve
    })
  }

  function submitPrompt() {
    promptVisible.value = false
    if (promptResolve) {
      promptResolve(promptValue.value)
      promptResolve = null
    }
  }

  function cancelPrompt() {
    promptVisible.value = false
    if (promptResolve) {
      promptResolve(null)
      promptResolve = null
    }
  }

  // ===== Copy dialog (iOS PWA fallback) =====
  // Copy dialog — shown as iOS PWA fallback when clipboard API fails
  const copyDialog = reactive({ visible: false, content: '' })

  // Copy from the fallback dialog. This runs inside a fresh click gesture, so the
  // clipboard/execCommand calls work here even when the original (post-await)
  // attempt failed for lack of user activation.
  async function copyFromDialog(textarea) {
    const content = copyDialog.content
    const ta = textarea
    if (ta) {
      ta.focus({ preventScroll: true })
      ta.setSelectionRange(0, ta.value.length)
    }
    try {
      if (navigator.clipboard?.writeText) {
        await navigator.clipboard.writeText(content)
        copyDialog.visible = false
        return
      }
    } catch (_) {}
    try {
      if (document.execCommand('copy')) {
        copyDialog.visible = false
        return
      }
    } catch (_) {}
    // Leave the dialog open with the text selected so the user can copy manually.
  }

  // ===== Folder Picker Modal =====
  // Folder Picker Modal State
  const folderPicker = reactive({ visible: false, selected: '', newFolderActive: false, newFolderName: '' })
  let folderPickerResolve = null

  function showFolderPicker(defaultFolder = '') {
    folderPicker.selected = defaultFolder
    folderPicker.newFolderActive = false
    folderPicker.newFolderName = ''
    folderPicker.visible = true
    return new Promise(resolve => { folderPickerResolve = resolve })
  }

  function closeFolderPicker() {
    folderPicker.visible = false
    folderPicker.newFolderActive = false
    if (folderPickerResolve) { folderPickerResolve(null); folderPickerResolve = null }
  }

  function confirmFolderPicker() {
    folderPicker.visible = false
    folderPicker.newFolderActive = false
    if (folderPickerResolve) { folderPickerResolve(folderPicker.selected); folderPickerResolve = null }
  }

  function startCreateFolderInPicker() {
    folderPicker.newFolderActive = true
    folderPicker.newFolderName = ''
  }

  function cancelNewFolderInPicker() {
    folderPicker.newFolderActive = false
    folderPicker.newFolderName = ''
  }

  async function submitNewFolderInPicker() {
    const name = folderPicker.newFolderName.trim()
    if (!name) { cancelNewFolderInPicker(); return }
    const parent = folderPicker.selected
    const path = parent ? parent + '/' + name : name
    try {
      await apiClient.createFolder(path)
      const res = await apiClient.listFolders()
      folders.value = res.data || []
      folderPicker.selected = path
      folderPicker.newFolderActive = false
      folderPicker.newFolderName = ''
    } catch (e) {
      alert(t('errors.createFolderFailed'))
    }
  }

  return {
    confirmDialog,
    showConfirm,
    acceptConfirm,
    cancelConfirm,
    promptVisible,
    promptTitle,
    promptValue,
    showPrompt,
    submitPrompt,
    cancelPrompt,
    copyDialog,
    copyFromDialog,
    folderPicker,
    showFolderPicker,
    closeFolderPicker,
    confirmFolderPicker,
    startCreateFolderInPicker,
    cancelNewFolderInPicker,
    submitNewFolderInPicker,
  }
}

import { ref, nextTick } from 'vue'
import { isTimestampName } from '../utils'

function createClientId() {
  try { if (crypto.randomUUID) return crypto.randomUUID() } catch (_) {}
  return Date.now().toString(36) + Math.random().toString(36).slice(2)
}

function parentOf(path) {
  const parts = (path || '').split('/')
  return parts.length > 1 ? parts.slice(0, -1).join('/') : ''
}

export function useNoteEditor() {
  const editingNote = ref(null)
  const editName = ref('')
  const editTags = ref([])
  const editFolder = ref('')
  const editContent = ref('')
  const tagInput = ref('')
  const editorKey = ref(0)
  const isDirty = ref(false)
  const isSaving = ref(false)
  const editorMode = ref('wysiwyg')
  let editorReady = false

  function loadDocument(data) {
    editorReady = false
    editingNote.value = data
    editName.value = isTimestampName(data.name) ? '' : (data.name || '')
    editTags.value = [...(data.tags || [])]
    editContent.value = data.content || ''
    editFolder.value = parentOf(data.path)
    isDirty.value = false
    editorKey.value++
  }

  function restoreDraft(entry) {
    loadDocument({
      content: entry.content || '',
      path: entry.path || '',
      name: entry.name || '',
      tags: entry.tags || [],
      clientId: entry.clientId || (entry.op === 'create' ? entry.key : createClientId()),
    })
    editName.value = entry.name || ''
    editFolder.value = entry.folder || ''
    isDirty.value = true
  }

  function createDocument(folder = '') {
    editorReady = false
    editingNote.value = { content: '', path: '', clientId: createClientId() }
    editName.value = ''
    editTags.value = []
    editFolder.value = folder
    editContent.value = ''
    isDirty.value = false
    editorKey.value++
  }

  function clearDocument() {
    editingNote.value = null
    isDirty.value = false
  }

  function onEditorUpdate(markdown) {
    if (!editorReady) return
    editContent.value = markdown
    isDirty.value = true
  }

  function onEditorReady(markdown) {
    if (typeof markdown === 'string') editContent.value = markdown
    editorReady = true
  }

  function addTag() {
    const value = tagInput.value.trim()
    if (value && !editTags.value.includes(value)) {
      editTags.value.push(value)
      isDirty.value = true
    }
    tagInput.value = ''
  }

  async function toggleEditorMode() {
    await nextTick()
    const switchingToWysiwyg = editorMode.value === 'raw'
    editorMode.value = switchingToWysiwyg ? 'wysiwyg' : 'raw'
    if (switchingToWysiwyg && editingNote.value) {
      editingNote.value.content = editContent.value
      editorKey.value++
    }
  }

  return {
    editingNote,
    editName,
    editTags,
    editFolder,
    editContent,
    tagInput,
    editorKey,
    isDirty,
    isSaving,
    editorMode,
    loadDocument,
    restoreDraft,
    createDocument,
    clearDocument,
    onEditorUpdate,
    onEditorReady,
    addTag,
    toggleEditorMode,
  }
}

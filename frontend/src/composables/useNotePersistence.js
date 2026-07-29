import { ref } from 'vue'
import { isTimestampName } from '../utils'
import { buildEntry, outboxPut } from './outbox.js'

function parentOf(path) {
  const parts = (path || '').split('/')
  return parts.length > 1 ? parts.slice(0, -1).join('/') : ''
}

export function useNotePersistence({
  api,
  editor,
  enqueue = outboxPut,
  makeEntry = buildEntry,
  onSaved = () => {},
} = {}) {
  if (!api) throw new Error('useNotePersistence requires an API implementation')

  const {
    editingNote, editName, editTags, editFolder, editContent,
    isDirty, isSaving, loadDocument,
  } = editor
  const saveError = ref(null)

  async function openDocument(note) {
    const response = await api.getNote(note.path)
    loadDocument(response.data)
    return response.data
  }

  async function queueCurrentWrite() {
    try {
      await enqueue(makeEntry({ editingNote, editContent, editName, editTags, editFolder }))
    } catch (_) {}
  }

  async function saveNote({ silent = false, replay = null } = {}) {
    const fromReplay = Boolean(replay)
    if (!fromReplay && isSaving.value) return

    const content = fromReplay ? replay.content : editContent.value
    const tags = fromReplay ? replay.tags : [...(editTags.value || [])]
    const name = fromReplay ? replay.name : editName.value
    const folder = fromReplay ? replay.folder : editFolder.value
    const path = fromReplay ? replay.path : editingNote.value?.path
    if (!fromReplay) isSaving.value = true

    try {
      let resultNode
      if (path) {
        const originalTitle = fromReplay
          ? ''
          : (isTimestampName(editingNote.value.name) ? '' : (editingNote.value.name || ''))
        const payload = { content, tags }
        if (name !== originalTitle) payload.rename = name
        let response = await api.updateNote(path, payload)
        resultNode = response.data
        if (folder !== parentOf(resultNode.path)) {
          response = await api.moveNote(resultNode.path, folder)
          resultNode = response.data
        }
      } else {
        const response = await api.createNote({ content, name: name || '', folder, tags })
        resultNode = response.data
      }

      if (!fromReplay) {
        const unchanged =
          editContent.value === content &&
          editName.value === name &&
          editFolder.value === folder &&
          JSON.stringify(editTags.value || []) === JSON.stringify(tags)

        editingNote.value.path = resultNode.path
        editingNote.value.name = resultNode.name
        editingNote.value.tags = [...tags]
        editingNote.value.content = content
        if (editName.value === name) {
          editName.value = isTimestampName(resultNode.name) ? '' : (resultNode.name || '')
        }
        if (editFolder.value === folder) editFolder.value = parentOf(resultNode.path)
        isDirty.value = !unchanged
        saveError.value = null
        onSaved(resultNode)
      } else if (editingNote.value && (
        (!path && replay.clientId && editingNote.value.clientId === replay.clientId) ||
        (path && editingNote.value.path === path)
      )) {
        editingNote.value.path = resultNode.path
        editingNote.value.name = resultNode.name
        const replayStillCurrent =
          editContent.value === content &&
          editName.value === name &&
          editFolder.value === folder &&
          JSON.stringify(editTags.value || []) === JSON.stringify(tags)
        if (replayStillCurrent) {
          editName.value = isTimestampName(resultNode.name) ? '' : (resultNode.name || '')
          editFolder.value = parentOf(resultNode.path)
          isDirty.value = false
        }
      }
      return resultNode
    } catch (error) {
      if (error?.response?.status === 401) {
        if (fromReplay) throw error
        await queueCurrentWrite()
        return
      }
      if (fromReplay || silent) throw error
      if (!error.response) {
        await queueCurrentWrite()
      } else {
        saveError.value = error.response?.data?.error || error.message
      }
    } finally {
      if (!fromReplay) isSaving.value = false
    }
  }

  async function deleteCurrent() {
    if (!editingNote.value?.path) return
    await api.deleteNote(editingNote.value.path)
    isDirty.value = false
  }

  return {
    saveError,
    openDocument,
    saveNote,
    deleteCurrent,
  }
}

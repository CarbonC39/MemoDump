import { ref } from 'vue'
import { isTimestampName } from '../utils'
import { buildEntry, buildDeleteEntry, outboxPut, outboxDelete } from './outbox.js'

function parentOf(path) {
  const parts = (path || '').split('/')
  return parts.length > 1 ? parts.slice(0, -1).join('/') : ''
}

export function useNotePersistence({
  api,
  editor,
  enqueue = outboxPut,
  remove = outboxDelete,
  makeEntry = buildEntry,
  makeDeleteEntry = buildDeleteEntry,
  onSaved = () => {},
} = {}) {
  if (!api) throw new Error('useNotePersistence requires an API implementation')

  const {
    editingNote, editName, editTags, editFolder, editContent,
    isDirty, isSaving, loadDocument,
  } = editor
  const saveError = ref(null)
  // A local revision conflict (409 local_revision_conflict) leaves the editor
  // buffer intact and flags the note for the user instead of overwriting.
  const conflict = ref(false)

  async function openDocument(note) {
    conflict.value = false
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
          ? (isTimestampName(replay.originalName)
              ? ''
              : (replay.originalName ?? name))
          : (isTimestampName(editingNote.value.name) ? '' : (editingNote.value.name || ''))
        // The CAS baseline. Replay entries and loaded notes carry a revision;
        // a legacy queued entry predating revisions falls back to the current
        // durable revision so an offline change never bypasses CAS.
        const capturedRev = fromReplay
          ? (replay.baseRevision || '')
          : (editingNote.value?.revision || '')
        let baseRevision = capturedRev
        if (!baseRevision) {
          try {
            const doc = await api.getNote(path)
            baseRevision = doc.data.revision || ''
          } catch (_) { baseRevision = '' }
        }
        const payload = { content, tags }
        if (baseRevision) payload.baseRevision = baseRevision
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
        if (resultNode.revision) editingNote.value.revision = resultNode.revision
        if (editName.value === name) {
          editName.value = isTimestampName(resultNode.name) ? '' : (resultNode.name || '')
        }
        if (editFolder.value === folder) editFolder.value = parentOf(resultNode.path)
        isDirty.value = !unchanged
        saveError.value = null
        conflict.value = false
        onSaved(resultNode)
      } else if (editingNote.value && (
        (!path && replay.clientId && editingNote.value.clientId === replay.clientId) ||
        (path && editingNote.value.path === path)
      )) {
        editingNote.value.path = resultNode.path
        editingNote.value.name = resultNode.name
        if (resultNode.revision) editingNote.value.revision = resultNode.revision
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

      // A successful write supersedes any queued offline entry for this note.
      if (resultNode?.path) {
        try { await remove(resultNode.path) } catch (_) {}
      }
      return resultNode
    } catch (error) {
      if (error?.response?.status === 401) {
        if (fromReplay) throw error
        await queueCurrentWrite()
        return
      }
      if (error?.response?.status === 409) {
        // The note changed since it was read. Keep the editor buffer, never
        // overwrite, and surface a visible conflict state.
        conflict.value = true
        if (fromReplay || silent) throw error
        saveError.value = null
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
    const path = editingNote.value.path
    const baseRevision = editingNote.value?.revision || ''
    try {
      await api.deleteNote(path, baseRevision)
      isDirty.value = false
      conflict.value = false
    } catch (error) {
      if (error?.response) throw error
      // Network failure: queue the delete (carrying the CAS baseline) so it
      // replays when connectivity returns.
      try {
        await enqueue(makeDeleteEntry({ editingNote }))
      } catch (_) {}
    }
  }

  return {
    saveError,
    conflict,
    openDocument,
    saveNote,
    deleteCurrent,
  }
}

import { describe, expect, it, vi } from 'vitest'
import { useNoteEditor } from './useNoteEditor'
import { useNotePersistence } from './useNotePersistence'

describe('useNotePersistence', () => {
  it('keeps manual creates single-flight', async () => {
    let resolveCreate
    const api = {
      createNote: vi.fn(() => new Promise(resolve => { resolveCreate = resolve })),
    }
    const editor = useNoteEditor()
    editor.createDocument('')
    editor.onEditorReady('')
    editor.onEditorUpdate('body')
    const persistence = useNotePersistence({ api, editor, enqueue: vi.fn() })

    const first = persistence.saveNote()
    const second = persistence.saveNote()
    expect(api.createNote).toHaveBeenCalledTimes(1)

    resolveCreate({ data: { path: 'note.md', name: 'note', tags: [] } })
    await first
    await second
    expect(editor.editingNote.value.path).toBe('note.md')
  })

  it('preserves edits made while a save request is in flight', async () => {
    let resolveUpdate
    const api = {
      updateNote: vi.fn(() => new Promise(resolve => { resolveUpdate = resolve })),
    }
    const editor = useNoteEditor()
    editor.loadDocument({ path: 'a.md', name: 'a', tags: [], content: 'old' })
    editor.onEditorReady('old')
    editor.onEditorUpdate('saving')
    const persistence = useNotePersistence({ api, editor, enqueue: vi.fn() })

    const saving = persistence.saveNote()
    editor.onEditorUpdate('newer')
    resolveUpdate({ data: { path: 'a.md', name: 'a', tags: [] } })
    await saving

    expect(editor.editContent.value).toBe('newer')
    expect(editor.isDirty.value).toBe(true)
  })

  it('queues a network failure without surfacing a server error', async () => {
    const enqueue = vi.fn().mockResolvedValue(undefined)
    const api = {
      updateNote: vi.fn().mockRejectedValue(new Error('offline')),
    }
    const editor = useNoteEditor()
    editor.loadDocument({ path: 'a.md', name: 'a', tags: [], content: 'old' })
    editor.onEditorReady('old')
    editor.onEditorUpdate('new')
    const persistence = useNotePersistence({
      api,
      editor,
      enqueue,
      makeEntry: () => ({ key: 'a.md' }),
    })

    await persistence.saveNote()
    expect(enqueue).toHaveBeenCalledWith({ key: 'a.md' })
    expect(persistence.saveError.value).toBeNull()
    expect(editor.isSaving.value).toBe(false)
  })

  it('does not rename timestamp notes while replaying an offline update', async () => {
    const api = {
      updateNote: vi.fn().mockResolvedValue({
        data: { path: '2026-07-29_120000.md', name: '2026-07-29_120000', tags: [] },
      }),
    }
    const editor = useNoteEditor()
    const persistence = useNotePersistence({ api, editor, enqueue: vi.fn() })

    await persistence.saveNote({
      replay: {
        path: '2026-07-29_120000.md',
        originalName: '2026-07-29_120000',
        name: '',
        content: 'offline edit',
        tags: [],
        folder: '',
      },
    })

    expect(api.updateNote).toHaveBeenCalledWith(
      '2026-07-29_120000.md',
      { content: 'offline edit', tags: [] },
    )
  })
})

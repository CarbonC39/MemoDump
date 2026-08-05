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
    editor.loadDocument({ path: 'a.md', name: 'a', tags: [], content: 'old', revision: 'r1' })
    editor.onEditorReady('old')
    editor.onEditorUpdate('saving')
    const persistence = useNotePersistence({ api, editor, enqueue: vi.fn() })

    const saving = persistence.saveNote()
    editor.onEditorUpdate('newer')
    resolveUpdate({ data: { path: 'a.md', name: 'a', tags: [], revision: 'r2' } })
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

describe('local revision contract (Phase 0)', () => {
  it('sends the loaded revision as baseRevision and adopts the returned one', async () => {
    const api = {
      updateNote: vi.fn().mockResolvedValue({ data: { path: 'a.md', name: 'a', tags: [], revision: 'r2' } }),
    }
    const editor = useNoteEditor()
    editor.loadDocument({ path: 'a.md', name: 'a', tags: [], content: 'old', revision: 'r1' })
    editor.onEditorReady('old')
    editor.onEditorUpdate('new')
    const persistence = useNotePersistence({ api, editor, enqueue: vi.fn() })
    await persistence.saveNote()
    expect(api.updateNote).toHaveBeenCalledWith('a.md', expect.objectContaining({ baseRevision: 'r1' }))
    expect(editor.editingNote.value.revision).toBe('r2')
  })

  it('flags a visible conflict on 409 and preserves the editor buffer', async () => {
    const api = {
      updateNote: vi.fn().mockRejectedValue({
        response: { status: 409, data: { error: { code: 'local_revision_conflict' } } },
      }),
    }
    const enqueue = vi.fn()
    const editor = useNoteEditor()
    editor.loadDocument({ path: 'a.md', name: 'a', tags: [], content: 'old', revision: 'r1' })
    editor.onEditorReady('old')
    editor.onEditorUpdate('mine')
    const persistence = useNotePersistence({ api, editor, enqueue })
    await persistence.saveNote()
    expect(persistence.conflict.value).toBe(true)
    expect(editor.editContent.value).toBe('mine')
    expect(enqueue).not.toHaveBeenCalled()
  })

  it('replays an offline update with its captured baseRevision', async () => {
    const api = {
      updateNote: vi.fn().mockResolvedValue({ data: { path: 'a.md', name: 'a', tags: [] } }),
    }
    const editor = useNoteEditor()
    const persistence = useNotePersistence({ api, editor, enqueue: vi.fn() })
    await persistence.saveNote({
      replay: {
        path: 'a.md', content: 'offline', tags: [], name: 'a', folder: '',
        baseRevision: 'r1', originalName: 'a',
      },
    })
    expect(api.updateNote).toHaveBeenCalledWith('a.md', expect.objectContaining({ baseRevision: 'r1' }))
  })

  it('queues an offline delete carrying its baseRevision', async () => {
    const api = { deleteNote: vi.fn().mockRejectedValue(new Error('offline')) }
    const enqueue = vi.fn().mockResolvedValue(undefined)
    const editor = useNoteEditor()
    editor.loadDocument({ path: 'a.md', name: 'a', tags: [], content: 'x', revision: 'r1' })
    const persistence = useNotePersistence({ api, editor, enqueue })
    await persistence.deleteCurrent()
    expect(enqueue).toHaveBeenCalledWith(expect.objectContaining({ op: 'delete', path: 'a.md', baseRevision: 'r1' }))
  })

  it('removes a queued entry after a successful live save', async () => {
    const api = {
      updateNote: vi.fn().mockResolvedValue({ data: { path: 'a.md', name: 'a', tags: [], revision: 'r2' } }),
    }
    const remove = vi.fn().mockResolvedValue(undefined)
    const editor = useNoteEditor()
    editor.loadDocument({ path: 'a.md', name: 'a', tags: [], content: 'old', revision: 'r1' })
    editor.onEditorReady('old')
    editor.onEditorUpdate('new')
    const persistence = useNotePersistence({ api, editor, enqueue: vi.fn(), remove })
    await persistence.saveNote()
    expect(remove).toHaveBeenCalledWith('a.md')
  })
})

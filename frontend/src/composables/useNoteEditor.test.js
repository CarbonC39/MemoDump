import { describe, it, expect } from 'vitest'
import { useNoteEditor } from './useNoteEditor'

describe('useNoteEditor', () => {
  it('loads an existing document and derives editable metadata', () => {
    const editor = useNoteEditor()
    editor.loadDocument({
      path: 'work/note.md',
      name: 'note',
      tags: ['a'],
      content: 'body',
    })
    expect(editor.editingNote.value.path).toBe('work/note.md')
    expect(editor.editFolder.value).toBe('work')
    expect(editor.editName.value).toBe('note')
    expect(editor.editContent.value).toBe('body')
    expect(editor.isDirty.value).toBe(false)
  })

  it('creates a client-identified unsaved document', () => {
    const editor = useNoteEditor()
    editor.createDocument('drafts')
    expect(editor.editingNote.value.path).toBe('')
    expect(editor.editingNote.value.clientId).toBeTruthy()
    expect(editor.editFolder.value).toBe('drafts')
    expect(editor.isDirty.value).toBe(false)
  })

  it('ignores updates until the active document is ready', () => {
    const editor = useNoteEditor()
    editor.loadDocument({ path: 'n.md', name: 'n', tags: [], content: 'old' })
    editor.onEditorUpdate('stale')
    expect(editor.isDirty.value).toBe(false)
    expect(editor.editContent.value).toBe('old')
    editor.onEditorReady('old')
    editor.onEditorUpdate('new')
    expect(editor.editContent.value).toBe('new')
    expect(editor.isDirty.value).toBe(true)
  })

  it('does not mark an unchanged editor callback as dirty', () => {
    const editor = useNoteEditor()
    editor.loadDocument({ path: 'n.md', name: 'n', tags: [], content: 'same' })
    editor.onEditorReady('same')
    editor.onEditorUpdate('same')

    expect(editor.editContent.value).toBe('same')
    expect(editor.isDirty.value).toBe(false)
  })

  it('marks checking or unchecking a checklist item as dirty', () => {
    const editor = useNoteEditor()
    editor.loadDocument({ path: 'n.md', name: 'n', tags: [], content: '- [ ] task\n' })
    editor.onEditorReady('- [ ] task\n')
    editor.onEditorUpdate('- [x] task\n')
    expect(editor.isDirty.value).toBe(true)

    editor.loadDocument({ path: 'n.md', name: 'n', tags: [], content: '- [x] task\n' })
    editor.onEditorReady('- [x] task\n')
    editor.onEditorUpdate('- [ ] task\n')
    expect(editor.isDirty.value).toBe(true)
  })

  it('accepts post-mount markdown normalization as a clean baseline', () => {
    const editor = useNoteEditor()
    editor.loadDocument({
      path: 'n.md',
      name: 'n',
      tags: [],
      content: '- [x] \n',
    })
    editor.onEditorReady('- [x] \n')
    editor.onEditorReady('- [ ] \n')

    expect(editor.editContent.value).toBe('- [ ] \n')
    expect(editor.isDirty.value).toBe(false)

    editor.onEditorUpdate('- [ ] typed\n')
    expect(editor.isDirty.value).toBe(true)
  })

  it('resets readiness when consecutive documents replace the editor content', () => {
    const editor = useNoteEditor()
    editor.loadDocument({ path: 'a.md', name: 'a', tags: [], content: 'a' })
    editor.onEditorReady('a')
    editor.onEditorUpdate('edited a')
    expect(editor.isDirty.value).toBe(true)

    editor.loadDocument({ path: 'b.md', name: 'b', tags: [], content: 'b' })
    editor.onEditorUpdate('late update from a')
    expect(editor.editContent.value).toBe('b')
    expect(editor.isDirty.value).toBe(false)

    editor.onEditorReady('b')
    editor.onEditorUpdate('edited b')
    expect(editor.editContent.value).toBe('edited b')
    expect(editor.isDirty.value).toBe(true)
  })

  it('marks a newly added tag dirty', () => {
    const editor = useNoteEditor()
    editor.loadDocument({ path: 'n.md', name: 'n', tags: [], content: '' })
    editor.tagInput.value = 'tag'
    editor.addTag()
    expect(editor.editTags.value).toEqual(['tag'])
    expect(editor.isDirty.value).toBe(true)
  })
})

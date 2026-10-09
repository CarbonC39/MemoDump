import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'

const source = readFileSync(new URL('./MilkdownEditor.vue', import.meta.url), 'utf8')

describe('MilkdownEditor document-change wiring', () => {
  it('tracks ProseMirror document transactions instead of DOM input targets', () => {
    expect(source).toContain('previousState.doc.eq(view.state.doc)')
    expect(source).toContain('_changeBridge?.changed(userChange)')
    expect(source).not.toContain("addEventListener('input'")
    expect(source).not.toContain('_hasUserInput')
  })

  it('uses the reliable native caret instead of the virtual cursor overlay', () => {
    expect(source).toContain('.addFeature(cursor, { virtual: false })')
    expect(source).toContain('caret-color: var(--primary)')
  })
})

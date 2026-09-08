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

  it('delegates task-list checked-state repair to the extracted plugin module', () => {
    expect(source).toContain("import { buildTaskItemResetPlugin } from './taskItemReset'")
    expect(source).toContain('buildTaskItemResetPlugin()')
    // The position-aware logic lives in taskItemReset.js, not inline here.
    expect(source).not.toContain('wasCheckedBefore')
  })

  it('never typewriter-scrolls on content updates, only on cursor navigation', () => {
    // publishUpdate must not scroll — typing relies on native caret-into-view.
    const publishUpdateBlock = source.slice(source.indexOf('publishUpdate:'), source.indexOf('publishReady:'))
    expect(publishUpdateBlock).not.toContain('doTypewriterScroll')
    expect(source).toContain("requestAnimationFrame(doTypewriterScroll)")
    // The keydown listener is restricted to navigation keys.
    expect(source).toContain("if (NAV_SCROLL_KEYS.has(e.key)) requestAnimationFrame(doTypewriterScroll)")
  })

  it('resets the shared content scroll when replacing the active document', () => {
    const replaceBlock = source.slice(source.indexOf('function replaceDocument'), source.indexOf('watch(() => props.documentVersion'))
    expect(replaceBlock).toContain('scrollContainer.scrollTop = 0')
  })
})

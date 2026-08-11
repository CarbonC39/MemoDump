// @vitest-environment happy-dom
import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import ContextMenu from './ContextMenu.vue'

describe('ContextMenu', () => {
  it('uses clearly different symbols for copy and duplicate', () => {
    const wrapper = mount(ContextMenu, { props: { visible: true } })
    const items = wrapper.findAll('.context-menu-item')
    const copyIcon = items[1].find('.material-icons-outlined').text()
    const duplicateIcon = items[2].find('.material-icons-outlined').text()

    expect(copyIcon).toBe('content_copy')
    expect(duplicateIcon).toBe('library_add')
    expect(duplicateIcon).not.toBe(copyIcon)
  })
})

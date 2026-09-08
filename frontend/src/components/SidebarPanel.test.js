// @vitest-environment happy-dom
import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import SidebarPanel from './SidebarPanel.vue'

function mountSidebar(isLocalBuild, props = {}) {
  return mount(SidebarPanel, {
    props: {
      themeIcon: 'dark_mode',
      isLocalBuild,
      serverNoAuth: true,
      ...props,
    },
  })
}

describe('SidebarPanel local-storage indicator', () => {
  it('places an icon-only browser-storage hint in the footer row', () => {
    const wrapper = mountSidebar(true)
    const indicator = wrapper.find('.local-storage-indicator')

    expect(indicator.exists()).toBe(true)
    expect(indicator.find('.material-icons-outlined').text()).toBe('storage')
    expect(wrapper.find('.footer-icons').text()).not.toContain('Saved in this browser')
    expect(indicator.find('button').attributes('aria-label')).toContain('stored in this browser')
  })

  it('does not show the browser-storage hint in server mode', () => {
    expect(mountSidebar(false).find('.local-storage-indicator').exists()).toBe(false)
  })
})

describe('SidebarPanel desktop collapse', () => {
  it('emits update:collapsed when the collapse toggle is clicked', async () => {
    const wrapper = mountSidebar(false, { collapsed: false })
    const toggle = wrapper.find('.sidebar-collapse-toggle')

    expect(toggle.exists()).toBe(true)
    expect(toggle.attributes('title')).toContain('Collapse')
    await toggle.trigger('click')
    expect(wrapper.emitted('update:collapsed')).toEqual([[true]])
  })

  it('reflects the collapsed prop and switches the toggle affordance', () => {
    const wrapper = mountSidebar(false, { collapsed: true })
    expect(wrapper.find('.sidebar').classes()).toContain('collapsed')
    expect(wrapper.find('.sidebar-collapse-toggle .material-icons-outlined').text()).toBe('menu')
    expect(wrapper.find('.sidebar-collapse-toggle').attributes('title')).toContain('Expand')
  })

  it('only shows the root drop zone while a card or folder is being dragged', async () => {
    const wrapper = mountSidebar(false, { storageExpanded: true, dragging: false })
    const dropZone = wrapper.find('.root-drop-zone')

    expect(dropZone.element.style.display).toBe('none')
    await wrapper.setProps({ dragging: true })
    expect(dropZone.element.style.display).not.toBe('none')
  })

  it('expands the rail before opening the folder tree from the collapsed storage item', async () => {
    const wrapper = mountSidebar(false, { collapsed: true, storageExpanded: false })
    await wrapper.find('.storage-nav-item').trigger('click')

    expect(wrapper.emitted('update:collapsed')).toEqual([[false]])
    expect(wrapper.emitted('toggle-storage')).toHaveLength(1)
  })

  it('keeps an already-open folder section open when expanded from the rail', async () => {
    const wrapper = mountSidebar(false, { collapsed: true, storageExpanded: true })
    await wrapper.find('.storage-nav-item').trigger('click')

    expect(wrapper.emitted('update:collapsed')).toEqual([[false]])
    expect(wrapper.emitted('toggle-storage')).toBeUndefined()
  })
})

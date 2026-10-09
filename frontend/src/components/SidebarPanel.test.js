// @vitest-environment happy-dom
import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import SidebarPanel from './SidebarPanel.vue'

function mountSidebar(isLocalBuild) {
  return mount(SidebarPanel, {
    props: {
      themeIcon: 'dark_mode',
      isLocalBuild,
      serverNoAuth: true,
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

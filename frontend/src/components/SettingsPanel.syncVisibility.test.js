// @vitest-environment happy-dom
import { afterEach, describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import SettingsPanel from './SettingsPanel.vue'
import { setCloudSyncAvailable } from '../composables/runtime'

function mountSettings() {
  return mount(SettingsPanel, {
    global: {
      stubs: {
        SyncPanel: { template: '<div class="sync-panel-stub" />' },
      },
    },
  })
}

describe('SettingsPanel cloud-sync divider', () => {
  afterEach(() => setCloudSyncAvailable(false))

  it('does not leave an extra divider when cloud sync is unavailable', () => {
    setCloudSyncAvailable(false)
    const wrapper = mountSettings()
    expect(wrapper.findAll('.settings-divider')).toHaveLength(5)
    wrapper.unmount()
  })

  it('adds the cloud-sync divider when the runtime supports sync', () => {
    setCloudSyncAvailable(true)
    const wrapper = mountSettings()
    expect(wrapper.findAll('.settings-divider')).toHaveLength(6)
    wrapper.unmount()
  })
})

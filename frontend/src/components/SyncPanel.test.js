// @vitest-environment happy-dom
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import SyncPanel from './SyncPanel.vue'
import { setCloudSyncAvailable } from '../composables/runtime'
import apiClient from '../api'

vi.mock('../api', () => ({
  default: {
    syncStatus: vi.fn().mockResolvedValue({ data: {} }),
    syncRecovery: vi.fn().mockResolvedValue({ data: { recovery: [] } }),
    syncEnable: vi.fn(),
    syncRun: vi.fn(),
    syncDisable: vi.fn(),
    syncReset: vi.fn(),
    syncTest: vi.fn(),
    syncRecoveryRestore: vi.fn(),
  },
}))

describe('SyncPanel runtime visibility (R6.0)', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  it('renders the cloud-sync section when the runtime owns sync (Wails)', async () => {
    setCloudSyncAvailable(true)
    const wrapper = mount(SyncPanel)
    expect(wrapper.find('.settings-section').exists()).toBe(true)
    wrapper.unmount()
  })

  it('renders nothing and makes no sync call when sync is unavailable (CLI Web server / PWA)', async () => {
    setCloudSyncAvailable(false)
    const wrapper = mount(SyncPanel)
    expect(wrapper.find('.settings-section').exists()).toBe(false)
    // The panel must not even initialize sync state on a runtime without sync.
    expect(apiClient.syncStatus).not.toHaveBeenCalled()
    expect(apiClient.syncRecovery).not.toHaveBeenCalled()
    wrapper.unmount()
  })
})

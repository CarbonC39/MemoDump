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
    // The S3 configuration form is browser-owned; Wails configures the server.
    expect(wrapper.find('.sync-config').exists()).toBe(false)
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

  it('renders the browser S3 configuration form in the Pure frontend/PWA build', async () => {
    // R6.5: the local build owns its sync engine, so its panel carries the
    // independent note-sync configuration form plus the browser warnings.
    vi.stubEnv('VITE_LOCAL', '1')
    vi.resetModules()
    const LocalSyncPanel = (await import('./SyncPanel.vue')).default
    const wrapper = mount(LocalSyncPanel)
    expect(wrapper.find('.settings-section').exists()).toBe(true)
    expect(wrapper.find('.sync-config').exists()).toBe(true)
    expect(wrapper.text()).toContain('Endpoint')
    wrapper.unmount()
    vi.unstubAllEnvs()
  })
})

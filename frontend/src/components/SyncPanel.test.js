// @vitest-environment happy-dom
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import SyncPanel from './SyncPanel.vue'
import { setCloudSyncAvailable } from '../composables/runtime'
import { getSyncConfigState } from '../composables/useSyncConfig'
import { getSyncSettings } from '../composables/useSyncSettings'
import apiClient from '../api'

vi.mock('../api', () => ({
  default: {
    syncStatus: vi.fn().mockResolvedValue({ data: {} }),
    syncRecovery: vi.fn().mockResolvedValue({ data: { recovery: [] } }),
    syncEnable: vi.fn().mockResolvedValue({ data: {} }),
    syncRun: vi.fn().mockResolvedValue({ data: { Synced: true } }),
    syncDisable: vi.fn().mockResolvedValue({ data: {} }),
    syncReset: vi.fn().mockResolvedValue({ data: {} }),
    syncTest: vi.fn().mockResolvedValue({ data: {} }),
    syncConfigGet: vi.fn().mockResolvedValue({ data: {} }),
    syncConfigSave: vi.fn().mockResolvedValue({ data: { configured: true, editable: true } }),
    syncConfigTest: vi.fn().mockResolvedValue({ data: {} }),
    syncRecoveryRestore: vi.fn().mockResolvedValue({ data: {} }),
  },
}))

describe('SyncPanel runtime visibility (R6.0)', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    Object.assign(getSyncSettings(), {
      initialized: false,
      enabled: false,
      connected: false,
      connection: false,
      connectionError: '',
      identityError: '',
      lastRun: null,
      lastCompleted: null,
      syncRunning: false,
      autoPaused: false,
      pauseReason: '',
      recovery: [],
      recoveryError: '',
      busy: false,
    })
    Object.assign(getSyncConfigState(), {
      endpoint: '',
      region: 'us-east-1',
      bucket: '',
      prefix: '',
      accessKey: '',
      secretKey: '',
      forcePathStyle: true,
      configured: false,
      editable: true,
    })
    apiClient.syncStatus.mockResolvedValue({ data: {} })
    apiClient.syncRecovery.mockResolvedValue({ data: { recovery: [] } })
    apiClient.syncConfigGet.mockResolvedValue({ data: {} })
  })

  it('renders the cloud-sync section with the server-backed config form (Wails)', async () => {
    setCloudSyncAvailable(true)
    const wrapper = mount(SyncPanel)
    expect(wrapper.find('.settings-section').exists()).toBe(true)
    // R6.7: the Wails desktop shows the note-sync config form too, backed by
    // the server-side /api/sync/config endpoints. The CORS template is
    // browser-direct only.
    expect(wrapper.find('.sync-config').exists()).toBe(true)
    expect(wrapper.find('.sync-config-heading').exists()).toBe(false)
    expect(wrapper.find('.sync-state-card').exists()).toBe(false)
    expect(apiClient.syncConfigGet).toHaveBeenCalled()
    expect(wrapper.find('.cors-template').exists()).toBe(false)
    expect(wrapper.text()).toContain('Connect')
    expect(wrapper.text()).not.toContain('Save configuration')
    expect(wrapper.text()).not.toContain('Test connection')
    wrapper.unmount()
  })

  it('renders nothing and makes no sync call when sync is unavailable (CLI Web server)', async () => {
    setCloudSyncAvailable(false)
    const wrapper = mount(SyncPanel)
    expect(wrapper.find('.settings-section').exists()).toBe(false)
    // The panel must not even initialize sync state on a runtime without sync.
    expect(apiClient.syncStatus).not.toHaveBeenCalled()
    expect(apiClient.syncRecovery).not.toHaveBeenCalled()
    expect(apiClient.syncConfigGet).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('hides the config form once sync is connected (no save/Enable race surface)', async () => {
    setCloudSyncAvailable(true)
    getSyncSettings().connected = true
    const wrapper = mount(SyncPanel)
    // While connected the provider must not be editable; the backend also
    // refuses a config save under the lifecycle lock.
    expect(wrapper.find('.sync-config').exists()).toBe(false)
    expect(wrapper.find('.sync-state-card').exists()).toBe(false)
    expect(wrapper.find('.sync-status-row').exists()).toBe(true)
    expect(wrapper.find('[data-testid="sync-run"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="sync-disconnect"]').exists()).toBe(true)
    expect(wrapper.text()).not.toContain('Test')
    wrapper.unmount()
  })

  it('saves editable storage configuration and connects with one action', async () => {
    setCloudSyncAvailable(true)
    const wrapper = mount(SyncPanel)
    await flushPromises()

    const inputs = wrapper.findAll('.sync-field input')
    await inputs[0].setValue('https://s3.example.com')
    await inputs[1].setValue('notes')
    await inputs[2].setValue('access')
    await inputs[3].setValue('secret')
    await wrapper.find('[data-testid="sync-connect"]').trigger('click')
    await flushPromises()

    expect(apiClient.syncConfigSave).toHaveBeenCalledWith(expect.objectContaining({
      endpoint: 'https://s3.example.com',
      bucket: 'notes',
      accessKey: 'access',
      secretKey: 'secret',
    }))
    expect(apiClient.syncEnable).toHaveBeenCalledTimes(1)
    expect(apiClient.syncConfigTest).not.toHaveBeenCalled()
    expect(apiClient.syncTest).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('shows an explicit disclosure caret for advanced settings', async () => {
    setCloudSyncAvailable(true)
    const wrapper = mount(SyncPanel)
    await flushPromises()

    const details = wrapper.find('.sync-advanced')
    expect(details.find('.details-caret').text()).toBe('expand_more')
    expect(details.attributes('open')).toBeUndefined()
    details.element.open = true
    await details.trigger('toggle')
    expect(details.element.open).toBe(true)
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
    // The CORS template must expose Retry-After so the browser can read the
    // provider's 429 instruction (R6.6 scheduler backoff), not just ETag.
    expect(wrapper.find('.cors-template').exists()).toBe(true)
    expect(wrapper.text()).toContain('"ExposeHeaders": ["ETag", "Retry-After"]')
    wrapper.unmount()
    vi.unstubAllEnvs()
  })
})

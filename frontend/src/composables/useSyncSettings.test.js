import { beforeEach, describe, expect, it, vi } from 'vitest'

vi.mock('../api', () => ({
  default: {
    syncStatus: vi.fn(),
    syncEnable: vi.fn(),
    syncRun: vi.fn(),
    syncDisable: vi.fn(),
    syncReset: vi.fn(),
    syncTest: vi.fn(),
    syncRecovery: vi.fn(),
    syncRecoveryRestore: vi.fn(),
  },
}))

import apiClient from '../api'
import { setCloudSyncAvailable } from './runtime'
import {
  applyLightweightStatus,
  enableSync,
  getSyncSettings,
  initSyncSettings,
  refreshSyncSettings,
  runSync,
  disableSync,
  resetSync,
  restoreRecovery,
  setOnSyncChanged,
  testSync,
} from './useSyncSettings'

describe('sync settings', () => {
  beforeEach(() => {
    // Runtime matrix: this suite exercises the Wails surface explicitly (R6.0);
    // the CLI Web server and Pure frontend/PWA runtimes have no sync surface.
    setCloudSyncAvailable(true)
    vi.clearAllMocks()
    apiClient.syncStatus.mockResolvedValue({
      data: {
        enabled: true,
        connected: true,
        experimental: true,
        noE2EE: true,
        lastRun: { Synced: true, Conflicts: 1, LastError: '' },
        lastCompleted: '2026-08-08T00:00:00Z',
        recoveryCount: 1,
      },
    })
    apiClient.syncRecovery.mockResolvedValue({
      data: { recovery: [{ syncId: 'x', stateHash: 'a', path: 'a.md', size: 9 }] },
    })
    apiClient.syncEnable.mockResolvedValue({ data: { enabled: true } })
    apiClient.syncRun.mockResolvedValue({ data: { Synced: true, Conflicts: 0 } })
    apiClient.syncDisable.mockResolvedValue({ data: { enabled: false } })
    apiClient.syncReset.mockResolvedValue({ data: { ok: true } })
    apiClient.syncTest.mockResolvedValue({ data: { ok: true } })
    apiClient.syncRecoveryRestore.mockResolvedValue({ data: { ok: true, path: 'a.md' } })
  })

  it('hydrates the redacted status and exposes the no-E2EE flag', async () => {
    await initSyncSettings()
    const s = getSyncSettings()
    expect(s.enabled).toBe(true)
    expect(s.connected).toBe(true)
    expect(s.noE2EE).toBe(true)
    expect(s.lastRun.Conflicts).toBe(1)
    expect(s.recovery).toHaveLength(1)
  })

  it('enable / run / disable / test drive the lifecycle', async () => {
    await initSyncSettings()
    await enableSync()
    expect(apiClient.syncEnable).toHaveBeenCalled()
    await runSync()
    expect(getSyncSettings().lastRun.Synced).toBe(true)
    expect(getSyncSettings().lastCompleted).toBeTruthy()
    await disableSync()
    expect(getSyncSettings().connected).toBe(false)
    expect(getSyncSettings().enabled).toBe(false)
    apiClient.syncStatus.mockResolvedValue({
      data: { enabled: false, connected: false, connection: false },
    })
    await resetSync()
    expect(apiClient.syncReset).toHaveBeenCalled()
    expect(getSyncSettings().connection).toBe(false)
    await testSync()
    expect(apiClient.syncTest).toHaveBeenCalled()
  })

  it('hydrates the connection record state and surfaces a connection error', async () => {
    apiClient.syncStatus.mockResolvedValue({
      data: { enabled: false, connected: false, connection: true, connectionError: 'corrupt' },
    })
    await refreshSyncSettings()
    const s = getSyncSettings()
    expect(s.connection).toBe(true)
    expect(s.connectionError).toBe('corrupt')
  })

  it('surfaces a recovery error instead of faking an empty list', async () => {
    apiClient.syncRecovery.mockRejectedValue({ response: { data: { error: 'sync index corrupt' } } })
    await refreshSyncSettings()
    const s = getSyncSettings()
    expect(s.recovery).toHaveLength(0)
    expect(s.recoveryError).toContain('sync index corrupt')
  })

  it('runtime without cloud sync never calls the sync API', async () => {
    setCloudSyncAvailable(false)
    await refreshSyncSettings()
    expect(apiClient.syncStatus).not.toHaveBeenCalled()
    expect(apiClient.syncRecovery).not.toHaveBeenCalled()
  })

  it('applyLightweightStatus updates the panel scheduling state without recovery', () => {
    applyLightweightStatus({
      enabled: true, connected: true, experimental: true, noE2EE: true,
      syncRunning: true, autoEnabled: true, autoIntervalSecs: 300,
      nextRun: '2026-08-09T00:05:00Z', autoPaused: true, pauseReason: 'permission',
      lastRun: { Synced: false, LastError: 'permission' },
      lastCompleted: '2026-08-09T00:00:00Z', lastTrigger: 'periodic', recoveryCount: 1,
    })
    const s = getSyncSettings()
    expect(s.syncRunning).toBe(true)
    expect(s.nextRun).toBe('2026-08-09T00:05:00Z')
    expect(s.autoPaused).toBe(true)
    expect(s.pauseReason).toBe('permission')
    expect(s.lastTrigger).toBe('periodic')
    expect(s.recoveryCount).toBe(1)
  })

  it('refreshSyncSettings re-fetches recovery details on every call (panel reopen retries)', async () => {
    // A temporarily-failed recovery fetch must be retried when the panel is
    // opened again: refreshSyncSettings is not guarded by initialized.
    await refreshSyncSettings()
    const first = apiClient.syncRecovery.mock.calls.length
    apiClient.syncRecovery.mockRejectedValueOnce({ response: { data: { error: 'temporary' } } })
    await refreshSyncSettings()
    expect(getSyncSettings().recoveryError).toContain('temporary')
    await refreshSyncSettings()
    expect(apiClient.syncRecovery.mock.calls.length).toBeGreaterThan(first + 1)
    expect(getSyncSettings().recovery).toHaveLength(1)
  })

  it('refreshes the UI after a manual run even when the cycle reports Synced=false', async () => {
    const onSyncChanged = vi.fn()
    setOnSyncChanged(onSyncChanged)
    // A cycle that pulled notes but deferred others reports Synced=false.
    apiClient.syncRun.mockResolvedValue({ data: { Synced: false, Retry: 1, Conflicts: 0 } })
    await runSync()
    expect(onSyncChanged).toHaveBeenCalled()
    expect(getSyncSettings().lastTrigger).toBe('manual')
  })

  it('refreshes the UI after restoring a recovery copy', async () => {
    const onSyncChanged = vi.fn()
    setOnSyncChanged(onSyncChanged)
    apiClient.syncRecoveryRestore.mockResolvedValue({ data: { ok: true, path: 'a.md' } })
    await restoreRecovery('x', 'y')
    expect(onSyncChanged).toHaveBeenCalled()
  })
})

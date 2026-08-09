import { beforeEach, describe, expect, it, vi } from 'vitest'

vi.mock('../api', () => ({
  default: {
    syncStatus: vi.fn(),
    syncEnable: vi.fn(),
    syncRun: vi.fn(),
    syncDisable: vi.fn(),
    syncTest: vi.fn(),
  },
}))

import apiClient from '../api'
import {
  enableSync,
  getSyncSettings,
  initSyncSettings,
  runSync,
  disableSync,
  testSync,
} from './useSyncSettings'

describe('sync settings', () => {
  beforeEach(() => {
    apiClient.syncStatus.mockResolvedValue({
      data: {
        enabled: true,
        connected: true,
        experimental: true,
        noE2EE: true,
        lastRun: { Synced: true, Conflicts: 1, LastError: '' },
        lastCompleted: '2026-08-08T00:00:00Z',
        recovery: [{ syncId: 'x', stateHash: 'a' }],
      },
    })
    apiClient.syncEnable.mockResolvedValue({ data: { enabled: true } })
    apiClient.syncRun.mockResolvedValue({ data: { Synced: true, Conflicts: 0 } })
    apiClient.syncDisable.mockResolvedValue({ data: { enabled: false } })
    apiClient.syncTest.mockResolvedValue({ data: { ok: true } })
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
    await testSync()
    expect(apiClient.syncTest).toHaveBeenCalled()
  })
})

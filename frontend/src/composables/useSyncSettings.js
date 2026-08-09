// Cloud-sync settings for the experimental phase. The server exposes a
// redacted status (never credentials or provider URLs); the panel drives the
// enable / run / disable / test lifecycle and shows the no-E2EE warning, last
// completed run, conflicts, and recovery state.
import { reactive } from 'vue'
import apiClient from '../api'

export const isLocalBuild = import.meta.env.VITE_LOCAL === '1'

const state = reactive({
  initialized: false,
  enabled: false,
  connected: false,
  connection: false,
  connectionError: '',
  experimental: false,
  noE2EE: false,
  lastRun: null,
  lastCompleted: null,
  recovery: [],
  recoveryError: '',
  busy: false,
})

export async function initSyncSettings() {
  if (state.initialized) return
  await refreshSyncSettings()
}

export async function refreshSyncSettings() {
  if (isLocalBuild) {
    state.initialized = true
    return
  }
  try {
    const resp = await apiClient.syncStatus()
    const d = resp.data || {}
    state.enabled = !!d.enabled
    state.connected = !!d.connected
    state.connection = !!d.connection
    state.connectionError = d.connectionError || ''
    state.experimental = !!d.experimental
    state.noE2EE = !!d.noE2EE
    state.lastRun = d.lastRun || null
    state.lastCompleted = d.lastCompleted || null
    // The status carries only the count; fetch the detailed copies always (the
    // endpoint safely returns an empty list when sync was never enabled), and
    // surface a failure instead of faking an empty list — a corrupt index must
    // show a recovery error even when the status reports disabled.
    try {
      const rec = await apiClient.syncRecovery()
      state.recovery = (rec.data && rec.data.recovery) || []
      state.recoveryError = ''
    } catch (e) {
      state.recovery = []
      state.recoveryError = e?.response?.data?.error || 'recovery-unavailable'
    }
  } catch (_) {
    // A pre-login 401 redirects to Login; leave initialization retryable.
    return
  }
  state.initialized = true
}

async function withBusy(fn) {
  state.busy = true
  try {
    return await fn()
  } finally {
    state.busy = false
  }
}

export async function enableSync() {
  return withBusy(async () => {
    const { data } = await apiClient.syncEnable()
    state.enabled = true
    await refreshSyncSettings()
    return data
  })
}

export async function runSync() {
  return withBusy(async () => {
    const { data } = await apiClient.syncRun()
    state.lastRun = data
    state.lastCompleted = new Date().toISOString()
    return data
  })
}

export async function disableSync() {
  return withBusy(async () => {
    const { data } = await apiClient.syncDisable()
    state.connected = false
    state.enabled = false
    return data
  })
}

export async function resetSync() {
  return withBusy(async () => {
    const { data } = await apiClient.syncReset()
    state.connected = false
    state.enabled = false
    state.connection = false
    await refreshSyncSettings()
    return data
  })
}

export async function testSync() {
  return withBusy(async () => {
    const { data } = await apiClient.syncTest()
    return data
  })
}

export async function restoreRecovery(syncId, stateHash) {
  return withBusy(async () => {
    const { data } = await apiClient.syncRecoveryRestore({ syncId, stateHash })
    return data
  })
}

export function getSyncSettings() {
  return state
}

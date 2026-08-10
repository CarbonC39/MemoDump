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
  lastTrigger: '',
  autoEnabled: false,
  autoIntervalSecs: 0,
  syncRunning: false,
  nextRun: null,
  autoPaused: false,
  pauseReason: '',
  recoveryCount: 0,
  recovery: [],
  recoveryError: '',
  busy: false,
})

// applyLightweightStatus writes the redacted status fields (no recovery content)
// into the shared panel state. The 30-second auto-sync poller calls this so the
// settings panel reflects running / next-run / paused state without downloading
// recovery copies on every poll.
export function applyLightweightStatus(d) {
  d = d || {}
  state.enabled = !!d.enabled
  state.connected = !!d.connected
  state.connection = !!d.connection
  state.connectionError = d.connectionError || ''
  state.experimental = !!d.experimental
  state.noE2EE = !!d.noE2EE
  state.lastRun = d.lastRun || null
  state.lastCompleted = d.lastCompleted || null
  state.lastTrigger = d.lastTrigger || ''
  state.autoEnabled = !!d.autoEnabled
  state.autoIntervalSecs = d.autoIntervalSecs || 0
  state.syncRunning = !!d.syncRunning
  state.nextRun = d.nextRun || null
  state.autoPaused = !!d.autoPaused
  state.pauseReason = d.pauseReason || ''
  state.recoveryCount = d.recoveryCount || 0
}

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
    applyLightweightStatus(resp.data)
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
    state.lastTrigger = 'manual'
    if (data && data.Synced && onManualSynced) {
      onManualSynced()
    }
    return data
  })
}

// onManualSynced is registered by the app shell: after a successful manual run
// the visible list and the open editor are refreshed through the same safe
// logic the auto-sync poller uses, so a manual pull is never stale.
let onManualSynced = null

export function setOnManualSynced(fn) {
  onManualSynced = fn
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

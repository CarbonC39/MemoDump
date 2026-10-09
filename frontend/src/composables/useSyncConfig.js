// Note-sync configuration. The form values live in a reactive module store; the
// backend depends on the runtime: the Pure frontend/PWA build persists them in
// browser localStorage through the in-page service (plaintext-credential
// warning), while the Wails desktop persists them server-side through the
// /api/sync/config endpoints (the secretKey is never returned to the browser).
import { reactive } from 'vue'
import apiClient from '../api'
import { cloudSyncAvailable, isLocalBuild } from './runtime'
import { getSyncConfig, saveSyncConfig, testSyncConfig } from '../sync/browserService'

const state = reactive({
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

function applyServer(d) {
  state.endpoint = d.endpoint || ''
  state.region = d.region || 'us-east-1'
  state.bucket = d.bucket || ''
  state.prefix = d.prefix || ''
  state.accessKey = d.accessKey || ''
  state.secretKey = '' // the server never returns the secretKey
  state.forcePathStyle = d.forcePathStyle !== false
  state.configured = !!d.configured
  state.editable = d.editable !== false
}

export async function initSyncConfig() {
  // A runtime without cloud sync (CLI Web server) has no sync configuration.
  if (!cloudSyncAvailable()) return
  if (isLocalBuild) {
    Object.assign(state, getSyncConfig())
    state.configured = Boolean(state.endpoint && state.bucket && state.accessKey && state.secretKey)
    state.editable = true
  } else {
    try {
      const { data } = await apiClient.syncConfigGet()
      applyServer(data || {})
    } catch (_) {
      // A pre-login 401 redirects to Login; leave initialization retryable.
    }
  }
}

export function getSyncConfigState() {
  return state
}

export function isSyncConfigEditable() {
  return state.editable
}

export function configSnapshot() {
  return {
    endpoint: state.endpoint,
    region: state.region,
    bucket: state.bucket,
    prefix: state.prefix,
    accessKey: state.accessKey,
    secretKey: state.secretKey,
    forcePathStyle: state.forcePathStyle,
  }
}

export async function saveConfig() {
  if (isLocalBuild) return saveSyncConfig(configSnapshot())
  const { data } = await apiClient.syncConfigSave(configSnapshot())
  applyServer(data)
  return { ok: true }
}

export async function testConfig() {
  if (isLocalBuild) return testSyncConfig(configSnapshot())
  const { data } = await apiClient.syncConfigTest(configSnapshot())
  return data
}

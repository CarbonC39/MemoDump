// Note-sync configuration for the Pure frontend/PWA build (R6.5). The form
// values live in a reactive module store; Save persists them through the
// browser service (localStorage, plaintext-credential warning) and Test probes
// the current draft without persisting it. This is independent of image
// settings and only exists where the browser owns sync.
import { reactive } from 'vue'
import { getSyncConfig, saveSyncConfig, testSyncConfig } from '../sync/browserService'

const state = reactive({
  endpoint: '',
  region: 'us-east-1',
  bucket: '',
  prefix: '',
  accessKey: '',
  secretKey: '',
  forcePathStyle: true,
})

export function initSyncConfig() {
  Object.assign(state, getSyncConfig())
}

export function getSyncConfigState() {
  return state
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
  return saveSyncConfig(configSnapshot())
}

export async function testConfig() {
  return testSyncConfig(configSnapshot())
}

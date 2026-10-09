// Image hosting settings. Pure-frontend build (VITE_LOCAL=1) stores the config
// in localStorage (may contain secrets — the settings UI warns about this).
// Web/Wails builds read the effective config from /api/config/image (secrets never
// leave the server) and persist edits via PUT /api/config/image.
import { reactive } from 'vue'
import apiClient from '../api'
import { s3Probe } from './s3Client'

export const isLocalBuild = import.meta.env.VITE_LOCAL === '1'

const LOCAL_KEY = 'memodump_image_config'

const state = reactive({
  initialized: false,
  provider: isLocalBuild ? 'off' : 'local', // local build: off | s3; server: local | s3
  configured: false,
  editable: true,
  endpoint: '',
  region: 'us-east-1',
  bucket: '',
  prefix: '',
  publicBaseUrl: '',
  accessKey: '',
  secretKey: '',
  forcePathStyle: true,
  cleanupEnabled: false,
  targetId: isLocalBuild ? '' : 'local',
})

function applyLocal(cfg) {
  state.provider = cfg.provider === 's3' ? 's3' : (isLocalBuild ? 'off' : 'local')
  state.configured = cfg.provider === 's3'
  state.endpoint = cfg.endpoint || ''
  state.region = cfg.region || 'us-east-1'
  state.bucket = cfg.bucket || ''
  state.prefix = cfg.prefix || ''
  state.publicBaseUrl = cfg.publicBaseUrl || ''
  state.accessKey = cfg.accessKey || ''
  state.secretKey = cfg.secretKey || ''
  state.forcePathStyle = cfg.forcePathStyle !== false
  state.cleanupEnabled = cfg.cleanup?.enabled === true
  state.targetId = ''
}

function applyServer(image) {
  state.provider = image.provider === 's3' ? 's3' : 'local'
  state.configured = !!image.configured
  state.editable = image.editable !== false
  state.bucket = image.bucket || ''
  state.prefix = image.prefix || ''
  state.publicBaseUrl = image.publicBaseUrl || ''
  state.endpoint = image.endpoint || ''
  state.region = image.region || 'us-east-1'
  state.accessKey = image.accessKey || ''
  state.forcePathStyle = image.forcePathStyle !== false
  state.cleanupEnabled = image.cleanup?.enabled === true
  state.targetId = image.targetId || (state.provider === 'local' ? 'local' : '')
  // Secrets are never returned by the server; keep the in-memory form values.
}

function loadLocal() {
  try {
    const raw = localStorage.getItem(LOCAL_KEY)
    if (raw) applyLocal(JSON.parse(raw))
  } catch (_) {}
}

export async function initImageSettings() {
  if (state.initialized) return
  await refreshImageSettings()
}

export async function refreshImageSettings() {
  if (isLocalBuild) {
    loadLocal()
  } else {
    try {
      const resp = await apiClient.imageConfigGet()
      applyServer(resp.data || {})
    } catch (_) {
      // A pre-login 401 redirects to Login. Leave initialization retryable so
      // the newly mounted authenticated workspace fetches the real config.
      return
    }
  }
  state.initialized = true
}

export function getImageSettings() {
  return state
}

// buildS3Target returns an immutable plain-object snapshot of the current S3
// destination (no secrets are needed here except at upload time).
export function currentTarget() {
  if (state.provider === 's3') {
    const endpoint = state.endpoint.trim()
    const bucket = state.bucket.trim()
    const prefix = state.prefix.trim()
    // Server/Wails builds upload through /api/images. The server deliberately
    // does not expose its endpoint or credentials, so use the public
    // destination as the stable target identity and mark the transport as a
    // proxy. Pure-frontend builds sign and upload directly in the browser.
    const proxy = import.meta.env.VITE_LOCAL !== '1'
    return {
      id: proxy
        ? state.targetId
        : `s3:${endpoint}|${bucket}|${prefix}`,
      provider: 's3',
      transport: proxy ? 'proxy' : 'direct',
      endpoint,
      region: state.region.trim(),
      bucket,
      prefix,
      publicBaseUrl: state.publicBaseUrl.trim(),
      forcePathStyle: state.forcePathStyle,
      accessKey: state.accessKey,
      secretKey: state.secretKey,
    }
  }
  if (state.provider === 'local') {
    return { id: state.targetId || 'local', provider: 'local' }
  }
  return null
}

export async function saveImageConfig(cfg) {
  if (isLocalBuild) {
    localStorage.setItem(LOCAL_KEY, JSON.stringify(cfg))
    applyLocal(cfg)
    return { ok: true }
  }
  const { data } = await apiClient.imageConfigSave(cfg)
  applyServer(data)
  return { ok: true }
}

export async function testImageConnection(cfg) {
  if (isLocalBuild) {
    return s3Probe({
      endpoint: cfg.endpoint,
      region: cfg.region,
      bucket: cfg.bucket,
      prefix: cfg.prefix,
      publicBaseUrl: cfg.publicBaseUrl,
      accessKey: cfg.accessKey,
      secretKey: cfg.secretKey,
    })
  }
  const { data } = await apiClient.imageConfigTest(cfg)
  return data
}

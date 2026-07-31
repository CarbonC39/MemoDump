// Media outbox — the durable queue for staged images.
//
// Implements the canonical media staging flow from docs/plan-v2.md:
// validate → detect format from magic bytes → sha256 key → durable IndexedDB
// persist BEFORE the editor node exists → object URL → return URL → immediate
// upload attempt → pending/uploaded/completed lifecycle.
//
// Lifecycle:
//   pending  --upload 2xx-->  uploaded  --read verified-->  completed (entry+blob removed)
//   Entries are never removed because a note was deleted (orphans accepted).
//   The durable blob is deleted only at completed.
import { ref } from 'vue'
import apiClient from '../api'
import { currentTarget } from './useImageSettings'
import { objectKey, objectUrl, s3PutObject, sha256Hex } from './s3Client'

const DB_NAME = 'memodump-media'
const STORE = 'pending'
const DB_VERSION = 1
const MAX_IMAGE_BYTES = 20 << 20 // 20 MiB, mirrored from the server
const FORMAT_PREFIX_BYTES = 4096

const BACKOFF_MS = [30_000, 120_000, 300_000, 900_000, 1_800_000]
const FLUSH_INTERVAL_MS = 30_000
const NOTICE_DURATION_MS = 5000

export const pendingImageCount = ref(0)
// Non-blocking, calm in-app notice ({ code } or null). Rendered by MainView.
export const mediaNotice = ref(null)

const _entryByUrl = new Map()
const _blobUrls = new Map()
const _inFlight = new Set()
let _dbPromise = null
let _initialized = false
let _flushTimer = null
let _noticeTimer = null
// Serialize all IndexedDB operations. Overlapping transactions on the same
// store are a classic source of "transaction inactive" errors in browsers and
// can deadlock fake-indexeddb; a simple promise chain avoids both.
let _dbQueue = Promise.resolve()

function withDbLock(fn) {
  const run = _dbQueue.then(fn)
  _dbQueue = run.catch(() => {})
  return run
}

function openDB() {
  if (_dbPromise) return _dbPromise
  _dbPromise = new Promise((resolve, reject) => {
    if (!('indexedDB' in globalThis)) {
      reject(new Error('indexeddb unavailable'))
      return
    }
    const req = indexedDB.open(DB_NAME, DB_VERSION)
    req.onupgradeneeded = () => {
      const db = req.result
      if (!db.objectStoreNames.contains(STORE)) {
        db.createObjectStore(STORE, { keyPath: 'id' })
      }
    }
    req.onsuccess = () => resolve(req.result)
    req.onerror = () => reject(req.error)
  })
  return _dbPromise
}

function reqP(request) {
  return new Promise((resolve, reject) => {
    request.onsuccess = () => resolve(request.result)
    request.onerror = () => reject(request.error)
  })
}

async function store(mode) {
  const db = await openDB()
  return db.transaction(STORE, mode).objectStore(STORE)
}

async function allEntries() {
  return withDbLock(async () => {
    const s = await store('readonly')
    return reqP(s.getAll())
  })
}

async function getEntry(id) {
  return withDbLock(async () => {
    const s = await store('readonly')
    return reqP(s.get(id))
  })
}

async function putEntry(entry) {
  return withDbLock(async () => {
    const s = await store('readwrite')
    await reqP(s.put(entry))
  })
}

async function deleteEntry(id) {
  return withDbLock(async () => {
    const s = await store('readwrite')
    await reqP(s.delete(id))
  })
}

function showNotice(code) {
  mediaNotice.value = { code }
  if (_noticeTimer) clearTimeout(_noticeTimer)
  _noticeTimer = setTimeout(() => { mediaNotice.value = null }, NOTICE_DURATION_MS)
}

async function refreshCount() {
  try {
    const entries = await allEntries()
    pendingImageCount.value = entries.filter((e) => e.state === 'pending' || e.state === 'uploaded').length
  } catch (_) {}
}

function backoffDelay(attempts) {
  return BACKOFF_MS[Math.min(Math.max(attempts - 1, 0), BACKOFF_MS.length - 1)]
}

function parseRetryAfter(value) {
  if (!value) return null
  const seconds = Number(value)
  if (Number.isFinite(seconds) && seconds > 0) return seconds * 1000
  const date = Date.parse(value)
  if (Number.isFinite(date)) return Math.max(0, date - Date.now())
  return null
}

// classifyError maps any upload/verify error to the plan's error taxonomy.
function classifyError(e) {
  const code = e?.response?.data?.error?.code
  if (code) {
    switch (code) {
      case 'verify_failed': return { kind: 'verify-failed', retryable: true }
      case 'invalid_config': return { kind: 'invalid-config', retryable: false }
      case 'invalid_image_key':
      case 'unsupported_format':
      case 'format_mismatch':
      case 'hash_mismatch': return { kind: 'invalid-file', retryable: false }
      case 'image_too_large': return { kind: 'too-large', retryable: false }
      default: break
    }
  }
  const status = e?.status || e?.response?.status
  if (status) {
    switch (status) {
      case 401: return { kind: 'auth', retryable: false }
      case 403: return { kind: 'permission', retryable: false }
      case 400: return { kind: 'invalid-file', retryable: false }
      case 404: return { kind: 'invalid-config', retryable: false }
      case 409: return { kind: 'invalid-config', retryable: false }
      case 413: return { kind: 'too-large', retryable: false }
      case 408:
      case 425:
      case 429:
      case 500:
      case 502:
      case 503:
      case 504: {
        const retryAfter = parseRetryAfter(e?.response?.headers?.get?.('retry-after'))
        return { kind: 'server', retryable: true, retryAfter }
      }
      default: return { kind: 'server', retryable: true }
    }
  }
  if (e?.kind === 'verify-failed') return { kind: 'verify-failed', retryable: true }
  if (e?.name === 'TypeError' && navigator.onLine) {
    return { kind: 'cors', retryable: false }
  }
  return { kind: 'network', retryable: true }
}

// detectImageFormat mirrors the server's detectFormat over the first 4 KiB:
// the canonical extension comes from the detected format, never the filename.
async function detectImageFormat(file) {
  const header = new Uint8Array(await file.slice(0, FORMAT_PREFIX_BYTES).arrayBuffer())
  const png = [0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A]
  if (header.length >= 8 && png.every((b, i) => header[i] === b)) {
    return { ext: '.png', contentType: 'image/png' }
  }
  if (header.length >= 3 && header[0] === 0xFF && header[1] === 0xD8 && header[2] === 0xFF) {
    return { ext: '.jpg', contentType: 'image/jpeg' }
  }
  const gif = new TextDecoder().decode(header.slice(0, 6))
  if (gif === 'GIF87a' || gif === 'GIF89a') {
    return { ext: '.gif', contentType: 'image/gif' }
  }
  if (header.length >= 12 &&
      new TextDecoder().decode(header.slice(0, 4)) === 'RIFF' &&
      new TextDecoder().decode(header.slice(8, 12)) === 'WEBP') {
    return { ext: '.webp', contentType: 'image/webp' }
  }
  const avif = detectAvif(header)
  if (avif) return { ext: '.avif', contentType: 'image/avif' }
  return null
}

function detectAvif(header) {
  const ascii = (from, to) => new TextDecoder().decode(header.slice(from, to))
  if (header.length < 12 || ascii(4, 8) !== 'ftyp') return false
  const boxSize = (header[0] << 24) | (header[1] << 16) | (header[2] << 8) | header[3]
  if (boxSize < 8 || boxSize > header.length || boxSize > FORMAT_PREFIX_BYTES) return false
  const brands = new Set(['avif', 'avis'])
  if (brands.has(ascii(8, 12))) return true
  for (let i = 16; i + 4 <= boxSize; i += 4) {
    if (brands.has(ascii(i, i + 4))) return true
  }
  return false
}

function ensureObjectUrl(url) {
  if (_blobUrls.has(url)) return _blobUrls.get(url)
  const entry = _entryByUrl.get(url)
  if (!entry?.blob) return null
  const objectUrl = URL.createObjectURL(entry.blob)
  _blobUrls.set(url, objectUrl)
  return objectUrl
}

// resolvePending feeds Crepe's proxyDomURL: pending URLs render from their
// local blob; everything else passes through untouched.
export function resolvePending(url) {
  return ensureObjectUrl(url) || url
}

// swapDisplayed flips any rendered <img> from its blob object URL to the final
// URL after completion/verification.
function swapDisplayed(url) {
  const objectUrl = _blobUrls.get(url)
  if (!objectUrl) return
  if (typeof document === 'undefined') return
  document.querySelectorAll(`img[src="${objectUrl}"]`).forEach((img) => { img.src = url })
}

export function revokeObjectUrls() {
  for (const objectUrl of _blobUrls.values()) URL.revokeObjectURL(objectUrl)
  _blobUrls.clear()
}

// ---- canonical media staging flow ----

export async function stageAndUploadImage(file) {
  const target = currentTarget()
  if (!target) {
    showNotice('image-off')
    return ''
  }
  if (file.size > MAX_IMAGE_BYTES) {
    showNotice('image-too-large')
    return ''
  }
  const format = await detectImageFormat(file)
  if (!format) {
    showNotice('image-format')
    return ''
  }
  const hash = await sha256Hex(await file.arrayBuffer())
  const key = hash + format.ext
  const url = target.provider === 'local'
    ? `/api/images/${key}`
    : objectUrl(target, key)
  const id = `${target.id}:${key}`

  const existing = await getEntry(id)
  if (existing) {
    _entryByUrl.set(existing.url, existing)
    ensureObjectUrl(existing.url)
    flushPendingImages().catch(() => {})
    return existing.url
  }

  // Durably persist BEFORE returning the URL (step 3 of the canonical flow):
  // if the app dies right after the node is inserted, the entry survives.
  const entry = {
    id,
    url,
    key,
    target: snapshotTarget(target),
    contentType: format.contentType,
    state: 'pending',
    blob: file,
    attempts: 0,
    nextAttemptAt: 0,
    lastError: null,
    createdAt: Date.now(),
  }
  await putEntry(entry)
  _entryByUrl.set(url, entry)
  ensureObjectUrl(url)
  refreshCount().catch(() => {})
  // Immediately attempt the upload (step 6).
  attemptUpload(id).catch(() => {})
  return url
}

function snapshotTarget(target) {
  return target.provider === 's3'
    ? {
        id: target.id,
        provider: 's3',
        endpoint: target.endpoint,
        region: target.region,
        bucket: target.bucket,
        prefix: target.prefix,
        publicBaseUrl: target.publicBaseUrl,
      }
    : { id: target.id, provider: 'local' }
}

function targetStillConfigured(entry) {
  const current = currentTarget()
  return !!current && current.id === entry.target.id
}

async function uploadPending(entry) {
  if (entry.target.provider === 'local') {
    // Web/Wails: the server proxies to the vault or to S3 and verifies public
    // readability itself; a 2xx means completed.
    const response = await apiClient.imageUpload(entry.key, entry.blob, entry.contentType)
    if (response.status >= 400) {
      const error = new Error(`Image upload failed: ${response.status}`)
      error.status = response.status
      throw error
    }
    await completeEntry(entry)
    return
  }
  // Pure frontend: direct S3 PUT.
  await s3PutObject(entry.target, entry.key, entry.blob, entry.contentType)
  entry.state = 'uploaded'
  entry.uploadedAt = Date.now()
  await putEntry(entry)
  refreshCount().catch(() => {})
}

async function verifyUploaded(entry) {
  const response = await fetch(entry.url, { method: 'GET', credentials: 'omit', mode: 'cors' })
  if (!response.ok) {
    const error = new Error(`Image not readable yet: ${response.status}`)
    error.status = response.status
    throw error
  }
  await completeEntry(entry)
}

async function completeEntry(entry) {
  await deleteEntry(entry.id)
  _entryByUrl.delete(entry.url)
  // Future renders must load the final URL; the object URL itself stays alive
  // (not revoked) until the page unload / editor destroy revoke pass.
  _blobUrls.delete(entry.url)
  swapDisplayed(entry.url)
  refreshCount().catch(() => {})
}

async function attemptUpload(id) {
  if (_inFlight.has(id)) return
  _inFlight.add(id)
  try {
    await attemptUploadInner(id)
  } finally {
    _inFlight.delete(id)
  }
}

async function attemptUploadInner(id) {
  const entry = await getEntry(id)
  if (!entry || entry.state === 'completed') return
  if (!targetStillConfigured(entry)) {
    entry.lastError = { kind: 'invalid-config', retryable: false, message: 'image host config changed; review settings' }
    await putEntry(entry)
    refreshCount().catch(() => {})
    return
  }
  try {
    if (entry.state === 'pending') {
      await uploadPending(entry)
    }
    if (entry.state === 'uploaded') {
      await verifyUploaded(entry)
    }
  } catch (e) {
    const info = classifyError(e)
    entry.attempts += 1
    entry.lastError = { kind: info.kind, retryable: info.retryable, message: e?.message || String(e) }
    if (info.retryable) {
      entry.nextAttemptAt = Date.now() + (info.retryAfter || backoffDelay(entry.attempts))
    } else {
      entry.nextAttemptAt = 0 // no automatic retries for permanent failures
    }
    await putEntry(entry)
    refreshCount().catch(() => {})
  }
}

export async function flushPendingImages() {
  if (!_initialized) await initMediaOutbox()
  let entries
  try {
    entries = await allEntries()
  } catch (_) {
    return
  }
  const now = Date.now()
  for (const entry of entries) {
    if (entry.state !== 'pending' && entry.state !== 'uploaded') continue
    if (entry.nextAttemptAt > now) continue
    // Permanent failures are only retried via retryAllPending().
    if (entry.lastError && !entry.lastError.retryable) continue
    await attemptUpload(entry.id)
  }
}

// retryAllPending resets backoff and re-attempts every entry, including
// permanent failures (used by the "retry" action in the save-status area).
export async function retryAllPending() {
  let entries
  try {
    entries = await allEntries()
  } catch (_) {
    return
  }
  for (const entry of entries) {
    if (entry.state === 'completed') continue
    entry.nextAttemptAt = 0
    entry.lastError = null
    await putEntry(entry)
  }
  await flushPendingImages()
}

export async function initMediaOutbox() {
  if (_initialized) return
  try {
    await openDB()
    for (const entry of await allEntries()) {
      if (entry.state === 'pending' || entry.state === 'uploaded') {
        _entryByUrl.set(entry.url, entry)
      }
    }
  } catch (_) {
    // IndexedDB unavailable: degrade to no persistence (entry staging will
    // fail loudly per image).
  }
  _initialized = true
  refreshCount().catch(() => {})
}

export function startMediaFlushLoop() {
  if (_flushTimer) return
  _flushTimer = setInterval(() => { flushPendingImages().catch(() => {}) }, FLUSH_INTERVAL_MS)
  globalThis.addEventListener?.('online', () => { flushPendingImages().catch(() => {}) })
  globalThis.addEventListener?.('beforeunload', revokeObjectUrls)
}

export function stopMediaFlushLoop() {
  if (_flushTimer) {
    clearInterval(_flushTimer)
    _flushTimer = null
  }
}

// Test-only: wipe the pending store so suites start clean.
export async function _mediaOutboxClear() {
  try {
    await withDbLock(async () => {
      const s = await store('readwrite')
      await reqP(s.clear())
    })
  } catch (_) {}
  _entryByUrl.clear()
  _blobUrls.clear()
  pendingImageCount.value = 0
}

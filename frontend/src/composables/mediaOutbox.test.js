import 'fake-indexeddb/auto'
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'

vi.mock('../api', () => ({
  default: {
    imageUpload: vi.fn(),
    imageConfigSave: vi.fn(),
    imageConfigTest: vi.fn(),
    config: vi.fn().mockResolvedValue({ data: { image: { provider: 'local' } } }),
  },
}))
vi.mock('./s3Client', async (importOriginal) => {
  const actual = await importOriginal()
  return { ...actual, s3PutObject: vi.fn(async () => {}) }
})

import apiClient from '../api'
import { s3PutObject } from './s3Client'
import { getImageSettings } from './useImageSettings'
import {
  stageAndUploadImage,
  resolvePending,
  flushPendingImages,
  retryAllPending,
  initMediaOutbox,
  pendingImageCount,
  mediaNotice,
  sweepExpiredEntries,
  _mediaOutboxClear,
  _mediaOutboxSeed,
} from './mediaOutbox'

const PNG_BYTES = new Uint8Array([0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 1, 2, 3, 4])

function pngFile() {
  return new File([PNG_BYTES], 'photo.png', { type: 'image/png' })
}

function tick(ms = 0) {
  return new Promise((resolve) => setTimeout(resolve, ms))
}

async function waitFor(fn, timeout = 1000) {
  const start = Date.now()
  while (Date.now() - start < timeout) {
    if (await fn()) return
    await tick(5)
  }
  throw new Error('waitFor timeout')
}

const settings = getImageSettings()

beforeEach(async () => {
  vi.clearAllMocks()
  await _mediaOutboxClear()
  await initMediaOutbox()
  settings.provider = 'local'
  settings.configured = false
  settings.endpoint = ''
  settings.bucket = ''
  settings.prefix = ''
  settings.publicBaseUrl = ''
  settings.accessKey = ''
  settings.secretKey = ''
  settings.cleanupEnabled = false
  mediaNotice.value = null
  vi.stubGlobal('fetch', vi.fn(async () => ({ ok: true })))
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.unstubAllEnvs()
})

describe('canonical staging flow (vault)', () => {
  it('durably persists before returning, then uploads and completes', async () => {
    apiClient.imageUpload.mockResolvedValue({ status: 200, data: { status: 'ok' } })
    const file = pngFile()
    const url = await stageAndUploadImage(file)
    expect(url).toMatch(/^\/api\/images\/[a-f0-9]{64}\.png$/)
    expect(apiClient.imageUpload).not.toHaveBeenCalled()

    await waitFor(() => apiClient.imageUpload.mock.calls.length === 1)
    await waitFor(() => pendingImageCount.value === 0)
    expect(apiClient.imageUpload).toHaveBeenCalledTimes(1)
    expect(apiClient.imageUpload.mock.calls[0][0]).toBe(url.replace('/api/images/', ''))
  })

  it('retries retryable failures with backoff', async () => {
    apiClient.imageUpload.mockRejectedValueOnce(new Error('network down'))
    apiClient.imageUpload.mockResolvedValue({ status: 200 })
    const url = await stageAndUploadImage(pngFile())
    await waitFor(() => apiClient.imageUpload.mock.calls.length === 1)
    await waitFor(() => pendingImageCount.value === 1)

    await retryAllPending()
    expect(apiClient.imageUpload).toHaveBeenCalledTimes(2)
    await waitFor(() => pendingImageCount.value === 0)
    expect(await resolvePending(url)).toBe(url)
  })

  it('marks permanent failures and skips them on flush', async () => {
    apiClient.imageUpload.mockRejectedValue({ status: 403, message: 'forbidden' })
    await stageAndUploadImage(pngFile())
    await waitFor(() => apiClient.imageUpload.mock.calls.length === 1)

    await flushPendingImages()
    expect(apiClient.imageUpload).toHaveBeenCalledTimes(1)
    expect(pendingImageCount.value).toBe(1)
  })

  it('rejects oversized and unrecognized files with a calm notice', async () => {
    const big = new File([new Uint8Array(21 << 20)], 'big.png', { type: 'image/png' })
    expect(await stageAndUploadImage(big)).toBe('')
    expect(mediaNotice.value?.code).toBe('image-too-large')

    const garbage = new File([new Uint8Array([9, 9, 9, 9])], 'x.png', { type: 'image/png' })
    expect(await stageAndUploadImage(garbage)).toBe('')
    expect(mediaNotice.value?.code).toBe('image-format')
  })
})

describe('S3 direct path', () => {
  beforeEach(() => {
    vi.stubEnv('VITE_LOCAL', '1')
    settings.provider = 's3'
    settings.endpoint = 'https://s3.example.com'
    settings.bucket = 'memodump'
    settings.prefix = 'img/'
    settings.publicBaseUrl = 'https://cdn.example.com'
    settings.accessKey = 'ak'
    settings.secretKey = 'sk'
  })

  it('uploads, verifies via anonymous GET, then completes', async () => {
    const url = await stageAndUploadImage(pngFile())
    expect(url).toMatch(/^https:\/\/cdn\.example\.com\/img\/[a-f0-9]{64}\.png$/)

    await waitFor(() => s3PutObject.mock.calls.length === 1)
    await waitFor(() => pendingImageCount.value === 0)
    expect(s3PutObject).toHaveBeenCalledTimes(1)
    expect(s3PutObject.mock.calls[0][0]).toMatchObject({
      accessKey: 'ak',
      secretKey: 'sk',
    })
  })

  it('keeps the blob while verification fails', async () => {
    globalThis.fetch = vi.fn(async () => ({ ok: false, status: 404 }))
    const url = await stageAndUploadImage(pngFile())
    await waitFor(() => pendingImageCount.value === 1)
    expect(resolvePending(url)).toMatch(/^blob:/)
  })
})

describe('server S3 proxy path', () => {
  it('keeps the public S3 URL but uploads through the Go proxy', async () => {
    settings.provider = 's3'
    settings.bucket = 'memodump'
    settings.prefix = 'img'
    settings.publicBaseUrl = 'https://cdn.example.com'
    apiClient.imageUpload.mockResolvedValue({ status: 201 })

    const url = await stageAndUploadImage(pngFile())
    expect(url).toMatch(/^https:\/\/cdn\.example\.com\/img\/[a-f0-9]{64}\.png$/)
    await waitFor(() => apiClient.imageUpload.mock.calls.length === 1)
    await waitFor(() => pendingImageCount.value === 0)
    expect(apiClient.imageUpload).toHaveBeenCalledTimes(1)
    expect(s3PutObject).not.toHaveBeenCalled()
  })
})

describe('provider off mode', () => {
  it('refuses with a notice and no URL', async () => {
    settings.provider = 'off'
    expect(await stageAndUploadImage(pngFile())).toBe('')
    expect(mediaNotice.value?.code).toBe('image-off')
    expect(pendingImageCount.value).toBe(0)
  })
})

describe('indexedDB cleanup sweep', () => {
  const key = 'a'.repeat(64) + '.png'
  const entry = () => ({
    id: 'local:' + key,
    url: '/api/images/' + key,
    key,
    target: { id: 'local', provider: 'local' },
    contentType: 'image/png',
    state: 'pending',
    blob: pngFile(),
    attempts: 3,
    nextAttemptAt: 0,
    lastError: { kind: 'permission', retryable: false },
    createdAt: Date.now() - 100000,
  })

  it('removes expired permanent failures only when cleanup is enabled', async () => {
    settings.cleanupEnabled = false
    await _mediaOutboxSeed(entry())
    await sweepExpiredEntries(0)
    expect(pendingImageCount.value).toBe(1)

    settings.cleanupEnabled = true
    await sweepExpiredEntries(0)
    expect(pendingImageCount.value).toBe(0)
  })

  it('keeps recent permanent failures', async () => {
    settings.cleanupEnabled = true
    const e = entry()
    e.createdAt = Date.now()
    await _mediaOutboxSeed(e)
    await sweepExpiredEntries(100000)
    expect(pendingImageCount.value).toBe(1)
  })
})

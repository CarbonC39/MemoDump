import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { readFileSync } from 'node:fs'
import { objectUrl, normalizePublicBaseUrl, normalizePrefix, s3Probe, s3PutObject, sha256Hex } from './s3Client'

const mocks = vi.hoisted(() => ({ awsFetch: vi.fn(async () => ({ ok: true })) }))
vi.mock('aws4fetch', () => ({
  AwsClient: class {
    constructor() {
      this.fetch = mocks.awsFetch
    }
  },
}))

const fixture = JSON.parse(
  readFileSync(new URL('../../../testdata/image-url-fixtures.json', import.meta.url), 'utf8')
)

describe('URL normalization (shared fixtures)', () => {
  it('builds final URLs per the shared spec', () => {
    for (const tc of fixture.normalize) {
      const target = {
        publicBaseUrl: tc.base,
        prefix: tc.prefix,
      }
      expect(objectUrl(target, tc.key)).toBe(tc.want)
    }
  })

  it('normalizes base and prefix', () => {
    expect(normalizePublicBaseUrl(' https://x.test/// ')).toBe('https://x.test')
    expect(normalizePrefix(' /a//b/ ')).toBe('a//b')
  })
})

describe('sha256Hex', () => {
  it('matches the known empty-string digest', async () => {
    expect(await sha256Hex(new Uint8Array(0))).toBe(
      'e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855'
    )
  })
})

describe('s3Probe', () => {
  const target = {
    endpoint: 'https://s3.example.com',
    region: 'us-east-1',
    bucket: 'b',
    prefix: '',
    publicBaseUrl: 'https://cdn.example.com',
    accessKey: 'ak',
    secretKey: 'sk',
  }

  beforeEach(() => {
    mocks.awsFetch.mockReset()
    mocks.awsFetch.mockImplementation(async () => ({ ok: true }))
    vi.stubGlobal('fetch', vi.fn(async () => ({ ok: true })))
  })
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('PUTs a probe, verifies anonymous GET, and deletes it', async () => {
    const result = await s3Probe(target)
    expect(result.ok).toBe(true)
    expect(result.warnings).toEqual([])
    expect(mocks.awsFetch).toHaveBeenCalledTimes(2) // PUT + DELETE
  })

  it('reports a warning when DELETE is not allowed', async () => {
    mocks.awsFetch.mockImplementation(async (url, opts) => {
      if (opts.method === 'DELETE') return { ok: false, status: 403 }
      return { ok: true }
    })
    const result = await s3Probe(target)
    expect(result.ok).toBe(true)
    expect(result.warnings[0]).toContain('could not be removed')
  })
})

describe('S3 addressing style', () => {
  it('uses virtual-host style when forcePathStyle is false', async () => {
    mocks.awsFetch.mockReset()
    mocks.awsFetch.mockResolvedValue({ ok: true })
    await s3PutObject({
      endpoint: 'https://s3.us-west-2.amazonaws.com',
      region: 'us-west-2',
      bucket: 'memo-images',
      prefix: 'notes',
      accessKey: 'ak',
      secretKey: 'sk',
      forcePathStyle: false,
    }, 'a.png', new Uint8Array([1]), 'image/png')
    expect(mocks.awsFetch.mock.calls[0][0]).toBe(
      'https://memo-images.s3.us-west-2.amazonaws.com/notes/a.png'
    )
  })
})

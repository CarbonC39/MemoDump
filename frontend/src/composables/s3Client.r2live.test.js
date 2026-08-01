// Opt-in live test of the pure-frontend direct-upload path (aws4fetch SigV4,
// path-style) against a real S3-compatible bucket. Only runs when R2_LIVE=1
// and .r2-test/image-config.json exists (the folder is gitignored):
//
//   cd frontend && R2_LIVE=1 npx vitest run s3Client.r2live.test.js
//
// Credentials are read programmatically and scrubbed from any output. This
// validates signing against the real bucket; the browser CORS layer itself
// still needs the manual checklist (R2 bucket CORS policy + real browser).
import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { AwsClient } from 'aws4fetch'
import { objectKey, objectUrl, s3Probe, s3PutObject, sha256Hex } from './s3Client'

const CONFIG = new URL('../../../.r2-test/image-config.json', import.meta.url)

function loadTarget() {
  let raw
  try {
    raw = readFileSync(CONFIG, 'utf8')
  } catch {
    return null
  }
  const cfg = JSON.parse(raw)
  const target = {
    provider: cfg.provider,
    endpoint: cfg.endpoint || '',
    region: cfg.region || '',
    bucket: cfg.bucket || '',
    prefix: cfg.prefix || '',
    publicBaseUrl: cfg.publicBaseUrl || '',
    accessKey: cfg.accessKey || '',
    secretKey: cfg.secretKey || '',
  }
  if (
    target.provider !== 's3' ||
    !target.endpoint ||
    !target.bucket ||
    !target.publicBaseUrl ||
    !target.accessKey ||
    !target.secretKey
  ) {
    return null
  }
  return target
}

function redact(target, text) {
  return String(text).replaceAll(target.accessKey, '***').replaceAll(target.secretKey, '***')
}

function liveTarget(target) {
  if (process.env.R2_LIVE === '1' && target) return target
  return null
}

describe('live R2 direct upload', () => {
  const target = liveTarget(loadTarget())
  const run = target ? it : it.skip

  run('probe: PUT, anonymous read, DELETE', async () => {
    try {
      await s3Probe(target)
    } catch (err) {
      throw new Error(redact(target, err?.message || err))
    }
  })

  run('upload + public readback + cleanup', async () => {
    const body = new Uint8Array([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, ...new Array(16).fill(0)])
    const key = (await sha256Hex(body)) + '.png'
    try {
      await s3PutObject(target, key, body, 'image/png')
    } catch (err) {
      throw new Error('upload failed: ' + redact(target, err?.message || err))
    }

    const resp = await fetch(objectUrl(target, key))
    const got = new Uint8Array(await resp.arrayBuffer())
    expect(resp.status).toBe(200)
    expect([...got]).toEqual([...body])

    // Best-effort cleanup so repeated runs stay tidy.
    const aws = new AwsClient({
      accessKeyId: target.accessKey,
      secretAccessKey: target.secretKey,
      region: target.region || 'us-east-1',
      service: 's3',
    })
    const del = await aws.fetch(
      target.endpoint.replace(/\/+$/, '') + '/' + target.bucket + '/' + objectKey(target, key),
      { method: 'DELETE' }
    )
    expect(del.ok).toBe(true)
  })
})

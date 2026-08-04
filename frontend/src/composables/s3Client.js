// Browser-side S3 helpers for the pure-frontend build (VITE_LOCAL=1).
// Uses aws4fetch (a mature, tree-shakeable SigV4 signer) rather than a
// hand-rolled signing implementation.
import { AwsClient } from 'aws4fetch'

export function normalizePublicBaseUrl(value) {
  return String(value || '').trim().replace(/\/+$/, '')
}

export function normalizePrefix(value) {
  return String(value || '').trim().replace(/^\/+|\/+$/g, '')
}

// objectUrl builds the final public URL for a key. Mirrors the Go spec in
// s3.go (shared fixtures in testdata/image-url-fixtures.json).
export function objectUrl(target, key) {
  const base = normalizePublicBaseUrl(target.publicBaseUrl)
  const prefix = normalizePrefix(target.prefix)
  return base + '/' + (prefix ? prefix + '/' : '') + key
}

export function objectKey(target, key) {
  const prefix = normalizePrefix(target.prefix)
  return prefix ? prefix + '/' + key : key
}

export function sha256Hex(data) {
  // Wails WebViews run in a secure context (https://wails.localhost in
  // production, http://localhost in dev), so crypto.subtle is available there
  // too — no Go-side HashSha256 fallback binding is needed (plan-v2.md's
  // contingency was dropped as unnecessary after this check).
  if (globalThis.crypto?.subtle?.digest) {
    return crypto.subtle.digest('SHA-256', data).then(toHex)
  }
  return Promise.reject(new Error('sha256 unavailable (crypto.subtle missing in a non-secure context)'))
}

function toHex(buffer) {
  return Array.from(new Uint8Array(buffer))
    .map((b) => b.toString(16).padStart(2, '0'))
    .join('')
}

function newAwsClient(target) {
  return new AwsClient({
    accessKeyId: target.accessKey,
    secretAccessKey: target.secretKey,
    region: target.region || 'auto',
    service: 's3',
  })
}

function putBaseUrl(target) {
  // v1 always signs a path-style URL; it works with AWS, R2, B2, MinIO, OSS
  // and COS. forcePathStyle remains a server-side (minio) concern.
  return String(target.endpoint || '').trim().replace(/\/+$/, '') + '/' + target.bucket
}

export async function s3PutObject(target, key, body, contentType) {
  const aws = newAwsClient(target)
  const response = await aws.fetch(`${putBaseUrl(target)}/${objectKey(target, key)}`, {
    method: 'PUT',
    body,
    headers: contentType ? { 'Content-Type': contentType } : undefined,
  })
  if (!response.ok) {
    const error = new Error(`S3 upload failed: ${response.status}`)
    error.status = response.status
    throw error
  }
  return response
}

// s3Probe is the pure-frontend "test connection": PUT a probe object whose key
// embeds the content hash, verify an anonymous GET of its public URL, then
// best-effort DELETE. DELETE failure is a warning, not a failure.
export async function s3Probe(target) {
  const body = new Blob(['memodump-probe'], { type: 'text/plain' })
  const hash = await sha256Hex(await body.arrayBuffer())
  const probeKey = '.memodump-probe/' + hash + '.txt'
  await s3PutObject(target, probeKey, body, 'text/plain')

  const read = await fetch(objectUrl(target, probeKey), {
    method: 'GET',
    credentials: 'omit',
    mode: 'cors',
  })
  if (!read.ok) {
    const error = new Error(`Probe not publicly readable: ${read.status}`)
    error.status = read.status
    error.kind = 'verify-failed'
    throw error
  }

  const warnings = []
  try {
    const aws = newAwsClient(target)
    const del = await aws.fetch(`${putBaseUrl(target)}/${objectKey(target, probeKey)}`, { method: 'DELETE' })
    if (!del.ok) {
      warnings.push(`Probe could not be removed (bucket policy may not allow DELETE): ${del.status}`)
    }
  } catch (e) {
    warnings.push('Probe cleanup failed: ' + e.message)
  }
  return { ok: true, warnings }
}

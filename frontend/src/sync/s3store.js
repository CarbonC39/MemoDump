// R6.3 browser S3 note-sync adapter. Mirrors internal/syncprovider/s3 for the
// Pure frontend/PWA build: a private-bucket RemoteStore over S3-compatible
// object storage using aws4fetch ONLY for SigV4 (aws.sign + an injected
// fetchImpl, so tests drive the network deterministically). Note objects are
// private — they are signed reads/writes against the configured endpoint and
// are never exposed through an anonymous public URL.
//
// The adapter implements the note wire layout under the configured prefix
// (repo.json, notes/<sync-id>.json), full paginated ListObjectsV2, conditional
// create (If-None-Match: *) and replace (If-Match), the same capability probe
// as Go (rejecting a provider/CORS policy that cannot read, expose ETag, list,
// or enforce both preconditions), and normalized StoreError kinds without ever
// reading or logging arbitrary response bodies.

import { AwsClient } from 'aws4fetch'
import { sha256Hex } from './hash.js'
import { NOTE_MAX_ENTITY_BYTES } from './note.js'

// ---- normalized errors ---------------------------------------------------

// StoreError is the browser counterpart of cloudsync.StoreError. Kinds match
// the retry/redaction classification in retry.js: not-found, precondition-
// failed, auth, permission, rate-limit, quota, invalid-response,
// unsupported-capability, retryable-transport, incomplete-list. Messages are
// static labels — never credentials, URLs, or response bodies.
export class StoreError extends Error {
  constructor(kind, message, { retryAfterSeconds } = {}) {
    super(message || kind)
    this.kind = kind
    this.retryAfterSeconds = retryAfterSeconds
  }
}

export function trimETag(etag) {
  if (typeof etag !== 'string') return ''
  return etag.trim().replace(/^"+|"+$/g, '')
}

// requireStatus enforces the exact success status an operation accepts. A
// missing ETag header becomes "" (see trimETag), so an absent or empty version
// is always rejected.
function requireStatus(response, expected, { notFoundIsOk = false } = {}) {
  if (response.status === expected) return
  if (notFoundIsOk && response.status === 404) return
  const e = classifyResponse(response)
  if (e) throw e
  throw new StoreError('invalid-response', `unexpected status ${response.status}`)
}

function parseRetryAfter(value) {
  if (!value) return undefined
  const secs = Number(value)
  if (Number.isFinite(secs) && secs >= 0) return secs
  const t = Date.parse(value)
  if (Number.isFinite(t)) return Math.max(0, Math.ceil((t - Date.now()) / 1000))
  return undefined
}

// classifyResponse maps an HTTP status onto a normalized StoreError (null for
// success). No response body is read.
export function classifyResponse(response) {
  switch (response.status) {
    case 200:
    case 201:
    case 204:
      return null
    case 404:
      return new StoreError('not-found', 's3 not-found')
    case 412:
      return new StoreError('precondition-failed', 's3 precondition-failed')
    case 401:
      return new StoreError('auth', 's3 auth')
    case 403:
      return new StoreError('permission', 's3 access-denied')
    case 429:
      return new StoreError('rate-limit', 's3 rate-limit', {
        retryAfterSeconds: parseRetryAfter(response.headers.get('Retry-After')),
      })
    case 507:
      return new StoreError('quota', 's3 quota')
    default:
      if (response.status >= 500) return new StoreError('retryable-transport', 's3 server error')
      return new StoreError('invalid-response', `s3 ${response.status}`)
  }
}

// ---- config --------------------------------------------------------------

function isLoopbackHost(host) {
  if (!host) return false
  const h = host.toLowerCase()
  if (h === 'localhost' || h === '::1' || h === '[::1]') return true
  return /^127(\.\d{1,3}){3}$/.test(h)
}

// normalizeConfig validates the endpoint exactly like Go's s3.New: http/https
// only, no userinfo/query/fragment/path, and plain HTTP only for loopback
// development endpoints. bucket and credentials are required.
export function normalizeConfig(cfg) {
  const endpoint = String(cfg.endpoint || '').trim().replace(/\/+$/, '')
  if (!endpoint) throw new Error('missing S3 endpoint')
  let u
  try {
    u = new URL(endpoint)
  } catch (_) {
    throw new Error('invalid S3 endpoint')
  }
  if (u.protocol !== 'http:' && u.protocol !== 'https:') {
    throw new Error('unsupported S3 endpoint scheme')
  }
  if (u.username || u.password || u.search || u.hash) {
    throw new Error('S3 endpoint must not carry userinfo, query, or fragment')
  }
  if (u.pathname !== '/' && u.pathname !== '') {
    throw new Error('S3 endpoint must not carry a path')
  }
  if (u.protocol === 'http:' && !isLoopbackHost(u.hostname)) {
    throw new Error('plain HTTP S3 endpoint is only allowed for localhost/loopback development')
  }
  const bucket = String(cfg.bucket || '').trim()
  if (!bucket) throw new Error('missing S3 bucket')
  const accessKey = String(cfg.accessKey || '')
  const secretKey = String(cfg.secretKey || '')
  if (!accessKey || !secretKey) throw new Error('missing S3 credentials')
  return {
    endpoint,
    region: String(cfg.region || 'us-east-1'),
    bucket,
    prefix: String(cfg.prefix || '').trim().replace(/^\/+|\/+$/g, ''),
    accessKey,
    secretKey,
    forcePathStyle: cfg.forcePathStyle !== false,
  }
}

// providerProfile is the secret-free provider fingerprint. It MUST hash the
// same raw `endpoint|bucket|prefix` string as Go's s3.Client.Profile so a PWA
// and a Wails replica configure the same snapshot identity.
export async function providerProfile(cfg) {
  return sha256Hex(`${String(cfg.endpoint)}|${String(cfg.bucket)}|${String(cfg.prefix || '')}`)
}

function putBaseUrl(cfg) {
  if (cfg.forcePathStyle !== false) return `${cfg.endpoint}/${cfg.bucket}`
  const url = new URL(cfg.endpoint)
  url.hostname = `${cfg.bucket}.${url.hostname}`
  return url.toString().replace(/\/+$/, '')
}

function objectKeyPrefix(cfg) {
  return cfg.prefix ? `${cfg.prefix}/` : ''
}

function operationUrl(cfg, key) {
  const encoded = `${objectKeyPrefix(cfg)}${key}`.split('/').map((s) => encodeURIComponent(s)).join('/')
  return `${putBaseUrl(cfg)}/${encoded}`
}

function listUrl(cfg, prefix, token) {
  const url = new URL(putBaseUrl(cfg))
  url.searchParams.set('list-type', '2')
  url.searchParams.set('prefix', `${objectKeyPrefix(cfg)}${prefix}`)
  if (token) url.searchParams.set('continuation-token', token)
  return url.toString()
}

// ---- ListObjectsV2 XML parsing -------------------------------------------

function unescapeXml(s) {
  return s
    .replace(/&lt;/g, '<')
    .replace(/&gt;/g, '>')
    .replace(/&quot;/g, '"')
    .replace(/&apos;/g, "'")
    .replace(/&amp;/g, '&')
}

function extractText(xml, tag) {
  const m = new RegExp(`<${tag}>(.*?)</${tag}>`, 's').exec(xml)
  return m ? unescapeXml(m[1]) : null
}

// parseListBucketResult is a strict parser for the flat ListBucketResult shape
// S3 returns: ordered <Contents> blocks each carrying a non-empty <Key> and
// <ETag>, plus a required <IsTruncated> and <NextContinuationToken> when
// truncated. Garbage, an unterminated root, a missing/absent IsTruncated,
// empty keys or ETags, a truncated listing without a continuation token, and
// trailing content are all invalid-response — a full listing is never silently
// incomplete.
export function parseListBucketResult(xml) {
  if (typeof xml !== 'string') {
    throw new StoreError('invalid-response', 'list response is not text')
  }
  const open = xml.indexOf('<ListBucketResult')
  if (open < 0) throw new StoreError('invalid-response', 'list response is not a ListBucketResult')
  const openEnd = xml.indexOf('>', open)
  if (openEnd < 0) throw new StoreError('invalid-response', 'malformed ListBucketResult root')
  const close = xml.lastIndexOf('</ListBucketResult>')
  if (close < openEnd) throw new StoreError('invalid-response', 'unterminated ListBucketResult')
  if (xml.slice(close + '</ListBucketResult>'.length).trim() !== '') {
    throw new StoreError('invalid-response', 'trailing content after list response')
  }
  const inner = xml.slice(openEnd + 1, close)

  const isTruncatedRaw = extractText(inner, 'IsTruncated')
  if (isTruncatedRaw === null || (isTruncatedRaw !== 'true' && isTruncatedRaw !== 'false')) {
    throw new StoreError('invalid-response', 'listing missing a valid IsTruncated')
  }
  const isTruncated = isTruncatedRaw === 'true'
  const nextToken = extractText(inner, 'NextContinuationToken') || ''
  if (isTruncated && nextToken === '') {
    throw new StoreError('invalid-response', 'truncated listing without a continuation token')
  }

  const contents = []
  let pos = 0
  while (true) {
    const cStart = inner.indexOf('<Contents>', pos)
    if (cStart < 0) break
    const cEnd = inner.indexOf('</Contents>', cStart)
    if (cEnd < 0) throw new StoreError('invalid-response', 'unterminated Contents block')
    const block = inner.slice(cStart + '<Contents>'.length, cEnd)
    const key = extractText(block, 'Key')
    const etag = extractText(block, 'ETag')
    if (key === null || key === '') throw new StoreError('invalid-response', 'Contents block missing a Key')
    if (etag === null || trimETag(etag) === '') throw new StoreError('invalid-response', 'Contents block missing an ETag')
    contents.push({ key, etag: trimETag(etag) })
    pos = cEnd + '</Contents>'.length
  }

  return { contents, isTruncated, nextToken }
}

function randomHex(bytes) {
  const b = new Uint8Array(bytes)
  crypto.getRandomValues(b)
  return Array.from(b, (x) => x.toString(16).padStart(2, '0')).join('')
}

function bytesEqual(a, b) {
  if (a.length !== b.length) return false
  for (let i = 0; i < a.length; i++) if (a[i] !== b[i]) return false
  return true
}

// readBody resolves a response body (arrayBuffer/text). fetch can deliver the
// headers first and then fail the body stream mid-transfer; that failure must
// be a retryable StoreError, never a raw TypeError. An abort still propagates.
async function readBody(fn) {
  try {
    return await fn()
  } catch (e) {
    if (e && e.name === 'AbortError') throw e
    throw new StoreError('retryable-transport', 's3 response body read failed')
  }
}

// ---- the store -----------------------------------------------------------

export class S3Store {
  // cfg is the raw configuration (endpoint/region/bucket/prefix/accessKey/
  // secretKey/forcePathStyle). fetchImpl is injected for tests; it defaults to
  // the global fetch and receives the signed Request.
  constructor(cfg, { fetchImpl } = {}) {
    this.raw = { ...cfg }
    this.cfg = normalizeConfig(cfg)
    this.fetchImpl = fetchImpl || ((input, init) => globalThis.fetch(input, init))
    this.aws = new AwsClient({
      accessKeyId: this.cfg.accessKey,
      secretAccessKey: this.cfg.secretKey,
      region: this.cfg.region,
      service: 's3',
    })
  }

  // profile is the secret-free provider fingerprint (see providerProfile).
  profile() {
    return providerProfile(this.raw)
  }

  // request signs and performs one request. A canceled signal propagates the
  // abort (never a StoreError); any other network/CORS failure maps to
  // typeErrorKind (retryable transport for data operations, permission for the
  // capability probe, which treats an unreachable/CORS-blocked provider as
  // unusable).
  async request(method, url, { headers, body, signal, typeErrorKind } = {}) {
    let signed
    try {
      signed = await this.aws.sign(url, { method, headers, body, signal })
    } catch (e) {
      throw new StoreError('invalid-response', 'failed to sign request')
    }
    let response
    try {
      response = await this.fetchImpl(signed, { signal })
    } catch (e) {
      if (e && e.name === 'AbortError') throw e
      throw new StoreError(typeErrorKind || 'retryable-transport', 's3 network request failed')
    }
    return response
  }

  // read returns the object bytes and its opaque ETag version. Only a 200 is a
  // full object (a 206 partial or any other status is not accepted as content);
  // the ETag must survive trimming; and the size is checked against
  // Content-Length before the body is read when the header is present.
  async read(key, { signal } = {}) {
    const response = await this.request('GET', operationUrl(this.cfg, key), { signal })
    requireStatus(response, 200)
    const version = trimETag(response.headers.get('ETag'))
    if (!version) throw new StoreError('invalid-response', 'object read without an ETag')
    const lengthHeader = response.headers.get('Content-Length')
    if (lengthHeader !== null && Number(lengthHeader) > NOTE_MAX_ENTITY_BYTES) {
      throw new StoreError('invalid-response', 'object exceeds the size limit')
    }
    const data = new Uint8Array(await readBody(() => response.arrayBuffer()))
    if (data.byteLength > NOTE_MAX_ENTITY_BYTES) {
      throw new StoreError('invalid-response', 'object exceeds the size limit')
    }
    return { data, version }
  }

  // list returns the complete set of objects under <configPrefix>/<prefix>,
  // walking every ListObjectsV2 page. Every raw object key must sit inside the
  // FULL config prefix, keys are returned relative to it and must stay inside
  // the requested prefix, and a repeated continuation token or a repeated key
  // stops with incomplete-list/invalid-response so a partial or duplicate view
  // never reaches the coordinator.
  async list(prefix, { signal } = {}) {
    const out = []
    const seenTokens = new Set()
    const seenKeys = new Set()
    const cfgPrefix = objectKeyPrefix(this.cfg)
    let token = ''
    let isTruncated = true
    while (isTruncated) {
      if (signal && signal.aborted) throw new DOMException('The operation was aborted.', 'AbortError')
      if (token) {
        if (seenTokens.has(token)) {
          throw new StoreError('incomplete-list', 'listing repeated a continuation token')
        }
        seenTokens.add(token)
      }
      const response = await this.request('GET', listUrl(this.cfg, prefix, token), { signal })
      requireStatus(response, 200)
      const page = parseListBucketResult(await readBody(() => response.text()))
      for (const c of page.contents) {
        if (cfgPrefix && !c.key.startsWith(cfgPrefix)) {
          throw new StoreError('invalid-response', 'listing returned a key outside the config prefix')
        }
        const rel = cfgPrefix ? c.key.slice(cfgPrefix.length) : c.key
        if (!rel.startsWith(prefix)) {
          throw new StoreError('invalid-response', 'listing returned a key outside the requested prefix')
        }
        if (seenKeys.has(rel)) {
          throw new StoreError('invalid-response', 'listing returned a duplicate key')
        }
        seenKeys.add(rel)
        out.push({ key: rel, version: c.etag })
      }
      isTruncated = page.isTruncated
      token = page.nextToken
    }
    return out
  }

  // create stores bytes only-if-absent (If-None-Match: *).
  create(key, data, opts) {
    return this.putObject(key, data, '*', opts)
  }

  // replace stores bytes only when expectedVersion is still current (If-Match).
  // An empty expectedVersion is rejected: without If-Match the write would
  // silently degrade into an unconditional overwrite.
  replace(key, data, expectedVersion, opts) {
    if (typeof expectedVersion !== 'string' || trimETag(expectedVersion) === '') {
      throw new StoreError('invalid-response', 'replace requires a non-empty expectedVersion')
    }
    return this.putObject(key, data, expectedVersion, opts)
  }

  // putObject performs a conditional PUT. precondition "*" = create-if-absent;
  // a non-empty value = replace-if-version; "" = unconditional (probe only).
  async putObject(key, data, precondition, { signal } = {}) {
    const headers = {}
    if (precondition === '*') {
      headers['If-None-Match'] = '*'
    } else if (precondition) {
      headers['If-Match'] = `"${trimETag(precondition)}"`
    }
    const response = await this.request('PUT', operationUrl(this.cfg, key), { headers, body: data, signal })
    requireStatus(response, 200)
    const version = trimETag(response.headers.get('ETag'))
    if (!version) throw new StoreError('invalid-response', 'object write without an ETag')
    return version
  }

  // deleteObject removes one object (probe cleanup only; 404 is success).
  async deleteObject(key, { signal } = {}) {
    const response = await this.request('DELETE', operationUrl(this.cfg, key), { signal })
    requireStatus(response, 204, { notFoundIsOk: true })
  }

  // test runs the capability probe: PUT a random isolated probe, verify that
  // create-if-absent and replace-if-version are both enforced (else
  // unsupported-capability), that reads work and expose the ETag (CORS
  // Access-Control-Expose-Headers), and that a full listing parses. The probe
  // is best-effort deleted afterwards. A network/CORS failure during the probe
  // is classified as permission: an unreachable or CORS-blocked provider is
  // unusable and must be configured/fixed, never silently retried forever.
  async test({ signal } = {}) {
    const probeKey = `_memodump_probe_${Date.now()}_${randomHex(8)}`
    const probe = new TextEncoder().encode('memodump-probe')
    try {
      try {
        return await this.runProbe(probeKey, probe, signal)
      } catch (e) {
        if (e instanceof StoreError && e.kind === 'retryable-transport') {
          throw new StoreError('permission', 'provider unreachable from this origin (check endpoint and CORS)')
        }
        throw e
      }
    } finally {
      try {
        await this.deleteObject(probeKey)
      } catch (_) {}
    }
  }

  async runProbe(probeKey, probe, signal) {
    // 1. Conditional create on an ABSENT key must succeed (If-None-Match: *).
    // A service that rejects every conditional create is unsupported.
    let etag
    try {
      etag = await this.create(probeKey, probe, { signal })
    } catch (e) {
      if (e instanceof StoreError && e.kind === 'precondition-failed') {
        throw new StoreError('unsupported-capability', 'service rejects create-if-absent')
      }
      throw e
    }
    if (!etag) throw new StoreError('unsupported-capability', 'service does not expose ETag')
    // 2. Re-creating the existing key must fail.
    if (!(await this.expectPreconditionFail(() => this.create(probeKey, probe, { signal })))) {
      throw new StoreError('unsupported-capability', 'service ignores If-None-Match')
    }
    // 3. Replace with the CURRENT version must succeed. Only the expected 412
    // maps to unsupported — cancellation, auth, and network errors propagate.
    let replaced
    try {
      replaced = await this.replace(probeKey, probe, etag, { signal })
    } catch (e) {
      if (e instanceof StoreError && e.kind === 'precondition-failed') {
        throw new StoreError('unsupported-capability', 'service rejects a matching If-Match')
      }
      throw e
    }
    if (!replaced) throw new StoreError('unsupported-capability', 'service does not expose ETag')
    // 4. A stale version must fail.
    if (!(await this.expectPreconditionFail(() => this.replace(probeKey, probe, `${replaced}-stale`, { signal })))) {
      throw new StoreError('unsupported-capability', 'service ignores If-Match')
    }
    // 5. A read must return the exact probe bytes AND the same version that the
    // replace returned. A stale/wrong ETag on GET would otherwise pass here and
    // then make every subsequent conditional write fail.
    let got
    try {
      got = await this.read(probeKey, { signal })
    } catch (e) {
      if (e instanceof StoreError && e.kind === 'invalid-response') {
        throw new StoreError('unsupported-capability', 'service does not expose ETag')
      }
      throw e
    }
    if (!got.version || !bytesEqual(got.data, probe)) {
      throw new StoreError('unsupported-capability', 'read did not return the written object')
    }
    if (got.version !== replaced) {
      throw new StoreError('unsupported-capability', 'read returned a different version than the write')
    }
    // 6. A full listing must contain the probe at its current version.
    try {
      const listed = await this.list('', { signal })
      const entry = listed.find((e) => e.key === probeKey)
      if (!entry || entry.version !== replaced) {
        throw new StoreError('unsupported-capability', 'listing did not return the probe object')
      }
    } catch (e) {
      if (e instanceof StoreError && e.kind === 'invalid-response') {
        throw new StoreError('unsupported-capability', 'cannot list objects')
      }
      throw e
    }
    return { ok: true, capabilities: { conditionalWrites: true, pagedListing: true } }
  }

  async expectPreconditionFail(fn) {
    try {
      await fn()
    } catch (e) {
      if (e instanceof StoreError && e.kind === 'precondition-failed') return true
      throw e // auth/permission/network errors propagate as-is, never misread as unsupported
    }
    return false
  }
}

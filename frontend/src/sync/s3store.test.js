import { describe, it, expect } from 'vitest'
import { S3Store, StoreError, classifyResponse, parseListBucketResult, trimETag, normalizeConfig, providerProfile } from './s3store'
import { sha256Hex } from './hash.js'

const encoder = new TextEncoder()

function makeStore(handler, overrides = {}) {
  const cfg = {
    endpoint: 'http://localhost:9000',
    region: 'us-east-1',
    bucket: 'notes-bucket',
    prefix: 'memodump',
    accessKey: 'AKIDEXAMPLE',
    secretKey: 'very-secret-key',
    forcePathStyle: true,
  }
  return new S3Store({ ...cfg, ...overrides }, { fetchImpl: async (input) => handler(input) })
}

function res(body = '', { status = 200, headers = {} } = {}) {
  return new Response(body, { status, headers })
}

// Extracts the object key ("memodump/notes/x.json") from a request URL.
function objectKeyOf(input) {
  const url = new URL(input.url)
  const parts = url.pathname.split('/')
  return parts.slice(2).join('/')
}

function listXml(contents, { isTruncated = false, nextToken } = {}) {
  const inner = contents
    .map(([key, etag]) => `<Contents><Key>${key}</Key><ETag>"${etag}"</ETag></Contents>`)
    .join('')
  const truncated = `<IsTruncated>${isTruncated}</IsTruncated>`
  const token = nextToken ? `<NextContinuationToken>${nextToken}</NextContinuationToken>` : ''
  return `<?xml version="1.0" encoding="UTF-8"?><ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">${inner}${truncated}${token}</ListBucketResult>`
}

// A stateful, compliant S3 provider for the probe tests.
function compliantProvider() {
  const objects = new Map() // object key -> { body, etag }
  let counter = 0
  const nextEtag = () => `etag-${++counter}`
  return (input) => {
    const url = new URL(input.url)
    const key = objectKeyOf(input)
    if (input.method === 'GET' && url.searchParams.has('list-type')) {
      const entries = [...objects.entries()].map(([k, o]) => [k, o.etag])
      return Promise.resolve(res(listXml(entries)))
    }
    if (input.method === 'GET') {
      const o = objects.get(key)
      if (!o) return Promise.resolve(res('', { status: 404 }))
      return Promise.resolve(res(new TextDecoder().decode(o.body), { headers: { ETag: `"${o.etag}"`, 'Content-Length': String(o.body.length) } }))
    }
    if (input.method === 'DELETE') {
      const removed = objects.delete(key)
      return Promise.resolve(res('', { status: removed ? 204 : 404 }))
    }
    if (input.method === 'PUT') {
      const ifNoneMatch = input.headers.get('If-None-Match')
      const ifMatch = input.headers.get('If-Match')
      if (ifNoneMatch === '*' && objects.has(key)) return Promise.resolve(res('', { status: 412 }))
      if (ifMatch) {
        const current = objects.get(key)
        if (!current || `"${current.etag}"` !== ifMatch) return Promise.resolve(res('', { status: 412 }))
      }
      return input.arrayBuffer().then((body) => {
        const etag = nextEtag()
        objects.set(key, { body: new Uint8Array(body), etag })
        return res('', { status: 200, headers: { ETag: `"${etag}"` } })
      })
    }
    return Promise.resolve(res('', { status: 400 }))
  }
}

describe('config validation', () => {
  it('normalizes a valid config', () => {
    const cfg = normalizeConfig({
      endpoint: 'http://localhost:9000/',
      bucket: 'b',
      prefix: '/notes/',
      accessKey: 'a',
      secretKey: 's',
    })
    expect(cfg.endpoint).toBe('http://localhost:9000')
    expect(cfg.prefix).toBe('notes')
    expect(cfg.region).toBe('us-east-1')
    expect(cfg.forcePathStyle).toBe(true)
  })

  it('rejects a non-loopback plain HTTP endpoint', () => {
    expect(() => normalizeConfig({ endpoint: 'http://s3.example.com', bucket: 'b', accessKey: 'a', secretKey: 's' }))
      .toThrow('localhost')
  })

  it('rejects endpoint userinfo, query, fragment, and path', () => {
    const base = { bucket: 'b', accessKey: 'a', secretKey: 's' }
    expect(() => normalizeConfig({ endpoint: 'https://user:pw@s3.example.com', ...base })).toThrow('userinfo')
    expect(() => normalizeConfig({ endpoint: 'https://s3.example.com/?x=1', ...base })).toThrow('query')
    expect(() => normalizeConfig({ endpoint: 'https://s3.example.com/base', ...base })).toThrow('path')
  })

  it('rejects a missing bucket or credentials', () => {
    expect(() => normalizeConfig({ endpoint: 'https://s3.example.com', accessKey: 'a', secretKey: 's' })).toThrow('bucket')
    expect(() => normalizeConfig({ endpoint: 'https://s3.example.com', bucket: 'b', secretKey: 's' })).toThrow('credentials')
  })
})

describe('provider profile', () => {
  it('hashes exactly endpoint|bucket|prefix (Go-identical string)', async () => {
    const p = await providerProfile({ endpoint: 'https://s3.example.com', bucket: 'b', prefix: 'p' })
    expect(p).toBe(await sha256Hex('https://s3.example.com|b|p'))
    expect(p).toMatch(/^[0-9a-f]{64}$/)
  })

  it('never hashes the credentials', async () => {
    const store = makeStore(() => res(''))
    const p = await store.profile()
    expect(p).toBe(await sha256Hex('http://localhost:9000|notes-bucket|memodump'))
  })
})

describe('URL and prefix encoding', () => {
  it('puts objects at <endpoint>/<bucket>/<prefix>/<key> with path-style', async () => {
    let seen
    const store = makeStore((input) => {
      seen = input
      return res('', { status: 200, headers: { ETag: '"e1"' } })
    })
    await store.create('notes/abc.json', encoder.encode('x'))
    expect(new URL(seen.url).pathname).toBe('/notes-bucket/memodump/notes/abc.json')
  })

  it('uses virtual-host style when forcePathStyle is false', async () => {
    let seen
    const store = makeStore((input) => {
      seen = input
      return res('', { status: 200, headers: { ETag: '"e1"' } })
    }, { forcePathStyle: false })
    await store.create('notes/abc.json', encoder.encode('x'))
    expect(new URL(seen.url).hostname).toBe('notes-bucket.localhost')
  })

  it('percent-encodes special characters in the prefix path', async () => {
    let seen
    const store = makeStore((input) => {
      seen = input
      return res('', { status: 200, headers: { ETag: '"e1"' } })
    }, { prefix: 'my notes' })
    await store.create('notes/abc.json', encoder.encode('x'))
    expect(new URL(seen.url).pathname).toBe('/notes-bucket/my%20notes/notes/abc.json')
  })

  it('lists under the config prefix with list-type=2 and the given prefix', async () => {
    const urls = []
    const store = makeStore((input) => {
      urls.push(new URL(input.url))
      return res(listXml([], { isTruncated: false }))
    })
    await store.list('notes/')
    expect(urls).toHaveLength(1)
    expect(urls[0].searchParams.get('list-type')).toBe('2')
    expect(urls[0].searchParams.get('prefix')).toBe('memodump/notes/')
  })
})

describe('read', () => {
  it('returns the object bytes and the opaque ETag version', async () => {
    const store = makeStore(() => res('hello', { headers: { ETag: '"v9"', 'Content-Length': '5' } }))
    const { data, version } = await store.read('notes/abc.json')
    expect(new TextDecoder().decode(data)).toBe('hello')
    expect(version).toBe('v9')
  })

  it('rejects a missing ETag (CORS not exposing it) as invalid-response', async () => {
    const store = makeStore(() => res('hello', { 'Content-Length': '5' }))
    await expect(store.read('repo.json')).rejects.toMatchObject({ kind: 'invalid-response' })
  })

  it('maps 404 to not-found and 412 to precondition-failed', async () => {
    const notFound = makeStore(() => res('', { status: 404 }))
    await expect(notFound.read('repo.json')).rejects.toMatchObject({ kind: 'not-found' })
    const stale = makeStore(() => res('', { status: 412 }))
    await expect(stale.read('repo.json')).rejects.toMatchObject({ kind: 'precondition-failed' })
  })

  it('rejects an over-limit object without reading the body', async () => {
    let read = false
    const store = makeStore((input) => {
      return new Promise((resolve) => {
        const resp = res('', { headers: { ETag: '"e"', 'Content-Length': String((1 << 20) + 1) } })
        read = true
        resolve(resp)
      })
    })
    await expect(store.read('repo.json')).rejects.toMatchObject({ kind: 'invalid-response' })
    expect(read).toBe(true)
  })

  it('rejects an over-limit body when Content-Length is absent', async () => {
    const store = makeStore(() => res('x'.repeat((1 << 20) + 1), { headers: { ETag: '"e"' } }))
    await expect(store.read('repo.json')).rejects.toMatchObject({ kind: 'invalid-response' })
  })

  it('does not accept a 206 partial response as a full object', async () => {
    const store = makeStore(() => res('partial', { status: 206, headers: { ETag: '"e"', 'Content-Length': '7' } }))
    await expect(store.read('repo.json')).rejects.toMatchObject({ kind: 'invalid-response' })
  })

  it('rejects an ETag header that trims to empty', async () => {
    const store = makeStore(() => res('x', { headers: { ETag: '""' } }))
    await expect(store.read('repo.json')).rejects.toMatchObject({ kind: 'invalid-response' })
  })

  it('maps a body-stream failure on read to retryable-transport', async () => {
    const store = makeStore(() => ({
      status: 200,
      headers: new Headers({ ETag: '"e"', 'Content-Length': '3' }),
      arrayBuffer: () => Promise.reject(new TypeError('stream interrupted')),
    }))
    await expect(store.read('repo.json')).rejects.toMatchObject({ kind: 'retryable-transport' })
  })

  it('maps a body-stream failure on list to retryable-transport', async () => {
    const store = makeStore(() => ({
      status: 200,
      headers: new Headers(),
      text: () => Promise.reject(new TypeError('stream interrupted')),
    }))
    await expect(store.list('notes/')).rejects.toMatchObject({ kind: 'retryable-transport' })
  })
})

describe('pagination', () => {
  it('walks every ListObjectsV2 page and strips the config prefix', async () => {
    const urls = []
    const store = makeStore((input) => {
      const url = new URL(input.url)
      urls.push(url)
      if (!url.searchParams.get('continuation-token')) {
        return res(listXml(
          [['memodump/notes/a.json', 'e1']],
          { isTruncated: true, nextToken: 'tok/1' },
        ))
      }
      return res(listXml([['memodump/notes/b.json', 'e2']], { isTruncated: false }))
    })
    const out = await store.list('notes/')
    expect(out).toEqual([
      { key: 'notes/a.json', version: 'e1' },
      { key: 'notes/b.json', version: 'e2' },
    ])
    expect(urls).toHaveLength(2)
    expect(urls[1].searchParams.get('continuation-token')).toBe('tok/1')
  })

  it('returns an empty result for an empty listing', async () => {
    const store = makeStore(() => res(listXml([], { isTruncated: false })))
    expect(await store.list('notes/')).toEqual([])
  })

  it('XML-escapes special characters in keys', async () => {
    const xml = '<ListBucketResult><Contents><Key>memodump/notes/a&amp;b.json</Key><ETag>"e"</ETag></Contents><IsTruncated>false</IsTruncated></ListBucketResult>'
    const { contents } = parseListBucketResult(xml)
    expect(contents[0].key).toBe('memodump/notes/a&b.json')
  })
})

describe('malformed list XML', () => {
  const cases = [
    ['garbage', '<html>error</html>'],
    ['unterminated root', '<ListBucketResult><IsTruncated>false</IsTruncated>'],
    ['unterminated Contents', '<ListBucketResult><Contents><Key>x</Key><IsTruncated>false</IsTruncated></ListBucketResult>'],
    ['wrong root', '<Foo><IsTruncated>false</IsTruncated></Foo>'],
    ['trailing content', listXml([], { isTruncated: false }) + 'junk'],
  ]
  for (const [name, xml] of cases) {
    it(`rejects ${name}`, () => {
      expect(() => parseListBucketResult(xml)).toThrow()
    })
  }

  it('rejects a truncated listing without a continuation token', async () => {
    const store = makeStore(() => res(listXml([['memodump/notes/a.json', 'e']], { isTruncated: true })))
    await expect(store.list('notes/')).rejects.toMatchObject({ kind: 'invalid-response' })
  })

  it('rejects a listing missing IsTruncated', () => {
    const xml = '<ListBucketResult><Contents><Key>k</Key><ETag>"e"</ETag></Contents></ListBucketResult>'
    expect(() => parseListBucketResult(xml)).toThrow()
  })

  it('rejects a Contents block with an empty Key or an empty ETag', () => {
    const emptyKey = '<ListBucketResult><Contents><Key></Key><ETag>"e"</ETag></Contents><IsTruncated>false</IsTruncated></ListBucketResult>'
    const emptyEtag = '<ListBucketResult><Contents><Key>k</Key><ETag>""</ETag></Contents><IsTruncated>false</IsTruncated></ListBucketResult>'
    expect(() => parseListBucketResult(emptyKey)).toThrow()
    expect(() => parseListBucketResult(emptyEtag)).toThrow()
  })

  it('rejects a key outside the requested prefix', async () => {
    const store = makeStore(() => res(listXml([['memodump/images/x.png', 'e']], { isTruncated: false })))
    await expect(store.list('notes/')).rejects.toMatchObject({ kind: 'invalid-response' })
  })

  it('stops with incomplete-list when a continuation token repeats (never an infinite loop)', async () => {
    let page = 0
    const store = makeStore(() => {
      page++
      const key = page === 1 ? 'memodump/notes/a.json' : 'memodump/notes/b.json'
      return res(listXml([[key, 'e']], { isTruncated: true, nextToken: 'loop' }))
    })
    await expect(store.list('notes/')).rejects.toMatchObject({ kind: 'incomplete-list' })
    expect(page).toBe(2)
  })

  it('rejects a raw key that is outside the config prefix', async () => {
    const store = makeStore(() => res(listXml([['notes/a.json', 'e']], { isTruncated: false })))
    await expect(store.list('notes/')).rejects.toMatchObject({ kind: 'invalid-response' })
  })

  it('rejects a key repeated across pages (never two versions of one object)', async () => {
    let page = 0
    const store = makeStore(() => {
      page++
      if (page === 1) return res(listXml([['memodump/notes/a.json', 'e1']], { isTruncated: true, nextToken: 't' }))
      return res(listXml([['memodump/notes/a.json', 'e2']], { isTruncated: false }))
    })
    await expect(store.list('notes/')).rejects.toMatchObject({ kind: 'invalid-response' })
  })
})

describe('conditional writes', () => {
  it('create sends If-None-Match: * and returns the ETag', async () => {
    let seen
    const store = makeStore((input) => {
      seen = input
      return res('', { status: 200, headers: { ETag: '"e1"' } })
    })
    const version = await store.create('notes/abc.json', encoder.encode('x'))
    expect(seen.method).toBe('PUT')
    expect(seen.headers.get('If-None-Match')).toBe('*')
    expect(version).toBe('e1')
  })

  it('replace sends a quoted If-Match version', async () => {
    let seen
    const store = makeStore((input) => {
      seen = input
      return res('', { status: 200, headers: { ETag: '"e2"' } })
    })
    await store.replace('notes/abc.json', encoder.encode('x'), 'e1')
    expect(seen.headers.get('If-Match')).toBe('"e1"')
  })

  it('maps 412 to precondition-failed (a stale CAS never overwrites)', async () => {
    const store = makeStore(() => res('', { status: 412 }))
    await expect(store.replace('notes/abc.json', encoder.encode('x'), 'stale')).rejects
      .toMatchObject({ kind: 'precondition-failed' })
  })

  it('rejects a successful write without an ETag', async () => {
    const store = makeStore(() => res('', { status: 200 }))
    await expect(store.create('notes/abc.json', encoder.encode('x'))).rejects
      .toMatchObject({ kind: 'invalid-response' })
  })

  it('rejects replace with an empty expectedVersion (never an unconditional overwrite)', async () => {
    const store = makeStore(() => res('', { status: 200, headers: { ETag: '"e"' } }))
    await expect(async () => store.replace('notes/abc.json', encoder.encode('x'), '   ')).rejects
      .toMatchObject({ kind: 'invalid-response' })
    await expect(async () => store.replace('notes/abc.json', encoder.encode('x'), '""')).rejects
      .toMatchObject({ kind: 'invalid-response' })
  })
})

describe('error classification', () => {
  it('honors Retry-After on 429', () => {
    const e = classifyResponse(res('', { status: 429, headers: { 'Retry-After': '120' } }))
    expect(e).toMatchObject({ kind: 'rate-limit', retryAfterSeconds: 120 })
  })

  it('maps 401/403/507/5xx and unknown 4xx', () => {
    expect(classifyResponse(res('', { status: 401 }))).toMatchObject({ kind: 'auth' })
    expect(classifyResponse(res('', { status: 403 }))).toMatchObject({ kind: 'permission' })
    expect(classifyResponse(res('', { status: 507 }))).toMatchObject({ kind: 'quota' })
    expect(classifyResponse(res('', { status: 500 }))).toMatchObject({ kind: 'retryable-transport' })
    expect(classifyResponse(res('', { status: 418 }))).toMatchObject({ kind: 'invalid-response' })
  })

  it('never leaks credentials or response bodies in error messages', async () => {
    const store = makeStore(() => res('<Error><Message>secret</Message></Error>', { status: 403 }))
    try {
      await store.read('repo.json')
      expect.unreachable()
    } catch (e) {
      expect(e).toBeInstanceOf(StoreError)
      expect(e.message).not.toContain('very-secret-key')
      expect(e.message).not.toContain('AKIDEXAMPLE')
    }
  })

  it('does not place the secret key in the signed Authorization header', async () => {
    let seen
    const store = makeStore((input) => {
      seen = input
      return res('', { status: 200, headers: { ETag: '"e"' } })
    })
    await store.read('repo.json')
    expect(seen.headers.get('Authorization')).not.toContain('very-secret-key')
  })
})

describe('abort', () => {
  it('propagates an aborted signal as an abort, never as a StoreError', async () => {
    const store = makeStore((input) => {
      if (input.signal && input.signal.aborted) {
        throw new DOMException('The operation was aborted.', 'AbortError')
      }
      return res('', { headers: { ETag: '"e"' } })
    })
    const ac = new AbortController()
    ac.abort()
    await expect(store.read('repo.json', { signal: ac.signal })).rejects.toMatchObject({ name: 'AbortError' })
  })

  it('classifies a mid-list abort the same way', async () => {
    const store = makeStore((input) => {
      if (input.signal && input.signal.aborted) {
        throw new DOMException('The operation was aborted.', 'AbortError')
      }
      return res(listXml([], { isTruncated: false }))
    })
    const ac = new AbortController()
    ac.abort()
    await expect(store.list('notes/', { signal: ac.signal })).rejects.toMatchObject({ name: 'AbortError' })
  })
})

describe('capability probe', () => {
  it('accepts a compliant provider', async () => {
    const store = makeStore(compliantProvider())
    const result = await store.test()
    expect(result.ok).toBe(true)
    expect(result.capabilities).toEqual({ conditionalWrites: true, pagedListing: true })
  })

  it('rejects a provider that ignores If-None-Match', async () => {
    const inner = compliantProvider()
    const handler = (input) => {
      if (input.method === 'PUT' && input.headers.get('If-None-Match') === '*') {
        return Promise.resolve(res('', { status: 200, headers: { ETag: '"e"' } }))
      }
      return inner(input)
    }
    const store = makeStore(handler)
    await expect(store.test()).rejects.toMatchObject({ kind: 'unsupported-capability' })
  })

  it('rejects a provider that ignores If-Match (a stale replace succeeds)', async () => {
    const inner = compliantProvider()
    const handler = (input) => {
      if (input.method === 'PUT' && input.headers.get('If-Match')) {
        return Promise.resolve(res('', { status: 200, headers: { ETag: '"e"' } }))
      }
      return inner(input)
    }
    const store = makeStore(handler)
    await expect(store.test()).rejects.toMatchObject({ kind: 'unsupported-capability' })
  })

  it('rejects a provider that fails a MATCHING If-Match (every replace is 412)', async () => {
    const inner = compliantProvider()
    const handler = (input) => {
      if (input.method === 'PUT' && input.headers.get('If-Match')) {
        return Promise.resolve(res('', { status: 412 }))
      }
      return inner(input)
    }
    const store = makeStore(handler)
    await expect(store.test()).rejects.toMatchObject({ kind: 'unsupported-capability' })
  })

  it('propagates an auth failure on a conditional PUT instead of misreading it', async () => {
    const handler = (input) => {
      if (input.method === 'PUT' && input.headers.get('If-None-Match') === '*') {
        return Promise.resolve(res('', { status: 403 }))
      }
      return compliantProvider()(input)
    }
    const store = makeStore(handler)
    await expect(store.test()).rejects.toMatchObject({ kind: 'permission' })
  })

  it('rejects a provider that does not expose the ETag on reads', async () => {
    const inner = compliantProvider()
    const handler = (input) => {
      if (input.method === 'GET' && !new URL(input.url).searchParams.has('list-type')) {
        return Promise.resolve(res('probe'))
      }
      return inner(input)
    }
    const store = makeStore(handler)
    await expect(store.test()).rejects.toMatchObject({ kind: 'unsupported-capability' })
  })

  it('rejects a provider whose listing is not parseable', async () => {
    const inner = compliantProvider()
    const handler = (input) => {
      if (new URL(input.url).searchParams.has('list-type')) {
        return Promise.resolve(res('<html>not a listing</html>'))
      }
      return inner(input)
    }
    const store = makeStore(handler)
    await expect(store.test()).rejects.toMatchObject({ kind: 'unsupported-capability' })
  })

  it('classifies a probe-time network failure as permission', async () => {
    const store = makeStore(() => {
      throw new TypeError('Failed to fetch')
    })
    await expect(store.test()).rejects.toMatchObject({ kind: 'permission' })
  })

  it('rejects a service that rejects every conditional create', async () => {
    const handler = (input) => {
      if (input.method === 'PUT' && input.headers.get('If-None-Match') === '*') {
        return Promise.resolve(res('', { status: 412 }))
      }
      return compliantProvider()(input)
    }
    const store = makeStore(handler)
    await expect(store.test()).rejects.toMatchObject({ kind: 'unsupported-capability' })
  })

  it('rejects a service whose read returns different content than was written', async () => {
    const inner = compliantProvider()
    const handler = (input) => {
      if (input.method === 'GET' && !new URL(input.url).searchParams.has('list-type')) {
        return Promise.resolve(res('something-else', { headers: { ETag: '"e"', 'Content-Length': '14' } }))
      }
      return inner(input)
    }
    const store = makeStore(handler)
    await expect(store.test()).rejects.toMatchObject({ kind: 'unsupported-capability' })
  })

  it('rejects a service whose read returns a stale/wrong ETag', async () => {
    const inner = compliantProvider()
    const handler = (input) => {
      if (input.method === 'GET' && !new URL(input.url).searchParams.has('list-type')) {
        // Correct content but a version that disagrees with the write/listing.
        return Promise.resolve(res('memodump-probe', { headers: { ETag: '"WRONG"', 'Content-Length': '14' } }))
      }
      return inner(input)
    }
    const store = makeStore(handler)
    await expect(store.test()).rejects.toMatchObject({ kind: 'unsupported-capability' })
  })

  it('rejects a service whose listing omits the probe object', async () => {
    const inner = compliantProvider()
    const handler = (input) => {
      if (new URL(input.url).searchParams.has('list-type')) {
        return Promise.resolve(res(listXml([], { isTruncated: false })))
      }
      return inner(input)
    }
    const store = makeStore(handler)
    await expect(store.test()).rejects.toMatchObject({ kind: 'unsupported-capability' })
  })

  it('cleans up the probe object best-effort after a successful probe', async () => {
    const deletes = []
    const inner = compliantProvider()
    const handler = (input) => {
      if (input.method === 'DELETE') deletes.push(objectKeyOf(input))
      return inner(input)
    }
    const store = makeStore(handler)
    await store.test()
    expect(deletes).toHaveLength(1)
    expect(deletes[0]).toMatch(/^memodump\/_memodump_probe_/)
  })
})

describe('trimETag', () => {
  it('strips surrounding whitespace and all quotes', () => {
    expect(trimETag(' "abc" ')).toBe('abc')
    expect(trimETag('"""xyz"""')).toBe('xyz')
  })

  it('returns "" for a missing or non-string ETag', () => {
    expect(trimETag(undefined)).toBe('')
    expect(trimETag(null)).toBe('')
  })
})

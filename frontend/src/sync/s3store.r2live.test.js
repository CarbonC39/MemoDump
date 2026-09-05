// Opt-in live test of the private-bucket note-sync S3 adapter (R6.3) against a
// real S3-compatible bucket. Only runs when R2_LIVE=1 and
// .r2-test/image-config.json exists (the folder is gitignored):
//
//   cd frontend && R2_LIVE=1 npx vitest run s3store.r2live.test.js
//
// It exercises the PRIVATE adapter (signed reads/writes, never a public URL),
// conditional create/replace, the listing path, and ETag consistency, under a
// RANDOM isolated prefix so repeated runs cannot collide. Credentials are read
// programmatically and scrubbed from any output, and the note body is a
// synthetic marker — never real note content.
import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { S3Store } from './s3store'

const CONFIG = new URL('../../../.r2-test/image-config.json', import.meta.url)

function randomHex(bytes) {
  const b = new Uint8Array(bytes)
  crypto.getRandomValues(b)
  return Array.from(b, (x) => x.toString(16).padStart(2, '0')).join('')
}

function loadConfig() {
  let raw
  try {
    raw = readFileSync(CONFIG, 'utf8')
  } catch {
    return null
  }
  const cfg = JSON.parse(raw)
  if (cfg.provider !== 's3' || !cfg.endpoint || !cfg.bucket || !cfg.accessKey || !cfg.secretKey) {
    return null
  }
  return cfg
}

function redact(cfg, text) {
  return String(text).replaceAll(cfg.accessKey, '***').replaceAll(cfg.secretKey, '***')
}

// Each test gets its own random isolated prefix under the configured one.
function storeFor(cfg) {
  const prefix = `${cfg.prefix || ''}/memodump-sync-live-${randomHex(6)}`.replace(/^\/+|\/+$/g, '')
  return new S3Store({
    endpoint: cfg.endpoint,
    region: cfg.region || 'us-east-1',
    bucket: cfg.bucket,
    prefix,
    accessKey: cfg.accessKey,
    secretKey: cfg.secretKey,
    forcePathStyle: cfg.forcePathStyle !== false,
  })
}

describe('live private note-sync adapter', () => {
  const cfg = process.env.R2_LIVE === '1' ? loadConfig() : null
  const run = cfg ? it : it.skip

  run('capability probe under a random isolated prefix', async () => {
    const store = storeFor(cfg)
    try {
      const result = await store.test()
      expect(result.ok).toBe(true)
      expect(result.capabilities).toEqual({ conditionalWrites: true, pagedListing: true })
    } catch (err) {
      throw new Error('probe failed: ' + redact(cfg, err?.message || err))
    }
  })

  run('conditional create/replace round-trip with a synthetic record', async () => {
    const store = storeFor(cfg)
    // A synthetic wire record; never real note content.
    const body = new TextEncoder().encode('{"schemaVersion":2,"syncId":"00000000-0000-4000-8000-000000000000","path":"live.md","markdown":"live-sync-probe\\n","deleted":false}')
    try {
      const v1 = await store.create('notes/live.json', body)
      expect(v1.length).toBeGreaterThan(0)

      // Read-back must match bytes AND the exact version the write returned.
      const { data, version } = await store.read('notes/live.json')
      expect(version).toBe(v1)
      expect([...data]).toEqual([...body])

      // A duplicate conditional create is rejected.
      await expect(store.create('notes/live.json', body)).rejects.toMatchObject({ kind: 'precondition-failed' })

      // A conditional replace with DIFFERENT content advances the version (real
      // S3/MinIO can return the same ETag for identical bytes, so a same-body
      // replace could not prove the version moved).
      const body2 = new TextEncoder().encode('{"schemaVersion":2,"syncId":"00000000-0000-4000-8000-000000000000","path":"live.md","markdown":"live-sync-probe-2\\n","deleted":false}')
      const v2 = await store.replace('notes/live.json', body2, v1)
      expect(v2).not.toBe(v1)

      // Read-back returns body2 at version v2.
      const { data: data2, version: v2read } = await store.read('notes/live.json')
      expect(v2read).toBe(v2)
      expect([...data2]).toEqual([...body2])

      // The now-stale v1 is rejected.
      await expect(store.replace('notes/live.json', body2, v1)).rejects.toMatchObject({ kind: 'precondition-failed' })

      // The listing contains the note at its current version.
      const listed = await store.list('notes/')
      const entry = listed.find((e) => e.key === 'notes/live.json')
      expect(entry).toBeDefined()
      expect(entry.version).toBe(v2)
    } catch (err) {
      throw new Error('round-trip failed: ' + redact(cfg, err?.message || err))
    } finally {
      // Best-effort cleanup of the isolated prefix so repeated runs stay tidy.
      try {
        const listed = await store.list('')
        for (const e of listed) await store.deleteObject(e.key)
      } catch (_) {}
    }
  })
})

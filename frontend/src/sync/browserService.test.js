// @vitest-environment happy-dom
import { IDBFactory } from 'fake-indexeddb'
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import localApi from '../localApi'
import { _close } from '../storage/localVaultDb'
import { loadIdentity, loadConnection, loadSyncIndex, writeRecovery } from '../storage/syncDb'
import {
  getSyncConfig, saveSyncConfig, testSyncConfig,
  classifyError, classifyCycleResult,
  _setStoreFactory, _setLocks, _resetService, SYNC_CONFIG_KEY,
  setSchedulerOnAttempt, startSyncScheduler, stopSyncScheduler,
} from './browserService'
import { serializeRepositoryDescriptor } from './repo'
import { noteKey } from './note'
import { StoreError, providerProfile, S3Store } from './s3store'
import { utf8Bytes } from './hash'

// The note-sync configuration the public surface enables with.
const TEST_CONFIG = {
  endpoint: 'https://s3.example.com',
  region: 'us-east-1',
  bucket: 'notes',
  prefix: 'sync',
  accessKey: 'AKIA1234567890ABCDEF',
  secretKey: 'super-secret-key',
  forcePathStyle: true,
}

const UUID_RE = /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/

// A serializing Web-Locks stand-in with PER-NAME queues, mirroring real Web
// Locks: an ifAvailable:true request is REFUSED immediately while that name is
// held (the cycle never queues), and an ifAvailable:false request waits in FIFO
// order behind the same name (lifecycle operations wait for a running cycle).
// Locks with different names are independent, so a test cannot accidentally
// serialize the initialization lock and the vault lock through one global queue.
class FakeLocks {
  constructor() {
    this.queues = new Map()
    this.held = new Set()
  }
  request(name, opts = {}, fn) {
    if (opts.ifAvailable) {
      if (this.held.has(name)) return Promise.resolve(fn(null))
      this.held.add(name)
      const run = Promise.resolve().then(async () => {
        try {
          return await fn({ name })
        } finally {
          this.held.delete(name)
        }
      })
      return run
    }
    const prev = this.queues.get(name) || Promise.resolve()
    const run = prev.then(async () => {
      this.held.add(name)
      try {
        return await fn({ name })
      } finally {
        this.held.delete(name)
      }
    })
    this.queues.set(name, run.catch(() => {}))
    return run
  }
}
const fakeLocks = new FakeLocks()

class MatchingRemote {
  constructor() {
    this.objects = new Map()
    this.versionCounter = 0
    this.repoRepositoryId = crypto.randomUUID()
    this.profileValue = providerProfile(TEST_CONFIG)
    this.failKind = null
  }
  maybeFail(opts) {
    if (opts?.signal?.aborted) throw new DOMException('The operation was aborted.', 'AbortError')
    if (this.failKind) throw new StoreError(this.failKind, `s3 ${this.failKind}`)
  }
  async profile() {
    return this.profileValue
  }
  async read(key, opts) {
    this.maybeFail(opts)
    const o = this.objects.get(key)
    if (!o) throw new StoreError('not-found', 's3 not-found')
    return { data: o.data, version: o.version }
  }
  async list(prefix, opts) {
    this.maybeFail(opts)
    return [...this.objects.entries()]
      .filter(([k]) => k.startsWith(prefix))
      .sort((a, b) => (a[0] < b[0] ? -1 : a[0] > b[0] ? 1 : 0))
      .map(([k, o]) => ({ key: k, version: o.version }))
  }
  async create(key, data) {
    if (this.objects.has(key)) throw new StoreError('precondition-failed', 'exists')
    return this.put(key, data)
  }
  async replace(key, data, expectedVersion) {
    const o = this.objects.get(key)
    if (!o || o.version !== expectedVersion) throw new StoreError('precondition-failed', 'stale')
    return this.put(key, data)
  }
  put(key, data) {
    this.versionCounter++
    const version = `v${this.versionCounter}`
    this.objects.set(key, { data, version })
    return version
  }
  async test() {
    return { ok: true, capabilities: { conditionalWrites: true, pagedListing: true } }
  }
  seedRepo() {
    this.put('repo.json', utf8Bytes(serializeRepositoryDescriptor({
      formatVersion: 1,
      repositoryId: this.repoRepositoryId,
      createdAt: 1785800000000,
      minimumClientVersion: '2.0.0',
    })))
  }
}

let remote
let fetchSpy

async function setReplica(factory) {
  await _close()
  globalThis.indexedDB = factory
}

async function putConnection(record) {
  const db = await new Promise((resolve, reject) => {
    const req = globalThis.indexedDB.open('memodump', 3)
    req.onsuccess = () => resolve(req.result)
    req.onerror = () => reject(req.error)
  })
  return new Promise((resolve, reject) => {
    const t = db.transaction('syncState', 'readwrite')
    t.objectStore('syncState').put({ key: 'connection', ...record })
    t.oncomplete = () => { db.close(); resolve() }
    t.onerror = () => reject(t.error)
  })
}

async function putIdentity(record) {
  const db = await new Promise((resolve, reject) => {
    const req = globalThis.indexedDB.open('memodump', 3)
    req.onsuccess = () => resolve(req.result)
    req.onerror = () => reject(req.error)
  })
  return new Promise((resolve, reject) => {
    const t = db.transaction('syncState', 'readwrite')
    t.objectStore('syncState').put({ key: 'identity', ...record })
    t.oncomplete = () => { db.close(); resolve() }
    t.onerror = () => reject(t.error)
  })
}

beforeEach(async () => {
  await setReplica(new IDBFactory())
  localStorage.removeItem(SYNC_CONFIG_KEY)
  _resetService()
  _setLocks(fakeLocks)
  remote = new MatchingRemote()
  _setStoreFactory(() => remote)
  // The public sync surface must never reach a MemoDump HTTP API.
  fetchSpy = vi.fn()
  globalThis.fetch = fetchSpy
})

afterEach(() => {
  expect(fetchSpy).not.toHaveBeenCalled()
})

describe('R6.5 browser sync service (public localApi surface)', () => {
  it('status before enable reports an unconnected, empty state', async () => {
    const st = (await localApi.syncStatus()).data
    expect(st.enabled).toBe(false)
    expect(st.connected).toBe(false)
    expect(st.connection).toBe(false)
    expect(st.recoveryCount).toBe(0)
    expect(st.experimental).toBe(true)
    expect(st.noE2EE).toBe(true)
    expect(st.lastRun).toBeUndefined()
  })

  it('saveSyncConfig validates and persists the configuration', async () => {
    await expect(saveSyncConfig({ endpoint: 'https://s3.example.com', bucket: '', accessKey: 'a', secretKey: 'b' }))
      .rejects.toMatchObject({ response: { status: 400 } })
    await saveSyncConfig(TEST_CONFIG)
    expect(getSyncConfig()).toMatchObject({
      endpoint: TEST_CONFIG.endpoint, bucket: TEST_CONFIG.bucket, region: 'us-east-1',
    })
    expect(JSON.parse(localStorage.getItem(SYNC_CONFIG_KEY)).secretKey).toBe(TEST_CONFIG.secretKey)
  })

  it('testSyncConfig surfaces a validation failure as the axios-shaped error the form reads', async () => {
    // The store factory drives normalizeConfig (unlike the in-memory remote),
    // so an invalid draft config must reject with the specific message instead
    // of a raw Error that the panel collapses into a generic failure.
    _setStoreFactory((cfg) => new S3Store(cfg))
    await expect(testSyncConfig({ endpoint: 'https://s3.example.com', bucket: '', accessKey: 'a', secretKey: 'b' }))
      .rejects.toMatchObject({ response: { status: 400, data: { error: 'missing S3 bucket' } } })
  })

  it('enable without a saved configuration is refused', async () => {
    await expect(localApi.syncEnable()).rejects.toMatchObject({
      response: { status: 400, data: { code: 'sync-config-required' } },
    })
  })

  it('enable creates identity + repo.json, pins the provider, assigns IDs, and runs the first cycle', async () => {
    await localApi.createNote({ name: 'hello', content: 'world\n' })
    await saveSyncConfig(TEST_CONFIG)

    const en = await localApi.syncEnable()
    expect(en.data.enabled).toBe(true)
    expect(en.data.vaultId).toMatch(UUID_RE)
    expect(en.data.repoId).toMatch(UUID_RE)

    expect(remote.objects.has('repo.json')).toBe(true)
    const conn = await loadConnection()
    expect(conn.connected).toBe(true)
    expect(conn.repositoryId).toBe(en.data.repoId)
    expect(conn.providerProfile).toBe(await providerProfile(TEST_CONFIG))
    const identity = await loadIdentity()
    expect(identity.vaultId).toBe(en.data.vaultId)

    const index = await loadSyncIndex()
    expect(Object.values(index)).toContain('hello.md')
    const syncId = Object.keys(index)[0]
    expect(remote.objects.has(noteKey(syncId))).toBe(true)

    const st = (await localApi.syncStatus()).data
    expect(st.connected).toBe(true)
    expect(st.lastTrigger).toBe('enable')
    expect(st.lastRun.Synced).toBe(true)
  })

  it('enable adopts an existing repository instead of recreating it', async () => {
    remote.seedRepo()
    await saveSyncConfig(TEST_CONFIG)
    const en = await localApi.syncEnable()
    expect(en.data.repoId).toBe(remote.repoRepositoryId)
    expect(remote.objects.has('repo.json')).toBe(true)
  })

  it('a changed provider is refused on re-enable until reset', async () => {
    await saveSyncConfig(TEST_CONFIG)
    await localApi.syncEnable()
    await localApi.syncDisable()

    const next = { ...TEST_CONFIG, bucket: 'other-notes' }
    remote.profileValue = providerProfile(next)
    await saveSyncConfig(next)
    await expect(localApi.syncEnable()).rejects.toMatchObject({
      response: { status: 400, data: { code: 'provider-changed' } },
    })
  })

  it('a changed remote repository is refused on re-enable until reset', async () => {
    await saveSyncConfig(TEST_CONFIG)
    await localApi.syncEnable()
    await localApi.syncDisable()

    remote.repoRepositoryId = crypto.randomUUID()
    remote.put('repo.json', utf8Bytes(serializeRepositoryDescriptor({
      formatVersion: 1,
      repositoryId: remote.repoRepositoryId,
      createdAt: Date.now(),
      minimumClientVersion: '2.0.0',
    })))
    await expect(localApi.syncEnable()).rejects.toMatchObject({
      response: { status: 400, data: { code: 'repo-changed' } },
    })
  })

  it('disable flips connected only; the pin, notes, and index survive', async () => {
    await localApi.createNote({ name: 'hello', content: 'world\n' })
    await saveSyncConfig(TEST_CONFIG)
    await localApi.syncEnable()

    const res = await localApi.syncDisable()
    expect(res.data).toEqual({ enabled: false, disconnected: true })
    const st = (await localApi.syncStatus()).data
    expect(st.enabled).toBe(false)
    expect(st.connected).toBe(false)
    expect(st.connection).toBe(true)
    expect(st.lastRun).toBeUndefined()
    expect((await localApi.listNotes('')).data).toHaveLength(1)
    expect(Object.keys(await loadSyncIndex())).toHaveLength(1)
  })

  it('reset clears the pin and snapshot but preserves identity, index, and notes', async () => {
    await localApi.createNote({ name: 'hello', content: 'world\n' })
    await saveSyncConfig(TEST_CONFIG)
    await localApi.syncEnable()
    const vaultId = (await loadIdentity()).vaultId

    await localApi.syncReset()
    const st = (await localApi.syncStatus()).data
    expect(st.connection).toBe(false)
    expect(st.enabled).toBe(false)
    expect((await loadIdentity()).vaultId).toBe(vaultId)
    expect(Object.keys(await loadSyncIndex())).toHaveLength(1)
    expect((await localApi.listNotes('')).data).toHaveLength(1)
  })

  it('Reset waits for the vault lock instead of racing a running cycle', async () => {
    await saveSyncConfig(TEST_CONFIG)
    await localApi.syncEnable()
    const { vaultId } = await loadIdentity()
    const lockName = `memodump-sync-${vaultId}`

    // Another tab holds the vault lock (a cycle in flight).
    let release
    const held = new Promise((resolve) => { release = resolve })
    const holder = fakeLocks.request(lockName, { ifAvailable: false }, async () => { await held })

    let resetDone = false
    const resetP = localApi.syncReset().then((r) => { resetDone = true; return r })
    await Promise.resolve()
    await Promise.resolve()
    // Reset must not return while the cycle holds the vault lock.
    expect(resetDone).toBe(false)

    release()
    await holder
    const res = await resetP
    expect(res.data).toEqual({ ok: true, reset: true })
    expect((await localApi.syncStatus()).data.connection).toBe(false)
  })

  it('a corrupt identity is surfaced in status and recovered by Reset', async () => {
    await localApi.listNotes('') // initialize the IndexedDB schema first
    await putIdentity({ vaultId: 'not-a-uuid', replicaId: 'also-not-a-uuid' })

    const st = (await localApi.syncStatus()).data
    expect(st.identityError).toBeTruthy()

    await localApi.syncReset()
    expect(await loadIdentity()).toBeNull()

    // The next Enable builds a valid identity from scratch.
    await saveSyncConfig(TEST_CONFIG)
    const en = await localApi.syncEnable()
    expect(en.data.vaultId).toMatch(UUID_RE)
    expect((await localApi.syncStatus()).data.identityError).toBeUndefined()
  })

  it('Enable rejects a corrupt identity instead of silently repairing it', async () => {
    await localApi.listNotes('') // initialize the IndexedDB schema first
    await putIdentity({ vaultId: 'not-a-uuid', replicaId: 'also-not-a-uuid' })
    await saveSyncConfig(TEST_CONFIG)

    // Corruption stops sync and requires an explicit Reset (spec R6.2); Enable
    // must never reinterpret it as a fresh vault or repair it in place.
    await expect(localApi.syncEnable()).rejects.toMatchObject({
      response: { status: 400, data: { code: 'identity-corrupt' } },
    })
    expect((await localApi.syncStatus()).data.identityError).toBeTruthy()
    // The corrupt record survives Enable untouched; only Reset clears it.
    await expect(loadIdentity()).rejects.toMatchObject({ code: 'identity-corrupt' })
  })

  it('Enable decides the identity under the initialization lock, so it cannot race a Reset', async () => {
    await saveSyncConfig(TEST_CONFIG)
    // No identity yet. Freeze the initialization lock, then start Enable and
    // Reset together: BOTH must wait on it — Enable must not mint a fresh
    // identity and move to a new vault lock a concurrent Reset would miss.
    let release
    const held = new Promise((resolve) => { release = resolve })
    const holder = fakeLocks.request('memodump-sync-init', { ifAvailable: false }, async () => { await held })

    let enableDone = false
    const enableP = localApi.syncEnable().then((r) => { enableDone = true; return r })
    let resetDone = false
    const resetP = localApi.syncReset().then((r) => { resetDone = true; return r })
    await Promise.resolve()
    await Promise.resolve()

    expect(enableDone).toBe(false)
    expect(resetDone).toBe(false)

    release()
    await holder
    // Enable runs first (FIFO on the init lock), then Reset re-derives the same
    // vault scope from the persisted identity and clears the pin after it —
    // never concurrently, so Reset cannot return and have Enable rewrite state.
    await enableP
    await resetP
    const st = (await localApi.syncStatus()).data
    expect(st.connection).toBe(false)
    expect(st.enabled).toBe(false)
  })

  it('run when not enabled is refused', async () => {
    await expect(localApi.syncRun()).rejects.toMatchObject({
      response: { status: 400, data: { code: 'not-enabled' } },
    })
  })

  it('a manual run records a redacted result in the status', async () => {
    await localApi.createNote({ name: 'hello', content: 'v1\n' })
    await saveSyncConfig(TEST_CONFIG)
    await localApi.syncEnable()
    await localApi.updateNote('hello.md', { content: 'changed\n' })

    const res = await localApi.syncRun()
    expect(res.data.Synced).toBe(true)
    expect(res.data.LastError).toBe('')
    expect(res.data.Scanned).toBeGreaterThan(0)
    const st = (await localApi.syncStatus()).data
    expect(st.lastTrigger).toBe('manual')
    expect(st.lastRun.Synced).toBe(true)
  })

  it('syncTest probes the configured provider; a missing configuration is refused', async () => {
    await expect(localApi.syncTest()).rejects.toMatchObject({
      response: { status: 400, data: { code: 'sync-config-required' } },
    })
    await saveSyncConfig(TEST_CONFIG)
    const res = await localApi.syncTest()
    expect(res.data).toEqual({ ok: true, conditionalWrites: true, pagedListing: true })
  })

  it('recovery list carries size metadata and restore returns the original path', async () => {
    const syncId = crypto.randomUUID()
    const stateHash = 'a'.repeat(64)
    await writeRecovery(syncId, stateHash, 'dir/a.md', '# recovered\n')

    const list = await localApi.syncRecovery()
    expect(list.data.recovery).toEqual([{
      syncId, stateHash, path: 'dir/a.md', size: new TextEncoder().encode('# recovered\n').byteLength,
    }])

    const rst = await localApi.syncRecoveryRestore({ syncId, stateHash })
    expect(rst.data).toEqual({ ok: true, path: 'dir/a.md' })
    const note = await localApi.getNote('dir/a.md')
    expect(note.data.content).toBe('# recovered\n')
  })

  it('status never leaks the stored configuration or its secrets', async () => {
    await saveSyncConfig(TEST_CONFIG)
    await localApi.syncEnable()
    const st = (await localApi.syncStatus()).data
    expect(JSON.stringify(st)).not.toContain(TEST_CONFIG.secretKey)
    expect(JSON.stringify(st)).not.toContain(TEST_CONFIG.accessKey)
    expect(JSON.stringify(st)).not.toContain(TEST_CONFIG.endpoint)
    expect(JSON.stringify(st)).not.toContain(TEST_CONFIG.bucket)
  })

  it('a corrupt connection pin surfaces connectionError and refuses re-enable', async () => {
    await localApi.listNotes('') // initialize the IndexedDB schema first
    await putConnection({ connected: true, providerProfile: 'not-hex', repositoryId: 'nope' })
    const st = (await localApi.syncStatus()).data
    expect(st.connected).toBe(false)
    expect(st.connectionError).toBeTruthy()

    await saveSyncConfig(TEST_CONFIG)
    await expect(localApi.syncEnable()).rejects.toMatchObject({
      response: { status: 400, data: { code: 'connection-corrupt' } },
    })
  })

  it('a successful Enable arms the page-lifetime scheduler for the ordinary interval', async () => {
    await localApi.createNote({ name: 'hello', content: 'v1\n' })
    await saveSyncConfig(TEST_CONFIG)
    await localApi.syncEnable()

    const st = (await localApi.syncStatus()).data
    expect(st.autoEnabled).toBe(true)
    expect(st.autoIntervalSecs).toBe(300)
    expect(st.autoPaused).toBe(false)
    expect(st.nextRun).toBeTruthy() // ~5 minutes out
    expect(Date.parse(st.nextRun)).toBeGreaterThan(Date.now())
  })

  it('Disable and Reset idle the scheduler', async () => {
    await saveSyncConfig(TEST_CONFIG)
    await localApi.syncEnable()
    await localApi.syncDisable()
    const afterDisable = (await localApi.syncStatus()).data
    expect(afterDisable.autoEnabled).toBe(false)
    expect(afterDisable.nextRun).toBeNull()

    await localApi.syncEnable()
    await localApi.syncReset()
    const afterReset = (await localApi.syncStatus()).data
    expect(afterReset.autoEnabled).toBe(false)
    expect(afterReset.nextRun).toBeNull()
  })

  it('a corrupt persisted configuration pauses permanently instead of a thrown hot loop', async () => {
    await localApi.createNote({ name: 'hello', content: 'v1\n' })
    await saveSyncConfig(TEST_CONFIG)
    await localApi.syncEnable()

    // Corrupt the persisted configuration and reload it into the module: the
    // next attempt cannot build a store (the default S3Store factory validates
    // via normalizeConfig). It must become a permanent provider-config pause,
    // never an unhandled rejection or a zero-delay loop.
    localStorage.setItem(SYNC_CONFIG_KEY, JSON.stringify({ endpoint: 'https://x.example.com', bucket: '', accessKey: '', secretKey: '' }))
    _resetService()
    _setLocks(fakeLocks)

    await localApi.syncRun()
    const st = (await localApi.syncStatus()).data
    expect(st.autoPaused).toBe(true)
    expect(st.pauseReason).toBe('provider-config')
    expect(st.nextRun).toBeNull()
  })

  it('a permanent failure pauses automatic sync until a manual run clears it', async () => {
    await localApi.createNote({ name: 'hello', content: 'v1\n' })
    await saveSyncConfig(TEST_CONFIG)
    await localApi.syncEnable()

    remote.failKind = 'permission'
    await localApi.syncRun()
    let st = (await localApi.syncStatus()).data
    expect(st.autoPaused).toBe(true)
    expect(st.pauseReason).toBe('permission')
    expect(st.nextRun).toBeNull()

    remote.failKind = null
    await localApi.syncRun() // a successful manual run clears the pause
    st = (await localApi.syncStatus()).data
    expect(st.autoPaused).toBe(false)
    expect(st.nextRun).toBeTruthy()
  })

  it('a cross-tab lock refusal is the ordinary interval, never a backoff or pause', async () => {
    await saveSyncConfig(TEST_CONFIG)
    await localApi.syncEnable()

    // Another tab holds the vault lock (a cycle in flight).
    const { vaultId } = await loadIdentity()
    let release
    const held = new Promise((resolve) => { release = resolve })
    const holder = fakeLocks.request(`memodump-sync-${vaultId}`, { ifAvailable: false }, async () => { await held })

    await localApi.syncRun() // refused: 'locked'
    const st = (await localApi.syncStatus()).data
    expect(st.lastRun.LastError).toBe('locked')
    expect(st.autoPaused).toBe(false)
    expect(st.nextRun).toBeTruthy() // ordinary 5-minute interval, not a retry

    release()
    await holder
  })

  it('classifyError reads the REAL error, not the redacted label', async () => {
    // retryable-transport redacts to 'provider-error' but must BACK OFF, not
    // wait the ordinary interval.
    expect(classifyError(new StoreError('retryable-transport', 'x')))
      .toEqual({ kind: 'retryable' })
    expect(classifyError(new StoreError('rate-limit', 'x', { retryAfterSeconds: 90 })))
      .toEqual({ kind: 'retryable', retryAfter: 90 })
    // A corrupt repo.json (coded invalid-repo) is a permanent pause.
    expect(classifyError(Object.assign(new Error('bad repo'), { code: 'invalid-repo' })))
      .toEqual({ kind: 'permanent', pauseReason: 'repo-lost' })
    // Permission and unknown errors pause; a cross-tab lock refusal is ordinary.
    expect(classifyError(new StoreError('permission', 'x')))
      .toEqual({ kind: 'permanent', pauseReason: 'permission' })
    expect(classifyError(Object.assign(new Error('boom'), { code: 'locked' })))
      .toEqual({ kind: 'success' })
    expect(classifyError(new Error('unknown')))
      .toEqual({ kind: 'permanent', pauseReason: 'error' })
  })

  it('classifyCycleResult backs off (with Retry-After) when notes were deferred', async () => {
    expect(classifyCycleResult({ Synced: false, Retry: 2, Blocked: 0, LastError: 'incomplete' }, 90))
      .toEqual({ kind: 'retryable', retryAfter: 90, result: { Synced: false, Retry: 2, Blocked: 0, LastError: 'incomplete' } })
    expect(classifyCycleResult({ Synced: true, Retry: 0, Blocked: 0, LastError: '' }))
      .toEqual({ kind: 'success', result: { Synced: true, Retry: 0, Blocked: 0, LastError: '' } })
  })

  it('a retryable-transport failure backs off instead of waiting the ordinary interval', async () => {
    await saveSyncConfig(TEST_CONFIG)
    await localApi.syncEnable()
    remote.failKind = 'retryable-transport'
    await localApi.syncRun()

    const st = (await localApi.syncStatus()).data
    expect(st.lastRun.LastError).toBe('provider-error') // redacted for the UI
    expect(st.autoPaused).toBe(false)
    const delayMs = Date.parse(st.nextRun) - Date.now()
    expect(delayMs).toBeGreaterThan(30_000) // ~1m backoff
    expect(delayMs).toBeLessThan(3 * 60_000) // never the 5m ordinary interval
  })

  it('a corrupt remote repo.json pauses automatic sync as repo-lost', async () => {
    await saveSyncConfig(TEST_CONFIG)
    await localApi.syncEnable()
    remote.put('repo.json', utf8Bytes(new TextEncoder().encode('{"garbage": true}')))

    await localApi.syncRun()
    const st = (await localApi.syncStatus()).data
    expect(st.lastRun.LastError).toBe('repo-loss')
    expect(st.autoPaused).toBe(true)
    expect(st.pauseReason).toBe('repo-lost')
    expect(st.nextRun).toBeNull()
  })

  it('attempts drive the registered UI refresh; stop clears it so shutdown never refreshes', async () => {
    const refresh = vi.fn()
    setSchedulerOnAttempt(refresh)
    await localApi.createNote({ name: 'hello', content: 'v1\n' })
    await saveSyncConfig(TEST_CONFIG)
    await localApi.syncEnable()

    await localApi.syncRun() // goes through runNow -> fire -> onAttemptDone
    expect(refresh).toHaveBeenCalled()

    await stopSyncScheduler() // unmount clears the hook and stops the scheduler
    refresh.mockClear()
    await localApi.syncRun() // a fresh scheduler: the callback is gone
    expect(refresh).not.toHaveBeenCalled()
  })

  it('a failed Run now returns the full redacted result, never data: undefined', async () => {
    await localApi.createNote({ name: 'hello', content: 'v1\n' })
    await saveSyncConfig(TEST_CONFIG)
    await localApi.syncEnable()

    remote.failKind = 'permission'
    const res = await localApi.syncRun()
    expect(res.data).toMatchObject({ Synced: false, LastError: 'permission' })
    expect(res.data).not.toBeUndefined()
  })

  it('stop detaches synchronously, so a re-mounted page gets a fresh working scheduler', async () => {
    const refresh = vi.fn()
    setSchedulerOnAttempt(refresh)
    await saveSyncConfig(TEST_CONFIG)
    await localApi.syncEnable()

    await stopSyncScheduler() // scheduler reference is nulled before the old await
    // A fresh mount starts a NEW instance and a manual run drives the hook.
    setSchedulerOnAttempt(refresh)
    startSyncScheduler()
    await localApi.syncRun()
    expect(refresh).toHaveBeenCalled()
    expect((await localApi.syncStatus()).data.autoEnabled).toBe(true)
  })

  it('two IndexedDB replicas converge through the same remote repository', async () => {
    const factoryA = new IDBFactory()
    const factoryB = new IDBFactory()

    await setReplica(factoryA)
    await saveSyncConfig(TEST_CONFIG)
    await localApi.syncEnable()
    await localApi.createNote({ name: 'shared', content: 'v1\n' })
    await localApi.syncRun()

    await setReplica(factoryB)
    await saveSyncConfig(TEST_CONFIG)
    await localApi.syncEnable()

    const notes = await localApi.listNotes('')
    expect(notes.data.map((n) => n.path)).toContain('shared.md')
    expect((await localApi.getNote('shared.md')).data.content).toBe('v1\n')
    const st = (await localApi.syncStatus()).data
    expect(st.lastRun.Synced).toBe(true)
  })
})

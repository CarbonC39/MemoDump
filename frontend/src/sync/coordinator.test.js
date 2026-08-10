import { IDBFactory } from 'fake-indexeddb'
import { describe, it, expect, beforeEach } from 'vitest'
import localApi from '../localApi'
import { _close } from '../storage/localVaultDb'
import { saveIdentity, setConnection, loadSyncIndex, listRecovery, loadSnapshot } from '../storage/syncDb'
import { runSyncCycle, observeAndDecide, executeDecisions } from './coordinator'
import { serializeRepositoryDescriptor } from './repo'
import { serializeNoteRecord, noteKey } from './note'
import { StoreError } from './s3store'
import { utf8Bytes, sha256Hex } from './hash.js'

// A tiny serializing Web-Locks stand-in: the first caller holds the lock, any
// concurrent Run now is refused (ifAvailable), never queued.
const fakeLocks = {
  held: false,
  async request(name, opts, fn) {
    if (fakeLocks.held) return fn(null)
    fakeLocks.held = true
    try {
      return await fn({ name })
    } finally {
      fakeLocks.held = false
    }
  },
}

// In-memory RemoteStore with CAS versions, mirroring what the coordinator uses
// on the S3 adapter: read/list/create/replace + profile.
class FakeRemote {
  constructor() {
    this.objects = new Map()
    this.versionCounter = 0
    this.repoRepositoryId = crypto.randomUUID()
  }

  async profile() {
    return sha256Hex('fake-remote')
  }

  async read(key, opts) {
    if (opts?.signal?.aborted) throw new DOMException('The operation was aborted.', 'AbortError')
    const o = this.objects.get(key)
    if (!o) throw new StoreError('not-found', 's3 not-found')
    return { data: o.data, version: o.version }
  }

  async list(prefix, opts) {
    if (opts?.signal?.aborted) throw new DOMException('The operation was aborted.', 'AbortError')
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

  seedRepo() {
    this.put('repo.json', utf8Bytes(serializeRepositoryDescriptor({
      formatVersion: 1,
      repositoryId: this.repoRepositoryId,
      createdAt: 1785800000000,
      minimumClientVersion: '2.0.0',
    })))
  }
}

beforeEach(() => {
  fakeLocks.held = false
})

// Point the shared storage layer at a fresh fake-indexeddb factory (one per
// replica) and reset the cached connection so openDB opens on it.
async function setReplica(factory) {
  await _close()
  globalThis.indexedDB = factory
}

async function enableReplica(remote) {
  await saveIdentity(crypto.randomUUID(), crypto.randomUUID())
  await setConnection({ providerProfile: await remote.profile(), repositoryId: remote.repoRepositoryId, connected: true })
  remote.seedRepo()
}

function freshReplica() {
  return new IDBFactory()
}

const runCycle = (remote, opts) => runSyncCycle(remote, { locks: fakeLocks, ...opts })

describe('serialized cycle lifecycle', () => {
  it('creates a note, pulls it on the other replica, and propagates edits and deletes', async () => {
    const remote = new FakeRemote()
    const factoryA = freshReplica()
    const factoryB = freshReplica()

    // A creates a note and uploads it.
    await setReplica(factoryA)
    await enableReplica(remote)
    await localApi.createNote({ name: 'hello', content: 'from A\n' })
    const resA = await runCycle(remote)
    expect(resA.snapshotCommitted).toBe(true)
    const syncId = Object.keys(await loadSyncIndex())[0]
    expect(remote.objects.has(noteKey(syncId))).toBe(true)

    // B (empty) pulls it.
    await setReplica(factoryB)
    await enableReplica(remote)
    await runCycle(remote)
    expect((await localApi.getNote('hello.md')).data.content).toBe('from A\n')

    // B edits; A pulls the edit.
    await localApi.updateNote('hello.md', { content: 'edited by B\n' })
    await runCycle(remote)
    await setReplica(factoryA)
    await runCycle(remote)
    expect((await localApi.getNote('hello.md')).data.content).toBe('edited by B\n')

    // B deletes; A applies the tombstone, writing a recovery copy first.
    await setReplica(factoryB)
    const bRec = await localApi.getNote('hello.md')
    await localApi.deleteNote('hello.md', bRec.data.revision)
    await runCycle(remote)
    await setReplica(factoryA)
    await runCycle(remote)
    await expect(localApi.getNote('hello.md')).rejects.toMatchObject({ response: { status: 404 } })
    expect((await listRecovery()).length).toBe(1)

    // A's next cycle removes the converged index mapping; B's too.
    await runCycle(remote)
    expect(Object.keys(await loadSyncIndex())).toHaveLength(0)
    await setReplica(factoryB)
    await runCycle(remote)
    expect(Object.keys(await loadSyncIndex())).toHaveLength(0)
  })

  it('preserves both divergent edits as an original + deterministic conflict note', async () => {
    const remote = new FakeRemote()
    const factoryA = freshReplica()
    const factoryB = freshReplica()

    await setReplica(factoryA)
    await enableReplica(remote)
    await localApi.createNote({ name: 'shared', content: 'v1\n' })
    await runCycle(remote)
    await setReplica(factoryB)
    await enableReplica(remote)
    await runCycle(remote)
    expect((await localApi.getNote('shared.md')).data.content).toBe('v1\n')

    // Both edit divergently WITHOUT syncing.
    await localApi.updateNote('shared.md', { content: 'B edit\n' })
    await setReplica(factoryA)
    await localApi.updateNote('shared.md', { content: 'A edit\n' })
    await runCycle(remote)

    // B now preserves its edit as a conflict note and accepts A's at the
    // original identity; no content is lost.
    await setReplica(factoryB)
    await runCycle(remote)
    expect((await localApi.getNote('shared.md')).data.content).toBe('A edit\n')
    const bAll = (await localApi.listNotes('')).data
    const conflict = bAll.find((n) => /^shared \(conflict [0-9a-f]{12}\)$/.test(n.name))
    expect(conflict).toBeDefined()
    expect((await localApi.getNote(conflict.path)).data.content).toBe('B edit\n')

    // A's next cycle pulls the conflict note down too.
    await setReplica(factoryA)
    await runCycle(remote)
    const aAll = (await localApi.listNotes('')).data
    expect(aAll.find((n) => /^shared \(conflict [0-9a-f]{12}\)$/.test(n.name))).toBeDefined()
    expect((await localApi.getNote('shared.md')).data.content).toBe('A edit\n')
  })

  it('propagates a remote path change as a local move preserving the Sync ID', async () => {
    const remote = new FakeRemote()
    await setReplica(freshReplica())
    await enableReplica(remote)
    await localApi.createNote({ name: 'old', content: 'content\n' })
    await runCycle(remote)
    const syncId = Object.keys(await loadSyncIndex())[0]
    const currentVersion = (await remote.read(noteKey(syncId))).version

    // Another device renames the note; the record's path changes (the content
    // hash covers the path, so this is a pull, and the local move is atomic).
    remote.put(noteKey(syncId), utf8Bytes(serializeNoteRecord({
      schemaVersion: 2, syncId, path: 'new.md', markdown: 'content\n', deleted: false,
    })))

    await runCycle(remote)
    await expect(localApi.getNote('old.md')).rejects.toMatchObject({ response: { status: 404 } })
    expect((await localApi.getNote('new.md')).data.content).toBe('content\n')
    expect((await loadSyncIndex())[syncId]).toBe('new.md')
    expect(currentVersion.length).toBeGreaterThan(0)
  })

  it('refuses a second Run now while the replica lock is held', async () => {
    const remote = new FakeRemote()
    await setReplica(freshReplica())
    await enableReplica(remote)
    await localApi.createNote({ name: 'a', content: 'x\n' })

    fakeLocks.held = true // another tab holds the Web Lock
    await expect(runCycle(remote)).rejects.toMatchObject({ code: 'locked' })
    fakeLocks.held = false

    await runCycle(remote) // releases fine afterwards
    expect(remote.objects.has('notes/')).toBe(false) // sanity: keys are per-id
  })

  it('propagates an abort and commits no snapshot', async () => {
    const remote = new FakeRemote()
    await setReplica(freshReplica())
    await enableReplica(remote)
    await localApi.createNote({ name: 'a', content: 'x\n' })

    const ac = new AbortController()
    ac.abort()
    await expect(runCycle(remote, { signal: ac.signal })).rejects.toMatchObject({ name: 'AbortError' })
    expect((await loadSnapshot({ vaultId: '', replicaId: '', providerProfile: '', repositoryId: '' })).reason).toBe('missing')
  })
})

describe('dirty editor racing a pull', () => {
  it('an edit that lands after the observation defers the pull and is never overwritten', async () => {
    const remote = new FakeRemote()
    await setReplica(freshReplica())
    await enableReplica(remote)
    await localApi.createNote({ name: 'a', content: 'old\n' })
    await runCycle(remote) // establish a baseline

    // Another device changes the remote.
    const syncId = Object.keys(await loadSyncIndex())[0]
    remote.put(noteKey(syncId), utf8Bytes(serializeNoteRecord({
      schemaVersion: 2, syncId, path: 'a.md', markdown: 'new\n', deleted: false,
    })))

    // Observe the cycle (local 'old', remote 'new', matching baseline => pull).
    const state = await observeAndDecide(remote)
    const pull = state.decisions.find((d) => d.kind === 'pull_live')
    expect(pull).toBeDefined()

    // The editor writes BEFORE the pull executes, bumping the local revision.
    await localApi.updateNote('a.md', { content: 'editor wins\n' })

    const { deferred } = await executeDecisions(remote, state.decisions, state.baselines, { index: state.index })
    expect(deferred).toBeGreaterThan(0)
    // The editor's content survives; the remote is untouched.
    expect((await localApi.getNote('a.md')).data.content).toBe('editor wins\n')
    const { data } = await remote.read(noteKey(syncId))
    expect(new TextDecoder().decode(data)).toContain('"new\\n"')
  })
})

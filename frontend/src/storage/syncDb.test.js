import 'fake-indexeddb/auto'
import { describe, it, expect, beforeEach } from 'vitest'
import localApi from '../localApi'
import { openDB, allOf, getRec, _close } from './localVaultDb'
import { sha256Hex } from './sha256'
import {
  loadIdentity, saveIdentity, loadConnection, setConnected, setConnection,
  loadSnapshot, replaceSnapshot, loadSyncIndex, removeIndexEntry,
  assignMissingSyncIds, reserveConflictNote, applyNoteMutation,
  writeRecovery, listRecovery, readRecovery, restoreRecovery, resetSyncState,
  SyncStateError, SNAPSHOT_SCHEMA_VERSION,
} from './syncDb'

const hex64 = (n) => String(n).padStart(64, '0')

// Start each test from a pristine database.
beforeEach(async () => {
  await _close()
  const req = indexedDB.deleteDatabase('memodump')
  await new Promise((resolve, reject) => {
    req.onsuccess = () => resolve()
    req.onerror = () => reject(req.error)
  })
})

function validUUID() {
  return crypto.randomUUID()
}

async function put(store, rec) {
  const db = await openDB()
  return new Promise((resolve, reject) => {
    const t = db.transaction(store, 'readwrite')
    t.objectStore(store).put(rec)
    t.oncomplete = resolve
    t.onerror = () => reject(t.error)
  })
}

async function putNote(rec) {
  await put('notes', rec)
}

async function indexOf() {
  return loadSyncIndex()
}

function noteRec(path, markdown, syncId) {
  return { path, markdown, syncId, content: markdown.replace(/^---\n[\s\S]*?\n---\n?/, ''), tags: [], revision: sha256Hex(markdown), modTime: 1, created: 1 }
}

// Open the database with the v2 layout, mirroring a pre-R6.2 installation.
async function openV2() {
  return new Promise((resolve, reject) => {
    const req = indexedDB.open('memodump', 2)
    req.onupgradeneeded = () => {
      const db = req.result
      if (!db.objectStoreNames.contains('notes')) db.createObjectStore('notes', { keyPath: 'path' })
      if (!db.objectStoreNames.contains('folders')) db.createObjectStore('folders', { keyPath: 'path' })
    }
    req.onsuccess = () => resolve(req.result)
    req.onerror = () => reject(req.error)
  })
}

describe('v2 -> v3 migration (populated database)', () => {
  it('preserves every existing note and folder record and adds the sync stores', async () => {
    const db = await openV2()
    const tx = db.transaction(['notes', 'folders'], 'readwrite')
    tx.objectStore('notes').put({ path: 'a.md', content: 'hello', tags: ['x'], markdown: 'hello', revision: sha256Hex('hello') })
    tx.objectStore('notes').put({ path: 'dir/b.md', content: 'bye', tags: [], markdown: 'bye', revision: sha256Hex('bye') })
    tx.objectStore('folders').put({ path: 'dir' })
    await new Promise((resolve, reject) => {
      tx.oncomplete = resolve
      tx.onerror = () => reject(tx.error)
    })
    db.close()

    const migrated = await openDB()
    expect(Array.from(migrated.objectStoreNames).sort()).toEqual(
      ['folders', 'notes', 'recovery', 'recoveryContent', 'syncIndex', 'syncState'].sort())
    const notes = await allOf('notes')
    expect(notes.map(n => n.path).sort()).toEqual(['a.md', 'dir/b.md'])
    expect(notes.find(n => n.path === 'a.md').markdown).toBe('hello')
    expect((await allOf('folders')).map(f => f.path)).toEqual(['dir'])
    // No sync identity is invented by the migration.
    expect(await loadIdentity()).toBeNull()
    expect(await indexOf()).toEqual({})
  })
})

describe('Vault/Replica identity', () => {
  it('returns null before enable and round-trips a valid identity', async () => {
    expect(await loadIdentity()).toBeNull()
    const vault = validUUID(); const replica = validUUID()
    await saveIdentity(vault, replica)
    expect(await loadIdentity()).toEqual({ vaultId: vault, replicaId: replica })
  })

  it('rejects non-UUID identities', async () => {
    await expect(saveIdentity('nope', validUUID())).rejects.toBeInstanceOf(SyncStateError)
  })

  it('treats a corrupt identity as corruption that requires Reset, never an empty vault', async () => {
    await put('syncState', { key: 'identity', vaultId: 'not-a-uuid', replicaId: validUUID() })
    await expect(loadIdentity()).rejects.toMatchObject({ code: 'identity-corrupt' })
  })
})

describe('connection pin', () => {
  const providerProfile = hex64(1)
  const repositoryId = validUUID()

  it('round-trips the pin and flips only the connected flag on Disable', async () => {
    await setConnection({ providerProfile, repositoryId, connected: true })
    expect(await loadConnection()).toEqual({ connected: true, providerProfile, repositoryId })
    await setConnected(false)
    expect(await loadConnection()).toEqual({ connected: false, providerProfile, repositoryId })
    await setConnected(true)
    expect((await loadConnection()).connected).toBe(true)
  })

  it('rejects an invalid pin', async () => {
    await expect(setConnection({ providerProfile: 'x', repositoryId, connected: true }))
      .rejects.toBeInstanceOf(SyncStateError)
  })

  it('treats a corrupt pin as corruption that stops sync, never an empty connection', async () => {
    await put('syncState', { key: 'connection', connected: 'yes', providerProfile, repositoryId })
    await expect(loadConnection()).rejects.toMatchObject({ code: 'connection-corrupt' })
  })
})

describe('disposable snapshot', () => {
  const identity = { vaultId: validUUID(), replicaId: validUUID() }
  const providerProfile = hex64(1)
  const repositoryId = validUUID()
  const expected = { ...identity, providerProfile, repositoryId }

  function validSnapshot() {
    return {
      schemaVersion: SNAPSHOT_SCHEMA_VERSION,
      vaultId: identity.vaultId,
      replicaId: identity.replicaId,
      repositoryId,
      providerProfile,
      notes: { [validUUID()]: { contentHash: hex64(2), deleted: false, remoteVersion: 'etag-1' } },
    }
  }

  it('reports missing before any snapshot exists (conservative onboarding)', async () => {
    expect(await loadSnapshot(expected)).toEqual({ reason: 'missing', snapshot: null })
  })

  it('round-trips a valid snapshot for the expected identity', async () => {
    const snap = validSnapshot()
    await replaceSnapshot(snap)
    const { reason, snapshot } = await loadSnapshot(expected)
    expect(reason).toBe('usable')
    expect(snapshot.repositoryId).toBe(repositoryId)
    expect(Object.keys(snapshot.notes)).toHaveLength(1)
  })

  it('rejects an invalid snapshot without writing anything (page-termination safety before/after replace)', async () => {
    await replaceSnapshot(validSnapshot())
    const bad = { ...validSnapshot(), repositoryId: 'not-a-uuid' }
    await expect(replaceSnapshot(bad)).rejects.toMatchObject({ code: 'invalid-snapshot' })
    // The prior snapshot is untouched: a crashed replace leaves either the old
    // or the new snapshot, never a partial one.
    const after = await loadSnapshot(expected)
    expect(after.reason).toBe('usable')
    expect(after.snapshot.repositoryId).toBe(repositoryId)
  })

  it('classifies a corrupt stored snapshot as corrupt, not as an empty replica', async () => {
    await put('syncState', { key: 'snapshot', schemaVersion: 99, notes: {} })
    const { reason } = await loadSnapshot(expected)
    expect(reason).toBe('corrupt')
  })

  it('a transaction aborted mid-replace leaves the old snapshot intact (page termination inside replace)', async () => {
    await replaceSnapshot(validSnapshot())
    const db = await openDB()
    // Simulate termination DURING the replace: the transaction puts a new
    // snapshot and then aborts, so the abort must roll the write back.
    await new Promise((resolve, reject) => {
      const t = db.transaction('syncState', 'readwrite')
      const store = t.objectStore('syncState')
      t.onabort = () => resolve()
      t.onerror = () => { if (t.error) reject(t.error) }
      t.oncomplete = () => reject(new Error('abort must roll the replace back'))
      store.put({ key: 'snapshot', ...validSnapshot(), repositoryId: validUUID() })
      t.abort()
    })
    const after = await loadSnapshot(expected)
    expect(after.reason).toBe('usable')
    expect(after.snapshot.repositoryId).toBe(repositoryId)
  })

  it('classifies profile and repository mismatches as explicit discard reasons', async () => {
    await replaceSnapshot(validSnapshot())
    expect((await loadSnapshot({ ...expected, providerProfile: hex64(3) })).reason)
      .toBe('provider-profile-mismatch')
    expect((await loadSnapshot({ ...expected, repositoryId: validUUID() })).reason)
      .toBe('repository-id-mismatch')
    expect((await loadSnapshot({ ...expected, vaultId: validUUID() })).reason).toBe('corrupt')
  })
})

describe('sync index and atomic ID assignment', () => {
  it('assigns a stable ID to every note lacking one, in one pass, and is idempotent', async () => {
    await putNote(noteRec('a.md', 'aaa', undefined))
    await putNote(noteRec('dir/b.md', 'bbb', undefined))

    const first = await assignMissingSyncIds()
    expect(first.assigned.map(a => a.path).sort()).toEqual(['a.md', 'dir/b.md'])
    const index = await indexOf()
    expect(Object.keys(index).length).toBe(2)
    // Live notes mirror the ID; valid UUIDs.
    const a = await getRec('notes', 'a.md')
    const b = await getRec('notes', 'dir/b.md')
    expect(a.syncId).toBe(indexOfFor(index, 'a.md'))
    expect(b.syncId).toBe(indexOfFor(index, 'dir/b.md'))
    expect(/^[0-9a-f]{8}-[0-9a-f]{4}-4/.test(a.syncId)).toBe(true)

    // Rerunning assigns nothing new and keeps the same IDs.
    const second = await assignMissingSyncIds()
    expect(second.assigned).toEqual([])
    expect(await indexOf()).toEqual(index)
  })

  it('reuses the surviving index identity when a deleted note reappears at its path', async () => {
    await putNote(noteRec('a.md', 'aaa', undefined))
    const first = await assignMissingSyncIds()
    const syncId = first.assigned[0].syncId
    // Local deletion keeps the mapping (so the tombstone can be emitted)...
    await localApi.deleteNote('a.md', (await getRec('notes', 'a.md')).revision)
    expect(await indexOf()).toEqual({ [syncId]: 'a.md' })
    // ...and a file recreated at the path reuses that identity.
    await localApi.createNote({ name: 'a', content: 'new body' })
    const again = await assignMissingSyncIds()
    // The recreated note reuses the surviving identity (mirrored on the record).
    expect(again.assigned).toEqual([{ syncId, path: 'a.md' }])
    expect((await getRec('notes', 'a.md')).syncId).toBe(syncId)
  })

  it('removes an index entry only on explicit converged-deletion cleanup', async () => {
    const syncId = validUUID()
    await put('syncIndex', { syncId, path: 'gone.md' })
    expect(await indexOf()).toEqual({ [syncId]: 'gone.md' })
    await removeIndexEntry(syncId)
    expect(await indexOf()).toEqual({})
  })

  it('rejects an invalid stored index entry as corruption', async () => {
    await put('syncIndex', { syncId: 'nope', path: 'a.md' })
    await expect(loadSyncIndex()).rejects.toMatchObject({ code: 'index-corrupt' })
  })
})

describe('in-app rename/move preserves the Sync ID and the indexed path', () => {
  async function seedSyncedNote(path) {
    const syncId = validUUID()
    const markdown = 'body'
    await putNote(noteRec(path, markdown, syncId))
    await put('syncIndex', { syncId, path })
    return syncId
  }

  it('updateNote rename moves the index mapping atomically', async () => {
    const syncId = await seedSyncedNote('a.md')
    const rec = await getRec('notes', 'a.md')
    await localApi.updateNote('a.md', { content: 'body', baseRevision: rec.revision, rename: 'renamed' })
    expect((await getRec('notes', 'renamed.md')).syncId).toBe(syncId)
    expect(await indexOf()).toEqual({ [syncId]: 'renamed.md' })
  })

  it('moveNote into a folder preserves the ID and remaps the index', async () => {
    const syncId = await seedSyncedNote('a.md')
    await localApi.moveNote('a.md', 'sub')
    expect((await getRec('notes', 'sub/a.md')).syncId).toBe(syncId)
    expect(await indexOf()).toEqual({ [syncId]: 'sub/a.md' })
  })

  it('a folder move rewrites every contained note mapping in one transaction', async () => {
    const syncId = await seedSyncedNote('docs/a.md')
    await putNote(noteRec('docs/b.md', 'b', validUUID()))
    await put('syncIndex', { syncId: (await getRec('notes', 'docs/b.md')).syncId, path: 'docs/b.md' })
    await localApi.moveFolder('docs', 'archive')
    const index = await indexOf()
    expect(index[syncId]).toBe('archive/docs/a.md')
    expect(index[(await getRec('notes', 'archive/docs/b.md')).syncId]).toBe('archive/docs/b.md')
  })

  it('moves a folder with many synced notes in one pass (single index read)', async () => {
    const ids = []
    for (let i = 0; i < 40; i++) {
      const syncId = validUUID()
      ids.push(syncId)
      await putNote(noteRec(`docs/n${i}.md`, `body ${i}`, syncId))
      await put('syncIndex', { syncId, path: `docs/n${i}.md` })
    }
    await localApi.moveFolder('docs', 'archive')
    const index = await indexOf()
    for (let i = 0; i < 40; i++) {
      expect(index[ids[i]]).toBe(`archive/docs/n${i}.md`)
      expect((await getRec('notes', `archive/docs/n${i}.md`)).syncId).toBe(ids[i])
    }
    expect(Object.keys(index).length).toBe(40)
  })

  it('a duplicate gets a fresh identity, never the source syncId', async () => {
    const syncId = await seedSyncedNote('a.md')
    const { data } = await localApi.duplicateNote('a.md')
    const copy = await getRec('notes', data.path)
    expect(copy.syncId).toBeUndefined()
    expect((await getRec('notes', 'a.md')).syncId).toBe(syncId)
    const after = await assignMissingSyncIds()
    expect(after.assigned.map(a => a.path)).toEqual([data.path])
    expect(after.assigned[0].syncId).not.toBe(syncId)
  })

  it('local delete keeps the mapping; only the converged-deletion cleanup drops it', async () => {
    const syncId = await seedSyncedNote('a.md')
    const rec = await getRec('notes', 'a.md')
    await localApi.deleteNote('a.md', rec.revision)
    expect(await indexOf()).toEqual({ [syncId]: 'a.md' })
    expect(await getRec('notes', 'a.md')).toBeUndefined()
  })

  it('a rename into a path still claimed by a tombstone-pending Sync ID is rejected', async () => {
    // A was deleted locally; its index claim survives until the tombstone
    // converges. Moving synced note B onto that path must fail in the same
    // transaction instead of corrupting the index with two IDs on one path.
    const a = validUUID()
    await put('syncIndex', { syncId: a, path: 'target.md' })
    const b = validUUID()
    await putNote(noteRec('b.md', 'b', b))
    await put('syncIndex', { syncId: b, path: 'b.md' })
    const rec = await getRec('notes', 'b.md')
    await expect(localApi.updateNote('b.md', { content: 'b', baseRevision: rec.revision, rename: 'target' }))
      .rejects.toMatchObject({ response: { status: 409, data: { error: { code: 'sync_path_conflict' } } } })
    expect((await getRec('notes', 'b.md')).syncId).toBe(b)
    expect(await getRec('notes', 'target.md')).toBeUndefined()
    expect(await indexOf()).toEqual({ [a]: 'target.md', [b]: 'b.md' })
  })

  it('a folder move onto a tombstone-claimed path is rejected in one transaction', async () => {
    const a = validUUID()
    await put('syncIndex', { syncId: a, path: 'sub/x/a.md' })
    const c = validUUID()
    await putNote(noteRec('x/a.md', 'c', c))
    await put('syncIndex', { syncId: c, path: 'x/a.md' })
    await expect(localApi.moveFolder('x', 'sub')).rejects.toMatchObject({ response: { status: 409 } })
    expect((await getRec('notes', 'x/a.md')).syncId).toBe(c)
    expect(await indexOf()).toEqual({ [a]: 'sub/x/a.md', [c]: 'x/a.md' })
  })
})

describe('applying remote mutations with the local revision CAS', () => {
  it('pulls a remote-only note (create-if-absent) and is idempotent on replay', async () => {
    const syncId = validUUID()
    const res = await applyNoteMutation({ mode: 'pull', syncId, path: 'dir/new.md', markdown: 'remote\n', expectedRevision: '' })
    expect(res).toEqual({ applied: true })
    const rec = await getRec('notes', 'dir/new.md')
    expect(rec.markdown).toBe('remote\n')
    expect(rec.syncId).toBe(syncId)
    expect(await indexOf()).toEqual({ [syncId]: 'dir/new.md' })
    expect(await applyNoteMutation({ mode: 'pull', syncId, path: 'dir/new.md', markdown: 'remote\n', expectedRevision: '' }))
      .toEqual({ applied: true })
  })

  it('a stale local revision never clobbers a concurrent edit', async () => {
    const syncId = validUUID()
    await putNote(noteRec('a.md', 'older', syncId))
    await put('syncIndex', { syncId, path: 'a.md' })
    const fresh = (await getRec('notes', 'a.md')).revision
    const stale = 'deadbeef'.repeat(8)

    const pull = await applyNoteMutation({ mode: 'pull', syncId, path: 'a.md', markdown: 'remote\n', expectedRevision: stale })
    expect(pull).toEqual({ applied: false, reason: 'revision-conflict' })
    expect((await getRec('notes', 'a.md')).markdown).toBe('older')

    const del = await applyNoteMutation({ mode: 'delete', syncId, path: 'a.md', expectedRevision: stale })
    expect(del).toEqual({ applied: false, reason: 'revision-conflict' })
    expect(await getRec('notes', 'a.md')).toBeDefined()

    const ok = await applyNoteMutation({ mode: 'pull', syncId, path: 'a.md', markdown: 'remote\n', expectedRevision: fresh })
    expect(ok).toEqual({ applied: true })
    expect((await getRec('notes', 'a.md')).markdown).toBe('remote\n')
  })

  it('a local edit that appeared during create-if-absent defers the pull', async () => {
    const syncId = validUUID()
    await putNote(noteRec('a.md', 'local edit', undefined))
    const res = await applyNoteMutation({ mode: 'pull', syncId, path: 'a.md', markdown: 'remote\n', expectedRevision: '' })
    expect(res).toEqual({ applied: false, reason: 'revision-conflict' })
    expect((await getRec('notes', 'a.md')).markdown).toBe('local edit')
  })

  it('a pull into a path claimed by another Sync ID defers', async () => {
    const other = validUUID()
    await put('syncIndex', { syncId: other, path: 'a.md' })
    const res = await applyNoteMutation({ mode: 'pull', syncId: validUUID(), path: 'a.md', markdown: 'x', expectedRevision: '' })
    expect(res).toEqual({ applied: false, reason: 'id-conflict' })
  })

  it('delete and path-change reject an empty expectedRevision (never unconditional)', async () => {
    const syncId = validUUID()
    await putNote(noteRec('a.md', 'old', syncId))
    await put('syncIndex', { syncId, path: 'a.md' })
    await expect(applyNoteMutation({ mode: 'delete', syncId, path: 'a.md', expectedRevision: '' }))
      .rejects.toMatchObject({ code: 'invalid-mutation' })
    await expect(applyNoteMutation({ mode: 'path-change', syncId, oldPath: 'a.md', path: 'b.md', markdown: 'x', expectedRevision: undefined }))
      .rejects.toMatchObject({ code: 'invalid-mutation' })
    expect(await getRec('notes', 'a.md')).toBeDefined()
    expect(await getRec('notes', 'b.md')).toBeUndefined()
    expect(await indexOf()).toEqual({ [syncId]: 'a.md' })
  })

  it('a delete keeps the index mapping so the next cycle can emit the tombstone', async () => {
    const syncId = validUUID()
    await putNote(noteRec('a.md', 'old', syncId))
    await put('syncIndex', { syncId, path: 'a.md' })
    const rev = (await getRec('notes', 'a.md')).revision
    const res = await applyNoteMutation({ mode: 'delete', syncId, path: 'a.md', expectedRevision: rev })
    expect(res).toEqual({ applied: true })
    expect(await getRec('notes', 'a.md')).toBeUndefined()
    expect(await indexOf()).toEqual({ [syncId]: 'a.md' })
  })

  it('a recovery copy written before a delete survives the deletion (recovery-before-delete)', async () => {
    const syncId = validUUID()
    const stateHash = hex64(12)
    await putNote(noteRec('a.md', 'old', syncId))
    await put('syncIndex', { syncId, path: 'a.md' })
    const rev = (await getRec('notes', 'a.md')).revision
    // The coordinator writes the durable recovery copy BEFORE the tombstone
    // delete; here that ordering is exercised and the copy must persist.
    await writeRecovery(syncId, stateHash, 'a.md', 'old')
    const res = await applyNoteMutation({ mode: 'delete', syncId, path: 'a.md', expectedRevision: rev })
    expect(res).toEqual({ applied: true })
    expect(await getRec('notes', 'a.md')).toBeUndefined()
    expect(await readRecovery(syncId, stateHash)).toEqual({ syncId, stateHash, path: 'a.md', markdown: 'old' })
    expect((await restoreRecovery(syncId, stateHash)).created).toBe(true)
  })

  it('a path-change (in-app move on the remote side) is one atomic transaction', async () => {
    const syncId = validUUID()
    await putNote(noteRec('old.md', 'older', syncId))
    await put('syncIndex', { syncId, path: 'old.md' })
    const rev = (await getRec('notes', 'old.md')).revision
    const res = await applyNoteMutation({ mode: 'path-change', syncId, oldPath: 'old.md', path: 'new.md', markdown: 'remote\n', expectedRevision: rev })
    expect(res).toEqual({ applied: true })
    expect(await getRec('notes', 'old.md')).toBeUndefined()
    expect((await getRec('notes', 'new.md')).markdown).toBe('remote\n')
    expect(await indexOf()).toEqual({ [syncId]: 'new.md' })
  })

  it('a stale old-path revision aborts a path-change without touching either side', async () => {
    const syncId = validUUID()
    await putNote(noteRec('old.md', 'older', syncId))
    await put('syncIndex', { syncId, path: 'old.md' })
    const res = await applyNoteMutation({ mode: 'path-change', syncId, oldPath: 'old.md', path: 'new.md', markdown: 'remote\n', expectedRevision: 'stale' })
    expect(res).toEqual({ applied: false, reason: 'revision-conflict' })
    expect(await getRec('notes', 'old.md')).toBeDefined()
    expect(await getRec('notes', 'new.md')).toBeUndefined()
    expect(await indexOf()).toEqual({ [syncId]: 'old.md' })
  })
})

describe('conflict reservation', () => {
  it('reserves a deterministic conflict note and is replay-safe', async () => {
    const syncId = validUUID()
    const res = await reserveConflictNote(syncId, 'a (conflict ab12cd34ef56).md', 'conflict body')
    expect(res).toEqual({ ok: true, created: true })
    const rec = await getRec('notes', 'a (conflict ab12cd34ef56).md')
    expect(rec.syncId).toBe(syncId)
    expect(rec.markdown).toBe('conflict body')
    expect(await indexOf()).toEqual({ [syncId]: 'a (conflict ab12cd34ef56).md' })
    // Replay with identical ID/path/content is a no-op.
    expect(await reserveConflictNote(syncId, 'a (conflict ab12cd34ef56).md', 'conflict body'))
      .toEqual({ ok: true, created: false })
  })

  it('blocks a path already occupied by different content', async () => {
    const syncId = validUUID()
    await putNote(noteRec('taken.md', 'someone else', undefined))
    await expect(reserveConflictNote(syncId, 'taken.md', 'mine')).rejects.toMatchObject({ code: 'path-occupied' })
  })

  it('blocks a path claimed by a different Sync ID', async () => {
    const other = validUUID()
    await put('syncIndex', { syncId: other, path: 'x.md' })
    await expect(reserveConflictNote(validUUID(), 'x.md', 'mine')).rejects.toMatchObject({ code: 'path-claimed' })
  })

  it('blocks reusing a conflict ID that is already mapped elsewhere', async () => {
    const syncId = validUUID()
    await reserveConflictNote(syncId, 'one.md', 'body')
    await expect(reserveConflictNote(syncId, 'two.md', 'body')).rejects.toMatchObject({ code: 'id-mapped' })
  })
})

describe('recovery store', () => {
  it('lists metadata without loading Markdown, and restores a copy safely', async () => {
    const syncId = validUUID()
    const stateHash = hex64(7)
    await writeRecovery(syncId, stateHash, 'dir/a.md', '# recovered\n')
    await writeRecovery(syncId, hex64(8), 'dir/b.md', 'second\n')

    const list = await listRecovery()
    expect(list).toEqual([
      { syncId, stateHash: hex64(7), path: 'dir/a.md' },
      { syncId, stateHash: hex64(8), path: 'dir/b.md' },
    ])
    for (const m of list) expect(m.markdown).toBeUndefined()

    const restored = await restoreRecovery(syncId, stateHash)
    expect(restored).toEqual({ ok: true, created: true })
    const rec = await getRec('notes', 'dir/a.md')
    expect(rec.markdown).toBe('# recovered\n')
    expect(rec.syncId).toBe(syncId)
    expect(await indexOf()).toEqual({ [syncId]: 'dir/a.md' })
    // Idempotent replay.
    expect(await restoreRecovery(syncId, stateHash)).toEqual({ ok: true, created: false })
  })

  it('writeRecovery is idempotent and readRecovery returns the full copy', async () => {
    const syncId = validUUID()
    const stateHash = hex64(9)
    await writeRecovery(syncId, stateHash, 'a.md', 'doc\n')
    await writeRecovery(syncId, stateHash, 'a.md', 'doc\n')
    const copy = await readRecovery(syncId, stateHash)
    expect(copy).toEqual({ syncId, stateHash, path: 'a.md', markdown: 'doc\n' })
  })

  it('refuses to restore over an unrelated note', async () => {
    const syncId = validUUID()
    const stateHash = hex64(10)
    await writeRecovery(syncId, stateHash, 'a.md', 'recovered\n')
    await putNote(noteRec('a.md', 'unrelated', validUUID()))
    await expect(restoreRecovery(syncId, stateHash)).rejects.toMatchObject({ code: 'path-occupied' })
  })

  it('blocks restoring a copy whose Sync ID is already mapped elsewhere', async () => {
    const syncId = validUUID()
    const stateHash = hex64(14)
    await writeRecovery(syncId, stateHash, 'a.md', 'recovered\n')
    await restoreRecovery(syncId, stateHash)
    // Move the restored note; the index now maps the Sync ID to b.md.
    const rec = await getRec('notes', 'a.md')
    await localApi.updateNote('a.md', { content: 'recovered\n', baseRevision: rec.revision, rename: 'b' })
    expect(await indexOf()).toEqual({ [syncId]: 'b.md' })
    // Restoring the same copy again would mint a second note with the same ID
    // and silently drop the mapping — it must be blocked.
    await expect(restoreRecovery(syncId, stateHash)).rejects.toMatchObject({ code: 'id-mapped' })
    expect(await getRec('notes', 'b.md')).toBeDefined()
  })

  it('blocks restoring into a path claimed by a different Sync ID', async () => {
    const syncId = validUUID()
    const stateHash = hex64(15)
    const other = validUUID()
    await writeRecovery(syncId, stateHash, 'a.md', 'recovered\n')
    await put('syncIndex', { syncId: other, path: 'a.md' })
    await expect(restoreRecovery(syncId, stateHash)).rejects.toMatchObject({ code: 'path-claimed' })
  })

  it('restore to an explicit target path works and keeps the recovery copy', async () => {
    const syncId = validUUID()
    const stateHash = hex64(11)
    await writeRecovery(syncId, stateHash, 'deleted.md', 'doc\n')
    await restoreRecovery(syncId, stateHash, 'restored.md')
    const rec = await getRec('notes', 'restored.md')
    expect(rec.syncId).toBe(syncId)
    expect(rec.markdown).toBe('doc\n')
    expect(await indexOf()).toEqual({ [syncId]: 'restored.md' })
    // The copy itself is not consumed by a restore.
    expect(await readRecovery(syncId, stateHash)).not.toBeNull()
  })
})

describe('Disable and Reset', () => {
  it('Disable flips only the connected flag', async () => {
    const identity = { vaultId: validUUID(), replicaId: validUUID() }
    const providerProfile = hex64(1)
    const repositoryId = validUUID()
    await saveIdentity(identity.vaultId, identity.replicaId)
    await setConnection({ providerProfile, repositoryId, connected: true })
    await putNote(noteRec('a.md', 'body', validUUID()))
    await put('syncIndex', { syncId: validUUID(), path: 'a.md' })
    await replaceSnapshot({ schemaVersion: SNAPSHOT_SCHEMA_VERSION, ...identity, repositoryId, providerProfile, notes: {} })
    await writeRecovery(validUUID(), hex64(2), 'a.md', 'body')

    await setConnected(false)
    expect((await loadConnection()).connected).toBe(false)
    expect((await loadConnection()).providerProfile).toBe(providerProfile)
    expect((await loadConnection()).repositoryId).toBe(repositoryId)
    expect((await allOf('notes')).length).toBe(1)
    expect(Object.keys(await indexOf()).length).toBe(1)
    expect((await loadSnapshot({ ...identity, repositoryId, providerProfile })).reason).toBe('usable')
    expect((await listRecovery()).length).toBe(1)
  })

  it('Reset clears the pin and snapshot but preserves notes, IDs, recovery, and identity', async () => {
    const identity = { vaultId: validUUID(), replicaId: validUUID() }
    const providerProfile = hex64(1)
    const repositoryId = validUUID()
    await saveIdentity(identity.vaultId, identity.replicaId)
    await setConnection({ providerProfile, repositoryId, connected: true })
    const syncId = validUUID()
    await putNote(noteRec('a.md', 'body', syncId))
    await put('syncIndex', { syncId, path: 'a.md' })
    await replaceSnapshot({ schemaVersion: SNAPSHOT_SCHEMA_VERSION, ...identity, repositoryId, providerProfile, notes: {} })
    const stateHash = hex64(2)
    await writeRecovery(syncId, stateHash, 'a.md', 'body')

    await resetSyncState()

    expect(await loadConnection()).toBeNull()
    expect((await loadSnapshot({ ...identity, repositoryId, providerProfile })).reason).toBe('missing')
    expect(await loadIdentity()).toEqual(identity)
    expect((await getRec('notes', 'a.md')).syncId).toBe(syncId)
    expect(await indexOf()).toEqual({ [syncId]: 'a.md' })
    expect(await readRecovery(syncId, stateHash)).not.toBeNull()
  })
})

function indexOfFor(index, path) {
  for (const [id, p] of Object.entries(index)) if (p === path) return id
  return undefined
}

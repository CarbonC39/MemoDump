// R6.5 in-page note-sync service for the Pure frontend/PWA build. The browser
// owns its own sync engine (R6.4 coordinator over the R6.3 S3 adapter), so the
// axios-shaped sync surface resolves to this in-page service instead of HTTP:
// localApi delegates every sync* method here and nothing ever calls
// /api/sync/*.
//
// The note-sync S3 configuration (endpoint/region/bucket/prefix/keys/path
// style) is persisted in localStorage — the same plaintext-credential warning
// the image settings carry applies, and it NEVER enters IndexedDB sync state,
// remote note records, recovery copies, fixtures, logs, or the status payload.
// Enable validates the configuration, probes capabilities, creates or adopts
// repo.json (the only place it is ever created), pins the provider/repository,
// assigns Sync IDs, and requests one immediate run. Status/recovery are read
// straight from IndexedDB and in-memory attempt state.

import {
  loadIdentity, loadIdentityRecord, createIdentityIfAbsent, loadConnection,
  setConnection, setConnected, resetSyncState, assignMissingSyncIds,
  listRecovery, readRecovery, restoreRecovery,
} from '../storage/syncDb.js'
import { S3Store, StoreError, normalizeConfig, providerProfile } from './s3store.js'
import { serializeRepositoryDescriptor, parseRepositoryDescriptor } from './repo.js'
import { runSyncCycle, cycleUnderLock } from './coordinator.js'
import { classifyErrorLabel } from './retry.js'
import { newUUIDv4 } from './uuid.js'
import { utf8Bytes } from './hash.js'

export const SYNC_CONFIG_KEY = 'memodump_sync_config'
export const MINIMUM_CLIENT_VERSION = '2.0.0'

// ---- module state ---------------------------------------------------------

let config = loadConfig()
let storeFactory = (cfg) => new S3Store(cfg)
let locks = (typeof navigator !== 'undefined' && navigator.locks) || null
let syncRunning = false
let lastRun = null
let lastCompleted = null
let lastTrigger = ''

function loadConfig() {
  try {
    const raw = localStorage.getItem(SYNC_CONFIG_KEY)
    if (raw) {
      const parsed = JSON.parse(raw)
      if (parsed && typeof parsed === 'object') return parsed
    }
  } catch (_) {}
  return null
}

// ---- error shape (axios-compatible) --------------------------------------

function reject(status, error, code) {
  const data = { error }
  if (code) data.code = code
  return { response: { status, data } }
}

// wrapError rethrows an already-shaped error and converts any other failure
// into the axios shape the panel's catch expects.
function wrapError(e, status) {
  if (e && e.response) throw e
  throw reject(status, e && e.message ? e.message : 'sync error', e && e.code)
}

// ---- config surface (not axios-shaped; used by the settings form) --------

export function getSyncConfig() {
  return config
    ? {
        endpoint: config.endpoint || '',
        region: config.region || 'us-east-1',
        bucket: config.bucket || '',
        prefix: config.prefix || '',
        accessKey: config.accessKey || '',
        secretKey: config.secretKey || '',
        forcePathStyle: config.forcePathStyle !== false,
      }
    : {
        endpoint: '', region: 'us-east-1', bucket: '', prefix: '',
        accessKey: '', secretKey: '', forcePathStyle: true,
      }
}

// saveSyncConfig validates and persists the note-sync configuration. Secrets
// live in localStorage only (the settings UI warns about this).
export async function saveSyncConfig(cfg) {
  let normalized
  try {
    normalized = normalizeConfig(cfg) // validates; throws with a clear message
  } catch (e) {
    throw reject(400, e && e.message ? e.message : 'invalid sync configuration', 'invalid-config')
  }
  config = { ...normalized }
  try {
    localStorage.setItem(SYNC_CONFIG_KEY, JSON.stringify(config))
  } catch (e) {
    config = loadConfig()
    throw reject(400, 'could not persist the sync configuration', 'storage-full')
  }
  return { ok: true }
}

// testSyncConfig probes a candidate configuration (the form's draft) without
// persisting it. A validation failure inside the store constructor is caught
// and surfaced as the axios-shaped error the settings form displays.
export async function testSyncConfig(cfg) {
  try {
    const store = makeStore(cfg)
    const { capabilities } = await store.test()
    return {
      ok: true,
      conditionalWrites: capabilities.conditionalWrites,
      pagedListing: capabilities.pagedListing,
    }
  } catch (e) {
    wrapError(e, 400)
  }
}

function makeStore(cfg) {
  return storeFactory(cfg)
}

// ---- redacted run bookkeeping --------------------------------------------

function redactCycle(res) {
  const synced = res.snapshotCommitted && res.blocked === 0 && res.retry === 0
  return {
    Synced: synced,
    Scanned: res.scanned,
    Blocked: res.blocked,
    Retry: res.retry,
    Conflicts: res.conflicts,
    SnapshotCommitted: res.snapshotCommitted,
    LastError: synced ? '' : 'incomplete',
  }
}

function classifyLabel(e) {
  if (e && (e.name === 'AbortError' || e.code === 'aborted')) return 'cancelled'
  if (e instanceof StoreError) return classifyErrorLabel({ kind: e.kind })
  const code = e && e.code
  switch (code) {
    case 'locked':
      return 'locked'
    case 'unsupported-lock':
      return 'unsupported'
    case 'not-enabled':
      return 'not-enabled'
    case 'profile-mismatch':
    case 'repository-mismatch':
    case 'snapshot-mismatch':
      return 'mismatch'
    case 'repository-loss':
      return 'repo-loss'
    case 'identity-corrupt':
    case 'connection-corrupt':
    case 'index-corrupt':
      return 'corrupt-state'
    default:
      return 'error'
  }
}

function recordRun(result, trigger) {
  lastRun = result
  lastCompleted = new Date().toISOString()
  lastTrigger = trigger
}

// runOnce runs one serialized cycle for the current replica and always records
// a redacted outcome; the cycle never rejects through here. A second concurrent
// attempt is refused with a locked result, mirroring the Go replica lock.
async function runOnce(store, trigger) {
  if (syncRunning) {
    const locked = { Synced: false, Scanned: 0, Blocked: 0, Retry: 0, Conflicts: 0, SnapshotCommitted: false, LastError: 'locked' }
    recordRun(locked, trigger)
    return locked
  }
  syncRunning = true
  try {
    const res = await runSyncCycle(store, { locks })
    const red = redactCycle(res)
    recordRun(red, trigger)
    return red
  } catch (e) {
    const red = { Synced: false, Scanned: 0, Blocked: 0, Retry: 0, Conflicts: 0, SnapshotCommitted: false, LastError: classifyLabel(e) }
    recordRun(red, trigger)
    return red
  } finally {
    syncRunning = false
  }
}

// runOnceUnderLock runs the cycle body when the caller ALREADY holds the vault
// Web Lock (enable must pin the connection and run the first cycle under ONE
// lock so a concurrent Reset/Disable can never interleave with it).
async function runOnceUnderLock(store, trigger) {
  if (syncRunning) {
    const locked = { Synced: false, Scanned: 0, Blocked: 0, Retry: 0, Conflicts: 0, SnapshotCommitted: false, LastError: 'locked' }
    recordRun(locked, trigger)
    return locked
  }
  syncRunning = true
  try {
    const res = await cycleUnderLock(store)
    const red = redactCycle(res)
    recordRun(red, trigger)
    return red
  } catch (e) {
    const red = { Synced: false, Scanned: 0, Blocked: 0, Retry: 0, Conflicts: 0, SnapshotCommitted: false, LastError: classifyLabel(e) }
    recordRun(red, trigger)
    return red
  } finally {
    syncRunning = false
  }
}

// ---- cross-tab serialization ---------------------------------------------

const INIT_LOCK = 'memodump-sync-init'

function vaultLockName(vaultId) {
  return `memodump-sync-${vaultId}`
}

// withInitLock serializes the identity decision itself. Lifecycle operations
// run in two phases: the initialization lock decides/creates the identity FIRST
// (so every tab agrees on one vault scope before anything else), and only then
// is the vault lock — the same one the coordinator's cycle uses — taken for the
// operation. Without this, an Enable that mints a fresh identity could race a
// Reset that read "no identity" and is waiting on the init lock, letting the
// Reset return while the Enable still rewrites connection/snapshot.
function withInitLock(fn) {
  if (!locks) throw reject(500, 'Web Locks are unavailable', 'unsupported-lock')
  return locks.request(INIT_LOCK, { ifAvailable: false }, (lock) => {
    if (!lock) throw reject(409, 'another sync operation holds the initialization lock', 'locked')
    return fn()
  })
}

// withVaultLock takes the vault Web Lock derived from the CURRENT identity
// record (a corrupt record keeps a stable scope so two tabs can clear it
// consistently). Every caller already holds the initialization lock, so a vault
// WITHOUT an identity record simply runs under that init scope — it never
// requests the same name again (Web Locks are not reentrant, and a cycle cannot
// run without an identity anyway). The lock WAITS for a running cycle, so a
// Reset can never return while an old cycle could still rewrite the snapshot.
async function withVaultLock(fn) {
  if (!locks) throw reject(500, 'Web Locks are unavailable', 'unsupported-lock')
  let name = INIT_LOCK
  try {
    const rec = await loadIdentityRecord()
    if (rec) name = vaultLockName(rec.vaultId)
  } catch (_) {}
  if (name === INIT_LOCK) return fn() // no vault: the caller's init lock is the scope
  return locks.request(name, { ifAvailable: false }, (lock) => {
    if (!lock) throw reject(409, 'another sync operation holds the vault lock', 'locked')
    return fn()
  })
}

// withLifecycleLock is the two-phase lifecycle wrapper: identity scope decided
// under the init lock, then the operation under the vault lock.
async function withLifecycleLock(fn) {
  return withInitLock(() => withVaultLock(fn))
}

async function safeLoadConnection() {
  try {
    return await loadConnection()
  } catch (e) {
    throw reject(400, e.message || 'sync connection is corrupt', e.code || 'connection-corrupt')
  }
}

// ---- repo.json establish / adopt (the only place it is created) ----------

async function establishRepository(store, prev) {
  const profile = await store.profile()
  if (prev && prev.providerProfile && prev.providerProfile !== profile) {
    throw reject(400, 'sync provider changed; reset and re-enable sync to switch', 'provider-changed')
  }
  let data
  try {
    ;({ data } = await store.read('repo.json'))
  } catch (e) {
    if (e instanceof StoreError && e.kind === 'not-found') {
      if (prev && prev.repositoryId) {
        throw reject(400, 'remote repository lost though sync was established; reset and re-enable sync to create a new one', 'repository-loss')
      }
      const descriptor = {
        formatVersion: 1,
        repositoryId: newUUIDv4(),
        createdAt: Date.now(),
        minimumClientVersion: MINIMUM_CLIENT_VERSION,
      }
      const bytes = utf8Bytes(serializeRepositoryDescriptor(descriptor))
      try {
        await store.create('repo.json', bytes)
      } catch (e2) {
        if (e2 instanceof StoreError && e2.kind === 'precondition-failed') {
          // Lost a concurrent first-create race: adopt the winner.
          return adoptRepository(store)
        }
        throw e2
      }
      return descriptor.repositoryId
    }
    throw e
  }
  return adoptDescriptor(data, prev)
}

async function adoptRepository(store) {
  let data
  try {
    ;({ data } = await store.read('repo.json'))
  } catch (e) {
    throw e
  }
  return adoptDescriptor(data, null)
}

function adoptDescriptor(data, prev) {
  let repo
  try {
    repo = parseRepositoryDescriptor(data)
  } catch (e) {
    throw reject(400, `invalid remote repo.json: ${e.message}`, 'invalid-repo')
  }
  if (prev && prev.repositoryId && repo.repositoryId !== prev.repositoryId) {
    throw reject(400, 'remote repository changed; reset and re-enable sync to switch', 'repo-changed')
  }
  return repo.repositoryId
}

// ---- axios-shaped sync surface -------------------------------------------

export async function syncStatus() {
  // A corrupt identity is surfaced so the panel offers Reset even though there
  // is no usable vault to connect to.
  let identityError = ''
  try {
    await loadIdentity()
  } catch (e) {
    identityError = e.message || 'sync identity is corrupt; reset sync'
  }
  let connected = false
  let connection = false
  let connectionError = ''
  try {
    const rec = await loadConnection()
    connection = !!rec
    connected = !!rec && rec.connected
  } catch (e) {
    // A corrupt connection pin is surfaced, never reinterpreted as disabled.
    connectionError = e.message || 'sync connection is corrupt'
  }
  let recoveryCount = 0
  if (connected) {
    try {
      recoveryCount = (await listRecovery()).length
    } catch (_) {
      recoveryCount = 0
    }
  }
  const data = {
    enabled: connected,
    connected,
    connection,
    experimental: true,
    noE2EE: true,
    recoveryCount,
    autoEnabled: false, // the page-lifetime scheduler lands in R6.6
    autoIntervalSecs: 0,
    syncRunning,
    autoPaused: false,
    pauseReason: '',
    nextRun: null,
  }
  if (connectionError) data.connectionError = connectionError
  if (identityError) data.identityError = identityError
  if (lastCompleted) {
    data.lastRun = lastRun
    data.lastCompleted = lastCompleted
    data.lastTrigger = lastTrigger
  }
  return { data }
}

export async function syncEnable() {
  if (!config) throw reject(400, 'configure note sync first', 'sync-config-required')

  // Phase 1 — the initialization lock: decide the identity. A missing identity
  // is created here (atomically, so two tabs adopt the same pair); a corrupt
  // one is REJECTED and requires an explicit Reset — never reinterpreted as a
  // fresh vault (spec R6.2). Because the decision happens under the init lock,
  // a concurrent Reset can never read a stale "no identity" scope and race us.
  return withInitLock(async () => {
    let identity
    try {
      identity = (await createIdentityIfAbsent(newUUIDv4(), newUUIDv4())).identity
    } catch (e) {
      if (e && e.code === 'identity-corrupt') {
        throw reject(400, e.message, 'identity-corrupt')
      }
      throw e
    }

    // Phase 2 — the vault lock: connection validation, capability probe, ID
    // assignment, repo.json establishment/adoption, the provider pin, and the
    // immediate first cycle all share ONE lock with the coordinator's cycles.
    return withVaultLock(async () => {
      let prev
      try {
        prev = await loadConnection()
      } catch (e) {
        throw reject(400, e.message || 'sync connection is corrupt; reset sync', e.code || 'connection-corrupt')
      }
      const store = makeStore(config)
      try {
        await store.test()
      } catch (e) {
        wrapError(e, 400)
      }

      // Scan and assign stable Sync IDs (idempotent across re-enables).
      await assignMissingSyncIds()

      // Establish or re-adopt repo.json, then pin the verified provider/repository.
      const repoId = await establishRepository(store, prev)
      const profile = await providerProfile(config)
      await setConnection({ providerProfile: profile, repositoryId: repoId, connected: true })

      // A successful Enable requests one immediate run under the SAME lock.
      await runOnceUnderLock(store, 'enable')
      return { identity, repoId }
    })
  }).then(({ identity, repoId }) => ({
    data: {
      enabled: true,
      vaultId: identity.vaultId,
      replicaId: identity.replicaId,
      repoId,
      experimental: true,
      noE2EE: true,
    },
  }))
}

export async function syncRun() {
  const conn = await safeLoadConnection()
  if (!conn || !conn.connected) throw reject(400, 'sync is not enabled', 'not-enabled')
  if (!config) throw reject(400, 'sync configuration is missing', 'sync-config-required')
  const store = makeStore(config)
  const red = await runOnce(store, 'manual')
  return { data: red }
}

export async function syncDisable() {
  await withLifecycleLock(async () => {
    const conn = await safeLoadConnection()
    if (conn) await setConnected(false)
  })
  lastRun = null
  lastCompleted = null
  lastTrigger = ''
  return { data: { enabled: false, disconnected: true } }
}

export async function syncReset() {
  // The two-phase lock makes Reset decide its scope under the initialization
  // lock and wait for any running cycle on the vault lock, so it can never race
  // an Enable that is minting an identity. resetSyncState removes a CORRUPT
  // identity record (making Reset the recovery path) and preserves a valid one.
  await withLifecycleLock(async () => {
    await resetSyncState()
  })
  lastRun = null
  lastCompleted = null
  lastTrigger = ''
  return { data: { ok: true, reset: true } }
}

export async function syncTest() {
  if (!config) throw reject(400, 'sync configuration is missing', 'sync-config-required')
  try {
    const store = makeStore(config)
    const { capabilities } = await store.test()
    return { data: { ok: true, conditionalWrites: capabilities.conditionalWrites, pagedListing: capabilities.pagedListing } }
  } catch (e) {
    wrapError(e, 400)
  }
}

export async function syncRecovery() {
  let copies
  try {
    copies = await listRecovery()
  } catch (e) {
    throw reject(500, e.message || 'could not list recovery copies', e.code)
  }
  return { data: { recovery: copies } }
}

export async function syncRecoveryRestore({ syncId, stateHash }) {
  if (!syncId || !stateHash) throw reject(400, 'invalid request body', 'invalid-request')
  const copy = await readRecovery(syncId, stateHash)
  if (!copy) throw reject(404, 'no such recovery copy', 'not-found')
  await withLifecycleLock(async () => {
    try {
      await restoreRecovery(syncId, stateHash)
    } catch (e) {
      wrapError(e, 400)
    }
  })
  return { data: { ok: true, path: copy.path } }
}

// ---- test seams ----------------------------------------------------------

export function _setStoreFactory(fn) {
  storeFactory = fn
}

export function _setLocks(value) {
  locks = value
}

export function _resetService() {
  config = loadConfig()
  storeFactory = (cfg) => new S3Store(cfg)
  locks = (typeof navigator !== 'undefined' && navigator.locks) || null
  syncRunning = false
  lastRun = null
  lastCompleted = null
  lastTrigger = ''
}

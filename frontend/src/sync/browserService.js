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
import { createSyncScheduler, PERIODIC_EVERY_MS } from './scheduler.js'
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
// lastAttemptClassification is the internal scheduler outcome of the most
// recent attempt, derived from the REAL error/cycle BEFORE redaction so the
// scheduler never reverse-engineers a decision from the redacted LastError.
let lastAttemptClassification = null
let scheduler = null
let schedulerDeps = {}
let onSchedulerAttempt = null

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
    case 'invalid-repo':
      return 'repo-loss'
    case 'invalid-record':
    case 'record-syncid-mismatch':
    case 'conflict-error':
      return 'invalid-response'
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
// signal carries page-closure cancellation into the coordinator and the store.
async function runOnce(store, trigger, { signal } = {}) {
  if (syncRunning) {
    const locked = redactedFailure('locked')
    recordRun(locked, trigger)
    lastAttemptClassification = { kind: 'success', result: locked }
    return locked
  }
  syncRunning = true
  try {
    const res = await runSyncCycle(store, { locks, signal })
    const red = redactCycle(res)
    recordRun(red, trigger)
    lastAttemptClassification = classifyCycleResult(red, res.retryAfterSeconds || 0)
    return red
  } catch (e) {
    const red = redactedFailure(classifyLabel(e))
    recordRun(red, trigger)
    // The classification carries the redacted result so the public surface can
    // return it even when the attempt failed (permission/transport/rate-limit/
    // corrupt repo): the caller never sees data: undefined.
    lastAttemptClassification = { ...classifyError(e), result: red }
    return red
  } finally {
    syncRunning = false
  }
}

// runOnceUnderLock runs the cycle body when the caller ALREADY holds the vault
// Web Lock (enable must pin the connection and run the first cycle under ONE
// lock so a concurrent Reset/Disable can never interleave with it).
async function runOnceUnderLock(store, trigger, { signal } = {}) {
  if (syncRunning) {
    const locked = redactedFailure('locked')
    recordRun(locked, trigger)
    lastAttemptClassification = { kind: 'success', result: locked }
    return locked
  }
  syncRunning = true
  try {
    const res = await cycleUnderLock(store, { signal })
    const red = redactCycle(res)
    recordRun(red, trigger)
    lastAttemptClassification = classifyCycleResult(red, res.retryAfterSeconds || 0)
    return red
  } catch (e) {
    const red = redactedFailure(classifyLabel(e))
    recordRun(red, trigger)
    lastAttemptClassification = { ...classifyError(e), result: red }
    return red
  } finally {
    syncRunning = false
  }
}

// redactedFailure builds the stable redacted result for a failed attempt.
function redactedFailure(label) {
  return { Synced: false, Scanned: 0, Blocked: 0, Retry: 0, Conflicts: 0, SnapshotCommitted: false, LastError: label }
}

// classifyCycleResult maps a COMPLETED cycle (no escaped error) onto the
// scheduler outcome: deferred notes (Retry > 0) retry with the provider's
// largest Retry-After; a clean or blocked cycle takes the ordinary interval.
export function classifyCycleResult(red, retryAfterSeconds) {
  if (red && red.Retry > 0) return { kind: 'retryable', retryAfter: retryAfterSeconds || 0, result: red }
  return { kind: 'success', result: red }
}

// classifyError maps a REAL thrown error (a normalized StoreError or a coded
// coordinator error) onto the scheduler outcome BEFORE any redaction, so
// retryable-transport backs off, a corrupt repo.json pauses, and a cross-tab
// lock refusal takes the ordinary interval. Unknown errors pause (defensive):
// never a hot loop, never a silent ordinary wait for a condition that will not
// fix itself.
export function classifyError(e) {
  if (e && e.name === 'AbortError') return { kind: 'success' }
  if (e instanceof StoreError) {
    switch (e.kind) {
      case 'auth':
      case 'permission':
        return { kind: 'permanent', pauseReason: 'permission' }
      case 'quota':
        return { kind: 'permanent', pauseReason: 'quota' }
      case 'invalid-response':
        return { kind: 'permanent', pauseReason: 'invalid-response' }
      case 'unsupported-capability':
        return { kind: 'permanent', pauseReason: 'unsupported' }
      case 'incomplete-list':
        return { kind: 'permanent', pauseReason: 'incomplete-list' }
      case 'rate-limit':
        return { kind: 'retryable', retryAfter: e.retryAfterSeconds }
      case 'retryable-transport':
        return { kind: 'retryable' }
      default:
        return { kind: 'success' }
    }
  }
  const code = e && e.code
  switch (code) {
    case 'locked':
      return { kind: 'success' }
    case 'unsupported-lock':
      return { kind: 'permanent', pauseReason: 'unsupported' }
    case 'not-enabled':
      return { kind: 'disabled' }
    case 'profile-mismatch':
    case 'repository-mismatch':
    case 'snapshot-mismatch':
      return { kind: 'permanent', pauseReason: 'mismatch' }
    case 'repository-loss':
    case 'invalid-repo':
      return { kind: 'permanent', pauseReason: 'repo-lost' }
    case 'invalid-record':
    case 'record-syncid-mismatch':
    case 'conflict-error':
      return { kind: 'permanent', pauseReason: 'invalid-response' }
    case 'identity-corrupt':
    case 'connection-corrupt':
    case 'index-corrupt':
      return { kind: 'permanent', pauseReason: 'corrupt-state' }
    default:
      return { kind: 'permanent', pauseReason: 'error' }
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

// ---- automatic scheduling (R6.6) -----------------------------------------

// schedulerRun runs one AUTOMATIC attempt (startup/periodic/retry fired by the
// scheduler timer) and returns the classification. A vault that is not
// connected is 'disabled' (idle); a missing or UNREADABLE configuration is a
// permanent pause, never a thrown error the scheduler would have to survive.
async function schedulerRun(trigger, signal) {
  let conn
  try {
    conn = await loadConnection()
  } catch (_) {
    const result = redactedFailure('corrupt-state')
    recordRun(result, trigger)
    return { kind: 'permanent', pauseReason: 'corrupt-state', result }
  }
  if (!conn || !conn.connected) return { kind: 'disabled' }
  if (!config) {
    const result = redactedFailure('provider-config')
    recordRun(result, trigger)
    return { kind: 'permanent', pauseReason: 'provider-config', result }
  }
  let store
  try {
    store = makeStore(config)
  } catch (_) {
    const result = redactedFailure('provider-config')
    recordRun(result, trigger)
    return { kind: 'permanent', pauseReason: 'provider-config', result }
  }
  await runOnce(store, trigger, { signal })
  return lastAttemptClassification
}

// ensureScheduler builds the page-lifetime scheduler on first use. Its run is
// schedulerRun; its automatic attempts refresh the visible list and the open
// note through the registered hook (local direct UI refresh).
function ensureScheduler() {
  if (scheduler) return scheduler
  scheduler = createSyncScheduler({
    run: schedulerRun,
    onAttemptDone: () => {
      if (onSchedulerAttempt) onSchedulerAttempt()
    },
    ...schedulerDeps,
  })
  scheduler.start()
  return scheduler
}

export function startSyncScheduler() {
  ensureScheduler().start()
}

export async function stopSyncScheduler() {
  if (scheduler) {
    // Detach the reference SYNCHRONOUSLY before awaiting the old instance, so a
    // fast re-mount that calls startSyncScheduler() during the wait gets a NEW
    // scheduler and the old stop can never clear it afterwards.
    const old = scheduler
    scheduler = null
    // Clear the UI hook so an aborted in-flight attempt can never touch an
    // unmounted page after shutdown.
    onSchedulerAttempt = null
    await old.stop()
  }
}

export function setSchedulerOnAttempt(fn) {
  onSchedulerAttempt = fn
}

// resetSyncScheduler leaves the scheduler idle (Disable/Reset).
function resetSyncScheduler() {
  if (scheduler) scheduler.reset()
}

// schedulerStatusSnapshot returns the scheduler's redacted scheduling fields
// for the status payload, or idle defaults when no scheduler is running.
function schedulerStatusSnapshot() {
  if (!scheduler) {
    return { active: false, paused: false, pauseReason: '', nextRun: null, syncRunning: false }
  }
  return scheduler.status()
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
  const sched = schedulerStatusSnapshot()
  const data = {
    enabled: connected,
    connected,
    connection,
    experimental: true,
    noE2EE: true,
    recoveryCount,
    autoEnabled: connected && !sched.paused,
    autoIntervalSecs: PERIODIC_EVERY_MS / 1000,
    syncRunning: syncRunning || sched.syncRunning,
    autoPaused: sched.paused,
    pauseReason: sched.pauseReason,
    nextRun: sched.nextRun,
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
      let store
      try {
        store = makeStore(config)
      } catch (e) {
        throw reject(400, e && e.message ? e.message : 'sync configuration is invalid', 'invalid-config')
      }
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

      // A successful Enable requests one immediate run under the SAME lock,
      // then feeds the outcome to the page-lifetime scheduler so it arms the
      // ordinary interval (a successful Enable wakes the scheduler).
      await runOnceUnderLock(store, 'enable')
      ensureScheduler().noteAttempt(lastAttemptClassification)
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
  // A manual run goes through the SAME in-process single-flight as automatic
  // attempts (runNow coalesces onto an in-flight attempt), and its outcome is
  // fed to the scheduler so a genuinely synced run clears a permanent pause —
  // a locked/cancelled/blocked run never does.
  const classification = await ensureScheduler().runNow('manual')
  return { data: classification && classification.result }
}

export async function syncDisable() {
  await withLifecycleLock(async () => {
    const conn = await safeLoadConnection()
    if (conn) await setConnected(false)
  })
  lastRun = null
  lastCompleted = null
  lastTrigger = ''
  resetSyncScheduler()
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
  resetSyncScheduler()
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

// _setSchedulerDeps injects fake clock/timer/visibility deps for deterministic
// scheduler tests (fake timers). Any scheduler instance is rebuilt on the next
// ensureScheduler() call.
export function _setSchedulerDeps(deps) {
  schedulerDeps = deps || {}
  scheduler = null
}

export function _resetService() {
  config = loadConfig()
  storeFactory = (cfg) => new S3Store(cfg)
  locks = (typeof navigator !== 'undefined' && navigator.locks) || null
  syncRunning = false
  lastRun = null
  lastCompleted = null
  lastTrigger = ''
  lastAttemptClassification = null
  onSchedulerAttempt = null
  schedulerDeps = {}
  if (scheduler) {
    const old = scheduler
    scheduler = null
    old.stop()
  }
}

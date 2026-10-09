// UUID identity helpers for the browser sync port (R6.1). Ordinary notes use
// UUID v4; deterministic conflict copies use UUID v5 derived via Web Crypto
// SHA-1 from the fixed-role state hashes. The namespace and derivation are
// pinned by testdata/sync/state-hashes.json and must stay identical to the Go
// engine (internal/cloudsync/names.go + entity.go).
import { sha1, utf8Bytes } from './hash.js'

// ConflictNamespace is the fixed MemoDump namespace for deterministic conflict
// Sync IDs, pinned verbatim by testdata/sync/state-hashes.json.
export const ConflictNamespace = '7f139d22-a0f6-50fe-855c-c416516180f0'

export const CONTENT_HASH_RE = /^[0-9a-f]{64}$/

const UUID_V4_RE = /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/
const UUID_V5_RE = /^[0-9a-f]{8}-[0-9a-f]{4}-5[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/

// isUUIDv4 reports whether s is a syntactically valid version-4 UUID. Vault,
// Replica, Device, and Repository IDs must remain version-4 only.
export function isUUIDv4(s) {
  return UUID_V4_RE.test(s)
}

// isSyncID reports whether s is a valid note Sync ID: UUID v4 for ordinary
// notes or UUID v5 for deterministic conflict copies.
export function isSyncID(s) {
  return UUID_V4_RE.test(s) || UUID_V5_RE.test(s)
}

// newUUIDv4 returns a fresh random version-4 UUID for note, Vault, and Replica
// identity assignment. crypto.randomUUID is available in secure browser
// contexts and in Node; the getRandomValues fallback covers non-secure test
// environments.
export function newUUIDv4() {
  if (typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function') {
    return crypto.randomUUID()
  }
  const bytes = new Uint8Array(16)
  crypto.getRandomValues(bytes)
  bytes[6] = (bytes[6] & 0x0f) | 0x40
  bytes[8] = (bytes[8] & 0x3f) | 0x80
  return formatUUID(bytes)
}

// uuidBytes parses a canonical hyphenated UUID string into its 16 bytes.
function uuidBytes(s) {
  const hex = s.replace(/-/g, '')
  const bytes = new Uint8Array(16)
  for (let i = 0; i < 16; i++) bytes[i] = parseInt(hex.slice(i * 2, i * 2 + 2), 16)
  return bytes
}

function formatUUID(bytes) {
  const hex = Array.from(bytes, (b) => b.toString(16).padStart(2, '0')).join('')
  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20, 32)}`
}

// deriveConflictSyncID returns the deterministic UUID v5 conflict identity for a
// divergence on source Sync ID S, hashing the fixed-role state hashes in the
// order local, then remote. It rejects malformed input rather than silently
// deriving an unusable identity. It is async because Web Crypto SHA-1 is async;
// Go's version is synchronous but the derivation bytes are identical.
export async function deriveConflictSyncID(sourceSyncID, localStateHash, remoteStateHash) {
  if (!isSyncID(sourceSyncID)) {
    throw new Error(`conflict derivation: invalid source syncId "${sourceSyncID}"`)
  }
  for (const h of [localStateHash, remoteStateHash]) {
    if (!CONTENT_HASH_RE.test(h)) {
      throw new Error(`conflict derivation: invalid state hash "${h}"`)
    }
  }
  const ns = uuidBytes(ConflictNamespace)
  const name = utf8Bytes(`${sourceSyncID}\x00${localStateHash}\x00${remoteStateHash}`)
  const input = new Uint8Array(ns.length + name.length)
  input.set(ns, 0)
  input.set(name, ns.length)
  const digest = await sha1(input)
  digest[6] = (digest[6] & 0x0f) | 0x50
  digest[8] = (digest[8] & 0x3f) | 0x80
  return formatUUID(digest.subarray(0, 16))
}

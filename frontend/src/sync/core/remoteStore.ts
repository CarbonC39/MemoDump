// Capability-based provider boundary mirroring internal/cloudsync/remote_store.go.
//
// Create fails when the key exists; Replace fails when expectedVersion is stale;
// versions are opaque and never parsed; retries are safe and idempotent.

export type StoreErrorKind =
  | 'not-found'
  | 'precondition-failed'
  | 'auth'
  | 'permission'
  | 'rate-limit'
  | 'quota'
  | 'invalid-response'
  | 'unsupported-capability'
  | 'retryable-transport'

/** A normalized provider failure. Providers map their native errors onto this. */
export class StoreError extends Error {
  readonly kind: StoreErrorKind
  /** Provider Retry-After hint, when kind is 'rate-limit'. */
  readonly retryAfterMs?: number

  constructor(kind: StoreErrorKind, message: string, retryAfterMs?: number) {
    super(`cloudsync ${kind}: ${message}`)
    this.name = 'StoreError'
    this.kind = kind
    this.retryAfterMs = retryAfterMs
  }
}

/** Reports whether err is a StoreError of the given kind. */
export function isStoreError(err: unknown, kind: StoreErrorKind): boolean {
  return err instanceof StoreError && err.kind === kind
}

/** Optional provider features. The sync algorithm stays correct without them. */
export interface Capabilities {
  conditionalWrites: boolean
  pagedListing: boolean
  deltaCursor: boolean
}

/** Classifies one observed change. */
export type ChangeType = 'created' | 'updated' | 'deleted'

/** One object observed by List. version is '' for a deleted change. */
export interface Change {
  key: string
  type: ChangeType
  version: string
}

/**
 * One page of List output.
 *
 * nextCursor is the pagination continuation for the CURRENT scan: pass it back
 * to List to fetch the next page; '' means the scan is complete.
 *
 * syncCursor is the provider's delta position AFTER the scan represented by
 * this page. The engine persists the syncCursor of the final page and passes it
 * to the next List call to resume incrementally. The two cursors are separate
 * concepts: pagination continues one scan, the sync cursor resumes the next.
 */
export interface ChangePage {
  changes: Change[]
  nextCursor: string
  syncCursor: string
}

export interface RemoteStore {
  /** Probes the provider and returns its capabilities (may fail with auth,
   * permission, or transport errors). */
  test(): Promise<Capabilities>
  /** Returns the bytes and opaque version of a key. */
  read(key: string): Promise<{ bytes: Uint8Array; version: string }>
  /** Returns changes under prefix since the sync cursor (or a full baseline
   * when the cursor is empty or rejected), paged via nextCursor. */
  list(prefix: string, syncCursor?: string): Promise<ChangePage>
  /** Stores bytes under a key that must not already exist. */
  create(key: string, bytes: Uint8Array): Promise<{ version: string }>
  /** Stores bytes only when expectedVersion matches the current version. */
  replace(key: string, bytes: Uint8Array, expectedVersion: string): Promise<{ version: string }>
}

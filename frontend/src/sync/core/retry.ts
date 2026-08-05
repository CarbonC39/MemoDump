// Retry classification mirroring internal/cloudsync/retry.go. Errors that
// require user action (auth, permission, quota, schema, invalid response,
// unsupported capability) never spin; precondition failures are reconciled by
// the engine rather than blindly retried.

import { StoreError } from './remoteStore'

export interface RetryDecision {
  retryable: boolean
  backoffMs: number
}

/** Maps a store/provider error onto a retry decision. */
export function classifyRetry(err: unknown): RetryDecision {
  if (err instanceof StoreError) {
    switch (err.kind) {
      case 'rate-limit':
        return { retryable: true, backoffMs: err.retryAfterMs ?? 1000 }
      case 'retryable-transport':
        return { retryable: true, backoffMs: 1000 }
      default:
        return { retryable: false, backoffMs: 0 }
    }
  }
  return { retryable: false, backoffMs: 0 }
}

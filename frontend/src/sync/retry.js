// Retry and redacted-label classification for the browser sync port (R6.1).
// Matches internal/cloudsync/retry.go + the retry-classes.json fixture, and
// internal/syncservice/service.go ClassifyError for the stable, secret-free
// error labels a status surfaces.

const BASE_TRANSPORT_BACKOFF_SECONDS = 1

// classifyRetry maps a normalized store-error kind (the stable string labels
// from R6.3) onto { retryable, backoffSeconds }.
export function classifyRetry({ kind, retryAfterSeconds = 0 }) {
  if (kind === 'rate-limit') {
    return { retryable: true, backoffSeconds: retryAfterSeconds > 0 ? retryAfterSeconds : BASE_TRANSPORT_BACKOFF_SECONDS }
  }
  if (kind === 'retryable-transport') {
    return { retryable: true, backoffSeconds: BASE_TRANSPORT_BACKOFF_SECONDS }
  }
  return { retryable: false, backoffSeconds: 0 }
}

// classifyErrorLabel maps a normalized store-error kind (or a cancelled flag)
// onto a stable, secret-free redacted label so a status never leaks provider
// details. Matches syncservice.ClassifyError.
export function classifyErrorLabel({ kind = '', cancelled = false } = {}) {
  if (cancelled) return 'cancelled'
  switch (kind) {
    case 'auth':
    case 'permission':
      return 'permission'
    case 'quota':
      return 'quota'
    case 'rate-limit':
      return 'rate-limit'
    case 'invalid-response':
      return 'invalid-response'
    case 'unsupported-capability':
      return 'unsupported'
    case 'incomplete-list':
      return 'incomplete-list'
    default:
      return 'provider-error'
  }
}

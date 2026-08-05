package cloudsync

import (
	"errors"
	"time"
)

// RetryDecision says whether an operation may be retried and with what initial
// backoff. Errors that require user action (auth, permission, quota, schema,
// invalid response, unsupported capability) never spin; precondition failures
// are reconciled by the engine rather than blindly retried.
type RetryDecision struct {
	Retryable bool
	Backoff   time.Duration
}

// ClassifyRetry maps a store/provider error onto a retry decision.
func ClassifyRetry(err error) RetryDecision {
	var se *StoreError
	if !errors.As(err, &se) {
		return RetryDecision{Retryable: false}
	}
	switch se.Kind {
	case ErrRateLimit:
		b := time.Second
		if se.RetryAfter > 0 {
			b = se.RetryAfter
		}
		return RetryDecision{Retryable: true, Backoff: b}
	case ErrRetryableTransport:
		return RetryDecision{Retryable: true, Backoff: time.Second}
	default:
		return RetryDecision{Retryable: false}
	}
}

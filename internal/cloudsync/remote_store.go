package cloudsync

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// StoreErrorKind is the normalized classification of a remote-store failure.
// Providers map their native errors onto these so the engine never depends on
// provider-specific types.
type StoreErrorKind int

const (
	// ErrNotFound: the key does not exist.
	ErrNotFound StoreErrorKind = iota
	// ErrPreconditionFailed: create on an existing key or replace with a stale
	// expected version.
	ErrPreconditionFailed
	// ErrAuth: credentials missing, expired, or rejected.
	ErrAuth
	// ErrPermission: authenticated but not allowed.
	ErrPermission
	// ErrRateLimit: provider asked us to slow down.
	ErrRateLimit
	// ErrQuota: provider or local storage is full.
	ErrQuota
	// ErrInvalidResponse: the provider returned something unusable.
	ErrInvalidResponse
	// ErrUnsupportedCapability: the provider lacks a required feature.
	ErrUnsupportedCapability
	// ErrRetryableTransport: a transient network failure that may succeed on
	// retry.
	ErrRetryableTransport
)

func (k StoreErrorKind) String() string {
	switch k {
	case ErrNotFound:
		return "not-found"
	case ErrPreconditionFailed:
		return "precondition-failed"
	case ErrAuth:
		return "auth"
	case ErrPermission:
		return "permission"
	case ErrRateLimit:
		return "rate-limit"
	case ErrQuota:
		return "quota"
	case ErrInvalidResponse:
		return "invalid-response"
	case ErrUnsupportedCapability:
		return "unsupported-capability"
	case ErrRetryableTransport:
		return "retryable-transport"
	default:
		return "unknown"
	}
}

// StoreError carries a normalized kind plus an opaque human message and, for
// rate limits, the provider's Retry-After hint.
type StoreError struct {
	Kind       StoreErrorKind
	Message    string
	RetryAfter time.Duration
}

func (e *StoreError) Error() string {
	return fmt.Sprintf("cloudsync %s: %s", e.Kind.String(), e.Message)
}

// IsStoreError reports whether err is a StoreError of the given kind.
func IsStoreError(err error, kind StoreErrorKind) bool {
	var se *StoreError
	return errors.As(err, &se) && se.Kind == kind
}

// Capabilities describes which optional provider features are available. The
// sync algorithm stays correct without them.
type Capabilities struct {
	ConditionalWrites bool // create-if-absent and replace-if-version
	PagedListing      bool
	DeltaCursor       bool // incremental change cursors
}

// ChangeType classifies one observed change.
type ChangeType int

const (
	// ChangeCreated: a key appeared (included in a full baseline listing).
	ChangeCreated ChangeType = iota
	// ChangeUpdated: a key's version advanced.
	ChangeUpdated
	// ChangeDeleted: a key was physically removed (not a V1 tombstone, which is
	// an entity record with deleted=true). Physical removal observed through a
	// DELTA listing is repository damage, not a deletion signal: only a valid
	// tombstone propagates a deletion.
	ChangeDeleted
)

func (t ChangeType) String() string {
	switch t {
	case ChangeCreated:
		return "created"
	case ChangeUpdated:
		return "updated"
	case ChangeDeleted:
		return "deleted"
	default:
		return "unknown"
	}
}

// Change is one object observed by List. Version is "" for ChangeDeleted.
type Change struct {
	Key     string
	Type    ChangeType
	Version string
}

// ChangePage is one page of List output.
//
// NextCursor is the pagination continuation for the CURRENT scan: pass it back
// to List to fetch the next page; "" means the scan is complete.
//
// SyncCursor is the provider's delta position AFTER the scan represented by
// this page. The engine persists the SyncCursor of the final page and passes it
// to the next List call to resume incrementally. The two cursors are separate
// concepts: pagination continues one scan, the sync cursor resumes the next.
type ChangePage struct {
	Changes    []Change
	NextCursor string
	SyncCursor string
}

// RemoteStore is the capability-based provider boundary. Create fails when the
// key exists; Replace fails when expectedVersion is stale; versions are opaque
// and never parsed; retries are safe and idempotent. All methods honor ctx.
type RemoteStore interface {
	// Test probes the provider and returns its capabilities. It can fail with
	// auth, permission, or transport errors.
	Test(ctx context.Context) (Capabilities, error)
	// Read returns the bytes and opaque version of a key.
	Read(ctx context.Context, key string) ([]byte, string, error)
	// List returns changes under prefix since the sync cursor (or a full
	// baseline when the cursor is empty or rejected), paged via NextCursor.
	// A full listing must enumerate the complete key set. Only a delta listing
	// may report physical removal (ChangeDeleted), and a physically removed key
	// is repository damage, never a tombstone — the engine must not treat it as
	// a deletion.
	List(ctx context.Context, prefix, syncCursor string) (ChangePage, error)
	// Create stores bytes under a key that must not already exist.
	Create(ctx context.Context, key string, data []byte) (string, error)
	// Replace stores bytes only when expectedVersion matches the current
	// version.
	Replace(ctx context.Context, key string, data []byte, expectedVersion string) (string, error)
}

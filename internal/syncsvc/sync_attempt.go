package syncsvc

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	"memodump/internal/cloudsync"
	"memodump/internal/syncindex"
	"memodump/internal/syncservice"
	"memodump/internal/syncstate"
)

// attemptTrigger identifies what caused a sync attempt. It is process status,
// never persisted.
type attemptTrigger string

const (
	triggerManual   attemptTrigger = "manual"
	triggerEnable   attemptTrigger = "enable"
	triggerStartup  attemptTrigger = "startup"
	triggerPeriodic attemptTrigger = "periodic"
	triggerRetry    attemptTrigger = "retry"
)

// Sentinels for attempt-level refusals and classification. errSyncDisabled and
// errSyncLocked are also surfaced to the manual HTTP handler.
var (
	errSyncDisabled        = errors.New("sync is disabled; enable it first")
	errSyncLocked          = errors.New("sync is running in another process; try again")
	errSyncProviderChanged = errors.New("sync provider changed since enable; disable and reset sync to reconnect")
	errSyncRepoChanged     = errors.New("remote repository changed since enable; disable and reset sync to reconnect")
	errSyncRepoLost        = errors.New("remote repository lost though sync was established")
	errSyncRepoInvalid     = errors.New("remote repository descriptor is invalid; reset and re-enable sync")
	errSyncStateCorrupt    = errors.New("sync state is corrupt; reset and re-enable sync")
)

// syncRunning reports whether an attempt currently owns the run boundary. It is
// process status, guarded by an atomic for concurrent status reads.
var syncRunning atomic.Bool

// syncAttempt is the internal outcome of one attempt. Result is the redacted
// public shape; the remaining fields are scheduler classification and are
// NEVER serialized, logged with provider bodies, or exposed to callers.
type syncAttempt struct {
	Result    *syncservice.Result
	Completed time.Time
	Trigger   attemptTrigger
	// Internal classification (never exposed):
	disabled    bool          // connection not enabled: a refusal, not an attempt
	locked      bool          // cross-process replica-lock contention
	retryable   bool          // transient transport error or Result.Retry > 0
	permanent   bool          // auth/permission/quota/mismatch/config → pause automatic sync
	pauseReason string        // stable redacted label for a permanent pause
	retryAfter  time.Duration // provider Retry-After; honored when larger than backoff
	err         error         // raw typed error, only for classification
}

// runSyncAttempt executes one full attempt under the process mutex and the
// replica OS lock, records the redacted last-attempt (with trigger), and
// returns the internal classification. Every trigger (manual, enable, startup,
// periodic, retry) uses this one function; the HTTP handler is a thin adapter
// and the scheduler never calls an HTTP handler.
func runSyncAttempt(ctx context.Context, trigger attemptTrigger) *syncAttempt {
	syncOpMu.Lock()
	defer syncOpMu.Unlock()
	syncRunning.Store(true)
	defer syncRunning.Store(false)
	return runSyncAttemptLocked(ctx, trigger)
}

// runSyncAttemptLocked is the shared attempt body; syncOpMu is already held.
func runSyncAttemptLocked(ctx context.Context, trigger attemptTrigger) *syncAttempt {
	att := &syncAttempt{Trigger: trigger}
	var res *syncservice.Result
	err := withSyncLifecycleLock(func(vaultID, replicaID, stateRoot string, lock *syncstate.Lock) error {
		rec, err := syncReadConnected()
		if err != nil {
			return err
		}
		if rec == nil || !rec.Connected {
			att.disabled = true
			return nil
		}
		remote, err := syncProvider()
		if err != nil {
			att.permanent = true // a provider/config problem will not fix itself
			att.pauseReason = "provider-config"
			return err
		}
		profile := providerProfile(remote)
		if rec.Profile == "" || rec.Profile != profile {
			return errSyncProviderChanged
		}
		repoID, _, err := syncRepoIdentity(ctx, remote)
		if err != nil {
			return err
		}
		if rec.RepoID == "" || repoID != rec.RepoID {
			return errSyncRepoChanged
		}
		svc, err := buildSyncService(ctx, repoID, profile, remote, lock)
		if err != nil {
			return err
		}
		res, err = svc.Run(ctx)
		return err
	})
	att.Completed = time.Now()
	if errors.Is(err, syncindex.ErrNotEnabled) {
		att.disabled = true
		err = nil
	}
	if att.disabled {
		// A refusal, not an attempt. A manual run records it so a stale earlier
		// success never shows, but an automatic trigger (startup/periodic/retry/
		// enable) on a never-connected vault must not record a spurious failure —
		// the scheduler simply stays idle until an Enable.
		if trigger == triggerManual {
			recordLastRunError(errSyncDisabled, trigger)
		}
		return att
	}
	if err != nil {
		att.err = err
		att.Result = &syncservice.Result{Synced: false, LastError: syncservice.ClassifyError(err)}
		if errors.Is(err, errSyncLocked) {
			att.locked = true
			att.Result.LastError = "locked"
		}
		att.classifyError(err)
	} else {
		att.Result = res
		if rawErr := res.RawError(); rawErr != nil {
			att.err = rawErr
			att.classifyError(rawErr)
		} else {
			att.classifyResult(res)
		}
	}
	syncLastRunMu.Lock()
	syncLastRun.Result = att.Result.Redacted() // never retain a raw provider error in status
	syncLastRun.Completed = att.Completed
	syncLastRun.Trigger = string(trigger)
	syncLastRunMu.Unlock()
	return att
}

// classifyError maps a raw attempt error onto scheduler classification.
func (att *syncAttempt) classifyError(err error) {
	switch {
	case errors.Is(err, errSyncProviderChanged):
		att.permanent = true
		att.pauseReason = "provider-changed"
		return
	case errors.Is(err, errSyncRepoChanged):
		att.permanent = true
		att.pauseReason = "repo-changed"
		return
	case errors.Is(err, errSyncRepoLost), errors.Is(err, errSyncRepoInvalid):
		// A lost or corrupt repo.json is repository damage, not a transient
		// condition: it pauses automatic sync until an explicit reset.
		att.permanent = true
		att.pauseReason = "repo-lost"
		return
	case errors.Is(err, errSyncStateCorrupt), errors.Is(err, syncindex.ErrCorrupt),
		errors.Is(err, syncindex.ErrUnsupportedSchema):
		// Corrupt local sync state (connection record or index) pauses
		// automatic sync instead of retrying every ordinary interval.
		att.permanent = true
		att.pauseReason = "corrupt-state"
		return
	}
	var se *cloudsync.StoreError
	if !errors.As(err, &se) {
		return
	}
	switch se.Kind {
	case cloudsync.ErrAuth, cloudsync.ErrPermission:
		att.permanent = true
		att.pauseReason = "permission"
	case cloudsync.ErrQuota:
		att.permanent = true
		att.pauseReason = "quota"
	case cloudsync.ErrInvalidResponse:
		att.permanent = true
		att.pauseReason = "invalid-response"
	case cloudsync.ErrUnsupportedCapability:
		att.permanent = true
		att.pauseReason = "unsupported"
	case cloudsync.ErrIncompleteList:
		att.permanent = true
		att.pauseReason = "incomplete-list"
	case cloudsync.ErrRateLimit:
		att.retryable = true
		att.retryAfter = se.RetryAfter
	case cloudsync.ErrRetryableTransport:
		att.retryable = true
	}
}

// classifyResult maps a completed cycle onto scheduler classification: a
// completed cycle with deferred (Retry > 0) notes is retryable even when no
// fatal error escaped the coordinator; Blocked > 0 is not a transport retry.
func (att *syncAttempt) classifyResult(res *syncservice.Result) {
	if res == nil {
		return
	}
	if res.Retry > 0 {
		att.retryable = true
	}
}

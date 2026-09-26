// Package syncservice owns the manual-product surface of note sync: provider
// selection, the replica OS lock, serialized runs, cancellation between note
// boundaries, and a redacted status. It is the only production entry point for
// running a note cycle; the coordinator itself verifies the replica lock is
// still held before it runs.
package syncservice

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"memodump/internal/cloudsync"
	"memodump/internal/syncindex"
	"memodump/internal/syncrun"
	"memodump/internal/syncstate"
	"memodump/internal/vaultfs"
)

// Provider selects the remote store for a manual run. A provider must return a
// store whose errors carry no credentials; the service never forwards raw
// provider error bodies into its status.
type Provider func() (cloudsync.RemoteStore, error)

// Config binds a Service to one vault replica. Provider selects the remote
// store when Remote is nil; Remote binds a specific instance so identity
// resolution and the cycle share one provider. Lock is an optional pre-held
// replica OS lock: when set, Run uses it and does NOT close it (the caller owns
// it), so lifecycle validation and the cycle can share one lock critical
// section; when nil, Run acquires and closes the lock itself.
type Config struct {
	RepoRoot  string
	StateRoot string
	VaultID   string
	ReplicaID string
	RepoID    string
	Profile   string
	Provider  Provider
	Remote    cloudsync.RemoteStore
	Lock      *syncstate.Lock
}

// Result is the redacted outcome of one manual run: counts and a stable phase
// label only. It never carries credentials, provider URLs, remote content, or
// raw provider error text. rawErr is the original cycle/provider error retained
// in memory for scheduler classification (ClassifyRetry); it is unexported and
// therefore never serialized or exposed.
type Result struct {
	Synced            bool // a snapshot was committed and no fatal error occurred
	Scanned           int
	Blocked           int
	Retry             int
	Conflicts         int
	SnapshotCommitted bool
	LastError         string // stable, redacted label for the last failure, if any
	rawErr            error  // internal, never serialized: original error for classification
}

// RawError returns the original cycle/provider error retained for scheduler
// classification, or nil. It is never part of the serialized status and must
// not be surfaced to callers.
func (r *Result) RawError() error { return r.rawErr }

// Redacted returns a copy of the result without the retained raw error, for
// storing in the public status/attempt record.
func (r *Result) Redacted() Result {
	c := *r
	c.rawErr = nil
	return c
}

// Service owns provider selection, the replica OS lock, and serialized manual
// runs for one vault replica. A run holds the replica lock for its whole
// duration, so concurrent manual runs (in-process or across processes) never
// overlap. Only the replica state directory is locked: note edits remain
// available while a lock loser is refused.
type Service struct {
	cfg Config
	mu  sync.Mutex // serializes in-process runs
}

// New returns a service bound to one vault replica.
func New(cfg Config) *Service { return &Service{cfg: cfg} }

// Run acquires the replica lock (or uses a pre-held Config.Lock), runs one
// serialized note-only cycle, and releases the lock. A lock loser returns a
// Result with Synced=false and a "locked" label. Fatal cycle errors (auth,
// permission, listing failure, invalid remote data, local I/O) return a Result
// with Synced=false — never a "synced" report. Cancellation lands between note
// boundaries.
func (s *Service) Run(ctx context.Context) (*Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var lock *syncstate.Lock
	if s.cfg.Lock != nil {
		// The caller holds the lock and owns its release (for example the
		// lifecycle handler that validated the connection inside it).
		lock = s.cfg.Lock
	} else {
		var err error
		lock, err = syncstate.AcquireReplicaLock(s.cfg.StateRoot, s.cfg.VaultID, s.cfg.ReplicaID)
		if err != nil {
			if errors.Is(err, syncstate.ErrLocked) {
				return &Result{LastError: "locked"}, nil
			}
			return nil, fmt.Errorf("acquire replica lock: %w", err)
		}
		defer lock.Close()
	}

	remote := s.cfg.Remote
	if remote == nil {
		var perr error
		remote, perr = s.cfg.Provider()
		if perr != nil {
			return &Result{LastError: "provider", rawErr: perr}, nil
		}
	}
	res, err := s.runOnce(ctx, remote, lock)
	if err != nil {
		return &Result{LastError: ClassifyError(err), rawErr: err}, nil
	}
	// A cycle with blocked or retried notes (including a potentially incomplete
	// listing surfacing as remote damage) has not converged: it is never
	// reported as "synced".
	res.Synced = res.SnapshotCommitted && res.Blocked == 0 && res.Retry == 0
	if !res.Synced && res.LastError == "" {
		res.LastError = "incomplete"
	}
	return res, nil
}

// runOnce assembles the coordinator and runs one cycle while the lock is held.
func (s *Service) runOnce(ctx context.Context, remote cloudsync.RemoteStore, lock *syncstate.Lock) (*Result, error) {
	repo, err := vaultfs.New(s.cfg.RepoRoot)
	if err != nil {
		return nil, err
	}
	idx, err := syncindex.LoadNoteStore(s.cfg.RepoRoot)
	if err != nil {
		return nil, err
	}
	snaps, err := syncstate.NewSnapshotStoreV2(s.cfg.StateRoot, s.cfg.VaultID, s.cfg.ReplicaID)
	if err != nil {
		return nil, err
	}
	recovery, err := syncstate.NewRecoveryStore(s.cfg.StateRoot, s.cfg.VaultID, s.cfg.ReplicaID)
	if err != nil {
		return nil, err
	}
	co := syncrun.NewNoteCoordinator(repo, idx, snaps, recovery, remote, syncrun.NoteConfig{
		VaultID: s.cfg.VaultID, ReplicaID: s.cfg.ReplicaID, StateRoot: s.cfg.StateRoot,
		RepoID: s.cfg.RepoID, Profile: s.cfg.Profile, Lock: lock,
	})
	st, err := co.Run(ctx)
	if err != nil {
		return nil, err
	}
	return &Result{
		Scanned:           st.Scanned,
		Blocked:           st.Blocked,
		Retry:             st.Retry,
		Conflicts:         conflictCount(st.Decisions),
		SnapshotCommitted: st.SnapshotCommitted,
	}, nil
}

// conflictCount counts the compound preservation outcomes in a cycle's
// decisions.
func conflictCount(decisions []cloudsync.NoteDecision) int {
	n := 0
	for _, d := range decisions {
		switch d.Kind {
		case cloudsync.NotePreserveLocalThenPull, cloudsync.NotePreserveLocalThenDelete,
			cloudsync.NotePreserveRemoteThenTombstone:
			n++
		}
	}
	return n
}

// ClassifyError maps a cycle or provider error onto a stable, secret-free label
// so a status never leaks provider details.
func ClassifyError(err error) string {
	if errors.Is(err, context.Canceled) {
		return "cancelled"
	}
	var se *cloudsync.StoreError
	if errors.As(err, &se) {
		switch se.Kind {
		case cloudsync.ErrAuth, cloudsync.ErrPermission:
			return "permission"
		case cloudsync.ErrQuota:
			return "quota"
		case cloudsync.ErrRateLimit:
			return "rate-limit"
		case cloudsync.ErrInvalidResponse:
			return "invalid-response"
		case cloudsync.ErrUnsupportedCapability:
			return "unsupported"
		case cloudsync.ErrIncompleteList:
			return "incomplete-list"
		default:
			return "provider-error"
		}
	}
	return "error"
}

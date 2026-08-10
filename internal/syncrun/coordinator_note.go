// Note-only serialized coordinator. NoteCoordinator connects the pure per-note
// decisions (reconcile_note.go) to the real local atomic boundaries (vaultfs,
// the note-only index, the disposable v2 snapshot, and the recovery store)
// against a RemoteStore, for the non-destructive decision kinds first. It
// performs no folder, cursor, or multi-entity action-graph work. The caller
// must hold the replica's OS lock for the duration of Run.
package syncrun

import (
	"context"
	"errors"
	"fmt"

	"memodump/internal/cloudsync"
	"memodump/internal/syncindex"
	"memodump/internal/syncstate"
	"memodump/internal/vaultfs"
)

// NoteConfig wires the note coordinator's identity. RepoID and Profile are the
// snapshot identity. ScanOptions controls the vault scan; tests use its hooks
// to inject deterministic mid-scan mutations (the zero value scans normally).
// TestFault is a test-only crash seam: when set, it is called at named
// execution boundaries and a non-nil error aborts the cycle exactly as a crash
// would, so tests can verify restart safety. Lock is the replica OS lock the
// caller holds; Run verifies it is still held so a production coordinator can
// never run without verified lock ownership.
type NoteConfig struct {
	VaultID     string
	ReplicaID   string
	StateRoot   string
	RepoID      string
	Profile     string
	Lock        *syncstate.Lock
	ScanOptions vaultfs.ScanOptions
	TestFault   func(point string) error
}

// NoteStatus summarizes one completed or failed note-only cycle.
type NoteStatus struct {
	Scanned           int
	HasBaseline       bool
	Decisions         []cloudsync.NoteDecision
	Blocked           int
	Retry             int
	Deferred          int // compound/tombstone outcomes not yet wired (R2.3/R2.4)
	SnapshotCommitted bool
}

// NoteCoordinator runs one serialized cycle per replica. A second Run in the
// same process blocks until the first finishes; concurrent processes are
// excluded by the replica lock the caller must hold.
type NoteCoordinator struct {
	repo     *vaultfs.Repository
	idx      *syncindex.NoteStore
	snaps    *syncstate.SnapshotStoreV2
	recovery *syncstate.RecoveryStore
	remote   cloudsync.RemoteStore
	cfg      NoteConfig
}

// NewNoteCoordinator assembles a note coordinator. The replica lock must be
// held by the caller before the first Run.
func NewNoteCoordinator(repo *vaultfs.Repository, idx *syncindex.NoteStore, snaps *syncstate.SnapshotStoreV2, recovery *syncstate.RecoveryStore, remote cloudsync.RemoteStore, cfg NoteConfig) *NoteCoordinator {
	return &NoteCoordinator{repo: repo, idx: idx, snaps: snaps, recovery: recovery, remote: remote, cfg: cfg}
}

// Run executes one full note-only cycle: stable scan, identity assignment,
// snapshot load, complete remote listing, observation assembly, per-note
// decisions in sorted Sync ID order, non-destructive execution, and a single
// atomic snapshot replace. A precondition failure or uncertain response is
// re-read rather than guessed; an index save failure prevents the snapshot
// commit. No cursor is read or written.
func (c *NoteCoordinator) Run(ctx context.Context) (*NoteStatus, error) {
	st := &NoteStatus{}

	// A production coordinator must never run without verified replica-lock
	// ownership: the lock guards the index and snapshot against a concurrent
	// process, and it must be THIS replica's lock (vault, replica, and state
	// root), not another one's.
	if c.cfg.Lock == nil || !c.cfg.Lock.For(c.cfg.VaultID, c.cfg.ReplicaID, c.cfg.StateRoot) {
		return st, fmt.Errorf("coordinator requires this replica's OS lock")
	}
	if !c.cfg.Lock.Held() {
		return st, fmt.Errorf("coordinator requires the replica OS lock")
	}

	res, err := vaultfs.Scan(c.repo.Root(), c.cfg.ScanOptions)
	if err != nil {
		return st, fmt.Errorf("scan: %w", err)
	}
	st.Scanned = len(res.Notes)

	// Assign durable identity to definite new notes before any upload.
	if err := addNewNoteIDs(c.idx, res); err != nil {
		return st, fmt.Errorf("assign identity: %w", err)
	}

	// Load the disposable snapshot; a missing/corrupt snapshot means
	// conservative onboarding with no baseline.
	baselines, discard, err := c.loadBaselines()
	if err != nil {
		return st, err
	}
	switch discard {
	case syncstate.DiscardProfileMismatch, syncstate.DiscardRepositoryMismatch, syncstate.DiscardUnsupportedPrototype:
		return st, fmt.Errorf("snapshot %s: requires explicit reconnect or re-enable", discard)
	}
	st.HasBaseline = discard == syncstate.NoDiscard

	// A complete remote listing; an incomplete or failed listing stops the
	// cycle before any decision.
	keys, err := listNoteKeys(ctx, c.remote)
	if err != nil {
		return st, fmt.Errorf("list remote: %w", err)
	}

	ids := unionNoteIDs(c.idx, baselines, keys)
	locals := noteLocalObservations(res, c.idx, ids, c.readNote)
	remotes, err := noteRemoteObservations(ctx, c.remote, keys, ids)
	if err != nil {
		return st, err // fatal remote error: stop before any decision or execution
	}
	blocked := notePathConflicts(locals, remotes)

	plan := make([]cloudsync.NoteDecision, 0, len(ids))
	for _, id := range ids {
		var b *cloudsync.Baseline
		if bl, ok := baselines[id]; ok {
			b = &cloudsync.Baseline{ContentHash: bl.ContentHash, Deleted: bl.Deleted, RemoteVersion: bl.RemoteVersion}
		}
		plan = append(plan, cloudsync.DecideNote(locals[id], remotes[id], b, blocked[id]))
	}
	st.Decisions = plan
	for _, d := range plan {
		switch d.Kind {
		case cloudsync.NoteBlock:
			st.Blocked++
		case cloudsync.NoteRetry:
			st.Retry++
		}
	}

	// Persist the new identities before any upload; a failure stops the cycle.
	if err := c.idx.Save(); err != nil {
		return st, fmt.Errorf("save index before upload: %w", err)
	}
	if err := c.fault("pre-execute"); err != nil {
		return st, err
	}

	deferred, err := c.execute(ctx, plan, baselines)
	if err != nil {
		return st, err
	}
	st.Retry += deferred

	// Save consolidated index changes; a failure prevents the snapshot commit.
	if err := c.idx.Save(); err != nil {
		return st, fmt.Errorf("save index: %w", err)
	}
	if err := c.commitSnapshot(baselines); err != nil {
		return st, fmt.Errorf("commit snapshot: %w", err)
	}
	st.SnapshotCommitted = true
	return st, nil
}

// readNote wires the observation layer to vaultfs.ReadVerbatim.
func (c *NoteCoordinator) readNote(path string) (string, string, error) {
	return c.repo.ReadVerbatim(path)
}

// fault is the test-only crash seam: a non-nil TestFault returning an error at
// a named point aborts the cycle as a crash would.
func (c *NoteCoordinator) fault(point string) error {
	if c.cfg.TestFault == nil {
		return nil
	}
	return c.cfg.TestFault(point)
}

// SetScanOptions installs deterministic scan hooks (used by race tests that
// mutate the vault mid-scan). It replaces the coordinator's scan options.
func (c *NoteCoordinator) SetScanOptions(opts vaultfs.ScanOptions) {
	c.cfg.ScanOptions = opts
}

// SetTestFault installs a test-only crash/mutation seam: a non-nil function is
// called at every named execution boundary and its error aborts the cycle as a
// crash would. A test that mutates state and returns nil injects mid-cycle
// state without aborting.
func (c *NoteCoordinator) SetTestFault(fn func(point string) error) {
	c.cfg.TestFault = fn
}

// loadBaselines reads the disposable snapshot against the coordinator identity.
func (c *NoteCoordinator) loadBaselines() (map[string]syncstate.SnapshotEntity, syncstate.DiscardReason, error) {
	snap, reason, err := c.snaps.Load(syncstate.ExpectedIdentity{
		VaultID:         c.cfg.VaultID,
		ReplicaID:       c.cfg.ReplicaID,
		ProviderProfile: c.cfg.Profile,
		RepositoryID:    c.cfg.RepoID,
	})
	if err != nil {
		return nil, syncstate.NoDiscard, fmt.Errorf("load snapshot: %w", err)
	}
	base := make(map[string]syncstate.SnapshotEntity)
	if snap != nil {
		for id, e := range snap.Notes {
			base[id] = e
		}
	}
	return base, reason, nil
}

// execute applies the non-destructive decision kinds: noop, baseline
// establishment, conditional live upload, and local revision-CAS pull. The
// compound preservation and tombstone outcomes are deferred to later phases:
// their notes keep their previous baselines and are re-decided next cycle.
// Every deferred execution (an ok=false outcome: a retryable failure, a
// concurrent change, or a local revision race) is counted and surfaced as
// Retry, so a cycle that could not converge a note is never reported as synced.
func (c *NoteCoordinator) execute(ctx context.Context, plan []cloudsync.NoteDecision, baselines map[string]syncstate.SnapshotEntity) (int, error) {
	deferred := 0
	for _, d := range plan {
		// Cancellation lands between note boundaries, never mid-note.
		if err := ctx.Err(); err != nil {
			return deferred, err
		}
		switch d.Kind {
		case cloudsync.NoteNoop:
			// Nothing; a converged deletion drops its live index mapping so a
			// future file at that path becomes a fresh note.
			if d.IsConvergedDeletion() {
				if err := c.idx.RemoveNote(d.SyncID); err != nil {
					return deferred, err
				}
			}
		case cloudsync.NoteEstablishBaseline:
			baselines[d.SyncID] = syncstate.SnapshotEntity{
				ContentHash: d.ContentHash, Deleted: d.Deleted, RemoteVersion: d.Version,
			}
		case cloudsync.NotePushLive:
			version, ok, err := c.pushLive(ctx, d)
			if err != nil {
				return deferred, err
			}
			if ok {
				baselines[d.SyncID] = syncstate.SnapshotEntity{
					ContentHash: d.ContentHash, Deleted: false, RemoteVersion: version,
				}
			} else {
				deferred++
			}
		case cloudsync.NotePullLive:
			ok, err := c.pullLive(ctx, d)
			if err != nil {
				return deferred, err
			}
			if ok {
				baselines[d.SyncID] = syncstate.SnapshotEntity{
					ContentHash: d.ContentHash, Deleted: false, RemoteVersion: d.Version,
				}
			} else {
				deferred++
			}
		case cloudsync.NotePushTombstone:
			version, ok, err := c.replaceWithTombstone(ctx, d.SyncID, d.Path, d.Version)
			if err != nil {
				return deferred, err
			}
			if ok {
				baselines[d.SyncID] = syncstate.SnapshotEntity{
					ContentHash: d.ContentHash, Deleted: true, RemoteVersion: version,
				}
			} else {
				deferred++
			}
		case cloudsync.NoteApplyTombstone:
			ok, err := c.applyTombstone(ctx, d, baselines)
			if err != nil {
				return deferred, err
			}
			if !ok {
				deferred++
			}
		case cloudsync.NotePreserveLocalThenPull, cloudsync.NotePreserveLocalThenDelete,
			cloudsync.NotePreserveRemoteThenTombstone:
			n, err := c.executeConflict(ctx, d, baselines)
			if err != nil {
				return deferred, err
			}
			deferred += n
		}
	}
	return deferred, nil
}

// applyTombstone applies a pulled tombstone: write the durable recovery copy
// first, then delete the local note with the observed revision CAS. A recovery
// failure or a stale local revision leaves the note intact and its baseline
// unchanged; the returned bool reports whether the tombstone was applied.
func (c *NoteCoordinator) applyTombstone(ctx context.Context, d cloudsync.NoteDecision, baselines map[string]syncstate.SnapshotEntity) (bool, error) {
	path, ok := c.idx.PathByID(d.SyncID)
	if !ok {
		return false, fmt.Errorf("note %s not indexed", d.SyncID)
	}
	if err := c.writeRecovery(d.SyncID, path); err != nil {
		return false, fmt.Errorf("recovery for %s: %w", d.SyncID, err)
	}
	// The injected fault fires AFTER the local observation captured the note's
	// revision and BEFORE the delete's revision CAS, so a race test can write a
	// newer local edit here and exercise the CAS-failure path (rather than a
	// plain edit/delete conflict the decision would already see).
	if err := c.fault("tombstone:before-delete"); err != nil {
		return false, err
	}
	deleted, err := c.deleteLocalNote(d.SyncID, path, d.LocalRevision)
	if err != nil {
		return false, err
	}
	if deleted {
		// The baseline records the REMOTE tombstone's own content hash and
		// path (carried on the decision), which may differ from the local
		// path when a rename happened before the deletion elsewhere.
		baselines[d.SyncID] = syncstate.SnapshotEntity{
			ContentHash: d.ContentHash, Deleted: true, RemoteVersion: d.Version,
		}
	}
	return deleted, nil
}

// pushLive conditionally uploads the local note, creating only-if-absent or
// replacing at the current remote version. On any failure the key is re-read so
// the outcome is never guessed: an identical landed write (lost/uncertain
// response, idempotent collision) is established at the actual version; a stale
// CAS, a concurrent change, or a retryable transport error leaves the note for
// the next cycle. A fatal store error (auth, permission, quota, unsupported,
// invalid response) stops the cycle.
func (c *NoteCoordinator) pushLive(ctx context.Context, d cloudsync.NoteDecision) (string, bool, error) {
	rec := &cloudsync.NoteRecord{
		SchemaVersion: cloudsync.NoteSchemaVersion,
		SyncID:        d.SyncID,
		Path:          d.Path,
		Markdown:      d.Markdown,
	}
	data, err := rec.Serialize()
	if err != nil {
		return "", false, err
	}
	key := cloudsync.NoteKey(d.SyncID)
	var version string
	if d.Version == "" {
		version, err = c.remote.Create(ctx, key, data)
	} else {
		version, err = c.remote.Replace(ctx, key, data, d.Version)
	}
	if err == nil {
		return version, true, nil
	}
	var se *cloudsync.StoreError
	if !errors.As(err, &se) {
		return "", false, err
	}
	switch se.Kind {
	case cloudsync.ErrAuth, cloudsync.ErrPermission, cloudsync.ErrQuota,
		cloudsync.ErrUnsupportedCapability, cloudsync.ErrInvalidResponse:
		// Fatal: stop the cycle.
		return "", false, err
	}
	// A retryable transport error, a precondition failure, or a concurrently
	// removed key: re-read to learn whether the write landed — never guess. An
	// identical landed write (lost/uncertain response, idempotent collision) is
	// established at the actual version; anything else is left for the next
	// cycle, and a fatal error during the confirmation stops the cycle.
	return c.confirmWrite(ctx, key, d.SyncID, func(rec *cloudsync.NoteRecord) bool {
		return !rec.Deleted && rec.Path == d.Path && rec.Markdown == d.Markdown
	})
}

// confirmWrite re-reads a key after a conditional-write failure to learn the
// outcome without guessing. A record matching want is an idempotent success at
// the actual version. A retryable transport error or a now-missing key defers
// (the next cycle's full listing recreates or reports remote damage); a fatal
// store error, a malformed record, or a record whose embedded syncId does not
// match the key stops the cycle (spec §9). Any other landed state (a
// concurrent edit) defers to the next cycle.
func (c *NoteCoordinator) confirmWrite(ctx context.Context, key, wantSyncID string, want func(rec *cloudsync.NoteRecord) bool) (string, bool, error) {
	existing, version, rerr := c.remote.Read(ctx, key)
	if rerr != nil {
		if cloudsync.IsStoreError(rerr, cloudsync.ErrNotFound) {
			return "", false, nil // write never landed or key removed concurrently
		}
		if cloudsync.ClassifyRetry(rerr).Retryable {
			return "", false, nil
		}
		return "", false, fmt.Errorf("re-read %s: %w", key, rerr)
	}
	rec, perr := cloudsync.ParseNoteRecord(existing)
	if perr != nil {
		return "", false, fmt.Errorf("invalid remote record %s: %w", key, perr)
	}
	if rec.SyncID != wantSyncID {
		return "", false, fmt.Errorf("remote record %s declares syncId %q", key, rec.SyncID)
	}
	if want(rec) {
		return version, true, nil
	}
	return "", false, nil // a concurrent change; defer to the next cycle
}

// pullLive materializes the remote note locally with the observed local
// revision CAS (create-if-absent when the note is remote-only). An editor
// racing the pull wins: the local revision conflict preserves the new edit and
// the note is left for the next cycle. An in-app path change pulls the note at
// its new path in a crash-safe order: the old location is removed first (its
// observed revision CAS), then the Sync ID is re-mapped and that mapping is
// saved BEFORE the new file appears, so a crash at any point never leaves the
// new path unindexed (which would mint a second identity) and a racing edit of
// the old path aborts before any index or baseline change.
func (c *NoteCoordinator) pullLive(ctx context.Context, d cloudsync.NoteDecision) (bool, error) {
	indexedPath, indexed := c.idx.PathByID(d.SyncID)
	path := d.Path

	if !indexed {
		// Remote-only note: reserve the Sync ID/path in the index and persist
		// it BEFORE the file appears, so a crash between the write and the
		// end-of-cycle index save never leaves the new file unindexed (which
		// would mint a second identity and a permanent path conflict). If the
		// target is still claimed by a different Sync ID (a not-yet-cleaned
		// tombstone entry), defer so the stale claim can be released first —
		// never abort the whole cycle.
		if prev, ok := c.idx.IDByPath(path); ok && prev != d.SyncID {
			return false, nil
		}
		if err := c.idx.AddNote(d.SyncID, path); err != nil {
			return false, fmt.Errorf("index pulled note %s: %w", d.SyncID, err)
		}
		if err := c.idx.Save(); err != nil {
			return false, fmt.Errorf("save pulled note index: %w", err)
		}
		if err := c.fault("pull:remote:index-saved"); err != nil {
			return false, err
		}
		ok, err := c.applyOrVerify(path, d.Markdown, "")
		if err != nil {
			return false, err
		}
		if !ok {
			return false, nil
		}
		if err := c.fault("pull:remote:file-written"); err != nil {
			return false, err
		}
		return true, nil
	}

	if indexedPath == path {
		// Same-path pull: local revision-CAS replace.
		ok, err := c.applyOrVerify(path, d.Markdown, d.LocalRevision)
		if err != nil {
			return false, err
		}
		return ok, nil
	}

	// In-app path change. The old file is the note at its former location,
	// unchanged (else this would be a conflict); delete it with the observed
	// revision CAS FIRST so a racing edit keeps the old path and identity.
	// Before deleting anything, confirm the target path is not claimed by a
	// different Sync ID (a not-yet-cleaned tombstone entry): a stale claim
	// defers the pull rather than deleting the old file and then failing.
	if prev, ok := c.idx.IDByPath(path); ok && prev != d.SyncID {
		return false, nil
	}
	if err := c.repo.Delete(indexedPath, d.LocalRevision); err != nil {
		if errors.Is(err, vaultfs.ErrRevisionConflict) {
			return false, nil // racing edit preserved; nothing else changed
		}
		if !errors.Is(err, vaultfs.ErrNotFound) {
			return false, fmt.Errorf("remove old path %s: %w", indexedPath, err)
		}
	}
	if err := c.fault("pull:path:old-deleted"); err != nil {
		return false, err
	}
	// Re-map the Sync ID and persist the mapping before the new file appears.
	if err := c.idx.UpdatePath(d.SyncID, path); err != nil {
		return false, err
	}
	if err := c.idx.Save(); err != nil {
		return false, fmt.Errorf("save path change: %w", err)
	}
	if err := c.fault("pull:path:index-saved"); err != nil {
		return false, err
	}
	ok, err := c.applyOrVerify(path, d.Markdown, "")
	if err != nil {
		return false, err
	}
	if !ok {
		return false, nil
	}
	if err := c.fault("pull:path:new-written"); err != nil {
		return false, err
	}
	return true, nil
}

// applyOrVerify writes a note with the expected revision CAS ("" =
// create-if-absent). A file that already holds the exact requested content is
// an idempotent success; any other revision conflict is a raced local write
// that is left intact for the next cycle.
func (c *NoteCoordinator) applyOrVerify(path, markdown, expectedRevision string) (bool, error) {
	if _, err := c.repo.Apply(path, markdown, expectedRevision); err != nil {
		if !errors.Is(err, vaultfs.ErrRevisionConflict) {
			return false, fmt.Errorf("pull %s: %w", path, err)
		}
		md, _, rerr := c.repo.ReadVerbatim(path)
		if rerr != nil {
			if errors.Is(rerr, vaultfs.ErrNotFound) {
				return false, nil // raced away; next cycle re-decides
			}
			return false, rerr
		}
		if md != markdown {
			return false, nil // a raced local edit; preserve it
		}
	}
	return true, nil
}

// commitSnapshot atomically replaces the disposable snapshot once per cycle
// with the consolidated baselines.
func (c *NoteCoordinator) commitSnapshot(baselines map[string]syncstate.SnapshotEntity) error {
	snap := &syncstate.SnapshotV2{
		SchemaVersion:   syncstate.SnapshotV2SchemaVersion,
		VaultID:         c.cfg.VaultID,
		ReplicaID:       c.cfg.ReplicaID,
		RepositoryID:    c.cfg.RepoID,
		ProviderProfile: c.cfg.Profile,
		Notes:           make(map[string]syncstate.SnapshotEntity, len(baselines)),
	}
	for id, b := range baselines {
		snap.Notes[id] = b
	}
	return c.snaps.Replace(snap)
}

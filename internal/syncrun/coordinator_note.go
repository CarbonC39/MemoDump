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
// snapshot identity.
type NoteConfig struct {
	VaultID   string
	ReplicaID string
	RepoID    string
	Profile   string
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

	res, err := vaultfs.Scan(c.repo.Root(), vaultfs.ScanOptions{})
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
	remotes := noteRemoteObservations(ctx, c.remote, keys, ids)
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
		case cloudsync.NotePushTombstone, cloudsync.NoteApplyTombstone,
			cloudsync.NotePreserveLocalThenPull, cloudsync.NotePreserveLocalThenDelete,
			cloudsync.NotePreserveRemoteThenTombstone:
			st.Deferred++
		}
	}

	// Persist the new identities before any upload; a failure stops the cycle.
	if err := c.idx.Save(); err != nil {
		return st, fmt.Errorf("save index before upload: %w", err)
	}

	if err := c.execute(ctx, plan, baselines); err != nil {
		return st, err
	}

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
func (c *NoteCoordinator) execute(ctx context.Context, plan []cloudsync.NoteDecision, baselines map[string]syncstate.SnapshotEntity) error {
	for _, d := range plan {
		switch d.Kind {
		case cloudsync.NoteNoop:
			// Nothing; the baseline (if any) already matches.
		case cloudsync.NoteEstablishBaseline:
			baselines[d.SyncID] = syncstate.SnapshotEntity{
				ContentHash: d.ContentHash, Deleted: d.Deleted, RemoteVersion: d.Version,
			}
		case cloudsync.NotePushLive:
			version, ok, err := c.pushLive(ctx, d)
			if err != nil {
				return err
			}
			if ok {
				baselines[d.SyncID] = syncstate.SnapshotEntity{
					ContentHash: d.ContentHash, Deleted: false, RemoteVersion: version,
				}
			}
		case cloudsync.NotePullLive:
			ok, err := c.pullLive(ctx, d)
			if err != nil {
				return err
			}
			if ok {
				baselines[d.SyncID] = syncstate.SnapshotEntity{
					ContentHash: d.ContentHash, Deleted: false, RemoteVersion: d.Version,
				}
			}
		}
	}
	return nil
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
	// cycle.
	existing, actual, rerr := c.remote.Read(ctx, key)
	if rerr != nil {
		return "", false, nil
	}
	parsed, perr := cloudsync.ParseNoteRecord(existing)
	if perr == nil && parsed.SyncID == d.SyncID && !parsed.Deleted &&
		parsed.Path == d.Path && parsed.Markdown == d.Markdown {
		return actual, true, nil
	}
	return "", false, nil
}

// pullLive materializes the remote note locally with the observed local
// revision CAS (create-if-absent when the note is remote-only or lives at a
// different local path). An editor racing the pull wins: the local revision
// conflict preserves the new edit and the note is left for the next cycle. An
// in-app path change pulls the note at its new path, removes the old location
// (unchanged, or this would be a conflict), and re-maps the Sync ID. On success
// the note is indexed at its materialized path.
func (c *NoteCoordinator) pullLive(ctx context.Context, d cloudsync.NoteDecision) (bool, error) {
	indexedPath, indexed := c.idx.PathByID(d.SyncID)
	path := d.Path
	// When the local note lives at a different path (an in-app path change on
	// the other side), the target path is new locally: create-if-absent, then
	// remove the old file afterwards.
	expectedRevision := d.LocalRevision
	if indexed && indexedPath != path {
		expectedRevision = ""
	}
	if _, err := c.repo.Apply(path, d.Markdown, expectedRevision); err != nil {
		if errors.Is(err, vaultfs.ErrRevisionConflict) {
			return false, nil // a local edit raced the pull; next cycle re-decides
		}
		return false, fmt.Errorf("pull %s: %w", d.SyncID, err)
	}
	if indexed && indexedPath != path {
		// The old location is the same note at its former path; it was unchanged
		// (otherwise this would be a conflict), so remove it with the observed
		// revision CAS and re-map.
		if err := c.repo.Delete(indexedPath, d.LocalRevision); err != nil &&
			!errors.Is(err, vaultfs.ErrNotFound) && !errors.Is(err, vaultfs.ErrRevisionConflict) {
			return false, fmt.Errorf("remove old path %s: %w", indexedPath, err)
		}
		if err := c.idx.UpdatePath(d.SyncID, path); err != nil {
			return false, err
		}
	} else if !indexed {
		if err := c.idx.AddNote(d.SyncID, path); err != nil {
			return false, fmt.Errorf("index pulled note %s: %w", d.SyncID, err)
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

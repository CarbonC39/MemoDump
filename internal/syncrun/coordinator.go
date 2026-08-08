// Package syncrun implements the serialized filesystem coordinator: it connects
// the pure reconciliation decisions to real local atomic boundaries (vaultfs,
// the portable index, the disposable snapshot, and the recovery store) against
// a RemoteStore. It owns no filesystem logic itself — every materialization
// goes through vaultfs — and requires the replica's OS lock to be held by the
// caller while Run is active.
package syncrun

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"memodump/internal/cloudsync"
	"memodump/internal/syncindex"
	"memodump/internal/syncscan"
	"memodump/internal/syncstate"
	"memodump/internal/vaultfs"
)

// Config wires the coordinator's identity and attribution. RepoID and Profile
// are the snapshot identity; UpdatedBy/Clock feed entity attribution only.
type Config struct {
	VaultID   string
	ReplicaID string
	RepoID    string
	Profile   string
	UpdatedBy string
	Clock     func() int64 // UpdatedAt attribution; nil defaults to UnixMilli
}

func (c *Config) clockNow() int64 {
	if c.Clock != nil {
		return c.Clock()
	}
	return time.Now().UnixMilli()
}

// Status summarizes one completed or failed cycle.
type Status struct {
	Phase             string
	Scanned           int
	HasBaseline       bool
	Decisions         []cloudsync.Decision
	Blocked           int
	Retry             int
	Conflicts         int
	SnapshotCommitted bool
}

// Coordinator runs one serialized cycle per replica. A second Run in the same
// process blocks until the first finishes; concurrent processes are excluded by
// the replica lock the caller must hold.
type Coordinator struct {
	mu       sync.Mutex
	repo     *vaultfs.Repository
	idx      *syncindex.Store
	snaps    *syncstate.SnapshotStore
	recovery *syncstate.RecoveryStore
	remote   cloudsync.RemoteStore
	cfg      Config
}

// New assembles a coordinator. The replica lock must be held by the caller
// before the first Run.
func New(repo *vaultfs.Repository, idx *syncindex.Store, snaps *syncstate.SnapshotStore, recovery *syncstate.RecoveryStore, remote cloudsync.RemoteStore, cfg Config) *Coordinator {
	return &Coordinator{repo: repo, idx: idx, snaps: snaps, recovery: recovery, remote: remote, cfg: cfg}
}

// Run executes one full cycle: stable scan and reconcile, identity assignment,
// snapshot load, remote observation, pure decision planning, execution, and a
// single atomic snapshot replace. It is serialized within the process.
func (c *Coordinator) Run(ctx context.Context) (*Status, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	st := &Status{Phase: "scan"}

	res, err := vaultfs.Scan(c.repo.Root(), vaultfs.ScanOptions{})
	if err != nil {
		return st, fmt.Errorf("scan: %w", err)
	}
	rec, err := syncscan.Reconcile(res, c.idx)
	if err != nil {
		return st, fmt.Errorf("reconcile: %w", err)
	}
	st.Scanned = len(res.Notes) + len(res.Folders)

	// Assign durable identity to every unindexed observation before deciding; a
	// failed index write stops the cycle.
	if len(rec.New) > 0 {
		if err := syncscan.ApplyIdentity(rec, c.idx); err != nil {
			return st, fmt.Errorf("assign identity: %w", err)
		}
	}

	// Load the disposable snapshot; a missing/corrupt snapshot means
	// conservative onboarding with no baseline.
	baselines, discard, err := c.loadBaselines()
	if err != nil {
		return st, err
	}
	switch discard {
	case syncstate.DiscardProfileMismatch, syncstate.DiscardRepositoryMismatch:
		return st, fmt.Errorf("snapshot %s: requires explicit reconnect or repository decision", discard)
	}
	st.HasBaseline = discard == syncstate.NoDiscard

	// Observe the remote, then plan over the union of index, baseline, and
	// remote identities.
	remoteKeys, err := c.listRemoteKeys(ctx)
	if err != nil {
		return st, fmt.Errorf("list remote: %w", err)
	}
	ids := c.unionIDs(rec, baselines, remoteKeys)
	locals := c.localObservations(res, rec, ids)
	remotes, err := c.remoteObservations(ctx, ids, remoteKeys)
	if err != nil {
		return st, fmt.Errorf("read remote: %w", err)
	}

	plan := make([]cloudsync.Decision, 0, len(ids))
	for _, id := range ids {
		var b *cloudsync.Baseline
		if bl, ok := baselines[id]; ok {
			b = &cloudsync.Baseline{ContentHash: bl.ContentHash, Deleted: bl.Deleted, RemoteVersion: bl.RemoteVersion}
		}
		plan = append(plan, cloudsync.DecideEntity(locals[id], remotes[id], b, cloudsync.Annotations{}))
	}
	plan = cloudsync.DecideRepository(plan)
	st.Decisions = plan
	st.Phase = "execute"

	if err := c.execute(ctx, plan, locals, baselines); err != nil {
		return st, err
	}
	for _, d := range plan {
		switch d.Kind {
		case cloudsync.DecisionBlock:
			st.Blocked++
		case cloudsync.DecisionRetry:
			st.Retry++
		case cloudsync.DecisionCreateConflict:
			st.Conflicts++
		}
	}

	// Commit the snapshot exactly once. A failure reports an incomplete cycle;
	// it does not roll back the already-durable local, index, recovery, or
	// remote writes.
	st.Phase = "snapshot"
	if err := c.commitSnapshot(baselines); err != nil {
		return st, fmt.Errorf("commit snapshot: %w", err)
	}
	st.SnapshotCommitted = true
	st.Phase = "done"
	return st, nil
}

// loadBaselines reads the disposable snapshot against the coordinator identity.
func (c *Coordinator) loadBaselines() (map[string]syncstate.SnapshotEntity, syncstate.DiscardReason, error) {
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
		for id, e := range snap.Entities {
			base[id] = e
		}
	}
	return base, reason, nil
}

// listRemoteKeys enumerates the complete remote entity key set.
func (c *Coordinator) listRemoteKeys(ctx context.Context) (map[string]bool, error) {
	keys := make(map[string]bool)
	page, err := c.remote.List(ctx, cloudsync.EntityKeyPrefix, "")
	if err != nil {
		return nil, err
	}
	for {
		for _, ch := range page.Changes {
			keys[ch.Key] = true
		}
		if page.NextCursor == "" {
			break
		}
		page, err = c.remote.List(ctx, cloudsync.EntityKeyPrefix, page.NextCursor)
		if err != nil {
			return nil, err
		}
	}
	return keys, nil
}

// unionIDs is the sorted set of Sync IDs found in the index, the snapshot, or
// the remote listing.
func (c *Coordinator) unionIDs(rec *syncscan.Reconciliation, baselines map[string]syncstate.SnapshotEntity, remoteKeys map[string]bool) []string {
	set := make(map[string]bool)
	for id := range c.idx.Index.Entities {
		set[id] = true
	}
	for id := range baselines {
		set[id] = true
	}
	for key := range remoteKeys {
		id := strings.TrimSuffix(strings.TrimPrefix(key, cloudsync.EntityKeyPrefix), ".json")
		set[id] = true
	}
	ids := make([]string, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// localObservations derives the local observation for every union Sync ID from
// the reconciled scan, reading Markdown only for present notes.
func (c *Coordinator) localObservations(res *vaultfs.ScanResult, rec *syncscan.Reconciliation, ids []string) map[string]cloudsync.LocalObservation {
	byID := make(map[string]syncscan.Entity, len(rec.Entities))
	for _, e := range rec.Entities {
		byID[e.SyncID] = e
	}
	obs := make(map[string]cloudsync.LocalObservation, len(ids))
	for _, id := range ids {
		e, ok := byID[id]
		if !ok {
			// Present only in the snapshot or remote: no local presence.
			obs[id] = cloudsync.LocalObservation{SyncID: id, State: cloudsync.LocalAbsent}
			continue
		}
		switch e.State {
		case syncscan.StatePresent:
			ent, rev, err := c.readLocalEntity(id, e.Path, e.Kind)
			if err != nil {
				obs[id] = cloudsync.LocalObservation{SyncID: id, Kind: e.Kind, State: cloudsync.LocalUnknown}
				continue
			}
			obs[id] = cloudsync.LocalObservation{SyncID: id, Kind: e.Kind, State: cloudsync.LocalLive, Entity: ent, Revision: rev}
		case syncscan.StateMissing:
			obs[id] = cloudsync.LocalObservation{SyncID: id, Kind: e.Kind, State: cloudsync.LocalAbsent}
		default:
			obs[id] = cloudsync.LocalObservation{SyncID: id, Kind: e.Kind, State: cloudsync.LocalUnknown}
		}
	}
	return obs
}

// readLocalEntity builds the canonical local entity for an indexed path,
// reading the raw Markdown fresh for notes.
func (c *Coordinator) readLocalEntity(syncID, path, kind string) (*cloudsync.Entity, string, error) {
	md := ""
	rev := ""
	if kind == cloudsync.KindNote {
		var err error
		md, rev, err = c.repo.ReadVerbatim(path)
		if err != nil {
			return nil, "", err
		}
	}
	e := &cloudsync.Entity{
		SchemaVersion: cloudsync.SchemaVersion,
		SyncID:        syncID,
		Kind:          kind,
		ParentID:      c.folderIDAt(parentDirOf(path)),
		Name:          baseNameOf(path, kind),
		Markdown:      md,
		UpdatedBy:     c.cfg.UpdatedBy,
		UpdatedAt:     c.cfg.clockNow(),
	}
	e.ContentHash = e.ComputeContentHash()
	return e, rev, nil
}

// remoteObservations derives the remote observation for every union Sync ID:
// listed keys are read and parsed; the rest are physically missing.
func (c *Coordinator) remoteObservations(ctx context.Context, ids []string, keys map[string]bool) (map[string]cloudsync.RemoteObservation, error) {
	obs := make(map[string]cloudsync.RemoteObservation, len(ids))
	for _, id := range ids {
		key := cloudsync.EntityKeyPrefix + id + ".json"
		if !keys[key] {
			obs[id] = cloudsync.RemoteObservation{SyncID: id, State: cloudsync.RemoteMissing}
			continue
		}
		data, version, err := c.remote.Read(ctx, key)
		if err != nil {
			obs[id] = cloudsync.RemoteObservation{
				SyncID: id, State: cloudsync.RemoteInvalid,
				Retryable: cloudsync.IsStoreError(err, cloudsync.ErrRetryableTransport),
			}
			continue
		}
		ent, perr := cloudsync.ParseEntity(data)
		if perr != nil {
			obs[id] = cloudsync.RemoteObservation{SyncID: id, State: cloudsync.RemoteInvalid}
			continue
		}
		state := cloudsync.RemoteLive
		if ent.Deleted {
			state = cloudsync.RemoteTombstone
		}
		obs[id] = cloudsync.RemoteObservation{SyncID: id, Kind: ent.Kind, State: state, Entity: ent, Version: version}
	}
	return obs, nil
}

// folderIDAt returns the indexed folder Sync ID for a vault path.
func (c *Coordinator) folderIDAt(dir string) string {
	if dir == "" {
		return ""
	}
	for id, e := range c.idx.Index.Entities {
		if e.Kind == cloudsync.KindFolder && e.Path == dir {
			return id
		}
	}
	return ""
}

func parentDirOf(path string) string {
	if i := strings.LastIndex(path, "/"); i >= 0 {
		return path[:i]
	}
	return ""
}

func baseNameOf(path, kind string) string {
	name := path
	if i := strings.LastIndex(path, "/"); i >= 0 {
		name = path[i+1:]
	}
	if kind == cloudsync.KindNote {
		name = strings.TrimSuffix(name, ".md")
	}
	return name
}

// pathForEntity resolves the vault path of an entity from the indexed folder
// graph.
func (c *Coordinator) pathForEntity(e *cloudsync.Entity) string {
	dir := ""
	if e.ParentID != "" {
		if p, ok := c.idx.FindBySyncID(e.ParentID); ok {
			dir = p.Path
		}
	}
	name := e.Name
	if e.Kind == cloudsync.KindNote {
		name += ".md"
	}
	if dir == "" {
		return name
	}
	return dir + "/" + name
}

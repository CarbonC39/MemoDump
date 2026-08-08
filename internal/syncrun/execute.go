package syncrun

import (
	"context"
	"fmt"

	"memodump/internal/cloudsync"
	"memodump/internal/syncindex"
	"memodump/internal/syncstate"
	"memodump/internal/vaultfs"
)

// syncindexEntity builds an index entry for a materialized path.
func syncindexEntity(path string) syncindex.Entity {
	return syncindex.Entity{Kind: cloudsync.KindNote, Path: path}
}

// execute applies a planned decision list in the spec's order: reserve conflict
// identities, create and verify conflict copies, write recovery copies before
// any deletion, apply local mutations, apply remote writes with CAS, then save
// the consolidated index mutations (a failed index write stops the cycle before
// the snapshot commit).
func (c *Coordinator) execute(ctx context.Context, plan []cloudsync.Decision, locals map[string]cloudsync.LocalObservation, baselines map[string]syncstate.SnapshotEntity) error {
	if err := c.applyIndex(ctx, plan); err != nil {
		return err
	}
	if err := c.applyConflicts(ctx, plan); err != nil {
		return err
	}
	if err := c.applyRecovery(ctx, plan); err != nil {
		return err
	}
	if err := c.applyLocal(ctx, plan, locals); err != nil {
		return err
	}
	if err := c.applyRemote(ctx, plan); err != nil {
		return err
	}
	c.applyBaselines(plan, baselines)
	// Consolidate index mutations; Save is a no-op when nothing changed, and a
	// failed write prevents the snapshot commit.
	if err := c.idx.Save(); err != nil {
		return fmt.Errorf("save index: %w", err)
	}
	return nil
}

// applyIndex reserves deterministic conflict identities before the original is
// replaced or deleted.
func (c *Coordinator) applyIndex(ctx context.Context, plan []cloudsync.Decision) error {
	for _, d := range plan {
		if d.Kind != cloudsync.DecisionCreateConflict {
			continue
		}
		cid := d.Conflict.ConflictSyncID
		if _, ok := c.idx.FindBySyncID(cid); ok {
			continue // already reserved
		}
		path := c.pathForEntity(d.Conflict.ConflictEntity)
		if prev, ok := c.idx.FindByPath(path); ok && prev != cid {
			return fmt.Errorf("conflict path collision: %s already indexed as %s", path, prev)
		}
		if err := c.idx.AddEntity(cid, syncindexEntity(path)); err != nil {
			return fmt.Errorf("reserve conflict %s: %w", cid, err)
		}
	}
	return nil
}

// applyConflicts creates and VERIFIES the deterministic conflict copies locally
// and remotely before the original is modified. An existing object is
// idempotent success only when its Sync ID and canonical state match.
func (c *Coordinator) applyConflicts(ctx context.Context, plan []cloudsync.Decision) error {
	for _, d := range plan {
		if d.Kind != cloudsync.DecisionCreateConflict {
			continue
		}
		conf := d.Conflict
		path := c.pathForEntity(conf.ConflictEntity)
		if _, err := c.repo.Apply(path, conf.ConflictEntity.Markdown, ""); err != nil {
			if !isRevisionConflict(err) {
				return fmt.Errorf("create conflict %s: %w", conf.ConflictSyncID, err)
			}
			// Already present: idempotent only when the content matches.
			md, _, rerr := c.repo.ReadVerbatim(path)
			if rerr != nil {
				return fmt.Errorf("verify conflict %s: %w", conf.ConflictSyncID, rerr)
			}
			if md != conf.ConflictEntity.Markdown {
				return fmt.Errorf("conflict local collision at %s", path)
			}
		}
		data, err := conf.ConflictEntity.Serialize()
		if err != nil {
			return err
		}
		if err := c.createRemoteVerified(ctx, c.remote, cloudsync.EntityKeyPrefix+conf.ConflictSyncID+".json", data, conf.ConflictEntity); err != nil {
			return fmt.Errorf("create remote conflict %s: %w", conf.ConflictSyncID, err)
		}
	}
	return nil
}

// applyRecovery writes recovery copies before the deletions they guard; a
// failure aborts the cycle so no deletion proceeds without its copy.
func (c *Coordinator) applyRecovery(ctx context.Context, plan []cloudsync.Decision) error {
	for _, d := range plan {
		switch d.Kind {
		case cloudsync.DecisionApplyTombstone, cloudsync.DecisionCreateConflict:
			if d.Kind == cloudsync.DecisionCreateConflict && (d.Conflict == nil || !d.Conflict.OriginalTombstone) {
				continue
			}
			if err := c.writeRecovery(d.SyncID); err != nil {
				return fmt.Errorf("recovery for %s: %w", d.SyncID, err)
			}
		}
	}
	return nil
}

// writeRecovery copies the current local Markdown to the recovery area keyed by
// (Sync ID, state hash), so a subsequent delete never loses the content. Empty
// folders need no recovery body.
func (c *Coordinator) writeRecovery(syncID string) error {
	ent, ok := c.idx.FindBySyncID(syncID)
	if !ok || ent.Kind == cloudsync.KindFolder {
		return nil
	}
	md, _, err := c.repo.ReadVerbatim(ent.Path)
	if err != nil {
		if err == vaultfs.ErrNotFound {
			return nil // nothing present to recover
		}
		return err
	}
	e := &cloudsync.Entity{
		SchemaVersion: cloudsync.SchemaVersion, SyncID: syncID, Kind: ent.Kind,
		Name: baseNameOf(ent.Path, ent.Kind), Markdown: md,
	}
	e.ContentHash = e.ComputeContentHash()
	return c.recovery.Write(syncID, cloudsync.StateHash(e.ContentHash, false), md)
}

// applyLocal applies local mutations: pull, conflict-original handling, and
// apply-tombstone deletes (after recovery).
func (c *Coordinator) applyLocal(ctx context.Context, plan []cloudsync.Decision, locals map[string]cloudsync.LocalObservation) error {
	for _, d := range plan {
		switch d.Kind {
		case cloudsync.DecisionPullLive:
			if err := c.pullLive(d); err != nil {
				return err
			}
		case cloudsync.DecisionCreateConflict:
			conf := d.Conflict
			if conf == nil {
				continue
			}
			if conf.AcceptRemoteOriginal && conf.OriginalEntity != nil {
				if err := c.pullLiveTo(d.SyncID, conf.OriginalEntity, locals[d.SyncID].Revision); err != nil {
					return err
				}
			} else if conf.OriginalTombstone {
				if ent, ok := c.idx.FindBySyncID(d.SyncID); ok {
					if err := c.deleteLocal(d.SyncID, ent, locals[d.SyncID].Revision); err != nil {
						return err
					}
				}
			}
		case cloudsync.DecisionApplyTombstone:
			ent, ok := c.idx.FindBySyncID(d.SyncID)
			if !ok {
				continue
			}
			if err := c.deleteLocal(d.SyncID, ent, locals[d.SyncID].Revision); err != nil {
				return err
			}
		case cloudsync.DecisionNoop:
			if c.isConvergedDeletion(d) {
				if err := c.idx.RemoveEntity(d.SyncID); err != nil {
					return fmt.Errorf("clean tombstone mapping %s: %w", d.SyncID, err)
				}
			}
		}
	}
	return nil
}

// deleteLocal deletes a note (CAS) or a folder (child-first guaranteed by the
// planner), tolerating a stale local revision.
func (c *Coordinator) deleteLocal(syncID string, ent syncindex.Entity, expectedRevision string) error {
	if ent.Kind == cloudsync.KindFolder {
		if err := c.repo.DeleteFolder(ent.Path); err != nil && err != vaultfs.ErrNotFound {
			return fmt.Errorf("delete folder %s: %w", syncID, err)
		}
		return nil
	}
	if err := c.repo.Delete(ent.Path, expectedRevision); err != nil && !isRevisionConflict(err) && err != vaultfs.ErrNotFound {
		return fmt.Errorf("delete %s: %w", syncID, err)
	}
	return nil
}

// pullLive applies a pull decision onto the indexed path (or a new path when
// the entity is remote-only).
func (c *Coordinator) pullLive(d cloudsync.Decision) error {
	return c.pullLiveTo(d.SyncID, d.Entity, d.LocalRevision)
}

func (c *Coordinator) pullLiveTo(syncID string, entity *cloudsync.Entity, expectedRevision string) error {
	path, ok := c.indexPathFor(syncID, entity)
	if entity.Kind == cloudsync.KindFolder {
		if _, err := c.repo.CreateFolderIfAbsent(path); err != nil {
			return fmt.Errorf("pull folder %s: %w", syncID, err)
		}
	} else if _, err := c.repo.Apply(path, entity.Markdown, expectedRevision); err != nil {
		if isRevisionConflict(err) {
			// A user edit raced the pull: preserve the local state; the next
			// cycle re-reads and re-decides.
			return nil
		}
		return fmt.Errorf("pull %s: %w", syncID, err)
	}
	// The path mapping must reflect what was materialized.
	if ok {
		return c.idx.UpdatePath(syncID, path)
	}
	return c.idx.AddEntity(syncID, syncindex.Entity{Kind: entity.Kind, Path: path})
}

// indexPathFor returns the indexed path for a Sync ID, or derives one from the
// entity's folder graph when the entity is remote-only (ok=false).
func (c *Coordinator) indexPathFor(syncID string, e *cloudsync.Entity) (string, bool) {
	if ent, ok := c.idx.FindBySyncID(syncID); ok {
		return ent.Path, true
	}
	return c.pathForEntity(e), false
}

// applyRemote applies remote create/replace/tombstone writes with CAS and
// re-reads on uncertainty.
func (c *Coordinator) applyRemote(ctx context.Context, plan []cloudsync.Decision) error {
	for _, d := range plan {
		switch d.Kind {
		case cloudsync.DecisionPushLive:
			data, err := d.Entity.Serialize()
			if err != nil {
				return err
			}
			key := cloudsync.EntityKeyPrefix + d.SyncID + ".json"
			if d.Version == "" {
				if err := c.createRemoteVerified(ctx, c.remote, key, data, d.Entity); err != nil {
					return fmt.Errorf("push %s: %w", d.SyncID, err)
				}
			} else if err := c.replaceRemoteVerified(ctx, c.remote, key, data, d.Entity, d.Version); err != nil {
				return fmt.Errorf("push %s: %w", d.SyncID, err)
			}
		case cloudsync.DecisionPushTombstone:
			data, err := d.Entity.Serialize()
			if err != nil {
				return err
			}
			if err := c.replaceRemoteVerified(ctx, c.remote, cloudsync.EntityKeyPrefix+d.SyncID+".json", data, d.Entity, d.Version); err != nil {
				return fmt.Errorf("push tombstone %s: %w", d.SyncID, err)
			}
		case cloudsync.DecisionCreateConflict:
			conf := d.Conflict
			if conf == nil || !conf.OriginalTombstone || conf.OriginalVersion == "" || conf.OriginalTombstoneEntity == nil {
				continue
			}
			data, err := conf.OriginalTombstoneEntity.Serialize()
			if err != nil {
				return err
			}
			if err := c.replaceRemoteVerified(ctx, c.remote, cloudsync.EntityKeyPrefix+d.SyncID+".json", data, conf.OriginalTombstoneEntity, conf.OriginalVersion); err != nil {
				return fmt.Errorf("tombstone original %s: %w", d.SyncID, err)
			}
		}
	}
	return nil
}

// applyBaselines records the established baselines into the durable snapshot
// map (committed once at cycle end).
func (c *Coordinator) applyBaselines(plan []cloudsync.Decision, baselines map[string]syncstate.SnapshotEntity) {
	for _, d := range plan {
		if d.Kind != cloudsync.DecisionEstablishBaseline {
			continue
		}
		baselines[d.SyncID] = syncstate.SnapshotEntity{
			ContentHash: d.ContentHash, Deleted: d.Deleted, RemoteVersion: d.Version,
		}
	}
}

// commitSnapshot atomically replaces the disposable snapshot once per cycle.
func (c *Coordinator) commitSnapshot(baselines map[string]syncstate.SnapshotEntity) error {
	snap := &syncstate.Snapshot{
		SchemaVersion:   syncstate.SnapshotSchemaVersion,
		VaultID:         c.cfg.VaultID,
		ReplicaID:       c.cfg.ReplicaID,
		RepositoryID:    c.cfg.RepoID,
		ProviderProfile: c.cfg.Profile,
		Entities:        make(map[string]syncstate.SnapshotEntity, len(baselines)),
		Cursor:          "c1",
	}
	for id, b := range baselines {
		snap.Entities[id] = syncstate.SnapshotEntity{
			ContentHash: b.ContentHash, RemoteVersion: b.RemoteVersion, Deleted: b.Deleted,
		}
	}
	return c.snaps.Replace(snap)
}

// createRemoteVerified creates a key and re-reads to confirm the intended
// canonical state on any error (a collision, or a lost/uncertain response).
func (c *Coordinator) createRemoteVerified(ctx context.Context, remote cloudsync.RemoteStore, key string, data []byte, expected *cloudsync.Entity) error {
	if _, err := remote.Create(ctx, key, data); err == nil {
		return nil
	}
	existing, _, rerr := remote.Read(ctx, key)
	if rerr != nil {
		return rerr
	}
	parsed, perr := cloudsync.ParseEntity(existing)
	if perr != nil || parsed.SyncID != expected.SyncID ||
		parsed.ContentHash != expected.ContentHash || parsed.Deleted != expected.Deleted {
		return fmt.Errorf("remote create collision at %s", key)
	}
	return nil // idempotent success: identical canonical state already present
}

// replaceRemoteVerified replaces a key with CAS and re-reads on failure: a
// write that landed with the intended state is idempotent success; a stale
// precondition is left to the next cycle.
func (c *Coordinator) replaceRemoteVerified(ctx context.Context, remote cloudsync.RemoteStore, key string, data []byte, expected *cloudsync.Entity, version string) error {
	if _, err := remote.Replace(ctx, key, data, version); err == nil {
		return nil
	}
	existing, _, rerr := remote.Read(ctx, key)
	if rerr != nil {
		return rerr
	}
	parsed, perr := cloudsync.ParseEntity(existing)
	if perr == nil && parsed.SyncID == expected.SyncID &&
		parsed.ContentHash == expected.ContentHash && parsed.Deleted == expected.Deleted {
		return nil // uncertain write landed idempotently
	}
	return nil // stale CAS or divergence: the next cycle re-reads and re-decides
}

func (c *Coordinator) isConvergedDeletion(d cloudsync.Decision) bool {
	return d.Kind == cloudsync.DecisionNoop && d.Reason == "converged deletion"
}

func isRevisionConflict(err error) bool {
	return err == vaultfs.ErrRevisionConflict
}

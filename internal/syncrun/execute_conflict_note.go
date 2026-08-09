// Conflict preservation executor (R2.3). The three compound outcomes are
// executed in the fixed order the spec prescribes: derive the conflict identity
// (done in the pure decision), reserve and durably save it in the index, create
// and verify the local conflict note, create and verify the remote conflict
// record, and only then act on the original. Every step is idempotent, so an
// injected stop between boundaries reuses the same conflict note on restart.
package syncrun

import (
	"context"
	"errors"
	"fmt"

	"memodump/internal/cloudsync"
	"memodump/internal/syncstate"
	"memodump/internal/vaultfs"
)

// executeConflict applies one compound preservation decision in the fixed
// spec-§8 order and records baselines only for final known-equal states.
func (c *NoteCoordinator) executeConflict(ctx context.Context, d cloudsync.NoteDecision, baselines map[string]syncstate.SnapshotEntity) error {
	conf := d.Conflict
	if conf == nil || conf.ConflictSyncID == "" || conf.ConflictPath == "" {
		return fmt.Errorf("note %s: missing conflict plan", d.SyncID)
	}

	// 1. Reserve the conflict identity/path in the index before the original
	// changes. Replay-safe: the same ID at the same path is an idempotent no-op.
	if err := c.reserveConflict(conf); err != nil {
		return fmt.Errorf("reserve conflict %s: %w", conf.ConflictSyncID, err)
	}
	if err := c.fault("conflict:reserved"); err != nil {
		return err
	}
	// The reservation must be durable before any conflict note is created.
	if err := c.idx.Save(); err != nil {
		return fmt.Errorf("save conflict reservation: %w", err)
	}
	if err := c.fault("conflict:saved"); err != nil {
		return err
	}

	// 2. Create/verify the local conflict note (create-if-absent).
	if err := c.createLocalConflict(conf); err != nil {
		return fmt.Errorf("create local conflict %s: %w", conf.ConflictSyncID, err)
	}
	if err := c.fault("conflict:local"); err != nil {
		return err
	}

	// 3. Create/verify the remote conflict record.
	conflictVersion, err := c.createRemoteConflict(ctx, conf)
	if err != nil {
		return fmt.Errorf("create remote conflict %s: %w", conf.ConflictSyncID, err)
	}
	if err := c.fault("conflict:remote"); err != nil {
		return err
	}

	// 4. Only now act on the original.
	switch d.Kind {
	case cloudsync.NotePreserveLocalThenPull:
		if ok, err := c.pullLive(ctx, d); err != nil {
			return err
		} else if ok {
			baselines[d.SyncID] = syncstate.SnapshotEntity{
				ContentHash: d.ContentHash, Deleted: false, RemoteVersion: d.Version,
			}
		}
	case cloudsync.NotePreserveLocalThenDelete:
		path, ok := c.idx.PathByID(d.SyncID)
		if !ok {
			return fmt.Errorf("note %s not indexed", d.SyncID)
		}
		// Recovery is durable before any local delete.
		if err := c.writeRecovery(d.SyncID, path); err != nil {
			return fmt.Errorf("recovery for %s: %w", d.SyncID, err)
		}
		deleted, err := c.deleteLocalNote(d.SyncID, path, d.LocalRevision)
		if err != nil {
			return err
		}
		if deleted {
			// The baseline records the remote tombstone's carried content hash.
			baselines[d.SyncID] = syncstate.SnapshotEntity{
				ContentHash: d.ContentHash, Deleted: true, RemoteVersion: d.Version,
			}
		}
	case cloudsync.NotePreserveRemoteThenTombstone:
		version, ok, err := c.replaceWithTombstone(ctx, d.SyncID, d.Path, d.Conflict.OriginalVersion)
		if err != nil {
			return err
		}
		if ok {
			baselines[d.SyncID] = syncstate.SnapshotEntity{
				ContentHash: d.ContentHash, Deleted: true, RemoteVersion: version,
			}
		}
	}
	if err := c.fault("conflict:original"); err != nil {
		return err
	}

	// 5. The conflict note is now known equal locally and remotely.
	baselines[conf.ConflictSyncID] = syncstate.SnapshotEntity{
		ContentHash:   noteRecordHash(conf.ConflictSyncID, conf.ConflictPath, conf.ConflictMarkdown, false),
		Deleted:       false,
		RemoteVersion: conflictVersion,
	}
	return nil
}

// reserveConflict records the conflict identity in the index. It is an
// idempotent no-op when the same ID already maps to the same path; a different
// mapping or a path owned by another ID is a block.
func (c *NoteCoordinator) reserveConflict(conf *cloudsync.NoteConflictInfo) error {
	if existing, ok := c.idx.PathByID(conf.ConflictSyncID); ok {
		if existing == conf.ConflictPath {
			return nil
		}
		return fmt.Errorf("conflict %s already reserved at %q, not %q", conf.ConflictSyncID, existing, conf.ConflictPath)
	}
	if prev, ok := c.idx.IDByPath(conf.ConflictPath); ok && prev != conf.ConflictSyncID {
		return fmt.Errorf("conflict path %q already indexed as %s", conf.ConflictPath, prev)
	}
	return c.idx.AddNote(conf.ConflictSyncID, conf.ConflictPath)
}

// createLocalConflict writes the conflict note create-if-absent and verifies an
// existing copy is idempotent-identical.
func (c *NoteCoordinator) createLocalConflict(conf *cloudsync.NoteConflictInfo) error {
	if _, err := c.repo.Apply(conf.ConflictPath, conf.ConflictMarkdown, ""); err != nil {
		if !errors.Is(err, vaultfs.ErrRevisionConflict) {
			return err
		}
		md, _, rerr := c.repo.ReadVerbatim(conf.ConflictPath)
		if rerr != nil {
			return rerr
		}
		if md != conf.ConflictMarkdown {
			return fmt.Errorf("conflict local collision at %s", conf.ConflictPath)
		}
	}
	return nil
}

// createRemoteConflict creates the conflict record only-if-absent and verifies
// an existing record is idempotent-identical (same ID and canonical state).
func (c *NoteCoordinator) createRemoteConflict(ctx context.Context, conf *cloudsync.NoteConflictInfo) (string, error) {
	rec := &cloudsync.NoteRecord{
		SchemaVersion: cloudsync.NoteSchemaVersion, SyncID: conf.ConflictSyncID,
		Path: conf.ConflictPath, Markdown: conf.ConflictMarkdown,
	}
	data, err := rec.Serialize()
	if err != nil {
		return "", err
	}
	key := cloudsync.NoteKey(conf.ConflictSyncID)
	if version, err := c.remote.Create(ctx, key, data); err == nil {
		return version, nil
	}
	existing, version, rerr := c.remote.Read(ctx, key)
	if rerr != nil {
		return "", rerr
	}
	parsed, perr := cloudsync.ParseNoteRecord(existing)
	if perr != nil || parsed.SyncID != conf.ConflictSyncID || parsed.Path != conf.ConflictPath ||
		parsed.Markdown != conf.ConflictMarkdown || parsed.Deleted {
		return "", fmt.Errorf("remote conflict collision at %s", key)
	}
	return version, nil // idempotent success
}

// deleteLocalNote deletes a note with the observed revision CAS. A stale
// revision (an editor raced the delete) leaves the note intact and reports
// deleted=false; the next cycle re-decides.
func (c *NoteCoordinator) deleteLocalNote(syncID, path, expectedRevision string) (bool, error) {
	if err := c.repo.Delete(path, expectedRevision); err != nil {
		if errors.Is(err, vaultfs.ErrNotFound) {
			return true, nil // already gone
		}
		if errors.Is(err, vaultfs.ErrRevisionConflict) {
			return false, nil
		}
		return false, fmt.Errorf("delete %s: %w", syncID, err)
	}
	return true, nil
}

// Tombstone and recovery executor (R2.4). A local deletion uploads a
// conditional remote tombstone; a pulled tombstone writes a durable recovery
// copy before deleting the local note with a re-validated revision CAS. A
// recovery failure or a local CAS failure leaves the note intact and its
// baseline unchanged. Deletion is single-note only — never a recursive folder
// removal; empty parent directories may remain.
package syncrun

import (
	"context"
	"errors"

	"memodump/internal/cloudsync"
	"memodump/internal/vaultfs"
)

// replaceWithTombstone replaces a remote live note with a tombstone using the
// current version CAS, re-reading to learn the true outcome. A landed tombstone
// (idempotent) is established at the actual version; a stale CAS or concurrent
// change is left for the next cycle; a fatal store error stops the cycle.
func (c *NoteCoordinator) replaceWithTombstone(ctx context.Context, syncID, path, expectedVersion string) (string, bool, error) {
	tomb := &cloudsync.NoteRecord{
		SchemaVersion: cloudsync.NoteSchemaVersion, SyncID: syncID, Path: path, Deleted: true,
	}
	data, err := tomb.Serialize()
	if err != nil {
		return "", false, err
	}
	key := cloudsync.NoteKey(syncID)
	version, err := c.remote.Replace(ctx, key, data, expectedVersion)
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
		return "", false, err
	}
	// Re-read to learn the true outcome; a fatal error during the confirmation
	// stops the cycle.
	return c.confirmWrite(ctx, key, syncID, func(rec *cloudsync.NoteRecord) bool {
		return rec.Deleted && rec.Path == path
	})
}

// noteRecordHash is the canonical NoteRecord content hash for a live record.
func noteRecordHash(syncID, path, markdown string, deleted bool) string {
	return (&cloudsync.NoteRecord{
		SchemaVersion: cloudsync.NoteSchemaVersion, SyncID: syncID, Path: path,
		Markdown: markdown, Deleted: deleted,
	}).ComputeContentHash()
}

// writeRecovery copies the current local Markdown for a note to the recovery
// area keyed by (Sync ID, local state hash), atomically and idempotently,
// BEFORE a delete. A failure prevents the delete. A note already gone has
// nothing to recover.
func (c *NoteCoordinator) writeRecovery(syncID, path string) error {
	md, _, err := c.repo.ReadVerbatim(path)
	if err != nil {
		if errors.Is(err, vaultfs.ErrNotFound) {
			return nil
		}
		return err
	}
	hash := noteRecordHash(syncID, path, cloudsync.NormalizeMarkdown(md), false)
	return c.recovery.Write(syncID, cloudsync.StateHash(hash, false), md)
}

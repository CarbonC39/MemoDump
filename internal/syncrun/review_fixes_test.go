package syncrun

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"memodump/internal/cloudsync"
	"memodump/internal/syncstate"
)

// --- P1-1: cross-path pull ---

// TestNotePathChangeRacingEditKeepsOldPathAndIdentity: when the old path's
// delete CAS fails (a racing edit after the observation), the pull must not
// update the index or commit a baseline; the edit keeps the old path and
// identity, and converges as a conflict.
func TestNotePathChangeRacingEditKeepsOldPathAndIdentity(t *testing.T) {
	ctx := context.Background()
	remote := cloudsync.NewMemoryStore()
	a := newNoteRep(t, t.TempDir(), t.TempDir(), noteRepA, remote)
	b := newNoteRep(t, t.TempDir(), t.TempDir(), noteRepB, remote)
	writeFiles(t, a.root, map[string]string{"a.md": "# base\n"})
	converge(ctx, t, a, b)
	idA := mustNoteID(t, a, "a.md")

	// A renames in-app to b.md and pushes.
	if err := os.WriteFile(filepath.Join(a.root, "b.md"), []byte("# base\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(a.root, "a.md")); err != nil {
		t.Fatal(err)
	}
	if err := a.idx.UpdatePath(idA, "b.md"); err != nil {
		t.Fatal(err)
	}
	a.runQuiescent(t, ctx)

	// A race rewrites B's a.md AFTER the observation read it, so the
	// path-change pull's old-path delete CAS must fail.
	var once sync.Once
	b.co.cfg.TestFault = func(point string) error {
		if point == "pre-execute" {
			once.Do(func() { _ = os.WriteFile(filepath.Join(b.root, "a.md"), []byte("# racing edit\n"), 0644) })
		}
		return nil
	}
	if _, err := b.co.Run(ctx); err != nil {
		t.Fatal(err)
	}
	// The CAS failure left everything untouched: the note keeps the old path,
	// identity, and edit; nothing new was created.
	if p, _ := b.idx.PathByID(idA); p != "a.md" {
		t.Fatalf("index moved to %q despite the CAS failure", p)
	}
	if !b.noteExists("a.md") || b.noteBody("a.md") != "# racing edit\n" {
		t.Fatal("racing edit lost or its path changed")
	}
	if b.noteExists("b.md") {
		t.Fatal("new path created despite the old-path CAS failure")
	}

	// Convergence preserves the edit as a conflict and moves the original.
	b.co.cfg.TestFault = nil
	b.runQuiescent(t, ctx)
	if p, _ := b.idx.PathByID(idA); p != "b.md" {
		t.Fatalf("original converged at %q, want b.md", p)
	}
	if !b.noteExists("b.md") || b.noteBody("b.md") != "# base\n" {
		t.Fatal("original lost the base content")
	}
	found := false
	for _, p := range conflictNotes(t, b.root) {
		if b.noteBody(p) == "# racing edit\n" {
			found = true
		}
	}
	if !found {
		t.Fatal("racing edit was lost: no conflict note carries it")
	}
}

// TestNotePathChangeCrashRestartNoDuplicate: a crash at any point of the
// cross-path pull must restart without a second identity for either path and
// without losing the note's content. After the new file is written the pull is
// already cleanly indexed (the index mapping is persisted before the new file
// appears), so that restart converges at the new path.
func TestNotePathChangeCrashRestartNoDuplicate(t *testing.T) {
	for _, point := range []string{"pull:path:old-deleted", "pull:path:index-saved", "pull:path:new-written"} {
		t.Run(point, func(t *testing.T) {
			ctx := context.Background()
			remote := cloudsync.NewMemoryStore()
			a := newNoteRep(t, t.TempDir(), t.TempDir(), noteRepA, remote)
			b := newNoteRep(t, t.TempDir(), t.TempDir(), noteRepB, remote)
			writeFiles(t, a.root, map[string]string{"a.md": "# base\n"})
			converge(ctx, t, a, b)
			idA := mustNoteID(t, a, "a.md")
			if err := os.WriteFile(filepath.Join(a.root, "b.md"), []byte("# base\n"), 0644); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(filepath.Join(a.root, "a.md")); err != nil {
				t.Fatal(err)
			}
			if err := a.idx.UpdatePath(idA, "b.md"); err != nil {
				t.Fatal(err)
			}
			a.runQuiescent(t, ctx)

			b.co.cfg.TestFault = func(p string) error {
				if p == point {
					return fmt.Errorf("injected crash at %s", p)
				}
				return nil
			}
			if _, err := b.co.Run(ctx); err == nil {
				t.Fatalf("cycle should crash at %s", point)
			}
			b.co.cfg.TestFault = nil
			if err := b.idx.Reload(); err != nil { // restart from durable state
				t.Fatal(err)
			}
			b.runQuiescent(t, ctx)

			// No portable path holds two identities: the note either converged
			// cleanly at b.md or was preserved conservatively as a conflict, but
			// no path is shared by two Sync IDs.
			if blocked := notePathConflicts(noteLocalMap(b), nil); len(blocked) != 0 {
				t.Fatalf("%s: a portable path conflict remains: %v", point, blocked)
			}
			if !b.noteExists("b.md") && len(conflictNotes(t, b.root)) == 0 {
				t.Fatalf("%s: the note content was lost", point)
			}
		})
	}
}

// noteLocalMap rebuilds the local observation map from B's index for the
// path-conflict check.
func noteLocalMap(r *noteRep) map[string]cloudsync.NoteLocalObservation {
	out := make(map[string]cloudsync.NoteLocalObservation)
	for _, id := range r.idx.SortedIDs() {
		path, _ := r.idx.PathByID(id)
		out[id] = cloudsync.NoteLocalObservation{SyncID: id, State: cloudsync.LocalLive, Path: path}
	}
	return out
}

// --- P1-2 / P1-3: fatal remote errors stop the cycle ---

// TestNoteFatalRemoteReadStopsCycle: a fatal remote read error exits the cycle
// before any execution, so no other note syncs and no snapshot is committed.
func TestNoteFatalRemoteReadStopsCycle(t *testing.T) {
	ctx := context.Background()
	remote := cloudsync.NewMemoryStore()
	a := newNoteRep(t, t.TempDir(), t.TempDir(), noteRepA, remote)
	writeFiles(t, a.root, map[string]string{"a.md": "# A\n", "b.md": "# B\n"})
	a.runQuiescent(t, ctx)

	remote.ArmFault("read", &cloudsync.StoreError{Kind: cloudsync.ErrPermission, Message: "denied"})
	stateB := t.TempDir()
	b := newNoteRep(t, t.TempDir(), stateB, noteRepB, remote)
	if _, err := b.co.Run(ctx); err == nil {
		t.Fatal("cycle must stop on a fatal remote read error")
	}
	if b.noteExists("a.md") || b.noteExists("b.md") {
		t.Fatal("a note was synced despite the fatal error")
	}
	snap, _, err := b.co.snaps.Load(syncstate.ExpectedIdentity{
		VaultID: noteVaultID, ReplicaID: noteRepB,
		ProviderProfile: noteProfile, RepositoryID: noteRepoID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if snap != nil {
		t.Fatal("snapshot committed despite the fatal error")
	}
}

// TestNoteMalformedRemoteRecordStopsCycle: invalid remote data (a malformed
// record) stops the cycle before any execution.
func TestNoteMalformedRemoteRecordStopsCycle(t *testing.T) {
	ctx := context.Background()
	remote := cloudsync.NewMemoryStore()
	// A valid note plus a malformed record.
	rec := &cloudsync.NoteRecord{
		SchemaVersion: cloudsync.NoteSchemaVersion, SyncID: "11111111-1111-4111-8111-111111111111",
		Path: "good.md", Markdown: "# good\n",
	}
	data, err := rec.Serialize()
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.Seed(cloudsync.NoteKey(rec.SyncID), data, "1"); err != nil {
		t.Fatal(err)
	}
	if err := remote.Seed(cloudsync.NoteKey("22222222-2222-4222-8222-222222222222"), []byte(`{not json`), "1"); err != nil {
		t.Fatal(err)
	}
	b := newNoteRep(t, t.TempDir(), t.TempDir(), noteRepB, remote)
	if _, err := b.co.Run(ctx); err == nil {
		t.Fatal("cycle must stop on a malformed remote record")
	}
	if b.noteExists("good.md") {
		t.Fatal("the valid note was synced despite the invalid record")
	}
}

// TestNoteRemoteSyncIDMismatchStopsCycle: a record whose embedded syncId does
// not match its key is invalid remote data and stops the cycle.
func TestNoteRemoteSyncIDMismatchStopsCycle(t *testing.T) {
	ctx := context.Background()
	remote := cloudsync.NewMemoryStore()
	keyID := "11111111-1111-4111-8111-111111111111"
	rec := &cloudsync.NoteRecord{
		SchemaVersion: cloudsync.NoteSchemaVersion, SyncID: "22222222-2222-4222-8222-222222222222",
		Path: "x.md", Markdown: "# x\n",
	}
	data, err := rec.Serialize()
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.Seed(cloudsync.NoteKey(keyID), data, "1"); err != nil {
		t.Fatal(err)
	}
	b := newNoteRep(t, t.TempDir(), t.TempDir(), noteRepB, remote)
	if _, err := b.co.Run(ctx); err == nil {
		t.Fatal("cycle must stop on a syncId/key mismatch")
	}
}

// TestNoteRetryableRemoteReadRetriesAffectedNote: a retryable transport read
// error only defers the affected note; unrelated notes sync and the snapshot
// commits.
func TestNoteRetryableRemoteReadRetriesAffectedNote(t *testing.T) {
	ctx := context.Background()
	remote := cloudsync.NewMemoryStore()
	a := newNoteRep(t, t.TempDir(), t.TempDir(), noteRepA, remote)
	writeFiles(t, a.root, map[string]string{"a.md": "# A\n", "b.md": "# B\n"})
	a.runQuiescent(t, ctx)

	// Fail the FIRST read (the smaller key, sorted first), retryably.
	remote.ArmFault("read", &cloudsync.StoreError{Kind: cloudsync.ErrRetryableTransport, Message: "flaky"})
	b := newNoteRep(t, t.TempDir(), t.TempDir(), noteRepB, remote)
	b.runQuiescent(t, ctx)

	// Both notes must eventually sync (the retry defers, never drops).
	if !b.noteExists("a.md") || !b.noteExists("b.md") {
		t.Fatal("a note was lost to the retryable read error")
	}
	if st, err := b.co.Run(ctx); err != nil || noteHasWork(st) {
		t.Fatalf("cycle should converge: work %v, err %v", err == nil && noteHasWork(st), err)
	}
}

// --- P2-1: pulled tombstone baseline uses the remote path/hash ---

// TestNoteApplyTombstoneBaselineUsesRemotePath: when a device has not yet
// received a rename, the pulled tombstone's baseline records the REMOTE
// tombstone's content hash and path, not one recomputed from the local path.
func TestNoteApplyTombstoneBaselineUsesRemotePath(t *testing.T) {
	ctx := context.Background()
	remote := cloudsync.NewMemoryStore()
	a := newNoteRep(t, t.TempDir(), t.TempDir(), noteRepA, remote)
	b := newNoteRep(t, t.TempDir(), t.TempDir(), noteRepB, remote)
	writeFiles(t, a.root, map[string]string{"a.md": "# base\n"})
	converge(ctx, t, a, b)
	idA := mustNoteID(t, a, "a.md")

	// The remote original becomes a tombstone at b.md (the deleting device had
	// already received the a.md -> b.md rename).
	tomb := &cloudsync.NoteRecord{
		SchemaVersion: cloudsync.NoteSchemaVersion, SyncID: idA, Path: "b.md", Deleted: true,
	}
	data, err := tomb.Serialize()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := remote.Replace(ctx, cloudsync.NoteKey(idA), data, "1"); err != nil {
		t.Fatal(err)
	}
	b.runQuiescent(t, ctx)

	// B applied the tombstone and deleted its a.md copy.
	if b.noteExists("a.md") {
		t.Fatal("a.md not deleted locally")
	}
	snap, reason, err := b.co.snaps.Load(syncstate.ExpectedIdentity{
		VaultID: noteVaultID, ReplicaID: noteRepB,
		ProviderProfile: noteProfile, RepositoryID: noteRepoID,
	})
	if err != nil || reason != syncstate.NoDiscard {
		t.Fatalf("snapshot load = %v, %v", reason, err)
	}
	base, ok := snap.Notes[idA]
	if !ok || !base.Deleted {
		t.Fatalf("no deleted baseline for %s: %+v", idA, base)
	}
	if want := tomb.ComputeContentHash(); base.ContentHash != want {
		t.Fatalf("baseline contentHash = %s, want the remote b.md tombstone hash %s", base.ContentHash, want)
	}
}

// --- P2-2: injected restart at every conflict boundary ---

// TestNoteRemoteOnlyPullCrashRestartNoDuplicate: a crash during a remote-only
// pull must not leave the new file unindexed (which would mint a second
// identity). The Sync ID/path is reserved and persisted before the file
// appears.
func TestNoteRemoteOnlyPullCrashRestartNoDuplicate(t *testing.T) {
	for _, point := range []string{"pull:remote:index-saved", "pull:remote:file-written"} {
		t.Run(point, func(t *testing.T) {
			ctx := context.Background()
			remote := cloudsync.NewMemoryStore()
			a := newNoteRep(t, t.TempDir(), t.TempDir(), noteRepA, remote)
			b := newNoteRep(t, t.TempDir(), t.TempDir(), noteRepB, remote)
			writeFiles(t, a.root, map[string]string{"a.md": "# base\n"})
			a.runQuiescent(t, ctx) // a.md exists only remotely

			b.co.cfg.TestFault = func(p string) error {
				if p == point {
					return fmt.Errorf("injected crash at %s", point)
				}
				return nil
			}
			if _, err := b.co.Run(ctx); err == nil {
				t.Fatalf("cycle should crash at %s", point)
			}
			b.co.cfg.TestFault = nil
			if err := b.idx.Reload(); err != nil { // restart from durable state
				t.Fatal(err)
			}
			b.runQuiescent(t, ctx)

			if got := len(b.idx.SortedIDs()); got != 1 {
				t.Fatalf("%s: restart minted %d identities, want 1", point, got)
			}
			if !b.noteExists("a.md") || b.noteBody("a.md") != "# base\n" {
				t.Fatal("pulled note lost")
			}
			if _, ok := b.idx.IDByPath("a.md"); !ok {
				t.Fatal("a.md is not indexed")
			}
		})
	}
}

// TestNoteConfirmWriteClassifiesReRead pins the confirmatory re-read policy:
// retryable errors and a concurrent different state defer, while fatal errors,
// malformed records, and syncId mismatches stop the cycle.
func TestNoteConfirmWriteClassifiesReRead(t *testing.T) {
	ctx := context.Background()
	key := cloudsync.NoteKey("11111111-1111-4111-8111-111111111111")
	wantID := "11111111-1111-4111-8111-111111111111"
	otherID := "22222222-2222-4222-8222-222222222222"

	// retryable re-read error -> defer.
	c := &NoteCoordinator{remote: cloudsync.NewMemoryStore()}
	c.remote.(*cloudsync.MemoryStore).ArmFault("read", &cloudsync.StoreError{Kind: cloudsync.ErrRetryableTransport, Message: "flaky"})
	if v, ok, err := c.confirmWrite(ctx, key, wantID, func(*cloudsync.NoteRecord) bool { return true }); err != nil || ok || v != "" {
		t.Fatalf("retryable re-read = %q, %v, %v; want defer", v, ok, err)
	}

	// not-found re-read -> defer (the next cycle's full listing decides).
	c5 := &NoteCoordinator{remote: cloudsync.NewMemoryStore()} // empty: key missing
	if v, ok, err := c5.confirmWrite(ctx, key, wantID, func(*cloudsync.NoteRecord) bool { return true }); err != nil || ok || v != "" {
		t.Fatalf("not-found re-read = %q, %v, %v; want defer", v, ok, err)
	}

	// fatal re-read error -> stop.
	c.remote.(*cloudsync.MemoryStore).ArmFault("read", &cloudsync.StoreError{Kind: cloudsync.ErrAuth, Message: "expired"})
	if _, _, err := c.confirmWrite(ctx, key, wantID, func(*cloudsync.NoteRecord) bool { return true }); err == nil {
		t.Fatal("fatal re-read error must stop the cycle")
	}

	// malformed record -> stop.
	c2 := &NoteCoordinator{remote: cloudsync.NewMemoryStore()}
	s2 := c2.remote.(*cloudsync.MemoryStore)
	if err := s2.Seed(key, []byte(`{bad`), "1"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c2.confirmWrite(ctx, key, wantID, func(*cloudsync.NoteRecord) bool { return true }); err == nil {
		t.Fatal("malformed re-read record must stop the cycle")
	}

	// syncId/key mismatch -> stop.
	c3 := &NoteCoordinator{remote: cloudsync.NewMemoryStore()}
	s3 := c3.remote.(*cloudsync.MemoryStore)
	rec := &cloudsync.NoteRecord{SchemaVersion: cloudsync.NoteSchemaVersion, SyncID: otherID, Path: "x.md", Markdown: "# x\n"}
	data, err := rec.Serialize()
	if err != nil {
		t.Fatal(err)
	}
	if err := s3.Seed(key, data, "1"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c3.confirmWrite(ctx, key, wantID, func(*cloudsync.NoteRecord) bool { return true }); err == nil {
		t.Fatal("syncId/key mismatch on re-read must stop the cycle")
	}

	// A concurrent different state defers; a matching state is idempotent.
	c4 := &NoteCoordinator{remote: cloudsync.NewMemoryStore()}
	s4 := c4.remote.(*cloudsync.MemoryStore)
	other := &cloudsync.NoteRecord{SchemaVersion: cloudsync.NoteSchemaVersion, SyncID: wantID, Path: "y.md", Markdown: "# other\n"}
	od, err := other.Serialize()
	if err != nil {
		t.Fatal(err)
	}
	if err := s4.Seed(key, od, "1"); err != nil {
		t.Fatal(err)
	}
	if v, ok, err := c4.confirmWrite(ctx, key, wantID, func(r *cloudsync.NoteRecord) bool { return r.Path == "x.md" }); err != nil || ok || v != "" {
		t.Fatalf("concurrent different state = %q, %v, %v; want defer", v, ok, err)
	}
	if v, ok, err := c4.confirmWrite(ctx, key, wantID, func(r *cloudsync.NoteRecord) bool { return r.Path == "y.md" }); err != nil || !ok || v == "" {
		t.Fatalf("matching re-read = %q, %v, %v; want idempotent success", v, ok, err)
	}
}

// TestNotePathChangeDefersWhenTargetClaimed: a cross-path pull must not delete
// the old file when the target path is still claimed by another Sync ID (a
// not-yet-cleaned tombstone entry); it defers until the claim is released.
func TestNotePathChangeDefersWhenTargetClaimed(t *testing.T) {
	ctx := context.Background()
	remote := cloudsync.NewMemoryStore()
	a := newNoteRep(t, t.TempDir(), t.TempDir(), noteRepA, remote)
	b := newNoteRep(t, t.TempDir(), t.TempDir(), noteRepB, remote)
	writeFiles(t, a.root, map[string]string{"a.md": "# base\n"})
	converge(ctx, t, a, b)
	idA := mustNoteID(t, a, "a.md")

	// A renames in-app to b.md and pushes.
	if err := os.WriteFile(filepath.Join(a.root, "b.md"), []byte("# base\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(a.root, "a.md")); err != nil {
		t.Fatal(err)
	}
	if err := a.idx.UpdatePath(idA, "b.md"); err != nil {
		t.Fatal(err)
	}
	a.runQuiescent(t, ctx)

	// B has a stale index claim on the target path (a tombstoned note whose
	// entry has not been cleaned yet).
	staleID := "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee"
	if err := b.idx.AddNote(staleID, "b.md"); err != nil {
		t.Fatal(err)
	}
	if err := b.idx.Save(); err != nil {
		t.Fatal(err)
	}

	// B's pull defers: the old file is untouched, the target untouched.
	if _, err := b.co.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if p, _ := b.idx.PathByID(idA); p != "a.md" {
		t.Fatalf("note moved to %q despite the claimed target", p)
	}
	if !b.noteExists("a.md") {
		t.Fatal("old file deleted despite the claimed target")
	}
	if b.noteExists("b.md") {
		t.Fatal("target materialized despite the stale claim")
	}

	// Release the stale claim: the pull proceeds and converges.
	if err := b.idx.RemoveNote(staleID); err != nil {
		t.Fatal(err)
	}
	if err := b.idx.Save(); err != nil {
		t.Fatal(err)
	}
	b.runQuiescent(t, ctx)
	if p, _ := b.idx.PathByID(idA); p != "b.md" {
		t.Fatalf("note converged at %q, want b.md", p)
	}
	if !b.noteExists("b.md") || b.noteExists("a.md") {
		t.Fatal("cross-path pull did not converge to b.md only")
	}
}

// TestNoteRemoteOnlyPullDefersWhenTargetClaimed: a remote-only pull whose
// target is still claimed by a different Sync ID (a not-yet-cleaned tombstone)
// defers instead of aborting the cycle, so the tombstone's baseline and index
// cleanup can commit and the pull completes on a later cycle.
func TestNoteRemoteOnlyPullDefersWhenTargetClaimed(t *testing.T) {
	ctx := context.Background()
	remote := cloudsync.NewMemoryStore()
	b := newNoteRep(t, t.TempDir(), t.TempDir(), noteRepB, remote)

	// A new remote note also uses x.md.
	newID := "11111111-1111-4111-8111-111111111111"
	rec := &cloudsync.NoteRecord{
		SchemaVersion: cloudsync.NoteSchemaVersion, SyncID: newID, Path: "x.md", Markdown: "# A\n",
	}
	data, err := rec.Serialize()
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.Seed(cloudsync.NoteKey(newID), data, "1"); err != nil {
		t.Fatal(err)
	}

	// B knows an old note C that was tombstoned at x.md: its index entry still
	// claims the path and its baseline is a deleted baseline, so its cleanup is
	// a converged-deletion noop.
	staleID := "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee"
	tomb := &cloudsync.NoteRecord{
		SchemaVersion: cloudsync.NoteSchemaVersion, SyncID: staleID, Path: "x.md", Deleted: true,
	}
	td, err := tomb.Serialize()
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.Seed(cloudsync.NoteKey(staleID), td, "1"); err != nil {
		t.Fatal(err)
	}
	if err := b.idx.AddNote(staleID, "x.md"); err != nil {
		t.Fatal(err)
	}
	if err := b.idx.Save(); err != nil {
		t.Fatal(err)
	}
	snap := &syncstate.SnapshotV2{
		SchemaVersion: syncstate.SnapshotV2SchemaVersion,
		VaultID:       noteVaultID, ReplicaID: noteRepB,
		RepositoryID: noteRepoID, ProviderProfile: noteProfile,
		Notes: map[string]syncstate.SnapshotEntity{
			staleID: {ContentHash: tomb.ComputeContentHash(), Deleted: true, RemoteVersion: "1"},
		},
	}
	if err := b.co.snaps.Replace(snap); err != nil {
		t.Fatal(err)
	}

	// B's cycle must NOT abort: A's pull defers and C's cleanup commits.
	if _, err := b.co.Run(ctx); err != nil {
		t.Fatal("cycle must not abort when the pull target is claimed")
	}
	if b.noteExists("x.md") {
		t.Fatal("pulled note materialized despite the stale claim")
	}
	if _, ok := b.idx.IDByPath("x.md"); ok {
		t.Fatal("stale claim was not cleaned up")
	}

	// Once the claim is gone, the pull completes.
	b.runQuiescent(t, ctx)
	if !b.noteExists("x.md") || b.noteBody("x.md") != "# A\n" {
		t.Fatal("pulled note did not converge after the claim was released")
	}
	if id, _ := b.idx.IDByPath("x.md"); id != newID {
		t.Fatalf("x.md indexed as %s, want the remote note", id)
	}
}

// TestNoteConflictInjectedRestartEveryBoundary crashes at each conflict
// boundary and restarts from durable state for all three compound outcomes:
// the same conflict identity is reused, the original converges, and no
// duplicate conflict note appears.
func TestNoteConflictInjectedRestartEveryBoundary(t *testing.T) {
	points := []string{"conflict:reserved", "conflict:saved", "conflict:local", "conflict:remote", "conflict:original"}
	scenarios := []struct {
		name         string
		wantConflict string
		setup        func(ctx context.Context, t *testing.T, a, b *noteRep, remote *cloudsync.MemoryStore)
		assert       func(t *testing.T, ctx context.Context, b *noteRep)
	}{
		{
			name:         "preserve_local_then_pull",
			wantConflict: "# B edit\n",
			setup: func(ctx context.Context, t *testing.T, a, b *noteRep, remote *cloudsync.MemoryStore) {
				writeFiles(t, a.root, map[string]string{"a.md": "# A edit\n"})
				writeFiles(t, b.root, map[string]string{"a.md": "# B edit\n"})
				a.runQuiescent(t, ctx)
			},
			assert: func(t *testing.T, ctx context.Context, b *noteRep) {
				if b.noteBody("a.md") != "# A edit\n" {
					t.Fatalf("original = %q, want A's edit", b.noteBody("a.md"))
				}
			},
		},
		{
			name:         "preserve_local_then_delete",
			wantConflict: "# B edit\n",
			setup: func(ctx context.Context, t *testing.T, a, b *noteRep, remote *cloudsync.MemoryStore) {
				writeFiles(t, b.root, map[string]string{"a.md": "# B edit\n"})
				tomb := &cloudsync.NoteRecord{
					SchemaVersion: cloudsync.NoteSchemaVersion, SyncID: mustNoteID(t, a, "a.md"),
					Path: "a.md", Deleted: true,
				}
				data, err := tomb.Serialize()
				if err != nil {
					t.Fatal(err)
				}
				if _, err := remote.Replace(ctx, cloudsync.NoteKey(tomb.SyncID), data, "1"); err != nil {
					t.Fatal(err)
				}
			},
			assert: func(t *testing.T, ctx context.Context, b *noteRep) {
				if b.noteExists("a.md") {
					t.Fatal("original should be deleted")
				}
			},
		},
		{
			name:         "preserve_remote_then_tombstone",
			wantConflict: "# A edit\n",
			setup: func(ctx context.Context, t *testing.T, a, b *noteRep, remote *cloudsync.MemoryStore) {
				writeFiles(t, a.root, map[string]string{"a.md": "# A edit\n"})
				if err := os.Remove(filepath.Join(b.root, "a.md")); err != nil {
					t.Fatal(err)
				}
				a.runQuiescent(t, ctx)
			},
			assert: func(t *testing.T, ctx context.Context, b *noteRep) {
				if b.noteExists("a.md") {
					t.Fatal("original should be tombstoned")
				}
			},
		},
	}
	for _, sc := range scenarios {
		for _, point := range points {
			t.Run(sc.name+"/"+point, func(t *testing.T) {
				ctx := context.Background()
				remote := cloudsync.NewMemoryStore()
				a := newNoteRep(t, t.TempDir(), t.TempDir(), noteRepA, remote)
				b := newNoteRep(t, t.TempDir(), t.TempDir(), noteRepB, remote)
				writeFiles(t, a.root, map[string]string{"a.md": "# base\n"})
				converge(ctx, t, a, b)
				sc.setup(ctx, t, a, b, remote)

				b.co.cfg.TestFault = func(p string) error {
					if p == point {
						return fmt.Errorf("injected crash at %s", point)
					}
					return nil
				}
				if _, err := b.co.Run(ctx); err == nil {
					t.Fatalf("cycle should crash at %s", point)
				}
				b.co.cfg.TestFault = nil
				if err := b.idx.Reload(); err != nil { // restart from durable state
					t.Fatal(err)
				}
				b.runQuiescent(t, ctx)

				if got := conflictNotes(t, b.root); len(got) != 1 {
					t.Fatalf("%s/%s: restart produced %d conflict notes, want 1: %v", sc.name, point, len(got), got)
				}
				// The preserved Markdown survives every injected restart.
				if got := b.noteBody(conflictNotes(t, b.root)[0]); got != sc.wantConflict {
					t.Fatalf("%s/%s: conflict note = %q, want %q", sc.name, point, got, sc.wantConflict)
				}
				sc.assert(t, ctx, b)
				converge(ctx, t, a, b)
				if got := conflictNotes(t, b.root); len(got) != 1 {
					t.Fatalf("%s/%s: convergence produced %d conflict notes, want 1: %v", sc.name, point, len(got), got)
				}
				if got := b.noteBody(conflictNotes(t, b.root)[0]); got != sc.wantConflict {
					t.Fatalf("%s/%s: conflict note after convergence = %q, want %q", sc.name, point, got, sc.wantConflict)
				}
			})
		}
	}
}

// TestNoteConflictSnapshotLossRestart: after a conflict converges, a lost
// snapshot is re-established on restart without losing the conflict note.
func TestNoteConflictSnapshotLossRestart(t *testing.T) {
	ctx := context.Background()
	remote := cloudsync.NewMemoryStore()
	a := newNoteRep(t, t.TempDir(), t.TempDir(), noteRepA, remote)
	stateB := t.TempDir()
	b := newNoteRep(t, t.TempDir(), stateB, noteRepB, remote)
	writeFiles(t, a.root, map[string]string{"a.md": "# base\n"})
	converge(ctx, t, a, b)
	writeFiles(t, a.root, map[string]string{"a.md": "# A edit\n"})
	writeFiles(t, b.root, map[string]string{"a.md": "# B edit\n"})
	converge(ctx, t, a, b)
	if got := conflictNotes(t, b.root); len(got) != 1 {
		t.Fatalf("pre-loss conflict notes = %v, want 1", got)
	}

	// Lose the device state; restart must re-establish baselines and keep the
	// single conflict note.
	if err := os.RemoveAll(stateB); err != nil {
		t.Fatal(err)
	}
	b.runQuiescent(t, ctx)
	if got := conflictNotes(t, b.root); len(got) != 1 {
		t.Fatalf("after snapshot loss conflict notes = %v, want 1", got)
	}
	if b.noteBody("a.md") != "# A edit\n" {
		t.Fatal("original content lost after snapshot loss")
	}
}

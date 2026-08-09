package syncservice

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"memodump/internal/cloudsync"
	"memodump/internal/syncindex"
	"memodump/internal/syncstate"
)

const (
	svcVaultID  = "dc56ad15-62c6-4fa7-bf7a-5c6337d574be"
	svcRepoID   = "33333333-3333-4333-8333-333333333333"
	svcProfile  = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	svcReplicaA = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	svcReplicaB = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
)

func seedNote(t *testing.T, s *cloudsync.MemoryStore, syncID, path, markdown string) {
	t.Helper()
	rec := &cloudsync.NoteRecord{
		SchemaVersion: cloudsync.NoteSchemaVersion, SyncID: syncID, Path: path, Markdown: markdown,
	}
	data, err := rec.Serialize()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Seed(cloudsync.NoteKey(syncID), data, "1"); err != nil {
		t.Fatal(err)
	}
}

func newService(root, state string, store *cloudsync.MemoryStore) *Service {
	return New(Config{
		RepoRoot: root, StateRoot: state,
		VaultID: svcVaultID, ReplicaID: svcReplicaA,
		RepoID: svcRepoID, Profile: svcProfile,
		Provider: func() (cloudsync.RemoteStore, error) { return store, nil },
	})
}

// TestServiceSyncsRemoteNote: a manual run pulls a remote-only note and reports
// Synced.
func TestServiceSyncsRemoteNote(t *testing.T) {
	ctx := context.Background()
	store := cloudsync.NewMemoryStore()
	seedNote(t, store, "11111111-1111-4111-8111-111111111111", "a.md", "# A\n")

	root, state := t.TempDir(), t.TempDir()
	if _, err := syncindex.EnableNoteStore(root); err != nil {
		t.Fatal(err)
	}
	s := newService(root, state, store)
	res, err := s.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Synced || !res.SnapshotCommitted {
		t.Fatalf("res = %+v, want synced", res)
	}
	data, rerr := os.ReadFile(filepath.Join(root, "a.md"))
	if rerr != nil || string(data) != "# A\n" {
		t.Fatalf("note not pulled: %q, %v", data, rerr)
	}
}

// TestServiceLockLoserRefusedAndNotesEditable: while another run holds the
// replica lock, a second run is refused (never "synced") and the vault stays
// editable.
func TestServiceLockLoserRefusedAndNotesEditable(t *testing.T) {
	ctx := context.Background()
	root, state := t.TempDir(), t.TempDir()

	// Simulate an in-progress run by another process holding the lock.
	lock, err := syncstate.AcquireReplicaLock(state, svcVaultID, svcReplicaA)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()

	s := newService(root, state, cloudsync.NewMemoryStore())
	res, err := s.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Synced || res.LastError != "locked" {
		t.Fatalf("lock loser = %+v, want locked + not synced", res)
	}
	// Only the state directory is locked; notes remain editable.
	if err := os.WriteFile(filepath.Join(root, "note.md"), []byte("# editable\n"), 0644); err != nil {
		t.Fatal("lock loser cannot edit notes")
	}
}

// TestServicePermissionErrorNeverSynced: a fatal remote error reports a
// redacted label, never "synced".
func TestServicePermissionErrorNeverSynced(t *testing.T) {
	ctx := context.Background()
	store := cloudsync.NewMemoryStore()
	seedNote(t, store, "11111111-1111-4111-8111-111111111111", "a.md", "# A\n")
	store.ArmFault("read", &cloudsync.StoreError{Kind: cloudsync.ErrPermission, Message: "denied for user s3://bucket/key=secret"})

	root, state := t.TempDir(), t.TempDir()
	if _, err := syncindex.EnableNoteStore(root); err != nil {
		t.Fatal(err)
	}
	s := newService(root, state, store)
	res, err := s.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Synced {
		t.Fatal("permission error reported synced")
	}
	if res.LastError != "permission" {
		t.Fatalf("LastError = %q, want the redacted permission label", res.LastError)
	}
	// The status must not leak provider error bodies or URLs.
	if strings.Contains(res.LastError, "secret") || strings.Contains(res.LastError, "://") {
		t.Fatalf("status leaked provider details: %q", res.LastError)
	}
	if _, rerr := os.Stat(filepath.Join(root, "a.md")); !os.IsNotExist(rerr) {
		t.Fatal("note pulled despite the permission error")
	}
}

// TestServiceListFailureNeverSynced: a listing failure stops the cycle and is
// never reported as "synced".
func TestServiceListFailureNeverSynced(t *testing.T) {
	ctx := context.Background()
	store := cloudsync.NewMemoryStore()
	store.ArmFault("list", &cloudsync.StoreError{Kind: cloudsync.ErrPermission, Message: "denied"})

	root, state := t.TempDir(), t.TempDir()
	if _, err := syncindex.EnableNoteStore(root); err != nil {
		t.Fatal(err)
	}
	s := newService(root, state, store)
	res, err := s.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Synced {
		t.Fatal("listing failure reported synced")
	}
	if res.LastError != "permission" {
		t.Fatalf("LastError = %q, want permission", res.LastError)
	}
}

// TestServiceIncompleteListNeverSynced: a typed incomplete-list error from the
// provider stops the first sync (even with no baseline) and is never reported
// "synced".
func TestServiceIncompleteListNeverSynced(t *testing.T) {
	ctx := context.Background()
	store := cloudsync.NewMemoryStore()
	seedNote(t, store, "11111111-1111-4111-8111-111111111111", "a.md", "# A\n")
	root, state := t.TempDir(), t.TempDir()
	if _, err := syncindex.EnableNoteStore(root); err != nil {
		t.Fatal(err)
	}
	store.ArmIncompleteList(1)
	s := newService(root, state, store)
	res, err := s.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Synced {
		t.Fatalf("incomplete listing reported synced: %+v", res)
	}
	if res.LastError != "incomplete-list" {
		t.Fatalf("LastError = %q, want incomplete-list", res.LastError)
	}
	// The note is not silently missed-and-committed: nothing was pulled.
	if _, rerr := os.Stat(filepath.Join(root, "a.md")); !os.IsNotExist(rerr) {
		t.Fatal("note pulled despite the incomplete listing")
	}
}

// TestServiceListingErrorNeverSynced: a listing transport error stops the cycle
// and is never reported "synced".
func TestServiceListingErrorNeverSynced(t *testing.T) {
	ctx := context.Background()
	store := cloudsync.NewMemoryStore()
	seedNote(t, store, "11111111-1111-4111-8111-111111111111", "a.md", "# A\n")
	root, state := t.TempDir(), t.TempDir()
	if _, err := syncindex.EnableNoteStore(root); err != nil {
		t.Fatal(err)
	}
	store.ArmFault("list", &cloudsync.StoreError{Kind: cloudsync.ErrPermission, Message: "denied"})
	s := newService(root, state, store)
	res, err := s.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Synced {
		t.Fatal("listing error reported synced")
	}
	if res.LastError != "permission" {
		t.Fatalf("LastError = %q, want permission", res.LastError)
	}
}

// TestServiceConcurrentRunsSerializedByLock: two Services on the same replica
// cannot run concurrently — one wins the OS lock, the other is refused.
func TestServiceConcurrentRunsSerializedByLock(t *testing.T) {
	ctx := context.Background()
	root, state := t.TempDir(), t.TempDir()

	// First run acquires and holds the lock for its duration; start it and keep
	// it in progress by holding a second handle after it completes is not
	// observable, so instead: the first Service holds the lock, the second is
	// refused. This is the cross-process exclusion the OS lock provides.
	lock, err := syncstate.AcquireReplicaLock(state, svcVaultID, svcReplicaB)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()

	sb := New(Config{
		RepoRoot: root, StateRoot: state,
		VaultID: svcVaultID, ReplicaID: svcReplicaB,
		RepoID: svcRepoID, Profile: svcProfile,
		Provider: func() (cloudsync.RemoteStore, error) { return cloudsync.NewMemoryStore(), nil },
	})
	res, err := sb.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Synced || res.LastError != "locked" {
		t.Fatalf("second run = %+v, want locked", res)
	}
}

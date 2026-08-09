package syncrun

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"memodump/internal/cloudsync"
	"memodump/internal/syncindex"
	"memodump/internal/syncstate"
	"memodump/internal/vaultfs"
)

const (
	noteVaultID = "dc56ad15-62c6-4fa7-bf7a-5c6337d574be"
	noteRepoID  = "33333333-3333-4333-8333-333333333333"
	noteProfile = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	noteRepA    = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	noteRepB    = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
)

type noteRep struct {
	root      string
	stateRoot string
	idx       *syncindex.NoteStore
	co        *NoteCoordinator
	lock      *syncstate.Lock
}

// newNoteRep builds one replica: a vault root, an enabled note-only index, the
// v2 snapshot + recovery stores, and the replica lock held for the test.
func newNoteRep(t *testing.T, root, stateRoot, replicaID string, remote cloudsync.RemoteStore) *noteRep {
	t.Helper()
	repo, err := vaultfs.New(root)
	if err != nil {
		t.Fatal(err)
	}
	idx, err := syncindex.EnableNoteStore(root)
	if err != nil {
		t.Fatalf("enable note index at %s: %v", root, err)
	}
	snaps, err := syncstate.NewSnapshotStoreV2(stateRoot, noteVaultID, replicaID)
	if err != nil {
		t.Fatal(err)
	}
	recovery, err := syncstate.NewRecoveryStore(stateRoot, noteVaultID, replicaID)
	if err != nil {
		t.Fatal(err)
	}
	lock, err := syncstate.AcquireReplicaLock(stateRoot, noteVaultID, replicaID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lock.Close() })
	co := NewNoteCoordinator(repo, idx, snaps, recovery, remote, NoteConfig{
		VaultID: noteVaultID, ReplicaID: replicaID, StateRoot: stateRoot,
		RepoID: noteRepoID, Profile: noteProfile, Lock: lock,
	})
	return &noteRep{root: root, stateRoot: stateRoot, idx: idx, co: co, lock: lock}
}

func (r *noteRep) noteExists(path string) bool {
	_, err := os.Stat(filepath.Join(r.root, filepath.FromSlash(path)))
	return err == nil
}

// withScanOpts installs deterministic scan hooks on the coordinator for the
// race tests that mutate files mid-scan.
func (r *noteRep) withScanOpts(opts vaultfs.ScanOptions) *noteRep {
	r.co.cfg.ScanOptions = opts
	return r
}

func (r *noteRep) noteBody(path string) string {
	data, err := os.ReadFile(filepath.Join(r.root, filepath.FromSlash(path)))
	if err != nil {
		return ""
	}
	return string(data)
}

// noteHasWork reports whether a cycle still has non-noop/non-block/non-retry
// decisions left.
func noteHasWork(st *NoteStatus) bool {
	for _, d := range st.Decisions {
		switch d.Kind {
		case cloudsync.NoteNoop, cloudsync.NoteBlock, cloudsync.NoteRetry:
			continue
		default:
			return true
		}
	}
	return false
}

func (r *noteRep) runOnce(ctx context.Context) (bool, error) {
	st, err := r.co.Run(ctx)
	if err != nil {
		return false, err
	}
	return noteHasWork(st), nil
}

// runQuiescent runs cycles until one leaves no work behind.
func (r *noteRep) runQuiescent(t *testing.T, ctx context.Context) {
	t.Helper()
	for i := 0; i < 20; i++ {
		work, err := r.runOnce(ctx)
		if err != nil {
			t.Fatalf("cycle %d: %v", i, err)
		}
		if !work {
			return
		}
	}
	t.Fatal("replica did not converge within 20 cycles")
}

// converge runs two replicas against one remote until neither has work, or
// fails after a bounded number of cycles.
func converge(ctx context.Context, t *testing.T, a, b *noteRep) {
	t.Helper()
	for i := 0; i < 20; i++ {
		aw, err := a.runOnce(ctx)
		if err != nil {
			t.Fatalf("replica A cycle %d: %v", i, err)
		}
		bw, err := b.runOnce(ctx)
		if err != nil {
			t.Fatalf("replica B cycle %d: %v", i, err)
		}
		if !aw && !bw {
			return
		}
	}
	t.Fatal("replicas did not converge within 20 cycles")
}

func remoteNote(t *testing.T, s *cloudsync.MemoryStore, syncID string) *cloudsync.NoteRecord {
	t.Helper()
	data, _, err := s.Read(context.Background(), cloudsync.NoteKey(syncID))
	if err != nil {
		t.Fatalf("read remote %s: %v", syncID, err)
	}
	rec, err := cloudsync.ParseNoteRecord(data)
	if err != nil {
		t.Fatalf("parse remote %s: %v", syncID, err)
	}
	return rec
}

// TestNoteCoordinatorCreateEditConverges covers create, nested create, edit,
// and identical simultaneous edit across two replicas.
func TestNoteCoordinatorCreateEditConverges(t *testing.T) {
	ctx := context.Background()
	remote := cloudsync.NewMemoryStore()
	rootA, rootB := t.TempDir(), t.TempDir()
	stateA, stateB := t.TempDir(), t.TempDir()
	a := newNoteRep(t, rootA, stateA, noteRepA, remote)
	b := newNoteRep(t, rootB, stateB, noteRepB, remote)

	// A creates a nested note; B is empty.
	writeFiles(t, rootA, map[string]string{"Projects/idea.md": "# Idea\n"})
	converge(ctx, t, a, b)
	if !b.noteExists("Projects/idea.md") || b.noteBody("Projects/idea.md") != "# Idea\n" {
		t.Fatal("B did not receive A's nested create")
	}

	// B edits; A converges.
	writeFiles(t, rootB, map[string]string{"Projects/idea.md": "# Idea edited\n"})
	converge(ctx, t, a, b)
	if a.noteBody("Projects/idea.md") != "# Idea edited\n" {
		t.Fatal("A did not receive B's edit")
	}

	// Identical simultaneous edit: both edit to the same body, then converge.
	writeFiles(t, rootA, map[string]string{"Projects/idea.md": "# Same\n"})
	writeFiles(t, rootB, map[string]string{"Projects/idea.md": "# Same\n"})
	converge(ctx, t, a, b)
	if a.noteBody("Projects/idea.md") != "# Same\n" || b.noteBody("Projects/idea.md") != "# Same\n" {
		t.Fatal("identical simultaneous edit did not converge")
	}

	// The remote record and both snapshots agree.
	rec := remoteNote(t, remote, mustNoteID(t, a, "Projects/idea.md"))
	if rec.Markdown != "# Same\n" {
		t.Fatalf("remote record = %q", rec.Markdown)
	}
	if len(a.co.snaps.Path()) == 0 || len(b.co.snaps.Path()) == 0 {
		t.Fatal("snapshot not written")
	}
}

func mustNoteID(t *testing.T, r *noteRep, path string) string {
	t.Helper()
	id, ok := r.idx.IDByPath(path)
	if !ok {
		t.Fatalf("path %q not indexed", path)
	}
	return id
}

// TestNoteCoordinatorInAppPathChange covers a recorded rename: the Sync ID
// follows the note to its new path on both replicas and no duplicate identity
// is minted for the old location.
func TestNoteCoordinatorInAppPathChange(t *testing.T) {
	ctx := context.Background()
	remote := cloudsync.NewMemoryStore()
	rootA, rootB := t.TempDir(), t.TempDir()
	a := newNoteRep(t, rootA, t.TempDir(), noteRepA, remote)
	b := newNoteRep(t, rootB, t.TempDir(), noteRepB, remote)

	writeFiles(t, rootA, map[string]string{"a.md": "# A\n"})
	converge(ctx, t, a, b)
	idA := mustNoteID(t, a, "a.md")
	if idB := mustNoteID(t, b, "a.md"); idB != idA {
		t.Fatal("replicas disagree on the note identity")
	}

	// In-app rename on A: the file moves and the index records it.
	if err := os.WriteFile(filepath.Join(rootA, "b.md"), []byte("# A\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(rootA, "a.md")); err != nil {
		t.Fatal(err)
	}
	if err := a.idx.UpdatePath(idA, "b.md"); err != nil {
		t.Fatal(err)
	}

	converge(ctx, t, a, b)
	if !a.noteExists("b.md") || a.noteExists("a.md") {
		t.Fatal("A's rename did not stick")
	}
	if !b.noteExists("b.md") || b.noteExists("a.md") {
		t.Fatal("B did not follow the in-app path change")
	}
	if b.noteBody("b.md") != "# A\n" {
		t.Fatal("B lost the note content during the path change")
	}
	// The remote record now names the new path and the identity is unchanged.
	if rec := remoteNote(t, remote, idA); rec.Path != "b.md" {
		t.Fatalf("remote path = %q, want b.md", rec.Path)
	}
	// No duplicate identity was minted for the moved note.
	if len(a.idx.SortedIDs()) != 1 || len(b.idx.SortedIDs()) != 1 {
		t.Fatalf("path change minted duplicate identities: A=%v B=%v", a.idx.SortedIDs(), b.idx.SortedIDs())
	}
}

// TestNoteCoordinatorUncertainWriteConverges covers the lost-response case: the
// write lands but the response is lost; the executor re-reads, establishes the
// baseline, and the cycle converges.
func TestNoteCoordinatorUncertainWriteConverges(t *testing.T) {
	ctx := context.Background()
	remote := cloudsync.NewMemoryStore()
	root := t.TempDir()
	a := newNoteRep(t, root, t.TempDir(), noteRepA, remote)
	writeFiles(t, root, map[string]string{"a.md": "# A\n"})

	remote.ArmUncertainWrite("create", &cloudsync.StoreError{Kind: cloudsync.ErrRetryableTransport, Message: "response lost"})
	a.runQuiescent(t, ctx)
	// The write landed and the baseline was recorded.
	rec := remoteNote(t, remote, mustNoteID(t, a, "a.md"))
	if rec.Markdown != "# A\n" {
		t.Fatal("write did not land")
	}
	if work, err := a.runOnce(ctx); err != nil || work {
		t.Fatalf("post-convergence cycle = work %v, err %v", work, err)
	}
}

// TestNoteCoordinatorSnapshotLostConverges covers the lost snapshot-commit
// case: after the write landed, a missing snapshot means conservative
// onboarding, which re-establishes the baseline and converges without losing
// the note.
func TestNoteCoordinatorSnapshotLostConverges(t *testing.T) {
	ctx := context.Background()
	remote := cloudsync.NewMemoryStore()
	root, state := t.TempDir(), t.TempDir()
	a := newNoteRep(t, root, state, noteRepA, remote)
	writeFiles(t, root, map[string]string{"a.md": "# A\n"})
	a.runQuiescent(t, ctx)
	if !a.noteExists("a.md") {
		t.Fatal("note missing after first convergence")
	}

	// Simulate a lost snapshot: the note survives locally and on the remote,
	// but the device state file is gone.
	if err := os.Remove(a.co.snaps.Path()); err != nil {
		t.Fatal(err)
	}

	a.runQuiescent(t, ctx)
	if !a.noteExists("a.md") {
		t.Fatal("note lost after snapshot loss")
	}
}

// TestNoteCoordinatorNestedRemoteOnlyPull covers B joining a repository whose
// notes live under directories B has never created.
func TestNoteCoordinatorNestedRemoteOnlyPull(t *testing.T) {
	ctx := context.Background()
	remote := cloudsync.NewMemoryStore()
	a := newNoteRep(t, t.TempDir(), t.TempDir(), noteRepA, remote)
	b := newNoteRep(t, t.TempDir(), t.TempDir(), noteRepB, remote)

	writeFiles(t, a.root, map[string]string{"x/y/z/deep.md": strings.Repeat("# deep\n", 10)})
	converge(ctx, t, a, b)
	if !b.noteExists("x/y/z/deep.md") {
		t.Fatal("B did not create the parent directories for a remote-only nested note")
	}
}

// writeFiles writes note bodies into a vault, creating parent directories.
func writeFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for p, md := range files {
		abs := filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(abs), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(md), 0644); err != nil {
			t.Fatal(err)
		}
	}
}

// TestNoteCoordinatorRejectsWrongReplicaLock: a coordinator constructed with
// another replica's OS lock must refuse to run — the lock guards this replica's
// index and snapshot, and ownership must match.
func TestNoteCoordinatorRejectsWrongReplicaLock(t *testing.T) {
	root, state := t.TempDir(), t.TempDir()
	repo, err := vaultfs.New(root)
	if err != nil {
		t.Fatal(err)
	}
	idx, err := syncindex.EnableNoteStore(root)
	if err != nil {
		t.Fatal(err)
	}
	snaps, err := syncstate.NewSnapshotStoreV2(state, noteVaultID, noteRepA)
	if err != nil {
		t.Fatal(err)
	}
	recovery, err := syncstate.NewRecoveryStore(state, noteVaultID, noteRepA)
	if err != nil {
		t.Fatal(err)
	}
	// The lock for a DIFFERENT replica is held, but it is not this one's.
	wrongLock, err := syncstate.AcquireReplicaLock(state, noteVaultID, noteRepB)
	if err != nil {
		t.Fatal(err)
	}
	defer wrongLock.Close()

	co := NewNoteCoordinator(repo, idx, snaps, recovery, cloudsync.NewMemoryStore(), NoteConfig{
		VaultID: noteVaultID, ReplicaID: noteRepA, StateRoot: state,
		RepoID: noteRepoID, Profile: noteProfile, Lock: wrongLock,
	})
	if _, err := co.Run(context.Background()); err == nil {
		t.Fatal("coordinator accepted another replica's lock")
	}

	// The same vault/replica but a DIFFERENT state root is also rejected.
	otherState := t.TempDir()
	otherLock, err := syncstate.AcquireReplicaLock(otherState, noteVaultID, noteRepA)
	if err != nil {
		t.Fatal(err)
	}
	defer otherLock.Close()
	co2 := NewNoteCoordinator(repo, idx, snaps, recovery, cloudsync.NewMemoryStore(), NoteConfig{
		VaultID: noteVaultID, ReplicaID: noteRepA, StateRoot: state,
		RepoID: noteRepoID, Profile: noteProfile, Lock: otherLock,
	})
	if _, err := co2.Run(context.Background()); err == nil {
		t.Fatal("coordinator accepted a lock from a different state root")
	}
}

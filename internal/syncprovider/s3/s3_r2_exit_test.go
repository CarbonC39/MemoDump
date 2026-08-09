// S3-adapter R2 exit gate. These tests drive two real filesystem replicas
// against ONE shared S3-compatible provider (the fake S3 server) through the
// note coordinator, covering every R2 scenario applicable to a live adapter:
// in-app path change, accepted-write and snapshot-loss restart, both
// edit/delete conflict directions, recovery idempotency, and the local CAS
// race. The coordinator is wired directly (not through the service) so the
// deterministic crash/race seams (TestFault, scan hooks, snapshot access) are
// available while still exercising the real S3 RemoteStore.
package s3

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"memodump/internal/cloudsync"
	"memodump/internal/syncindex"
	"memodump/internal/syncrun"
	"memodump/internal/syncstate"
	"memodump/internal/vaultfs"
)

// s3ExitRep is one replica wired directly to the coordinator against the S3
// remote, with the replica lock held for the test and the snapshot/recovery
// stores exposed so tests can simulate loss and inspect recovery copies.
type s3ExitRep struct {
	root      string
	stateRoot string
	vaultID   string
	replicaID string
	idx       *syncindex.NoteStore
	co        *syncrun.NoteCoordinator
	snaps     *syncstate.SnapshotStoreV2
}

func newS3ExitRep(t *testing.T, root, stateRoot, replicaID string, remote cloudsync.RemoteStore, profile string) *s3ExitRep {
	t.Helper()
	repo, err := vaultfs.New(root)
	if err != nil {
		t.Fatal(err)
	}
	idx, err := syncindex.EnableNoteStore(root)
	if err != nil {
		t.Fatal(err)
	}
	vaultID := idx.Index.VaultID
	snaps, err := syncstate.NewSnapshotStoreV2(stateRoot, vaultID, replicaID)
	if err != nil {
		t.Fatal(err)
	}
	recovery, err := syncstate.NewRecoveryStore(stateRoot, vaultID, replicaID)
	if err != nil {
		t.Fatal(err)
	}
	lock, err := syncstate.AcquireReplicaLock(stateRoot, vaultID, replicaID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lock.Close() })
	co := syncrun.NewNoteCoordinator(repo, idx, snaps, recovery, remote, syncrun.NoteConfig{
		VaultID: vaultID, ReplicaID: replicaID, StateRoot: stateRoot,
		RepoID: s3RepoID, Profile: profile, Lock: lock,
	})
	return &s3ExitRep{root: root, stateRoot: stateRoot, vaultID: vaultID, replicaID: replicaID, idx: idx, co: co, snaps: snaps}
}

func (r *s3ExitRep) noteExists(path string) bool {
	_, err := os.Stat(filepath.Join(r.root, filepath.FromSlash(path)))
	return err == nil
}

func (r *s3ExitRep) noteBody(path string) string {
	data, err := os.ReadFile(filepath.Join(r.root, filepath.FromSlash(path)))
	if err != nil {
		return ""
	}
	return string(data)
}

func (r *s3ExitRep) noteID(path string) (string, bool) {
	return r.idx.IDByPath(path)
}

func (r *s3ExitRep) withScanOpts(opts vaultfs.ScanOptions) *s3ExitRep {
	r.co.SetScanOptions(opts)
	return r
}

// conflictNotes lists the deterministic conflict-note files in the vault.
func (r *s3ExitRep) conflictNotes(t *testing.T) []string {
	t.Helper()
	var found []string
	entries, err := os.ReadDir(r.root)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), " (conflict ") && strings.HasSuffix(e.Name(), ".md") {
			found = append(found, e.Name())
		}
	}
	return found
}

func (r *s3ExitRep) recoveryDir() string {
	return filepath.Join(syncstate.StateDir(r.stateRoot, r.vaultID, r.replicaID), syncstate.RecoveryDirName)
}

// recoveryFiles lists the recovery Markdown copies written for a Sync ID.
func (r *s3ExitRep) recoveryFiles(t *testing.T, syncID string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(r.recoveryDir(), syncID))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".md") {
			out = append(out, e.Name())
		}
	}
	return out
}

// runQuiescent runs cycles until one leaves no non-noop/block/retry work.
func (r *s3ExitRep) runQuiescent(t *testing.T, ctx context.Context) {
	t.Helper()
	for i := 0; i < 20; i++ {
		st, err := r.co.Run(ctx)
		if err != nil {
			t.Fatalf("cycle %d: %v", i, err)
		}
		work := false
		for _, d := range st.Decisions {
			switch d.Kind {
			case cloudsync.NoteNoop, cloudsync.NoteBlock, cloudsync.NoteRetry:
				continue
			}
			work = true
		}
		if !work {
			return
		}
	}
	t.Fatal("replica did not converge within 20 cycles")
}

// s3RemoteNote reads and parses a note record from the S3 provider.
func s3RemoteNote(t *testing.T, c *Client, syncID string) *cloudsync.NoteRecord {
	t.Helper()
	data, _, err := c.Read(context.Background(), cloudsync.NoteKey(syncID))
	if err != nil {
		t.Fatalf("read remote %s: %v", syncID, err)
	}
	rec, perr := cloudsync.ParseNoteRecord(data)
	if perr != nil {
		t.Fatal(perr)
	}
	return rec
}

// TestS3ExitInAppPathChange: a recorded rename follows the Sync ID to the new
// path on both replicas with no duplicate identity and no lost content.
func TestS3ExitInAppPathChange(t *testing.T) {
	ctx := context.Background()
	cc, _ := newServer(t, newFakeS3())
	a := newS3ExitRep(t, t.TempDir(), t.TempDir(), "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", cc, cc.Profile())
	b := newS3ExitRep(t, t.TempDir(), t.TempDir(), "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", cc, cc.Profile())

	if err := os.MkdirAll(filepath.Join(a.root, "x"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(a.root, "x", "a.md"), []byte("# A\n"), 0644); err != nil {
		t.Fatal(err)
	}
	a.runQuiescent(t, ctx)
	b.runQuiescent(t, ctx)
	idA, ok := a.noteID("x/a.md")
	if !ok {
		t.Fatal("A did not index the note")
	}
	if idB, ok := b.noteID("x/a.md"); !ok || idB != idA {
		t.Fatal("replicas disagree on the note identity")
	}

	// In-app rename on A: file moves and the index records the path change.
	if err := os.MkdirAll(filepath.Join(a.root, "y"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(a.root, "y", "a.md"), []byte("# A\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(a.root, "x", "a.md")); err != nil {
		t.Fatal(err)
	}
	if err := a.idx.UpdatePath(idA, "y/a.md"); err != nil {
		t.Fatal(err)
	}

	a.runQuiescent(t, ctx)
	b.runQuiescent(t, ctx)
	if !a.noteExists("y/a.md") || a.noteExists("x/a.md") {
		t.Fatal("A's rename did not stick")
	}
	if !b.noteExists("y/a.md") || b.noteExists("x/a.md") {
		t.Fatal("B did not follow the in-app path change")
	}
	if b.noteBody("y/a.md") != "# A\n" {
		t.Fatal("B lost the note content during the path change")
	}
	if rec := s3RemoteNote(t, cc, idA); rec.Path != "y/a.md" {
		t.Fatalf("remote path = %q, want y/a.md", rec.Path)
	}
	if len(a.idx.SortedIDs()) != 1 || len(b.idx.SortedIDs()) != 1 {
		t.Fatal("path change minted duplicate identities")
	}
}

// TestS3ExitAcceptedWriteAndSnapshotLoss: an accepted-but-lost-response write
// lands and is confirmed by re-read, and a lost snapshot afterwards is rebuilt
// from the remote — the note survives both restarts through S3.
func TestS3ExitAcceptedWriteAndSnapshotLoss(t *testing.T) {
	ctx := context.Background()
	server := newFakeS3()
	cc, _ := newServer(t, server)
	a := newS3ExitRep(t, t.TempDir(), t.TempDir(), "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", cc, cc.Profile())
	b := newS3ExitRep(t, t.TempDir(), t.TempDir(), "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", cc, cc.Profile())

	// The create lands but the response is lost (500 after persisting).
	server.failPutNext = true
	if err := os.WriteFile(filepath.Join(a.root, "a.md"), []byte("# A\n"), 0644); err != nil {
		t.Fatal(err)
	}
	a.runQuiescent(t, ctx)
	idA, ok := a.noteID("a.md")
	if !ok {
		t.Fatal("note not indexed after accepted write")
	}
	// The write landed and the baseline was recorded via the confirm re-read.
	if rec := s3RemoteNote(t, cc, idA); rec.Markdown != "# A\n" {
		t.Fatalf("accepted write did not land: %q", rec.Markdown)
	}
	b.runQuiescent(t, ctx)
	if b.noteBody("a.md") != "# A\n" {
		t.Fatal("B did not pull the accepted write")
	}

	// A's snapshot is lost: the next cycle rebuilds the baseline from the
	// remote without losing the note or minting a duplicate remote record.
	if err := os.Remove(a.snaps.Path()); err != nil {
		t.Fatal(err)
	}
	a.runQuiescent(t, ctx)
	if a.noteBody("a.md") != "# A\n" {
		t.Fatal("note lost after snapshot loss")
	}
	keys, err := cc.List(ctx, "notes/", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(keys.Changes) != 1 {
		t.Fatalf("snapshot loss minted duplicate remote records: %d", len(keys.Changes))
	}
}

// TestS3ExitConflictEditVersusTombstone: one device's local edit meets the
// other device's deletion; the edit is preserved as a single conflict note and
// the original is deleted, through the S3 adapter.
func TestS3ExitConflictEditVersusTombstone(t *testing.T) {
	ctx := context.Background()
	cc, _ := newServer(t, newFakeS3())
	a := newS3ExitRep(t, t.TempDir(), t.TempDir(), "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", cc, cc.Profile())
	b := newS3ExitRep(t, t.TempDir(), t.TempDir(), "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", cc, cc.Profile())

	if err := os.WriteFile(filepath.Join(a.root, "a.md"), []byte("# base\n"), 0644); err != nil {
		t.Fatal(err)
	}
	a.runQuiescent(t, ctx)
	b.runQuiescent(t, ctx)

	// B edits locally; A deletes and propagates the tombstone.
	if err := os.WriteFile(filepath.Join(b.root, "a.md"), []byte("# B edit\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(a.root, "a.md")); err != nil {
		t.Fatal(err)
	}
	a.runQuiescent(t, ctx)

	b.runQuiescent(t, ctx)
	if b.noteExists("a.md") {
		t.Fatal("B's original was not deleted")
	}
	conflicts := b.conflictNotes(t)
	if len(conflicts) != 1 {
		t.Fatalf("B preserved %d conflict notes, want 1: %v", len(conflicts), conflicts)
	}
	if b.noteBody(conflicts[0]) != "# B edit\n" {
		t.Fatal("conflict note does not carry B's edit")
	}
	b.runQuiescent(t, ctx)
	if got := b.conflictNotes(t); len(got) != 1 {
		t.Fatalf("re-run minted %d conflict notes, want 1", len(got))
	}
}

// TestS3ExitConflictDeleteVersusEdit: one device's deletion meets the other
// device's edit; the remote edit is preserved as a conflict note and the
// original is tombstoned, through the S3 adapter.
func TestS3ExitConflictDeleteVersusEdit(t *testing.T) {
	ctx := context.Background()
	cc, _ := newServer(t, newFakeS3())
	a := newS3ExitRep(t, t.TempDir(), t.TempDir(), "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", cc, cc.Profile())
	b := newS3ExitRep(t, t.TempDir(), t.TempDir(), "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", cc, cc.Profile())

	if err := os.WriteFile(filepath.Join(a.root, "a.md"), []byte("# base\n"), 0644); err != nil {
		t.Fatal(err)
	}
	a.runQuiescent(t, ctx)
	b.runQuiescent(t, ctx)

	// A edits and pushes; B deletes locally.
	if err := os.WriteFile(filepath.Join(a.root, "a.md"), []byte("# A edit\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(b.root, "a.md")); err != nil {
		t.Fatal(err)
	}
	a.runQuiescent(t, ctx)

	b.runQuiescent(t, ctx)
	conflicts := b.conflictNotes(t)
	if len(conflicts) != 1 {
		t.Fatalf("B preserved %d conflict notes, want 1: %v", len(conflicts), conflicts)
	}
	if b.noteBody(conflicts[0]) != "# A edit\n" {
		t.Fatal("conflict note does not carry A's edit")
	}
	idA, ok := a.noteID("a.md")
	if !ok {
		t.Fatal("original not indexed")
	}
	if rec := s3RemoteNote(t, cc, idA); !rec.Deleted {
		t.Fatal("the original remote record was not tombstoned")
	}
	b.runQuiescent(t, ctx)
	if got := b.conflictNotes(t); len(got) != 1 {
		t.Fatalf("re-run minted %d conflict notes, want 1", len(got))
	}
}

// TestS3ExitRecoveryIdempotent: applying a pulled tombstone writes exactly one
// recovery copy, and re-converging writes nothing new.
func TestS3ExitRecoveryIdempotent(t *testing.T) {
	ctx := context.Background()
	cc, _ := newServer(t, newFakeS3())
	a := newS3ExitRep(t, t.TempDir(), t.TempDir(), "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", cc, cc.Profile())
	b := newS3ExitRep(t, t.TempDir(), t.TempDir(), "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", cc, cc.Profile())

	if err := os.WriteFile(filepath.Join(a.root, "a.md"), []byte("# A\n"), 0644); err != nil {
		t.Fatal(err)
	}
	a.runQuiescent(t, ctx)
	b.runQuiescent(t, ctx)
	idA, ok := a.noteID("a.md")
	if !ok {
		t.Fatal("note not indexed")
	}

	if err := os.Remove(filepath.Join(b.root, "a.md")); err != nil {
		t.Fatal(err)
	}
	b.runQuiescent(t, ctx) // B pushes the tombstone
	a.runQuiescent(t, ctx) // A applies it and writes the recovery copy
	if got := a.recoveryFiles(t, idA); len(got) != 1 {
		t.Fatalf("A recovery copies = %v, want 1", got)
	}
	a.runQuiescent(t, ctx)
	b.runQuiescent(t, ctx)
	if got := a.recoveryFiles(t, idA); len(got) != 1 {
		t.Fatalf("recovery duplicated: %v", got)
	}
}

// TestS3ExitLocalCASRace: an external edit racing the pulled-tombstone delete
// survives the local revision CAS, and is then preserved as a conflict note —
// nothing is lost through the S3 adapter.
func TestS3ExitLocalCASRace(t *testing.T) {
	ctx := context.Background()
	cc, _ := newServer(t, newFakeS3())
	a := newS3ExitRep(t, t.TempDir(), t.TempDir(), "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", cc, cc.Profile())
	b := newS3ExitRep(t, t.TempDir(), t.TempDir(), "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", cc, cc.Profile())

	if err := os.WriteFile(filepath.Join(a.root, "a.md"), []byte("# base\n"), 0644); err != nil {
		t.Fatal(err)
	}
	a.runQuiescent(t, ctx)
	b.runQuiescent(t, ctx)

	// A deletes and propagates the tombstone.
	if err := os.Remove(filepath.Join(a.root, "a.md")); err != nil {
		t.Fatal(err)
	}
	a.runQuiescent(t, ctx)

	// B's scan observes the unchanged note, then a race rewrites it right after
	// the stable read: the apply-tombstone delete's CAS fails and the new edit
	// survives; the next cycle preserves it as a conflict note.
	b.withScanOpts(vaultfs.ScanOptions{
		AfterCandidate: func(path string) {
			if path == "a.md" {
				_ = os.WriteFile(filepath.Join(b.root, "a.md"), []byte("# newer local edit\n"), 0644)
			}
		},
	})
	b.runQuiescent(t, ctx)
	if b.noteExists("a.md") {
		t.Fatal("original note should converge to deleted")
	}
	b.withScanOpts(vaultfs.ScanOptions{})
	b.runQuiescent(t, ctx)
	a.runQuiescent(t, ctx)
	found := false
	for _, p := range b.conflictNotes(t) {
		if b.noteBody(p) == "# newer local edit\n" {
			found = true
		}
	}
	if !found {
		t.Fatal("the racing edit was lost: no conflict note carries it")
	}
}

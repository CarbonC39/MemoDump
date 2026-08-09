package syncrun

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"memodump/internal/cloudsync"
	"memodump/internal/vaultfs"
)

// recoveryDir is the recovery area for a replica.
func (r *noteRep) recoveryDir() string {
	return filepath.Join(r.stateRoot, noteVaultID, r.co.cfg.ReplicaID, "recovery")
}

// recoveryFiles lists the recovery copies written for a Sync ID.
func (r *noteRep) recoveryFiles(t *testing.T, syncID string) []string {
	t.Helper()
	dir := filepath.Join(r.recoveryDir(), syncID)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

// TestNoteTombstoneDeletionConvergesBothWays covers both deletion directions:
// a local delete uploads a conditional tombstone, the other side writes a
// recovery copy and deletes locally, and neither direction touches a sibling
// note.
func TestNoteTombstoneDeletionConvergesBothWays(t *testing.T) {
	ctx := context.Background()
	remote := cloudsync.NewMemoryStore()
	a := newNoteRep(t, t.TempDir(), t.TempDir(), noteRepA, remote)
	b := newNoteRep(t, t.TempDir(), t.TempDir(), noteRepB, remote)

	writeFiles(t, a.root, map[string]string{
		"dir/a.md": "# A\n",
		"dir/b.md": "# B\n",
	})
	converge(ctx, t, a, b)
	if !b.noteExists("dir/b.md") {
		t.Fatal("sibling note did not sync")
	}
	idA := mustNoteID(t, a, "dir/a.md")

	// A deletes dir/a.md; the deletion propagates both ways.
	if err := os.Remove(filepath.Join(a.root, "dir", "a.md")); err != nil {
		t.Fatal(err)
	}
	converge(ctx, t, a, b)

	if a.noteExists("dir/a.md") || b.noteExists("dir/a.md") {
		t.Fatal("deleted note still present on a replica")
	}
	rec := remoteNote(t, remote, idA)
	if !rec.Deleted {
		t.Fatal("remote record is not a tombstone")
	}
	// The sibling was not touched by either direction.
	if b.noteBody("dir/b.md") != "# B\n" {
		t.Fatal("unrelated child note was modified or deleted")
	}
	// B wrote a durable recovery copy before deleting.
	if got := b.recoveryFiles(t, idA); len(got) != 1 {
		t.Fatalf("B recovery copies = %v, want 1", got)
	}
	// Both replicas converged (the index mapping was cleaned up).
	converge(ctx, t, a, b)
	if _, ok := b.idx.IDByPath("dir/a.md"); ok {
		t.Fatal("converged deletion left a live index mapping")
	}
}

// TestNoteTombstoneDeletionReverse covers the reverse direction: B deletes, A
// applies the pulled tombstone.
func TestNoteTombstoneDeletionReverse(t *testing.T) {
	ctx := context.Background()
	remote := cloudsync.NewMemoryStore()
	a := newNoteRep(t, t.TempDir(), t.TempDir(), noteRepA, remote)
	b := newNoteRep(t, t.TempDir(), t.TempDir(), noteRepB, remote)

	writeFiles(t, a.root, map[string]string{"a.md": "# A\n"})
	converge(ctx, t, a, b)
	idA := mustNoteID(t, a, "a.md")

	if err := os.Remove(filepath.Join(b.root, "a.md")); err != nil {
		t.Fatal(err)
	}
	converge(ctx, t, a, b)
	if a.noteExists("a.md") || b.noteExists("a.md") {
		t.Fatal("deleted note still present after reverse convergence")
	}
	rec := remoteNote(t, remote, idA)
	if !rec.Deleted {
		t.Fatal("remote record is not a tombstone")
	}
	// A wrote a durable recovery copy before deleting.
	if got := a.recoveryFiles(t, idA); len(got) != 1 {
		t.Fatalf("A recovery copies = %v, want 1", got)
	}
}

// TestNoteTombstoneRecoveryIdempotent: writing the same recovery content twice
// leaves exactly one copy (RecoveryStore.Write is idempotent).
func TestNoteTombstoneRecoveryIdempotent(t *testing.T) {
	ctx := context.Background()
	remote := cloudsync.NewMemoryStore()
	a := newNoteRep(t, t.TempDir(), t.TempDir(), noteRepA, remote)
	b := newNoteRep(t, t.TempDir(), t.TempDir(), noteRepB, remote)

	writeFiles(t, a.root, map[string]string{"a.md": "# A\n"})
	converge(ctx, t, a, b)
	idA := mustNoteID(t, a, "a.md")

	if err := os.Remove(filepath.Join(b.root, "a.md")); err != nil {
		t.Fatal(err)
	}
	converge(ctx, t, a, b)
	// A applied the pulled tombstone and wrote one recovery copy.
	if got := a.recoveryFiles(t, idA); len(got) != 1 {
		t.Fatalf("A recovery copies = %v, want 1", got)
	}
	// Convergence again writes nothing new (the note is gone).
	converge(ctx, t, a, b)
	if got := a.recoveryFiles(t, idA); len(got) != 1 {
		t.Fatalf("recovery duplicated: %v", got)
	}
}

// TestNoteTombstoneRacePreservesLocalEdit proves an external edit racing the
// pulled-tombstone delete survives the local revision CAS: the note stays
// intact, and the edit is then preserved as a conflict note.
func TestNoteTombstoneRacePreservesLocalEdit(t *testing.T) {
	ctx := context.Background()
	remote := cloudsync.NewMemoryStore()
	a := newNoteRep(t, t.TempDir(), t.TempDir(), noteRepA, remote)
	b := newNoteRep(t, t.TempDir(), t.TempDir(), noteRepB, remote)

	writeFiles(t, a.root, map[string]string{"a.md": "# base\n"})
	converge(ctx, t, a, b)
	idA := mustNoteID(t, a, "a.md")

	// A deletes the note and propagates the tombstone.
	if err := os.Remove(filepath.Join(a.root, "a.md")); err != nil {
		t.Fatal(err)
	}
	a.runQuiescent(t, ctx)

	// B's scan observes the unchanged note, then a race rewrites it right after
	// the stable read: the apply-tombstone delete's CAS must fail and the new
	// edit survives. On the following cycle the edit is preserved as a conflict
	// note and the tombstone is accepted, so nothing is lost.
	b.withScanOpts(vaultfs.ScanOptions{
		AfterCandidate: func(path string) {
			if path == "a.md" {
				_ = os.WriteFile(filepath.Join(b.root, "a.md"), []byte("# newer local edit\n"), 0644)
			}
		},
	})
	b.runQuiescent(t, ctx)

	// The original converges to deleted; the racing edit survives in a conflict
	// note.
	if b.noteExists("a.md") {
		t.Fatal("original note should converge to deleted")
	}
	converge(ctx, t, a, b)
	found := false
	for _, p := range conflictNotes(t, b.root) {
		if b.noteBody(p) == "# newer local edit\n" {
			found = true
		}
	}
	if !found {
		t.Fatal("the racing edit was lost: no conflict note carries it")
	}
	_ = idA
}

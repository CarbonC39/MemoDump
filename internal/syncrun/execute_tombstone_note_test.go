package syncrun

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"memodump/internal/cloudsync"
	"memodump/internal/vaultfs"
)

// recoveryDir is the recovery area for a replica.
func (r *noteRep) recoveryDir() string {
	return filepath.Join(r.stateRoot, noteVaultID, r.co.cfg.ReplicaID, "recovery")
}

// recoveryFiles lists the recovery Markdown copies written for a Sync ID (the
// .path sidecars are metadata, not copies).
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
		if strings.HasSuffix(e.Name(), ".md") {
			out = append(out, e.Name())
		}
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
// pulled-tombstone delete survives the local revision CAS: the edit is injected
// AFTER the local observation (which captured the old revision) and BEFORE the
// delete's revision CAS, so the delete genuinely fails and the new edit
// survives. On the following cycle the edit is preserved as a conflict note and
// the tombstone is accepted, so nothing is lost.
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

	// B's scan observes the unchanged note, then the fault fires between the
	// observation and the delete, rewriting the note: the apply-tombstone
	// delete's CAS must fail, deferring the note (Retry) and leaving the new
	// edit in place.
	b.co.cfg.TestFault = func(point string) error {
		if point == "tombstone:before-delete" {
			_ = os.WriteFile(filepath.Join(b.root, "a.md"), []byte("# newer local edit\n"), 0644)
		}
		return nil
	}
	if st, err := b.co.Run(ctx); err != nil {
		t.Fatal(err)
	} else if st.Retry == 0 {
		t.Fatalf("cycle = %+v, want the tombstone apply deferred as a retry", st)
	}
	b.co.cfg.TestFault = nil
	if b.noteBody("a.md") != "# newer local edit\n" {
		t.Fatal("the injected edit did not survive the delete CAS")
	}

	// The original converges to deleted; the racing edit survives in a conflict
	// note.
	converge(ctx, t, a, b)
	if b.noteExists("a.md") {
		t.Fatal("original note should converge to deleted")
	}
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

// TestNoteRecoveryRestoresAfterConvergedDeletion: after a pulled tombstone
// deletes a note and the converged-deletion cleanup drops the index mapping,
// the recovery copy still carries the original path and can be restored.
func TestNoteRecoveryRestoresAfterConvergedDeletion(t *testing.T) {
	ctx := context.Background()
	remote := cloudsync.NewMemoryStore()
	a := newNoteRep(t, t.TempDir(), t.TempDir(), noteRepA, remote)
	b := newNoteRep(t, t.TempDir(), t.TempDir(), noteRepB, remote)
	writeFiles(t, a.root, map[string]string{"dir/a.md": "# recover me\n"})
	converge(ctx, t, a, b)

	// A deletes dir/a.md; B applies the pulled tombstone and its index mapping
	// is cleaned up on the converged deletion.
	if err := os.Remove(filepath.Join(a.root, "dir", "a.md")); err != nil {
		t.Fatal(err)
	}
	converge(ctx, t, a, b)
	if _, ok := b.idx.IDByPath("dir/a.md"); ok {
		t.Fatal("converged deletion left the index mapping")
	}

	// The recovery copy kept the original path.
	copies, err := b.co.recovery.ListAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(copies) != 1 || copies[0].Path != "dir/a.md" || copies[0].Markdown != "# recover me\n" {
		t.Fatalf("recovery copies = %+v, want dir/a.md with the markdown", copies)
	}

	// Restore writes the markdown back at the recorded path.
	repo, err := vaultfs.New(b.root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Apply(copies[0].Path, copies[0].Markdown, ""); err != nil {
		t.Fatal(err)
	}
	if b.noteBody("dir/a.md") != "# recover me\n" {
		t.Fatal("restored note missing or wrong")
	}
}

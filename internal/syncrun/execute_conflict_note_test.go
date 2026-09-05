package syncrun

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"memodump/internal/cloudsync"
)

// conflictNotes counts the vault files whose name matches the deterministic
// conflict form, proving no duplicate/suffixed conflict note was minted.
func conflictNotes(t *testing.T, root string) []string {
	t.Helper()
	var found []string
	entries, err := os.ReadDir(root)
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

// TestNoteConflictConcurrentEdits covers preserve_local_then_pull: divergent
// simultaneous edits keep both documents, the remote wins the original path,
// and retries create no duplicate conflict note.
func TestNoteConflictConcurrentEdits(t *testing.T) {
	ctx := context.Background()
	remote := cloudsync.NewMemoryStore()
	a := newNoteRep(t, t.TempDir(), t.TempDir(), noteRepA, remote)
	b := newNoteRep(t, t.TempDir(), t.TempDir(), noteRepB, remote)

	writeFiles(t, a.root, map[string]string{"a.md": "# base\n"})
	converge(ctx, t, a, b)
	idA := mustNoteID(t, a, "a.md")

	// Both edit the same note to different bodies.
	writeFiles(t, a.root, map[string]string{"a.md": "# A edit\n"})
	writeFiles(t, b.root, map[string]string{"a.md": "# B edit\n"})
	converge(ctx, t, a, b)

	// The remote edit won the original path on B; B's edit is preserved.
	if b.noteBody("a.md") != "# A edit\n" {
		t.Fatalf("B original = %q, want A's edit", b.noteBody("a.md"))
	}
	if got := conflictNotes(t, b.root); len(got) != 1 {
		t.Fatalf("B did not preserve its edit as a single conflict note: %v", got)
	}
	// A converges on B's conflict note with no duplicate.
	if got := conflictNotes(t, a.root); len(got) != 1 {
		t.Fatalf("A did not pull B's conflict note (got %d): %v", len(got), got)
	}
	// The conflict record exists remotely and carries B's edit.
	rec := remoteNote(t, remote, mustNoteID(t, b, conflictPathOf(t, b.root)))
	if rec.Markdown != "# B edit\n" {
		t.Fatalf("remote conflict markdown = %q, want B's edit", rec.Markdown)
	}
	if idA != mustNoteID(t, b, "a.md") {
		t.Fatal("the original identity changed after the conflict")
	}
}

// conflictPathOf returns the sole conflict-note path in a vault.
func conflictPathOf(t *testing.T, root string) string {
	t.Helper()
	notes := conflictNotes(t, root)
	if len(notes) != 1 {
		t.Fatalf("want exactly one conflict note, got %v", notes)
	}
	return notes[0]
}

// TestNoteConflictEditVersusTombstone covers preserve_local_then_delete: a
// local edit facing a remote tombstone is preserved as a conflict note before
// the original is deleted. (The remote side that only receives the tombstone
// without editing is apply_tombstone, wired in R2.4.)
func TestNoteConflictEditVersusTombstone(t *testing.T) {
	ctx := context.Background()
	remote := cloudsync.NewMemoryStore()
	a := newNoteRep(t, t.TempDir(), t.TempDir(), noteRepA, remote)
	b := newNoteRep(t, t.TempDir(), t.TempDir(), noteRepB, remote)

	writeFiles(t, a.root, map[string]string{"a.md": "# base\n"})
	converge(ctx, t, a, b)
	idA := mustNoteID(t, a, "a.md")

	// The remote original becomes a tombstone (the other device deleted it).
	writeFiles(t, b.root, map[string]string{"a.md": "# B edit\n"})
	tomb := &cloudsync.NoteRecord{
		SchemaVersion: cloudsync.NoteSchemaVersion, SyncID: idA, Path: "a.md", Deleted: true,
	}
	data, err := tomb.Serialize()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := remote.Replace(ctx, cloudsync.NoteKey(idA), data, "1"); err != nil {
		t.Fatal(err)
	}

	// B edits the note: its edit is preserved as a conflict, then the original
	// is deleted locally.
	b.runQuiescent(t, ctx)
	if b.noteExists("a.md") {
		t.Fatal("B's original note was not deleted")
	}
	if got := conflictNotes(t, b.root); len(got) != 1 {
		t.Fatalf("B did not preserve its edit as a single conflict note: %v", got)
	}
	if b.noteBody(conflictPathOf(t, b.root)) != "# B edit\n" {
		t.Fatal("conflict note does not carry B's edit")
	}
	// Re-running B stays converged with no duplicate conflict note.
	b.runQuiescent(t, ctx)
	if got := conflictNotes(t, b.root); len(got) != 1 {
		t.Fatalf("B has %d conflict notes after re-run, want 1: %v", len(got), got)
	}
}

// TestNoteConflictLocalAbsentVersusRemoteEdit covers
// preserve_remote_then_tombstone: a locally-deleted note whose remote was
// edited is preserved as a conflict note and the original is tombstoned. (The
// editing device's own convergence needs apply_tombstone, wired in R2.4.)
func TestNoteConflictLocalAbsentVersusRemoteEdit(t *testing.T) {
	ctx := context.Background()
	remote := cloudsync.NewMemoryStore()
	a := newNoteRep(t, t.TempDir(), t.TempDir(), noteRepA, remote)
	b := newNoteRep(t, t.TempDir(), t.TempDir(), noteRepB, remote)

	writeFiles(t, a.root, map[string]string{"a.md": "# base\n"})
	converge(ctx, t, a, b)

	// A edits the note; B deletes it locally (the file vanishes, the index
	// keeps the mapping so the deletion can propagate).
	writeFiles(t, a.root, map[string]string{"a.md": "# A edit\n"})
	if err := os.Remove(filepath.Join(b.root, "a.md")); err != nil {
		t.Fatal(err)
	}
	a.runQuiescent(t, ctx) // A pushes its edit

	// B sees local absence versus the remote edit: preserve the remote edit as
	// a conflict note and tombstone the original.
	b.runQuiescent(t, ctx)
	if got := conflictNotes(t, b.root); len(got) != 1 {
		t.Fatalf("B did not preserve the remote edit as a conflict note: %v", got)
	}
	if b.noteBody(conflictPathOf(t, b.root)) != "# A edit\n" {
		t.Fatal("conflict note does not carry A's edit")
	}
	rec := remoteNote(t, remote, mustNoteID(t, a, "a.md"))
	if !rec.Deleted {
		t.Fatal("the original remote record was not tombstoned")
	}
	// B's conflict note is replay-safe across a re-run.
	b.runQuiescent(t, ctx)
	if got := conflictNotes(t, b.root); len(got) != 1 {
		t.Fatalf("B minted %d conflict notes after re-run, want 1: %v", len(got), got)
	}
}

// TestNoteConflictInjectedRestartReusesIdentity proves replay-safety: a stop
// injected between the local conflict create and the remote conflict create is
// resumed with the SAME conflict identity and path — never a second one.
func TestNoteConflictInjectedRestartReusesIdentity(t *testing.T) {
	ctx := context.Background()
	remote := cloudsync.NewMemoryStore()
	a := newNoteRep(t, t.TempDir(), t.TempDir(), noteRepA, remote)
	b := newNoteRep(t, t.TempDir(), t.TempDir(), noteRepB, remote)

	writeFiles(t, a.root, map[string]string{"a.md": "# base\n"})
	converge(ctx, t, a, b)
	writeFiles(t, a.root, map[string]string{"a.md": "# A edit\n"})
	writeFiles(t, b.root, map[string]string{"a.md": "# B edit\n"})
	a.runQuiescent(t, ctx) // A pushes its edit

	// B's next cycle starts the conflict: reserve, save, create the local
	// conflict note, then fails creating the REMOTE conflict record.
	remote.ArmFault("create", &cloudsync.StoreError{Kind: cloudsync.ErrRetryableTransport, Message: "injected stop"})
	if _, err := b.co.Run(ctx); err == nil {
		t.Fatal("cycle should fail at the remote conflict create")
	}
	if got := conflictNotes(t, b.root); len(got) != 1 {
		t.Fatalf("stop before remote conflict create left %d conflict notes, want 1: %v", len(got), got)
	}
	conflictPath := conflictNotes(t, b.root)[0]

	// Restart: the same conflict identity and path are reused.
	b.runQuiescent(t, ctx)
	if got := conflictNotes(t, b.root); len(got) != 1 {
		t.Fatalf("restart minted %d conflict notes, want 1: %v", len(got), got)
	}
	if got := conflictNotes(t, b.root)[0]; got != conflictPath {
		t.Fatalf("restart reused a different conflict path: %q vs %q", got, conflictPath)
	}
	// The original converged to A's edit and the remote holds exactly one
	// conflict record for B's edit.
	converge(ctx, t, a, b)
	if b.noteBody("a.md") != "# A edit\n" {
		t.Fatalf("original = %q, want A's edit", b.noteBody("a.md"))
	}
	if got := conflictNotes(t, b.root); len(got) != 1 {
		t.Fatalf("final B conflict notes = %v, want 1", got)
	}
}

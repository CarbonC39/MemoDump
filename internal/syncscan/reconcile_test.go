package syncscan

import (
	"os"
	"path/filepath"
	"testing"

	"memodump/internal/cloudsync"
	"memodump/internal/syncindex"
	"memodump/internal/syncstate"
	"memodump/internal/vaultfs"
)

func writeNote(t *testing.T, root, rel, content string) {
	t.Helper()
	abs := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func enableVault(t *testing.T, root string) *syncindex.Store {
	t.Helper()
	s, err := syncindex.Enable(root)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func openState(t *testing.T, dir string) *syncstate.Store {
	t.Helper()
	s, err := syncstate.Open(dir, syncstate.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func syncIDFor(t *testing.T, idx *syncindex.Store, path string) string {
	t.Helper()
	id, ok := idx.FindByPath(path)
	if !ok {
		t.Fatalf("path %q not indexed", path)
	}
	return id
}

func baseline(t *testing.T, st *syncstate.Store, syncID, localHash string) {
	t.Helper()
	if err := syncstate.PutBaseline(st, syncID, syncstate.Baseline{LocalHash: localHash}); err != nil {
		t.Fatal(err)
	}
}

func reconcileScan(t *testing.T, root string, idx *syncindex.Store, st *syncstate.Store, opts vaultfs.ScanOptions) *Reconciliation {
	t.Helper()
	res, err := vaultfs.Scan(root, opts)
	if err != nil {
		t.Fatal(err)
	}
	r, err := Reconcile(res, idx, st)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func reconcile(t *testing.T, root string, idx *syncindex.Store, st *syncstate.Store) *Reconciliation {
	t.Helper()
	return reconcileScan(t, root, idx, st, vaultfs.ScanOptions{})
}

func entityState(t *testing.T, r *Reconciliation, syncID string) Entity {
	t.Helper()
	for _, e := range r.Entities {
		if e.SyncID == syncID {
			return e
		}
	}
	t.Fatalf("entity %s not in reconciliation", syncID)
	return Entity{}
}

func newPaths(r *Reconciliation) []string {
	var out []string
	for _, n := range r.New {
		out = append(out, n.Path)
	}
	return out
}

func TestReconcileUnchangedAndLocalOnly(t *testing.T) {
	root := t.TempDir()
	writeNote(t, root, "a.md", "# A\n")
	writeNote(t, root, "b.md", "# B\n")
	idx := enableVault(t, root)
	st := openState(t, t.TempDir())

	before := st.Len()
	baseline(t, st, syncIDFor(t, idx, "a.md"), vaultfs.LocalHash("# A\n"))
	// Created after enable: c.md and empty are unindexed and need identity.
	writeNote(t, root, "c.md", "# C\n")
	if err := os.MkdirAll(filepath.Join(root, "empty"), 0755); err != nil {
		t.Fatal(err)
	}

	r := reconcile(t, root, idx, st)
	if e := entityState(t, r, syncIDFor(t, idx, "a.md")); e.State != StateUnchanged {
		t.Fatalf("a.md = %v, want unchanged", e.State)
	}
	if e := entityState(t, r, syncIDFor(t, idx, "b.md")); e.State != StateLocalOnly {
		t.Fatalf("b.md = %v, want local-only", e.State)
	}
	got := newPaths(r)
	if len(got) != 2 || got[0] != "c.md" || got[1] != "empty" {
		t.Fatalf("new = %v", got)
	}
	// Ordinary note changes never append dirty WAL rows: the only durable state
	// is the baseline the test itself wrote.
	if st.Len() != before+1 {
		t.Fatalf("reconcile appended %d WAL rows (want 0)", st.Len()-before-1)
	}
}

func TestReconcileModified(t *testing.T) {
	root := t.TempDir()
	writeNote(t, root, "a.md", "v1")
	idx := enableVault(t, root)
	st := openState(t, t.TempDir())
	baseline(t, st, syncIDFor(t, idx, "a.md"), vaultfs.LocalHash("v1"))

	writeNote(t, root, "a.md", "v2")
	r := reconcile(t, root, idx, st)
	e := entityState(t, r, syncIDFor(t, idx, "a.md"))
	if e.State != StateModified {
		t.Fatalf("a.md = %v, want modified", e.State)
	}
	if e.LocalHash != vaultfs.LocalHash("v2") {
		t.Fatalf("a.md local hash = %s", e.LocalHash)
	}
}

func TestReconcileLocallyDeleted(t *testing.T) {
	root := t.TempDir()
	writeNote(t, root, "a.md", "v1")
	idx := enableVault(t, root)
	st := openState(t, t.TempDir())
	baseline(t, st, syncIDFor(t, idx, "a.md"), vaultfs.LocalHash("v1"))

	if err := os.Remove(filepath.Join(root, "a.md")); err != nil {
		t.Fatal(err)
	}
	r := reconcile(t, root, idx, st)
	if e := entityState(t, r, syncIDFor(t, idx, "a.md")); e.State != StateLocallyDeleted {
		t.Fatalf("a.md = %v, want locally-deleted", e.State)
	}
}

func TestReconcileOfflineRename(t *testing.T) {
	root := t.TempDir()
	writeNote(t, root, "a.md", "same content")
	idx := enableVault(t, root)
	st := openState(t, t.TempDir())
	baseline(t, st, syncIDFor(t, idx, "a.md"), vaultfs.LocalHash("same content"))

	// The note moved while the app was closed, content unchanged.
	if err := os.Remove(filepath.Join(root, "a.md")); err != nil {
		t.Fatal(err)
	}
	writeNote(t, root, "b.md", "same content")

	r := reconcile(t, root, idx, st)
	e := entityState(t, r, syncIDFor(t, idx, "a.md"))
	if e.State != StateRenamed || e.NewPath != "b.md" {
		t.Fatalf("a.md = %+v, want renamed -> b.md", e)
	}
	// The renamed path is claimed by the old Sync ID, not a new one.
	if got := newPaths(r); len(got) != 0 {
		t.Fatalf("renamed path listed as new: %v", got)
	}
}

func TestReconcileInAppRename(t *testing.T) {
	root := t.TempDir()
	writeNote(t, root, "a.md", "content")
	idx := enableVault(t, root)
	st := openState(t, t.TempDir())
	baseline(t, st, syncIDFor(t, idx, "a.md"), vaultfs.LocalHash("content"))

	// An in-app rename updates the index in the same logical transaction as
	// the filesystem move; the next scan sees an ordinary, unchanged entity.
	syncID := syncIDFor(t, idx, "a.md")
	if err := os.Rename(filepath.Join(root, "a.md"), filepath.Join(root, "b.md")); err != nil {
		t.Fatal(err)
	}
	if err := idx.UpdatePath(syncID, "b.md"); err != nil {
		t.Fatal(err)
	}

	r := reconcile(t, root, idx, st)
	e := entityState(t, r, syncID)
	if e.State != StateUnchanged {
		t.Fatalf("in-app renamed note = %v, want unchanged", e.State)
	}
}

func TestReconcileAmbiguousWithoutBaseline(t *testing.T) {
	root := t.TempDir()
	writeNote(t, root, "a.md", "A")
	writeNote(t, root, "c.md", "C")
	idx := enableVault(t, root)
	st := openState(t, t.TempDir())
	baseline(t, st, syncIDFor(t, idx, "a.md"), vaultfs.LocalHash("A"))
	// c.md has no baseline (a never-synced entity).

	for _, name := range []string{"a.md", "c.md"} {
		if err := os.Remove(filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}
	r := reconcile(t, root, idx, st)
	if e := entityState(t, r, syncIDFor(t, idx, "a.md")); e.State != StateLocallyDeleted {
		t.Fatalf("a.md (baseline) = %v, want locally-deleted", e.State)
	}
	// An indexed path gone with no baseline is ambiguous: probe, never delete.
	e := entityState(t, r, syncIDFor(t, idx, "c.md"))
	if e.State != StateAmbiguous || !e.Probe {
		t.Fatalf("c.md (no baseline) = %+v, want ambiguous+probe", e)
	}
}

func TestReconcileMissingAppDataProbesEverything(t *testing.T) {
	root := t.TempDir()
	writeNote(t, root, "a.md", "A")
	writeNote(t, root, "b.md", "B")
	idx := enableVault(t, root)
	// The replica has no durable state at all: its AppData was lost (or sync
	// was never run). Every indexed entity is baseline-unknown.
	st := openState(t, t.TempDir())
	writeNote(t, root, "c.md", "C") // new local entity during the outage

	r := reconcile(t, root, idx, st)
	if !r.BaselineUnknown {
		t.Fatal("empty replica state not reported baseline-unknown")
	}
	for _, path := range []string{"a.md", "b.md"} {
		e := entityState(t, r, syncIDFor(t, idx, path))
		if e.State != StateBaselineUnknown || !e.Probe {
			t.Fatalf("%s = %+v, want baseline-unknown+probe", path, e)
		}
	}
	// No destructive state ever appears: nothing is deleted, tombstoned, or
	// considered locally-modified against a forgotten baseline.
	for _, e := range r.Entities {
		switch e.State {
		case StateLocallyDeleted, StateModified, StateUnchanged, StateRenamed:
			t.Fatalf("destructive/guessed state in missing-AppData scan: %+v", e)
		}
	}
	if got := newPaths(r); len(got) != 1 || got[0] != "c.md" {
		t.Fatalf("new = %v, want c.md only", got)
	}
}

func TestReconcileAmbiguousIdenticalHashesAreNotRenames(t *testing.T) {
	root := t.TempDir()
	writeNote(t, root, "a.md", "identical")
	writeNote(t, root, "b.md", "identical")
	idx := enableVault(t, root)
	st := openState(t, t.TempDir())
	hash := vaultfs.LocalHash("identical")
	baseline(t, st, syncIDFor(t, idx, "a.md"), hash)
	baseline(t, st, syncIDFor(t, idx, "b.md"), hash)

	for _, name := range []string{"a.md", "b.md"} {
		if err := os.Remove(filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}
	writeNote(t, root, "c.md", "identical")

	r := reconcile(t, root, idx, st)
	// Two identical disappearances plus one identical newcomer: ambiguous, so
	// no rename; the newcomer is a copy and the originals are deletions.
	if e := entityState(t, r, syncIDFor(t, idx, "a.md")); e.State != StateLocallyDeleted {
		t.Fatalf("a.md = %v, want locally-deleted", e.State)
	}
	if e := entityState(t, r, syncIDFor(t, idx, "b.md")); e.State != StateLocallyDeleted {
		t.Fatalf("b.md = %v, want locally-deleted", e.State)
	}
	if got := newPaths(r); len(got) != 1 || got[0] != "c.md" {
		t.Fatalf("new = %v, want c.md", got)
	}
}

func TestReconcileKindMismatchIsBlocked(t *testing.T) {
	root := t.TempDir()
	// A directory whose name looks like a note path is a valid folder entity;
	// when it is replaced by a note file at the same path, the index and the
	// filesystem disagree about what the path is, so nothing may be inferred.
	if err := os.MkdirAll(filepath.Join(root, "x.md"), 0755); err != nil {
		t.Fatal(err)
	}
	idx := enableVault(t, root)
	st := openState(t, t.TempDir())
	if err := os.RemoveAll(filepath.Join(root, "x.md")); err != nil {
		t.Fatal(err)
	}
	writeNote(t, root, "x.md", "now a file")

	r := reconcile(t, root, idx, st)
	if e := entityState(t, r, syncIDFor(t, idx, "x.md")); e.State != StateBlocked {
		t.Fatalf("kind mismatch = %v, want blocked", e.State)
	}
}

func TestReconcileSymlinkIsBlocked(t *testing.T) {
	root := t.TempDir()
	writeNote(t, root, "a.md", "content")
	idx := enableVault(t, root)
	st := openState(t, t.TempDir())
	baseline(t, st, syncIDFor(t, idx, "a.md"), vaultfs.LocalHash("content"))

	// An indexed note replaced by a symlink is blocked, never deleted.
	if err := os.Remove(filepath.Join(root, "a.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(t.TempDir(), "elsewhere.md"), filepath.Join(root, "a.md")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	r := reconcile(t, root, idx, st)
	if e := entityState(t, r, syncIDFor(t, idx, "a.md")); e.State != StateBlocked {
		t.Fatalf("symlinked note = %v, want blocked", e.State)
	}
}

func TestReconcileUnstableIsDeferred(t *testing.T) {
	root := t.TempDir()
	writeNote(t, root, "a.md", "v1")
	idx := enableVault(t, root)
	st := openState(t, t.TempDir())
	baseline(t, st, syncIDFor(t, idx, "a.md"), vaultfs.LocalHash("v1"))

	r := reconcileScan(t, root, idx, st, vaultfs.ScanOptions{BetweenPasses: func() {
		writeNote(t, root, "a.md", "v2 a different length than v1")
	}})
	e := entityState(t, r, syncIDFor(t, idx, "a.md"))
	if e.State != StateUnstable {
		t.Fatalf("unstable note = %v, want unstable", e.State)
	}
}

func TestApplyIdentityAddsNewAndAppliesRename(t *testing.T) {
	root := t.TempDir()
	writeNote(t, root, "a.md", "shared")
	idx := enableVault(t, root)
	st := openState(t, t.TempDir())
	baseline(t, st, syncIDFor(t, idx, "a.md"), vaultfs.LocalHash("shared"))

	// Offline rename a.md -> b.md (same content), plus a genuinely new note.
	if err := os.Remove(filepath.Join(root, "a.md")); err != nil {
		t.Fatal(err)
	}
	writeNote(t, root, "b.md", "shared")
	writeNote(t, root, "new.md", "brand new")

	r := reconcile(t, root, idx, st)
	oldID := syncIDFor(t, idx, "a.md") // before ApplyIdentity renames it
	if err := ApplyIdentity(r, idx); err != nil {
		t.Fatal(err)
	}
	if got, _ := idx.FindByPath("b.md"); got != oldID {
		t.Fatalf("b.md has %s, want old sync ID %s", got, oldID)
	}
	if _, ok := idx.FindByPath("a.md"); ok {
		t.Fatal("old path still indexed after rename")
	}
	if _, ok := idx.FindByPath("new.md"); !ok {
		t.Fatal("new note did not receive a Sync ID")
	}

	// The identity change is durable: reloading the index sees it.
	reloaded, err := syncindex.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := reloaded.FindByPath("b.md"); got != oldID {
		t.Fatalf("reloaded b.md has %s, want %s", got, oldID)
	}
}

func TestReconcileCaseOnlyRename(t *testing.T) {
	root := t.TempDir()
	writeNote(t, root, "Note.md", "content")
	idx := enableVault(t, root)
	st := openState(t, t.TempDir())
	baseline(t, st, syncIDFor(t, idx, "Note.md"), vaultfs.LocalHash("content"))

	// A case-only rename is content-identical, so rename inference works
	// regardless of filesystem case sensitivity — but only when the filesystem
	// actually lets both names differ (a case-insensitive FS already updated
	// the same file, leaving nothing to detect).
	if err := os.Remove(filepath.Join(root, "Note.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "note.md"), []byte("content"), 0644); err != nil {
		t.Fatal(err)
	}
	r := reconcile(t, root, idx, st)
	if e := entityState(t, r, syncIDFor(t, idx, "Note.md")); e.State != StateRenamed || e.NewPath != "note.md" {
		t.Fatalf("case-only rename = %+v, want renamed -> note.md", e)
	}
	if got := newPaths(r); len(got) != 0 {
		t.Fatalf("case-only renamed path listed as new: %v", got)
	}
}

func TestReconcileEmptyFolderLocalOnly(t *testing.T) {
	root := t.TempDir()
	writeNote(t, root, "a.md", "A")
	idx := enableVault(t, root)
	st := openState(t, t.TempDir())
	if err := os.MkdirAll(filepath.Join(root, "empty"), 0755); err != nil {
		t.Fatal(err)
	}
	r := reconcile(t, root, idx, st)
	// The empty folder is unindexed: it is a real folder entity needing identity.
	if got := newPaths(r); len(got) != 1 || got[0] != "empty" {
		t.Fatalf("new = %v, want empty folder", got)
	}
}

func TestReconcileSymlinkedDirSubtreeIsBlocked(t *testing.T) {
	root := t.TempDir()
	writeNote(t, root, "dir/a.md", "content")
	idx := enableVault(t, root)
	st := openState(t, t.TempDir())
	baseline(t, st, syncIDFor(t, idx, "dir/a.md"), vaultfs.LocalHash("content"))

	// The directory becomes a symlink to a location outside the vault: the
	// note under it is blocked, never judged locally-deleted.
	if err := os.RemoveAll(filepath.Join(root, "dir")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(root, "dir")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	r := reconcile(t, root, idx, st)
	if e := entityState(t, r, syncIDFor(t, idx, "dir/a.md")); e.State != StateBlocked {
		t.Fatalf("note under symlinked dir = %v, want blocked (not locally-deleted)", e.State)
	}
}

func TestReconcileSyncedFolderDeletion(t *testing.T) {
	root := t.TempDir()
	writeNote(t, root, "a.md", "A")
	if err := os.MkdirAll(filepath.Join(root, "empty"), 0755); err != nil {
		t.Fatal(err)
	}
	idx := enableVault(t, root)
	st := openState(t, t.TempDir())
	baseline(t, st, syncIDFor(t, idx, "a.md"), vaultfs.LocalHash("A"))
	// A folder's baseline has an empty LocalHash by nature; baseline presence
	// must not be inferred from the hash being empty.
	baseline(t, st, syncIDFor(t, idx, "empty"), "")

	if err := os.RemoveAll(filepath.Join(root, "empty")); err != nil {
		t.Fatal(err)
	}
	r := reconcile(t, root, idx, st)
	if e := entityState(t, r, syncIDFor(t, idx, "empty")); e.State != StateLocallyDeleted {
		t.Fatalf("synced empty folder deletion = %v, want locally-deleted", e.State)
	}
}

func TestEnableAndScannerShareIgnoreRules(t *testing.T) {
	root := t.TempDir()
	writeNote(t, root, "real.md", "x")
	writeNote(t, root, "~$lock.md", "office lock")

	// The office lock file must never gain a Sync ID at enable...
	idx := enableVault(t, root)
	if _, ok := idx.FindByPath("~$lock.md"); ok {
		t.Fatal("~$lock.md gained a Sync ID at enable")
	}
	// ...and the authoritative scan agrees it is not a note.
	res, err := vaultfs.Scan(root, vaultfs.ScanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range res.Notes {
		if n.Path == "~$lock.md" {
			t.Fatal("scanner observed the office lock file")
		}
	}
	if len(res.Notes) != 1 || res.Notes[0].Path != "real.md" {
		t.Fatalf("notes = %+v, want real.md only", res.Notes)
	}
}

func TestApplyIdentityFailsWithoutPartialState(t *testing.T) {
	root := t.TempDir()
	writeNote(t, root, "a.md", "A")
	writeNote(t, root, "b.md", "B")
	idx := enableVault(t, root)
	aID := syncIDFor(t, idx, "a.md")
	bID := syncIDFor(t, idx, "b.md")

	// A reconciliation in which two renames claim the same new path: the second
	// is invalid, and the whole batch must fail without applying the first.
	r := &Reconciliation{Entities: []Entity{
		{SyncID: aID, Kind: cloudsync.KindNote, Path: "a.md", State: StateRenamed, NewPath: "c.md"},
		{SyncID: bID, Kind: cloudsync.KindNote, Path: "b.md", State: StateRenamed, NewPath: "c.md"},
	}}
	if err := ApplyIdentity(r, idx); err == nil {
		t.Fatal("conflicting rename batch succeeded")
	}
	// Nothing was applied: both entities keep their paths and no c.md exists.
	if got, _ := idx.FindByPath("a.md"); got != aID {
		t.Fatalf("a.md = %s, want %s (partial rename applied)", got, aID)
	}
	if got, _ := idx.FindByPath("b.md"); got != bID {
		t.Fatalf("b.md = %s, want %s", got, bID)
	}
	if _, ok := idx.FindByPath("c.md"); ok {
		t.Fatal("failed batch left c.md indexed")
	}
	// The store never became dirty: Save is a no-op and the disk index matches.
	if err := idx.Save(); err != nil {
		t.Fatal(err)
	}
	reloaded, err := syncindex.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := reloaded.FindByPath("a.md"); got != aID {
		t.Fatalf("reloaded a.md = %s", got)
	}
}

func TestReconcileCursorStateDoesNotMaskBaselineUnknown(t *testing.T) {
	root := t.TempDir()
	writeNote(t, root, "a.md", "A")
	idx := enableVault(t, root)
	st := openState(t, t.TempDir())

	// Durable state that is not a baseline (a provider cursor): it must never
	// make a baseline-less note look local-only.
	if _, err := st.Put("cursor", map[string]any{"tok": "x"}); err != nil {
		t.Fatal(err)
	}
	r := reconcile(t, root, idx, st)
	if !r.BaselineUnknown {
		t.Fatal("cursor-only durable state masked baseline-unknown")
	}
	if e := entityState(t, r, syncIDFor(t, idx, "a.md")); e.State != StateBaselineUnknown {
		t.Fatalf("a.md = %v, want baseline-unknown", e.State)
	}
}

func TestStateStringIsNonEmptyAndUnique(t *testing.T) {
	seen := map[string]bool{}
	for s := StateUnchanged; s <= StateBaselineUnknown; s++ {
		label := s.String()
		if label == "" {
			t.Fatalf("empty String for %d", int(s))
		}
		if seen[label] {
			t.Fatalf("duplicate String %q", label)
		}
		seen[label] = true
	}
	if State(999).String() == "" {
		t.Fatal("unknown state has empty String")
	}
}

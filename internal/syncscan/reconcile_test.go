package syncscan

import (
	"os"
	"path/filepath"
	"testing"

	"memodump/internal/syncindex"
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

func syncIDFor(t *testing.T, idx *syncindex.Store, path string) string {
	t.Helper()
	id, ok := idx.FindByPath(path)
	if !ok {
		t.Fatalf("path %q not indexed", path)
	}
	return id
}

func reconcileScan(t *testing.T, root string, idx *syncindex.Store, lastKnown map[string]string, opts vaultfs.ScanOptions) *Reconciliation {
	t.Helper()
	res, err := vaultfs.Scan(root, opts)
	if err != nil {
		t.Fatal(err)
	}
	r, err := Reconcile(res, idx, lastKnown)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func reconcile(t *testing.T, root string, idx *syncindex.Store, lastKnown map[string]string) *Reconciliation {
	t.Helper()
	return reconcileScan(t, root, idx, lastKnown, vaultfs.ScanOptions{})
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

func repairFor(t *testing.T, r *Reconciliation, syncID string) (RepairHint, bool) {
	t.Helper()
	for _, h := range r.Repairs {
		if h.SyncID == syncID {
			return h, true
		}
	}
	return RepairHint{}, false
}

func newPaths(r *Reconciliation) []string {
	var out []string
	for _, n := range r.New {
		out = append(out, n.Path)
	}
	return out
}

func TestReconcilePresentObservations(t *testing.T) {
	root := t.TempDir()
	writeNote(t, root, "a.md", "# A\n")
	writeNote(t, root, "b.md", "# B\n")
	idx := enableVault(t, root)

	// Created after enable: c.md and empty are unindexed and need identity.
	writeNote(t, root, "c.md", "# C\n")
	if err := os.MkdirAll(filepath.Join(root, "empty"), 0755); err != nil {
		t.Fatal(err)
	}

	r := reconcile(t, root, idx, nil)
	for _, path := range []string{"a.md", "b.md"} {
		e := entityState(t, r, syncIDFor(t, idx, path))
		if e.State != StatePresent {
			t.Fatalf("%s = %v, want present", path, e.State)
		}
		if e.LocalHash == "" {
			t.Fatalf("%s present without a local hash", path)
		}
	}
	got := newPaths(r)
	if len(got) != 2 || got[0] != "c.md" || got[1] != "empty" {
		t.Fatalf("new = %v", got)
	}
}

func TestReconcilePresentCarriesCurrentHash(t *testing.T) {
	root := t.TempDir()
	writeNote(t, root, "a.md", "v1")
	idx := enableVault(t, root)

	writeNote(t, root, "a.md", "v2")
	r := reconcile(t, root, idx, nil)
	e := entityState(t, r, syncIDFor(t, idx, "a.md"))
	if e.State != StatePresent {
		t.Fatalf("a.md = %v, want present", e.State)
	}
	if e.LocalHash != vaultfs.LocalHash("v2") {
		t.Fatalf("a.md local hash = %s", e.LocalHash)
	}
}

func TestReconcileMissingIsObservation(t *testing.T) {
	root := t.TempDir()
	writeNote(t, root, "a.md", "v1")
	idx := enableVault(t, root)

	if err := os.Remove(filepath.Join(root, "a.md")); err != nil {
		t.Fatal(err)
	}
	// Even with a last-known hash, absence is a missing observation — the
	// engine decides whether a deletion is justified.
	r := reconcile(t, root, idx, map[string]string{
		syncIDFor(t, idx, "a.md"): vaultfs.LocalHash("v1"),
	})
	if e := entityState(t, r, syncIDFor(t, idx, "a.md")); e.State != StateMissing {
		t.Fatalf("a.md = %v, want missing", e.State)
	}
}

func TestReconcileMissingWithoutLastKnownHasNoRepair(t *testing.T) {
	root := t.TempDir()
	writeNote(t, root, "a.md", "A")
	idx := enableVault(t, root)

	// Absence with no last-known content is ambiguous for the engine; the scan
	// reports a plain missing observation and never a repair hint.
	if err := os.Remove(filepath.Join(root, "a.md")); err != nil {
		t.Fatal(err)
	}
	r := reconcile(t, root, idx, nil)
	if e := entityState(t, r, syncIDFor(t, idx, "a.md")); e.State != StateMissing {
		t.Fatalf("a.md = %v, want missing", e.State)
	}
	if _, ok := repairFor(t, r, syncIDFor(t, idx, "a.md")); ok {
		t.Fatal("a.md got a repair hint with no last-known content")
	}
}

func TestReconcileOfflineRenameRepair(t *testing.T) {
	root := t.TempDir()
	writeNote(t, root, "a.md", "same content")
	idx := enableVault(t, root)
	aID := syncIDFor(t, idx, "a.md")

	// The note moved while the app was closed, content unchanged.
	if err := os.Remove(filepath.Join(root, "a.md")); err != nil {
		t.Fatal(err)
	}
	writeNote(t, root, "b.md", "same content")

	r := reconcile(t, root, idx, map[string]string{aID: vaultfs.LocalHash("same content")})
	hint, ok := repairFor(t, r, aID)
	if !ok || hint.NewPath != "b.md" {
		t.Fatalf("a.md repair = %+v, want -> b.md", hint)
	}
	if e := entityState(t, r, aID); e.State != StateMissing {
		t.Fatalf("a.md = %v, want missing (observation) alongside the hint", e.State)
	}
	// The repaired path is claimed by the old Sync ID, not a new one.
	if got := newPaths(r); len(got) != 0 {
		t.Fatalf("repaired path listed as new: %v", got)
	}
}

func TestReconcileInAppRename(t *testing.T) {
	root := t.TempDir()
	writeNote(t, root, "a.md", "content")
	idx := enableVault(t, root)

	// An in-app rename updates the index in the same logical transaction as
	// the filesystem move; the next scan sees an ordinary, present entity.
	syncID := syncIDFor(t, idx, "a.md")
	if err := os.Rename(filepath.Join(root, "a.md"), filepath.Join(root, "b.md")); err != nil {
		t.Fatal(err)
	}
	if err := idx.UpdatePath(syncID, "b.md"); err != nil {
		t.Fatal(err)
	}

	r := reconcile(t, root, idx, nil)
	e := entityState(t, r, syncID)
	if e.State != StatePresent || e.Path != "b.md" {
		t.Fatalf("in-app renamed note = %+v, want present at b.md", e)
	}
}

func TestReconcileAmbiguousIdenticalHashesAreNotRepairs(t *testing.T) {
	root := t.TempDir()
	writeNote(t, root, "a.md", "identical")
	writeNote(t, root, "b.md", "identical")
	idx := enableVault(t, root)
	hash := vaultfs.LocalHash("identical")
	lastKnown := map[string]string{
		syncIDFor(t, idx, "a.md"): hash,
		syncIDFor(t, idx, "b.md"): hash,
	}

	for _, name := range []string{"a.md", "b.md"} {
		if err := os.Remove(filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}
	writeNote(t, root, "c.md", "identical")

	r := reconcile(t, root, idx, lastKnown)
	// Two identical disappearances plus one identical newcomer: ambiguous, so
	// no repair hint; the newcomer is a copy and the originals stay missing.
	if e := entityState(t, r, syncIDFor(t, idx, "a.md")); e.State != StateMissing {
		t.Fatalf("a.md = %v, want missing", e.State)
	}
	if e := entityState(t, r, syncIDFor(t, idx, "b.md")); e.State != StateMissing {
		t.Fatalf("b.md = %v, want missing", e.State)
	}
	if len(r.Repairs) != 0 {
		t.Fatalf("ambiguous case produced repairs: %+v", r.Repairs)
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
	if err := os.RemoveAll(filepath.Join(root, "x.md")); err != nil {
		t.Fatal(err)
	}
	writeNote(t, root, "x.md", "now a file")

	r := reconcile(t, root, idx, nil)
	if e := entityState(t, r, syncIDFor(t, idx, "x.md")); e.State != StateBlocked {
		t.Fatalf("kind mismatch = %v, want blocked", e.State)
	}
}

func TestReconcileSymlinkIsBlocked(t *testing.T) {
	root := t.TempDir()
	writeNote(t, root, "a.md", "content")
	idx := enableVault(t, root)

	// An indexed note replaced by a symlink is blocked, never missing.
	if err := os.Remove(filepath.Join(root, "a.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(t.TempDir(), "elsewhere.md"), filepath.Join(root, "a.md")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	r := reconcile(t, root, idx, nil)
	if e := entityState(t, r, syncIDFor(t, idx, "a.md")); e.State != StateBlocked {
		t.Fatalf("symlinked note = %v, want blocked", e.State)
	}
}

func TestReconcileUnstableIsDeferred(t *testing.T) {
	root := t.TempDir()
	writeNote(t, root, "a.md", "v1")
	idx := enableVault(t, root)

	r := reconcileScan(t, root, idx, nil, vaultfs.ScanOptions{BetweenPasses: func() {
		writeNote(t, root, "a.md", "v2 a different length than v1")
	}})
	e := entityState(t, r, syncIDFor(t, idx, "a.md"))
	if e.State != StateUnstable {
		t.Fatalf("unstable note = %v, want unstable", e.State)
	}
}

func TestBlockedAndUnstableNeverBecomeMissing(t *testing.T) {
	root := t.TempDir()
	writeNote(t, root, "a.md", "blocked")
	writeNote(t, root, "b.md", "unstable")
	idx := enableVault(t, root)
	aID := syncIDFor(t, idx, "a.md")
	bID := syncIDFor(t, idx, "b.md")

	// A synced entity (lastKnown present) that becomes a symlink or an
	// unstable write must never be classified missing — that would justify an
	// unjustified deletion.
	if err := os.Remove(filepath.Join(root, "a.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir()+"/x", filepath.Join(root, "a.md")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	lastKnown := map[string]string{aID: vaultfs.LocalHash("blocked"), bID: vaultfs.LocalHash("unstable")}
	r := reconcileScan(t, root, idx, lastKnown, vaultfs.ScanOptions{BetweenPasses: func() {
		writeNote(t, root, "b.md", "unstable with a longer body to force a mismatch")
	}})
	if e := entityState(t, r, aID); e.State != StateBlocked {
		t.Fatalf("blocked note = %v, want blocked (never missing)", e.State)
	}
	if e := entityState(t, r, bID); e.State != StateUnstable {
		t.Fatalf("unstable note = %v, want unstable (never missing)", e.State)
	}
}

func TestApplyIdentityAddsNewAndAppliesRepair(t *testing.T) {
	root := t.TempDir()
	writeNote(t, root, "a.md", "shared")
	idx := enableVault(t, root)

	// Offline rename a.md -> b.md (same content), plus a genuinely new note.
	if err := os.Remove(filepath.Join(root, "a.md")); err != nil {
		t.Fatal(err)
	}
	writeNote(t, root, "b.md", "shared")
	writeNote(t, root, "new.md", "brand new")

	r := reconcile(t, root, idx, map[string]string{
		syncIDFor(t, idx, "a.md"): vaultfs.LocalHash("shared"),
	})
	oldID := syncIDFor(t, idx, "a.md") // before ApplyIdentity moves it
	if err := ApplyIdentity(r, idx); err != nil {
		t.Fatal(err)
	}
	if got, _ := idx.FindByPath("b.md"); got != oldID {
		t.Fatalf("b.md has %s, want old sync ID %s", got, oldID)
	}
	if _, ok := idx.FindByPath("a.md"); ok {
		t.Fatal("old path still indexed after repair")
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
	id := syncIDFor(t, idx, "Note.md")

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
	r := reconcile(t, root, idx, map[string]string{id: vaultfs.LocalHash("content")})
	hint, ok := repairFor(t, r, id)
	if !ok || hint.NewPath != "note.md" {
		t.Fatalf("case-only repair = %+v, want -> note.md", hint)
	}
	if got := newPaths(r); len(got) != 0 {
		t.Fatalf("case-only repaired path listed as new: %v", got)
	}
}

func TestReconcileEmptyFolderNew(t *testing.T) {
	root := t.TempDir()
	idx := enableVault(t, root)
	if err := os.MkdirAll(filepath.Join(root, "empty"), 0755); err != nil {
		t.Fatal(err)
	}
	r := reconcile(t, root, idx, nil)
	// The empty folder is unindexed: it is a real folder entity needing identity.
	if got := newPaths(r); len(got) != 1 || got[0] != "empty" {
		t.Fatalf("new = %v, want empty folder", got)
	}
}

func TestReconcileSymlinkedDirSubtreeIsBlocked(t *testing.T) {
	root := t.TempDir()
	writeNote(t, root, "dir/a.md", "content")
	idx := enableVault(t, root)

	// The directory becomes a symlink to a location outside the vault: the
	// note under it is blocked, never judged missing.
	if err := os.RemoveAll(filepath.Join(root, "dir")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(root, "dir")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	r := reconcile(t, root, idx, nil)
	if e := entityState(t, r, syncIDFor(t, idx, "dir/a.md")); e.State != StateBlocked {
		t.Fatalf("note under symlinked dir = %v, want blocked (not missing)", e.State)
	}
}

func TestReconcileSyncedFolderDeletion(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "empty"), 0755); err != nil {
		t.Fatal(err)
	}
	idx := enableVault(t, root)
	id := syncIDFor(t, idx, "empty")

	if err := os.RemoveAll(filepath.Join(root, "empty")); err != nil {
		t.Fatal(err)
	}
	// A folder has no content digest, so it is never a repair candidate even
	// with a lastKnown entry; it is a plain missing observation.
	r := reconcile(t, root, idx, map[string]string{id: ""})
	if e := entityState(t, r, id); e.State != StateMissing {
		t.Fatalf("synced empty folder deletion = %v, want missing", e.State)
	}
	if _, ok := repairFor(t, r, id); ok {
		t.Fatal("folder produced a repair hint")
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

	// A reconciliation in which two repair candidates claim the same new path:
	// the second is invalid, and the whole batch must fail without applying the
	// first.
	r := &Reconciliation{Repairs: []RepairHint{
		{SyncID: aID, Path: "a.md", NewPath: "c.md", LocalHash: "x"},
		{SyncID: bID, Path: "b.md", NewPath: "c.md", LocalHash: "x"},
	}}
	if err := ApplyIdentity(r, idx); err == nil {
		t.Fatal("conflicting repair batch succeeded")
	}
	// Nothing was applied: both entities keep their paths and no c.md exists.
	if got, _ := idx.FindByPath("a.md"); got != aID {
		t.Fatalf("a.md = %s, want %s (partial repair applied)", got, aID)
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

func TestStateStringIsNonEmptyAndUnique(t *testing.T) {
	seen := map[string]bool{}
	for s := StatePresent; s <= StateUnstable; s++ {
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

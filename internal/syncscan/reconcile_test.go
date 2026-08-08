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

func reconcileScan(t *testing.T, root string, idx *syncindex.Store, opts vaultfs.ScanOptions) *Reconciliation {
	t.Helper()
	res, err := vaultfs.Scan(root, opts)
	if err != nil {
		t.Fatal(err)
	}
	r, err := Reconcile(res, idx)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func reconcile(t *testing.T, root string, idx *syncindex.Store) *Reconciliation {
	t.Helper()
	return reconcileScan(t, root, idx, vaultfs.ScanOptions{})
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

	r := reconcile(t, root, idx)
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
	r := reconcile(t, root, idx)
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
	// Absence is a missing observation, never a deletion decision — the engine
	// decides whether a deletion is justified.
	r := reconcile(t, root, idx)
	if e := entityState(t, r, syncIDFor(t, idx, "a.md")); e.State != StateMissing {
		t.Fatalf("a.md = %v, want missing", e.State)
	}
}

func TestReconcileOfflineRenameDegradesToMissingPlusNew(t *testing.T) {
	root := t.TempDir()
	writeNote(t, root, "a.md", "same content")
	idx := enableVault(t, root)

	// The note moved while the app was closed, content unchanged. The scanner
	// makes no rename decision: the old path is missing and the new path is an
	// unindexed entity that needs identity. Identity/repair is the engine's
	// job (Phase 3); until then this is lossless delete-plus-create.
	if err := os.Remove(filepath.Join(root, "a.md")); err != nil {
		t.Fatal(err)
	}
	writeNote(t, root, "b.md", "same content")

	r := reconcile(t, root, idx)
	if e := entityState(t, r, syncIDFor(t, idx, "a.md")); e.State != StateMissing {
		t.Fatalf("a.md = %v, want missing", e.State)
	}
	if got := newPaths(r); len(got) != 1 || got[0] != "b.md" {
		t.Fatalf("new = %v, want b.md", got)
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

	r := reconcile(t, root, idx)
	e := entityState(t, r, syncID)
	if e.State != StatePresent || e.Path != "b.md" {
		t.Fatalf("in-app renamed note = %+v, want present at b.md", e)
	}
}

func TestReconcileIdenticalContentIsIndependent(t *testing.T) {
	root := t.TempDir()
	writeNote(t, root, "a.md", "identical")
	writeNote(t, root, "b.md", "identical")
	idx := enableVault(t, root)

	for _, name := range []string{"a.md", "b.md"} {
		if err := os.Remove(filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}
	writeNote(t, root, "c.md", "identical")

	// Identical content never conflates identities: the originals stay missing
	// and the newcomer is a separate unindexed entity needing its own Sync ID.
	r := reconcile(t, root, idx)
	if e := entityState(t, r, syncIDFor(t, idx, "a.md")); e.State != StateMissing {
		t.Fatalf("a.md = %v, want missing", e.State)
	}
	if e := entityState(t, r, syncIDFor(t, idx, "b.md")); e.State != StateMissing {
		t.Fatalf("b.md = %v, want missing", e.State)
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

	r := reconcile(t, root, idx)
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
	r := reconcile(t, root, idx)
	if e := entityState(t, r, syncIDFor(t, idx, "a.md")); e.State != StateBlocked {
		t.Fatalf("symlinked note = %v, want blocked", e.State)
	}
}

func TestReconcileUnstableIsDeferred(t *testing.T) {
	root := t.TempDir()
	writeNote(t, root, "a.md", "v1")
	idx := enableVault(t, root)

	r := reconcileScan(t, root, idx, vaultfs.ScanOptions{BetweenPasses: func() {
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

	// A synced entity that becomes a symlink or an unstable write must never be
	// classified missing — that would justify an unjustified deletion.
	if err := os.Remove(filepath.Join(root, "a.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir()+"/x", filepath.Join(root, "a.md")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	r := reconcileScan(t, root, idx, vaultfs.ScanOptions{BetweenPasses: func() {
		writeNote(t, root, "b.md", "unstable with a longer body to force a mismatch")
	}})
	if e := entityState(t, r, aID); e.State != StateBlocked {
		t.Fatalf("blocked note = %v, want blocked (never missing)", e.State)
	}
	if e := entityState(t, r, bID); e.State != StateUnstable {
		t.Fatalf("unstable note = %v, want unstable (never missing)", e.State)
	}
}

func TestApplyIdentityAssignsIDsToNew(t *testing.T) {
	root := t.TempDir()
	writeNote(t, root, "a.md", "shared")
	idx := enableVault(t, root)

	// A genuinely new note (and an empty folder) need identity.
	writeNote(t, root, "new.md", "brand new")
	if err := os.MkdirAll(filepath.Join(root, "empty"), 0755); err != nil {
		t.Fatal(err)
	}

	r := reconcile(t, root, idx)
	if err := ApplyIdentity(r, idx); err != nil {
		t.Fatal(err)
	}
	if _, ok := idx.FindByPath("new.md"); !ok {
		t.Fatal("new note did not receive a Sync ID")
	}
	if _, ok := idx.FindByPath("empty"); !ok {
		t.Fatal("empty folder did not receive a Sync ID")
	}

	// The identity change is durable: reloading the index sees it.
	reloaded, err := syncindex.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := reloaded.FindByPath("new.md"); !ok {
		t.Fatal("reloaded index lost the new note Sync ID")
	}
}

func TestReconcileEmptyFolderNew(t *testing.T) {
	root := t.TempDir()
	idx := enableVault(t, root)
	if err := os.MkdirAll(filepath.Join(root, "empty"), 0755); err != nil {
		t.Fatal(err)
	}
	r := reconcile(t, root, idx)
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
	r := reconcile(t, root, idx)
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
	// A folder's deletion is a plain missing observation; no identity decision
	// is made here.
	r := reconcile(t, root, idx)
	if e := entityState(t, r, id); e.State != StateMissing {
		t.Fatalf("synced empty folder deletion = %v, want missing", e.State)
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
	idx := enableVault(t, root)
	aID := syncIDFor(t, idx, "a.md")

	// A reconciliation whose New list includes a path the index already owns is
	// invalid: the whole batch must fail without applying anything.
	r := &Reconciliation{New: []NewEntity{
		{Path: "a.md", Kind: "note"},
	}}
	if err := ApplyIdentity(r, idx); err == nil {
		t.Fatal("colliding New path succeeded")
	}
	if got, _ := idx.FindByPath("a.md"); got != aID {
		t.Fatalf("a.md = %s, want %s (partial identity applied)", got, aID)
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

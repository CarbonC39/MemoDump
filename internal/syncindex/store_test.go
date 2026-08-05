package syncindex

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/google/uuid"

	"memodump/internal/cloudsync"
)

func readVault(t *testing.T, root, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func assertIndexFilesParse(t *testing.T, root string) {
	t.Helper()
	for _, name := range []string{IndexName, BackupName} {
		data, err := os.ReadFile(filepath.Join(root, DirName, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if _, err := parseIndex(data); err != nil {
			t.Fatalf("%s is not a valid index: %v", name, err)
		}
	}
}

func TestNeverEnabledVaultHasNoSyncMetadata(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.md"), []byte("# A"), 0644); err != nil {
		t.Fatal(err)
	}
	// No .memodump directory is ever created by loading.
	if _, err := Load(root); !errors.Is(err, ErrNotEnabled) {
		t.Fatalf("Load = %v, want ErrNotEnabled", err)
	}
	if _, err := os.Stat(filepath.Join(root, DirName)); !os.IsNotExist(err) {
		t.Fatalf(".memodump exists after a Load on a never-enabled vault")
	}
}

func TestEnableAssignsStableUUIDsWithoutChangingMarkdown(t *testing.T) {
	root := t.TempDir()
	md := "---\ntags: [\"project\"]\n---\n# Idea\n"
	if err := os.WriteFile(filepath.Join(root, "idea.md"), []byte(md), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sub", "deep.md"), []byte("# Deep\n"), 0644); err != nil {
		t.Fatal(err)
	}

	s, err := Enable(root)
	if err != nil {
		t.Fatal(err)
	}
	if !cloudsync.IsUUIDv4(s.Index.VaultID) {
		t.Fatalf("enable did not assign a vault UUID: %q", s.Index.VaultID)
	}
	// Every existing note and folder received a stable Sync ID on first enable.
	if s.Len() != 3 {
		t.Fatalf("enable indexed %d entities, want 3 (2 notes + 1 folder)", s.Len())
	}
	for _, p := range []string{"idea.md", "sub/deep.md", "sub"} {
		if _, ok := s.FindByPath(p); !ok {
			t.Fatalf("enable did not index %q", p)
		}
	}
	// The enable never rewrites existing Markdown.
	if got := readVault(t, root, "idea.md"); got != md {
		t.Fatalf("note bytes changed after enable: %q", got)
	}
	// Both index files exist and parse (the spec shows .bak after first enable).
	assertIndexFilesParse(t, root)

	loaded, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Index.VaultID != s.Index.VaultID || loaded.Len() != 3 {
		t.Fatalf("reload mismatch: vaultId %q len %d", loaded.Index.VaultID, loaded.Len())
	}
}

func TestEnableIsIdempotentAcrossRestart(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.md"), []byte("# A"), 0644); err != nil {
		t.Fatal(err)
	}
	s1, err := Enable(root)
	if err != nil {
		t.Fatal(err)
	}
	idA := mustSyncID(t, s1, "a.md")

	// A "restart" re-enables the same vault: identity must be stable and the
	// index must not be rewritten (nothing new was discovered).
	s2, err := Enable(root)
	if err != nil {
		t.Fatal(err)
	}
	if s2.Index.VaultID != s1.Index.VaultID {
		t.Fatalf("vault ID changed across restart: %s -> %s", s1.Index.VaultID, s2.Index.VaultID)
	}
	if idB := mustSyncID(t, s2, "a.md"); idB != idA {
		t.Fatalf("note Sync ID changed across restart: %s -> %s", idA, idB)
	}
	if s2.writes != 0 {
		t.Fatalf("idle restart rewrote the index %d times", s2.writes)
	}
}

func TestEnableAddsNewFilesOnlyOnReEnable(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.md"), []byte("# A"), 0644); err != nil {
		t.Fatal(err)
	}
	s1, err := Enable(root)
	if err != nil {
		t.Fatal(err)
	}
	idA := mustSyncID(t, s1, "a.md")

	// A new note appears while the app is closed; the next enable adds it
	// without disturbing existing identity.
	if err := os.WriteFile(filepath.Join(root, "b.md"), []byte("# B"), 0644); err != nil {
		t.Fatal(err)
	}
	s2, err := Enable(root)
	if err != nil {
		t.Fatal(err)
	}
	if s2.Len() != 2 {
		t.Fatalf("re-enable indexed %d entities, want 2", s2.Len())
	}
	if mustSyncID(t, s2, "a.md") != idA {
		t.Fatalf("existing identity changed when a new file appeared")
	}
	if _, ok := s2.FindByPath("b.md"); !ok {
		t.Fatalf("new note was not indexed")
	}
}

func TestEnableFailsOnDualCorruption(t *testing.T) {
	root := t.TempDir()
	if _, err := Enable(root); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(IndexPath(root), []byte("{bad"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, DirName, BackupName), []byte("bad too"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := Enable(root); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("Enable with dual corruption = %v, want ErrCorrupt", err)
	}
}

func mustSyncID(t *testing.T, s *Store, path string) string {
	t.Helper()
	id, ok := s.FindByPath(path)
	if !ok {
		t.Fatalf("path %q not indexed", path)
	}
	return id
}

func TestStructuralBatchWritesIndexOnce(t *testing.T) {
	root := t.TempDir()
	s, err := Create(root, uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100; i++ {
		if err := s.AddEntity(uuid.NewString(), Entity{Kind: "note", Path: fmt.Sprintf("n%d.md", i)}); err != nil {
			t.Fatal(err)
		}
	}
	if s.writes != 0 {
		t.Fatalf("writes before save = %d, want 0", s.writes)
	}
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	if s.writes != 1 {
		t.Fatalf("batch of 100 mutations did one save = %d writes, want 1", s.writes)
	}
	// A second Save with nothing dirty is a pure no-op.
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	if s.writes != 1 {
		t.Fatalf("no-op save wrote %d times, want 0 additional", s.writes-1)
	}
}

func TestContentOnlyChangesNeverRewriteIndex(t *testing.T) {
	root := t.TempDir()
	vaultID := uuid.NewString()
	if _, err := Create(root, vaultID); err != nil {
		t.Fatal(err)
	}
	s, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Save(); err != nil { // dirty is false
		t.Fatal(err)
	}
	if s.writes != 0 {
		t.Fatalf("content-only save wrote %d times", s.writes)
	}
}

func TestPrimaryCorruptionFallsBackToBackupAndRestores(t *testing.T) {
	root := t.TempDir()
	vaultID := uuid.NewString()
	s, err := Create(root, vaultID)
	if err != nil {
		t.Fatal(err)
	}
	id1 := uuid.NewString()
	if err := s.AddEntity(id1, Entity{Kind: "note", Path: "a.md"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	id2 := uuid.NewString()
	if err := s.AddEntity(id2, Entity{Kind: "note", Path: "b.md"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	// After the second save the backup holds the pre-save known-good state:
	// one entity (a.md). The primary holds both.

	// Corrupt the primary only; the backup must serve the load.
	if err := os.WriteFile(IndexPath(root), []byte("{corrupt"), 0644); err != nil {
		t.Fatal(err)
	}
	s2, err := Load(root)
	if err != nil {
		t.Fatalf("Load with corrupt primary failed: %v", err)
	}
	if s2.Index.VaultID != vaultID {
		t.Fatalf("backup did not serve the index: vaultId %q", s2.Index.VaultID)
	}
	if _, ok := s2.FindByPath("a.md"); !ok {
		t.Fatalf("backup lost the last known-good entity")
	}
	if _, ok := s2.FindByPath("b.md"); ok {
		t.Fatalf("backup advanced past the last known-good state")
	}

	// A structural save from the backup-loaded store must restore a valid
	// primary WITHOUT clobbering the valid backup with the corrupt primary.
	if err := s2.AddEntity(uuid.NewString(), Entity{Kind: "note", Path: "c.md"}); err != nil {
		t.Fatal(err)
	}
	if err := s2.Save(); err != nil {
		t.Fatal(err)
	}
	assertIndexFilesParse(t, root)
}

func TestDualCorruptionStopsSafelyAndRebuilds(t *testing.T) {
	root := t.TempDir()
	vaultID := uuid.NewString()
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0755); err != nil {
		t.Fatal(err)
	}
	mustWrite := func(rel, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(rel)), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite("a.md", "# A\n")
	mustWrite("sub/b.md", "# B\n")

	s, err := Create(root, vaultID)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range []Entity{
		{Kind: "note", Path: "a.md"}, {Kind: "note", Path: "sub/b.md"}, {Kind: "folder", Path: "sub"},
	} {
		if err := s.AddEntity(uuid.NewString(), e); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}

	// Corrupt both files: load must stop safely (ErrCorrupt), not guess.
	if err := os.WriteFile(IndexPath(root), []byte("{bad"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, DirName, BackupName), []byte("bad too"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(root); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("Load = %v, want ErrCorrupt", err)
	}

	// Rebuild re-indexes local notes and folders without touching files, and
	// both output files parse.
	rb, err := Rebuild(root, vaultID)
	if err != nil {
		t.Fatal(err)
	}
	if rb.Len() != 3 {
		t.Fatalf("rebuild indexed %d entities, want 3 (2 notes + 1 folder)", rb.Len())
	}
	if _, ok := rb.FindByPath("a.md"); !ok {
		t.Fatalf("rebuild lost a.md")
	}
	if _, ok := rb.FindByPath("sub/b.md"); !ok {
		t.Fatalf("rebuild lost sub/b.md")
	}
	if got := readVault(t, root, "a.md"); got != "# A\n" {
		t.Fatalf("rebuild modified note bytes: %q", got)
	}
	assertIndexFilesParse(t, root)
}

func TestRebuildSkipsReservedDirsAndSymlinks(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.md"), []byte("# A"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, DirName), 0755); err != nil {
		t.Fatal(err)
	}
	// A stray .md inside .memodump must never be indexed by a rebuild.
	if err := os.WriteFile(filepath.Join(root, DirName, "junk.md"), []byte("junk"), 0644); err != nil {
		t.Fatal(err)
	}
	// A symlinked .md pointing outside the vault must be ignored, not followed.
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.md"), []byte("secret"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret.md"), filepath.Join(root, "link.md")); err != nil {
		t.Fatal(err)
	}

	rb, err := Rebuild(root, uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	if rb.Len() != 1 {
		t.Fatalf("rebuild indexed %d entities, want 1", rb.Len())
	}
	if _, ok := rb.FindByPath("a.md"); !ok {
		t.Fatalf("rebuild lost a.md")
	}
}

func TestAddUpdateRemoveEntity(t *testing.T) {
	root := t.TempDir()
	s, err := Create(root, uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	id1, id2 := uuid.NewString(), uuid.NewString()

	if err := s.AddEntity(id1, Entity{Kind: "note", Path: "a.md"}); err != nil {
		t.Fatalf("first add: %v", err)
	}
	// Adding a second entity at the same path is a conflict, never a silent
	// displacement: reconciliation decides identity, not AddEntity.
	if err := s.AddEntity(id2, Entity{Kind: "note", Path: "a.md"}); err == nil {
		t.Fatal("duplicate-path add accepted")
	}
	if _, ok := s.FindBySyncID(id1); !ok {
		t.Fatal("original Sync ID lost on a rejected add")
	}

	if err := s.UpdatePath(id1, "b.md"); err != nil {
		t.Fatal(err)
	}
	// A path indexed by a different Sync ID is a conflict.
	if err := s.AddEntity(uuid.NewString(), Entity{Kind: "note", Path: "z.md"}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdatePath(id1, "z.md"); err == nil {
		t.Fatal("UpdatePath onto an indexed path accepted")
	}
	if e, ok := s.FindBySyncID(id1); !ok || e.Path != "b.md" {
		t.Fatalf("FindBySyncID(id1) = %+v, %v", e, ok)
	}

	s.RemoveEntity(id1)
	if s.Len() != 1 {
		t.Fatalf("after remove len = %d, want 1", s.Len())
	}
	if _, ok := s.FindByPath("b.md"); ok {
		t.Fatal("removed entity still found by path")
	}
}

func TestAddEntityRejectsUnsafeMutations(t *testing.T) {
	root := t.TempDir()
	s, err := Create(root, uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		syncID string
		e      Entity
	}{
		{"bad-id", Entity{Kind: "note", Path: "a.md"}},
		{uuid.NewString(), Entity{Kind: "widget", Path: "a.md"}},
		{uuid.NewString(), Entity{Kind: "note", Path: "../../a.md"}},
		{uuid.NewString(), Entity{Kind: "note", Path: `a\b.md`}},
		{uuid.NewString(), Entity{Kind: "note", Path: ".memodump/x.md"}},
		{uuid.NewString(), Entity{Kind: "folder", Path: ".images"}},
	}
	for _, tc := range cases {
		if err := s.AddEntity(tc.syncID, tc.e); err == nil {
			t.Errorf("AddEntity(%q, %+v) accepted", tc.syncID, tc.e)
		}
	}
	if s.Len() != 0 {
		t.Fatalf("rejected mutations still indexed %d entities", s.Len())
	}
}

func TestUpdatePathRejectsUnsafePaths(t *testing.T) {
	root := t.TempDir()
	s, err := Create(root, uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	id := uuid.NewString()
	if err := s.AddEntity(id, Entity{Kind: "note", Path: "a.md"}); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"", "../a.md", "/abs.md", `a\b.md`, ".memodump/x.md", ".images"} {
		if err := s.UpdatePath(id, bad); err == nil {
			t.Errorf("UpdatePath to %q accepted", bad)
		}
	}
	// The original mapping is untouched by rejected moves.
	if e, ok := s.FindBySyncID(id); !ok || e.Path != "a.md" {
		t.Fatalf("entity moved despite rejected UpdatePath: %+v", e)
	}
}

func TestEntitiesNullIsCorruptAndFallsBackToBackup(t *testing.T) {
	root := t.TempDir()
	vaultID := uuid.NewString()
	s, err := Create(root, vaultID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddEntity(uuid.NewString(), Entity{Kind: "note", Path: "a.md"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	if err := s.AddEntity(uuid.NewString(), Entity{Kind: "note", Path: "b.md"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	// The backup now holds the pre-second-save known-good: a.md.

	// A primary that lost its entities map must NOT be accepted as an empty
	// vault: that would silently reassign every Sync ID on the next enable. It
	// is corrupt, so Load falls back to the backup, which still has real IDs.
	nullDoc := fmt.Sprintf(`{"schemaVersion":1,"vaultId":"%s","entities":null}`, vaultID)
	if err := os.WriteFile(IndexPath(root), []byte(nullDoc), 0644); err != nil {
		t.Fatal(err)
	}
	s2, err := Load(root)
	if err != nil {
		t.Fatalf("backup did not serve the load: %v", err)
	}
	if s2.Index.VaultID != vaultID {
		t.Fatalf("backup vaultId = %q, want %q", s2.Index.VaultID, vaultID)
	}
	if _, ok := s2.FindByPath("a.md"); !ok {
		t.Fatal("backup lost the known-good entity")
	}

	// With no backup either, the null-entities document is corrupt.
	if err := os.Remove(filepath.Join(root, DirName, BackupName)); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(root); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("Load(null-entities primary, no backup) = %v, want ErrCorrupt", err)
	}
}

func TestNilEntitiesMapDoesNotPanicOnMutation(t *testing.T) {
	// A hand-built Store with a nil map (never produced by Load, but possible in
	// memory) must not panic when mutated.
	s := &Store{root: t.TempDir(), Index: &Index{SchemaVersion: 1, VaultID: uuid.NewString()}}
	if err := s.AddEntity(uuid.NewString(), Entity{Kind: "note", Path: "a.md"}); err != nil {
		t.Fatalf("AddEntity on a nil map failed: %v", err)
	}
	if s.Index.Entities == nil {
		t.Fatal("entities map still nil after AddEntity")
	}
}

func TestCreateRejectsInvalidVaultID(t *testing.T) {
	for _, bad := range []string{"", "not-a-uuid", "00000000-0000-0000-0000-000000000000"} {
		if _, err := Create(t.TempDir(), bad); err == nil {
			t.Errorf("Create accepted invalid vaultId %q", bad)
		}
	}
}

func TestCreateRejectsAlreadyEnabledVault(t *testing.T) {
	root := t.TempDir()
	s, err := Enable(root)
	if err != nil {
		t.Fatal(err)
	}
	// A second Create must never overwrite an existing index.
	if _, err := Create(root, uuid.NewString()); err == nil {
		t.Fatal("Create overwrote an already-enabled vault")
	}
	loaded, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Index.VaultID != s.Index.VaultID {
		t.Fatal("vault identity changed by a refused Create")
	}
}

// TestEnableConcurrentFirstCreate proves that several processes first-enabling
// the same vault at once agree on ONE Vault ID and ONE Sync ID set — otherwise
// they would each resolve a different Replica ID and sync the same vault
// concurrently under different state directories.
func TestEnableConcurrentFirstCreate(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.md"), []byte("# A"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "b.md"), []byte("# B"), 0644); err != nil {
		t.Fatal(err)
	}

	const n = 3
	outDir := t.TempDir()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmds := make([]*exec.Cmd, n)
	for i := 0; i < n; i++ {
		cmd := exec.Command(exe, "-test.run=TestEnableHelperChild")
		cmd.Env = append(os.Environ(),
			"MEMODUMP_ENABLE_HELPER=1",
			"MEMODUMP_ENABLE_ROOT="+root,
			"MEMODUMP_ENABLE_OUT="+filepath.Join(outDir, fmt.Sprintf("out%d", i)),
		)
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		cmds[i] = cmd
	}
	for _, c := range cmds {
		if err := c.Wait(); err != nil {
			t.Fatalf("enable child failed: %v", err)
		}
	}

	var firstID string
	var firstPaths []string
	for i := 0; i < n; i++ {
		data, err := os.ReadFile(filepath.Join(outDir, fmt.Sprintf("out%d", i)))
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(strings.TrimSpace(string(data)), "\n")
		paths := append([]string(nil), lines[1:]...)
		sort.Strings(paths)
		if i == 0 {
			firstID, firstPaths = lines[0], paths
			continue
		}
		if lines[0] != firstID {
			t.Fatalf("concurrent enables disagreed on vault ID: %s vs %s", lines[0], firstID)
		}
		if !slices.Equal(paths, firstPaths) {
			t.Fatalf("concurrent enables disagreed on entity set: %v vs %v", paths, firstPaths)
		}
	}
	// The persisted index agrees with every returned identity.
	loaded, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Index.VaultID != firstID {
		t.Fatalf("persisted index disagrees with the enables: %s vs %s", loaded.Index.VaultID, firstID)
	}
}

// TestEnableHelperChild is only meaningful when re-executed by
// TestEnableConcurrentFirstCreate: it enables the vault and reports the Vault
// ID plus sorted entity paths to the parent.
func TestEnableHelperChild(t *testing.T) {
	if os.Getenv("MEMODUMP_ENABLE_HELPER") != "1" {
		return
	}
	s, err := Enable(os.Getenv("MEMODUMP_ENABLE_ROOT"))
	if err != nil {
		os.Exit(1)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n", s.Index.VaultID)
	paths := make([]string, 0, len(s.Index.Entities))
	for _, e := range s.Index.Entities {
		paths = append(paths, e.Path)
	}
	sort.Strings(paths)
	for _, p := range paths {
		b.WriteString(p)
		b.WriteByte('\n')
	}
	if err := os.WriteFile(os.Getenv("MEMODUMP_ENABLE_OUT"), []byte(b.String()), 0600); err != nil {
		os.Exit(2)
	}
}

func TestSaveRejectsInvalidIndex(t *testing.T) {
	root := t.TempDir()
	s, err := Create(root, uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	// Force the index into an invalid state (duplicate paths) behind the
	// mutation API's back; Save must refuse to persist it.
	id1, id2 := uuid.NewString(), uuid.NewString()
	s.Index.Entities[id1] = Entity{Kind: "note", Path: "dup.md"}
	s.Index.Entities[id2] = Entity{Kind: "note", Path: "dup.md"}
	s.dirty = true
	if err := s.Save(); err == nil {
		t.Fatal("Save persisted an index that Load would reject")
	}
	if s.writes != 0 {
		t.Fatalf("invalid index was written %d times", s.writes)
	}
}

func TestSymlinkedMemodumpRefused(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, DirName)); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := Enable(root); !errors.Is(err, ErrSymlink) {
		t.Fatalf("Enable through a symlinked .memodump = %v, want ErrSymlink", err)
	}
	if _, err := Load(root); !errors.Is(err, ErrSymlink) {
		t.Fatalf("Load through a symlinked .memodump = %v, want ErrSymlink", err)
	}
	// Nothing was written through the symlink.
	entries, _ := os.ReadDir(outside)
	if len(entries) != 0 {
		t.Fatalf("sync metadata was written through the symlink: %v", entries)
	}
}

func TestSymlinkedIndexFileRefused(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, DirName), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "real.json"), filepath.Join(root, DirName, IndexName)); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	// Enable must not read or write through a symlinked index file.
	if _, err := Enable(root); !errors.Is(err, ErrSymlink) {
		t.Fatalf("Enable with a symlinked index = %v, want ErrSymlink", err)
	}
	if _, err := Load(root); !errors.Is(err, ErrSymlink) {
		t.Fatalf("Load with a symlinked index = %v, want ErrSymlink", err)
	}
}

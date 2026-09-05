package syncindex

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"memodump/internal/cloudsync"
)

func mustNoteSyncID(t *testing.T, s *NoteStore, path string) string {
	t.Helper()
	id, ok := s.IDByPath(path)
	if !ok {
		t.Fatalf("path %q not indexed", path)
	}
	return id
}

// assertNoteIndexFilesParse verifies both on-disk index files are valid v2
// note-only documents.
func assertNoteIndexFilesParse(t *testing.T, root string) {
	t.Helper()
	for _, name := range []string{IndexName, BackupName} {
		data, err := os.ReadFile(filepath.Join(root, DirName, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if _, err := ParseNoteIndex(data); err != nil {
			t.Fatalf("%s is not a valid note index: %v", name, err)
		}
	}
}

func TestNoteStoreNeverEnabledHasNoMetadata(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.md"), []byte("# A"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadNoteStore(root); !errors.Is(err, ErrNotEnabled) {
		t.Fatalf("LoadNoteStore = %v, want ErrNotEnabled", err)
	}
	if _, err := os.Stat(filepath.Join(root, DirName)); !os.IsNotExist(err) {
		t.Fatal(".memodump exists after a Load on a never-enabled vault")
	}
}

func TestNoteStoreEnableAssignsStableIDsWithoutChangingMarkdown(t *testing.T) {
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
	// Folders are not entities in v2; only the two notes are indexed.
	if err := os.WriteFile(filepath.Join(root, "sub", "readme.txt"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}

	s, err := EnableNoteStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if !cloudsync.IsUUIDv4(s.Index.VaultID) {
		t.Fatalf("enable did not assign a vault UUID: %q", s.Index.VaultID)
	}
	if s.Len() != 2 {
		t.Fatalf("enable indexed %d notes, want 2", s.Len())
	}
	for _, p := range []string{"idea.md", "sub/deep.md"} {
		if _, ok := s.IDByPath(p); !ok {
			t.Fatalf("enable did not index %q", p)
		}
	}
	if got := readVault(t, root, "idea.md"); got != md {
		t.Fatalf("note bytes changed after enable: %q", got)
	}
	assertNoteIndexFilesParse(t, root)

	loaded, err := LoadNoteStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Index.VaultID != s.Index.VaultID || loaded.Len() != 2 {
		t.Fatalf("reload mismatch: vaultId %q len %d", loaded.Index.VaultID, loaded.Len())
	}
}

func TestNoteStoreEnableIsIdempotentAndAddsNewNotes(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.md"), []byte("# A"), 0644); err != nil {
		t.Fatal(err)
	}
	s1, err := EnableNoteStore(root)
	if err != nil {
		t.Fatal(err)
	}
	idA := mustNoteSyncID(t, s1, "a.md")

	// Re-enable: identity stable, no structural change.
	s2, err := EnableNoteStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if s2.Index.VaultID != s1.Index.VaultID {
		t.Fatalf("vault ID changed across restart: %s -> %s", s1.Index.VaultID, s2.Index.VaultID)
	}
	if got := mustNoteSyncID(t, s2, "a.md"); got != idA {
		t.Fatalf("sync ID changed across restart: %s -> %s", idA, got)
	}

	// A new note appears and gains a stable identity; a new folder does not.
	if err := os.MkdirAll(filepath.Join(root, "proj"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "b.md"), []byte("# B"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "proj", "c.md"), []byte("# C"), 0644); err != nil {
		t.Fatal(err)
	}
	s3, err := EnableNoteStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if s3.Len() != 3 {
		t.Fatalf("re-enable indexed %d notes, want 3", s3.Len())
	}
	if got := mustNoteSyncID(t, s3, "a.md"); got != idA {
		t.Fatalf("pre-existing identity changed after discovering new notes")
	}
	if got := mustNoteSyncID(t, s3, "proj/c.md"); got == idA {
		t.Fatal("distinct notes must not share an identity")
	}
	if p, ok := s3.PathByID(idA); !ok || p != "a.md" {
		t.Fatalf("PathByID = %q, %v, want a.md", p, ok)
	}
}

// TestNoteStoreLoadRejectsPrototypeSchema is the migration-classification exit
// gate: a schema-v1 prototype index is rejected as unsupported, never loaded as
// a baseline, and never overwritten by Enable.
func TestNoteStoreLoadRejectsPrototypeSchema(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, DirName), 0755); err != nil {
		t.Fatal(err)
	}
	v1 := `{"schemaVersion":1,"vaultId":"dc56ad15-62c6-4fa7-bf7a-5c6337d574be",` +
		`"entities":{"5d5d8b2c-94f7-4a38-8318-8cd4cb53dfa8":{"kind":"note","path":"idea.md"}}}`
	if err := os.WriteFile(filepath.Join(root, DirName, IndexName), []byte(v1), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadNoteStore(root); !errors.Is(err, ErrUnsupportedSchema) {
		t.Fatalf("LoadNoteStore on v1 = %v, want ErrUnsupportedSchema", err)
	}
	// Enable must not silently replace a prototype index.
	if _, err := EnableNoteStore(root); !errors.Is(err, ErrUnsupportedSchema) {
		t.Fatalf("EnableNoteStore on v1 = %v, want ErrUnsupportedSchema", err)
	}
	// The v1 bytes are untouched.
	data, err := os.ReadFile(filepath.Join(root, DirName, IndexName))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"schemaVersion":1`) {
		t.Fatal("prototype index was modified")
	}
}

// TestNoteStoreLoadFallsBackToBackup covers the migration-crash edge: a valid
// v2 backup is used when the primary is a non-v2 document or missing. The
// backup holds the last-known-good v2 identity, so identity is never lost.
func TestNoteStoreLoadFallsBackToBackup(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, DirName), 0755); err != nil {
		t.Fatal(err)
	}
	backupIdx := NewNoteIndex("dc56ad15-62c6-4fa7-bf7a-5c6337d574be")
	backupIdx.Notes["5d5d8b2c-94f7-4a38-8318-8cd4cb53dfa8"] = NoteEntry{Path: "idea.md"}
	backupData, err := backupIdx.Serialize()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, DirName, BackupName), backupData, 0600); err != nil {
		t.Fatal(err)
	}

	// Primary replaced by a v1 prototype document (a crash between renames
	// during a migration): the v2 backup is still used.
	v1 := `{"schemaVersion":1,"vaultId":"dc56ad15-62c6-4fa7-bf7a-5c6337d574be","entities":{}}`
	if err := os.WriteFile(filepath.Join(root, DirName, IndexName), []byte(v1), 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadNoteStore(root)
	if err != nil {
		t.Fatalf("LoadNoteStore with v2 backup = %v, want usable", err)
	}
	if p, ok := loaded.PathByID("5d5d8b2c-94f7-4a38-8318-8cd4cb53dfa8"); !ok || p != "idea.md" {
		t.Fatalf("backup baseline lost: %q, %v", p, ok)
	}

	// Primary missing, backup valid: still usable.
	if err := os.Remove(filepath.Join(root, DirName, IndexName)); err != nil {
		t.Fatal(err)
	}
	loaded, err = LoadNoteStore(root)
	if err != nil {
		t.Fatalf("LoadNoteStore with missing primary = %v", err)
	}
	if _, ok := loaded.PathByID("5d5d8b2c-94f7-4a38-8318-8cd4cb53dfa8"); !ok {
		t.Fatal("backup baseline lost after primary removal")
	}
}

// TestNoteStoreLoadRejectsPrototypeSchemaInBackup locks the classification when
// the only non-v2 document is the backup (the primary lost or corrupt): the
// vault still holds prototype state and must be reported as unsupported, never
// as ordinary corruption.
func TestNoteStoreLoadRejectsPrototypeSchemaInBackup(t *testing.T) {
	v1 := `{"schemaVersion":1,"vaultId":"dc56ad15-62c6-4fa7-bf7a-5c6337d574be","entities":{}}`
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, DirName), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, DirName, BackupName), []byte(v1), 0600); err != nil {
		t.Fatal(err)
	}
	// Primary missing, backup v1.
	if _, err := LoadNoteStore(root); !errors.Is(err, ErrUnsupportedSchema) {
		t.Fatalf("missing primary + v1 backup = %v, want ErrUnsupportedSchema", err)
	}
	// Primary corrupt, backup v1.
	if err := os.WriteFile(filepath.Join(root, DirName, IndexName), []byte(`{bad json`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadNoteStore(root); !errors.Is(err, ErrUnsupportedSchema) {
		t.Fatalf("corrupt primary + v1 backup = %v, want ErrUnsupportedSchema", err)
	}
}

// TestNoteStoreBackupPreservesPriorVersion locks the durability contract that
// the reviewer's finding targeted: a normal save must leave the .bak holding
// the PREVIOUS valid v2 identity, not the new bytes, so a later primary
// corruption still recovers the last-known-good state.
func TestNoteStoreBackupPreservesPriorVersion(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.md"), []byte("# A"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sub", "b.md"), []byte("# B"), 0644); err != nil {
		t.Fatal(err)
	}
	s, err := EnableNoteStore(root)
	if err != nil {
		t.Fatal(err)
	}
	idB := mustNoteSyncID(t, s, "sub/b.md")

	// First enable: primary and backup hold the same initial identity.
	primary1 := readIndexFile(t, root, IndexName)
	backup1 := readIndexFile(t, root, BackupName)
	if string(primary1) != string(backup1) {
		t.Fatal("first enable should write identical primary and backup")
	}

	// Add a note and save: the backup must keep the PRIOR version (without the
	// new note), the primary advances.
	if err := s.AddNote(NewVaultID(), "c.md"); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	primary2 := readIndexFile(t, root, IndexName)
	backup2 := readIndexFile(t, root, BackupName)
	if !bytes.Contains(primary2, []byte("c.md")) {
		t.Fatal("primary did not advance with the new note")
	}
	if !bytes.Equal(backup2, primary1) {
		t.Fatal("backup must hold the previous version after a save")
	}

	// If the primary is later lost, the backup still restores the prior
	// identity (which includes sub/b.md but not c.md).
	if err := os.Remove(filepath.Join(root, DirName, IndexName)); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadNoteStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := loaded.PathByID(idB); !ok {
		t.Fatal("backup recovery lost sub/b.md identity")
	}
	if _, ok := loaded.IDByPath("c.md"); ok {
		t.Fatal("backup unexpectedly contains the never-backed-up c.md")
	}
}

func readIndexFile(t *testing.T, root, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, DirName, name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// TestNoteStoreRebuildFromPrototypeLeavesBothFilesV2 covers the reviewer's
// scenario: after an explicit rebuild from a v1 prototype index, both primary
// and backup are v2, so a later primary corruption still recovers the fresh v2
// identity instead of falling back to the v1 prototype.
func TestNoteStoreRebuildFromPrototypeLeavesBothFilesV2(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.md"), []byte("# A"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, DirName), 0755); err != nil {
		t.Fatal(err)
	}
	v1 := `{"schemaVersion":1,"vaultId":"dc56ad15-62c6-4fa7-bf7a-5c6337d574be","entities":{}}`
	if err := os.WriteFile(filepath.Join(root, DirName, IndexName), []byte(v1), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, DirName, BackupName), []byte(v1), 0600); err != nil {
		t.Fatal(err)
	}

	if _, err := RebuildNoteStore(root, NewVaultID()); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{IndexName, BackupName} {
		if _, err := ParseNoteIndex(readIndexFile(t, root, name)); err != nil {
			t.Fatalf("%s is not v2 after rebuild: %v", name, err)
		}
	}
	// A corrupt primary after rebuild falls back to the v2 backup (not the v1
	// prototype, not unsupported).
	if err := os.WriteFile(filepath.Join(root, DirName, IndexName), []byte(`{bad json`), 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadNoteStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := loaded.IDByPath("a.md"); !ok {
		t.Fatal("fresh v2 identity lost after primary corruption")
	}
}

// TestNoteStoreReadIOErrorsStop is the loader's I/O-error policy: a real read
// failure on the primary or the backup stops the load instead of silently
// loading a stale file.
func TestNoteStoreReadIOErrorsStop(t *testing.T) {
	setup := func(t *testing.T) string {
		t.Helper()
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "a.md"), []byte("# A"), 0644); err != nil {
			t.Fatal(err)
		}
		if _, err := EnableNoteStore(root); err != nil {
			t.Fatal(err)
		}
		return root
	}

	t.Run("primary read failure", func(t *testing.T) {
		root := setup(t)
		fault := newFaultIndexIO(osIndexIO{})
		fault.armReadFail(IndexName, errors.New("permission denied"))
		if _, err := loadNoteStoreWithIO(root, fault); err == nil {
			t.Fatal("primary read failure silently ignored")
		}
	})

	t.Run("backup read failure", func(t *testing.T) {
		root := setup(t)
		// Remove the primary so the loader must consult the backup, which then
		// fails to read; the load must stop, not degrade to "missing backup".
		if err := os.Remove(filepath.Join(root, DirName, IndexName)); err != nil {
			t.Fatal(err)
		}
		fault := newFaultIndexIO(osIndexIO{})
		fault.armReadFail(BackupName, errors.New("permission denied"))
		if _, err := loadNoteStoreWithIO(root, fault); err == nil {
			t.Fatal("backup read failure silently ignored")
		} else if errors.Is(err, ErrNotEnabled) || errors.Is(err, ErrCorrupt) {
			t.Fatalf("backup read failure degraded to %v", err)
		}
	})
}

// TestNoteStoreSaveReadErrorsStop covers the reviewer's write-path finding: a
// real I/O error while a Save consults the current primary or backup must stop
// the save (poisoning the store) instead of silently rewriting the identity
// files on top of unreadable state.
func TestNoteStoreSaveReadErrorsStop(t *testing.T) {
	open := func(t *testing.T, root string) (*NoteStore, *faultIndexIO) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, "a.md"), []byte("# A"), 0644); err != nil {
			t.Fatal(err)
		}
		if _, err := EnableNoteStore(root); err != nil {
			t.Fatal(err)
		}
		fault := newFaultIndexIO(osIndexIO{})
		s, err := loadNoteStoreWithIO(root, fault)
		if err != nil {
			t.Fatal(err)
		}
		return s, fault
	}

	t.Run("primary read failure", func(t *testing.T) {
		root := t.TempDir()
		s, fault := open(t, root)
		before := readIndexFile(t, root, IndexName)
		fault.armReadFail(IndexName, errors.New("permission denied"))
		if err := s.AddNote(NewVaultID(), "x.md"); err != nil {
			t.Fatal(err)
		}
		if err := s.Save(); err == nil {
			t.Fatal("Save proceeded despite a primary read failure")
		}
		// The identity files are untouched: the save never wrote over
		// unreadable state.
		if got := readIndexFile(t, root, IndexName); !bytes.Equal(got, before) {
			t.Fatal("primary rewritten after a read failure")
		}
	})

	t.Run("backup read failure", func(t *testing.T) {
		root := t.TempDir()
		s, fault := open(t, root)
		before := readIndexFile(t, root, BackupName)
		fault.armReadFail(BackupName, errors.New("permission denied"))
		if err := s.AddNote(NewVaultID(), "x.md"); err != nil {
			t.Fatal(err)
		}
		if err := s.Save(); err == nil {
			t.Fatal("Save proceeded despite a backup read failure")
		}
		// The backup is never overwritten when it cannot be read.
		if got := readIndexFile(t, root, BackupName); !bytes.Equal(got, before) {
			t.Fatal("backup rewritten after a read failure")
		}
	})
}

// TestNoteStoreAddNoteSemantics pins the identity rules the conflict
// reservation depends on: same ID + same path is an idempotent no-op, same ID +
// different path is a conflict, and a path owned by another ID is a conflict.
func TestNoteStoreAddNoteSemantics(t *testing.T) {
	root := t.TempDir()
	s, err := EnableNoteStore(root)
	if err != nil {
		t.Fatal(err)
	}
	id := NewVaultID()
	if err := s.AddNote(id, "a.md"); err != nil {
		t.Fatal(err)
	}
	if s.dirty != true {
		t.Fatal("first add should mark the store dirty")
	}
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	if s.dirty != false {
		t.Fatal("Save should clear the dirty flag")
	}
	// Idempotent no-op: same ID, same path — no error, no structural change.
	if err := s.AddNote(id, "a.md"); err != nil {
		t.Fatalf("idempotent re-add failed: %v", err)
	}
	if s.dirty != false {
		t.Fatal("idempotent re-add must not mark the store dirty")
	}
	// Same ID, different path: a conflict, never a silent move.
	if err := s.AddNote(id, "b.md"); err == nil {
		t.Fatal("same Sync ID moved to a new path accepted")
	}
	if p, _ := s.PathByID(id); p != "a.md" {
		t.Fatalf("conflicting add changed the mapping to %q", p)
	}
	// A path owned by another ID is a conflict.
	if err := s.AddNote(NewVaultID(), "a.md"); err == nil {
		t.Fatal("path already indexed by another Sync ID accepted")
	}
}

// failed durable write at any numbered step leaves the prior primary and backup
// valid and reloadable, and poisons the store until Reload.
func TestNoteStoreReplaceRetainsPriorValidFiles(t *testing.T) {
	open := func(t *testing.T, root string) (*NoteStore, *faultIndexIO) {
		t.Helper()
		if _, err := EnableNoteStore(root); err != nil {
			t.Fatal(err)
		}
		fault := newFaultIndexIO(osIndexIO{})
		s, err := loadNoteStoreWithIO(root, fault)
		if err != nil {
			t.Fatal(err)
		}
		return s, fault
	}
	clone := func(idx *NoteIndex) *NoteIndex {
		out := NewNoteIndex(idx.VaultID)
		for id, e := range idx.Notes {
			out.Notes[id] = e
		}
		return out
	}

	t.Run("primary write failure", func(t *testing.T) {
		root := t.TempDir()
		s, fault := open(t, root)
		next := clone(s.Index)
		next.Notes["5d5d8b2c-94f7-4a38-8318-8cd4cb53dfa8"] = NoteEntry{Path: "x.md"}
		fault.armWriteFail(IndexName, errors.New("injected primary rename failure"))

		if err := s.ReplaceIndex(next); !errors.Is(err, ErrPoisoned) {
			t.Fatalf("ReplaceIndex = %v, want ErrPoisoned", err)
		}
		if s.poisoned != true {
			t.Fatal("store not poisoned after a failed primary write")
		}
		// Prior on-disk files are still valid v2 and the store reloads them.
		assertNoteIndexFilesParse(t, root)
		if err := s.Reload(); err != nil {
			t.Fatal(err)
		}
		if _, ok := s.PathByID("5d5d8b2c-94f7-4a38-8318-8cd4cb53dfa8"); ok {
			t.Fatal("failed ReplaceIndex leaked the new note into memory")
		}
	})

	t.Run("backup write failure", func(t *testing.T) {
		root := t.TempDir()
		s, fault := open(t, root)
		// Force the backup-write step to run (it is skipped while backup == primary).
		alt := NewNoteIndex(NewVaultID())
		alt.Notes["5d5d8b2c-94f7-4a38-8318-8cd4cb53dfa8"] = NoteEntry{Path: "alt.md"}
		data, _ := alt.Serialize()
		if err := os.WriteFile(filepath.Join(root, DirName, BackupName), data, 0600); err != nil {
			t.Fatal(err)
		}
		next := clone(s.Index)
		next.Notes["6e6e8b2c-94f7-4a38-8318-8cd4cb53dfa8"] = NoteEntry{Path: "y.md"}
		fault.armWriteFail(BackupName, errors.New("injected backup write failure"))

		if err := s.ReplaceIndex(next); !errors.Is(err, ErrPoisoned) {
			t.Fatalf("ReplaceIndex = %v, want ErrPoisoned", err)
		}
		// The backup-write failure happens AFTER the primary rename: the
		// in-memory index is unchanged, both on-disk files stay valid v2, and
		// Reload recovers whatever the primary durably holds.
		if _, ok := s.PathByID("6e6e8b2c-94f7-4a38-8318-8cd4cb53dfa8"); ok {
			t.Fatal("failed ReplaceIndex leaked the new note into memory before Reload")
		}
		assertNoteIndexFilesParse(t, root)
		if err := s.Reload(); err != nil {
			t.Fatal(err)
		}
		if s.poisoned {
			t.Fatal("Reload did not clear the poison")
		}
	})

	t.Run("directory sync failure", func(t *testing.T) {
		root := t.TempDir()
		s, fault := open(t, root)
		next := clone(s.Index)
		next.Notes["6e6e8b2c-94f7-4a38-8318-8cd4cb53dfa8"] = NoteEntry{Path: "y.md"}
		fault.armSyncFail(errors.New("injected directory fsync failure"))

		// The rename landed but the directory sync failed: the write is
		// indeterminate, so the store reports ErrPoisoned and requires Reload.
		if err := s.ReplaceIndex(next); !errors.Is(err, ErrPoisoned) {
			t.Fatalf("ReplaceIndex = %v, want ErrPoisoned", err)
		}
		assertNoteIndexFilesParse(t, root)
		if err := s.Reload(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestNoteStoreMutations(t *testing.T) {
	root := t.TempDir()
	s, err := EnableNoteStore(root)
	if err != nil {
		t.Fatal(err)
	}
	id := NewVaultID()
	if err := s.AddNote(id, "a.md"); err != nil {
		t.Fatal(err)
	}
	// Re-adding the SAME Sync ID is idempotent; a different Sync ID on the same
	// path is rejected.
	if err := s.AddNote(id, "a.md"); err != nil {
		t.Fatalf("same-ID re-add failed: %v", err)
	}
	if err := s.AddNote(NewVaultID(), "a.md"); err == nil {
		t.Fatal("duplicate path with a different Sync ID accepted")
	}
	if err := s.UpdatePath(id, "b.md"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.PathByID(id); got != "b.md" {
		t.Fatalf("PathByID = %q, want b.md", got)
	}
	if err := s.RemoveNote(id); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.PathByID(id); ok {
		t.Fatal("removed note still indexed")
	}
	// AddNote/UpdatePath reject unsafe paths and non-v4/v5 IDs.
	if err := s.AddNote(NewVaultID(), "../evil.md"); err == nil {
		t.Fatal("unsafe path accepted")
	}
	if err := s.AddNote("not-a-uuid", "c.md"); err == nil {
		t.Fatal("invalid sync ID accepted")
	}
	if err := s.UpdatePath(id, "x\\y.md"); err == nil {
		t.Fatal("backslash path accepted")
	}
}

func TestNoteStoreSortedIDs(t *testing.T) {
	root := t.TempDir()
	for _, p := range []string{"b.md", "a.md", "sub/c.md"} {
		if err := os.MkdirAll(filepath.Join(root, filepath.Dir(p)), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(p)), []byte("# x"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	s, err := EnableNoteStore(root)
	if err != nil {
		t.Fatal(err)
	}
	ids := s.SortedIDs()
	if !slices.IsSorted(ids) {
		t.Fatalf("SortedIDs not sorted: %v", ids)
	}
	if len(ids) != 3 {
		t.Fatalf("SortedIDs = %d IDs, want 3", len(ids))
	}
}

func TestNoteStoreRebuild(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.md"), []byte("# A"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := EnableNoteStore(root); err != nil {
		t.Fatal(err)
	}
	// A rebuild mints a fresh index from the vault scan with the given vault ID.
	vaultID := NewVaultID()
	rb, err := RebuildNoteStore(root, vaultID)
	if err != nil {
		t.Fatal(err)
	}
	if rb.Index.VaultID != vaultID || rb.Len() != 1 {
		t.Fatalf("rebuild vaultId %q len %d, want %q len 1", rb.Index.VaultID, rb.Len(), vaultID)
	}
	if p, ok := rb.PathByID(mustNoteSyncID(t, rb, "a.md")); !ok || p != "a.md" {
		t.Fatalf("rebuild lost a.md identity")
	}
}

func TestNoteStoreCreate(t *testing.T) {
	root := t.TempDir()
	vaultID := "dc56ad15-62c6-4fa7-bf7a-5c6337d574be"
	s, err := CreateNoteStore(root, vaultID)
	if err != nil {
		t.Fatal(err)
	}
	if s.Len() != 0 {
		t.Fatalf("CreateNoteStore indexed %d notes, want 0", s.Len())
	}
	// Create refuses a second enable.
	if _, err := CreateNoteStore(root, NewVaultID()); err == nil {
		t.Fatal("CreateNoteStore on an enabled vault accepted")
	}
	// Create refuses a prototype index.
	root2 := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root2, DirName), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root2, DirName, IndexName),
		[]byte(`{"schemaVersion":1,"vaultId":"`+vaultID+`","entities":{}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := CreateNoteStore(root2, NewVaultID()); !errors.Is(err, ErrUnsupportedSchema) {
		t.Fatalf("CreateNoteStore on prototype = %v, want ErrUnsupportedSchema", err)
	}
}

// readVault reads a note body from a vault root.
func readVault(t *testing.T, root, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

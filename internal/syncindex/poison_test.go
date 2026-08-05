package syncindex

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// openFaultStore enables sync in an empty vault and reopens the index through
// a fault-injectable io, so tests can fail any numbered durability step.
func openFaultStore(t *testing.T, root string) (*Store, *faultIndexIO) {
	t.Helper()
	if _, err := Enable(root); err != nil {
		t.Fatal(err)
	}
	fault := newFaultIndexIO(osIndexIO{})
	s, err := loadWithIO(root, fault)
	if err != nil {
		t.Fatal(err)
	}
	return s, fault
}

func cloneIndex(idx *Index) *Index {
	out := New(idx.VaultID)
	for id, e := range idx.Entities {
		out.Entities[id] = e
	}
	return out
}

// divergeBackup rewrites the .bak with a different valid index, so the next
// durable write actually exercises the backup-write step (it is skipped while
// the backup already holds the known-good bytes).
func divergeBackup(t *testing.T, root string) {
	t.Helper()
	alt := New(NewVaultID())
	alt.Entities[NewVaultID()] = Entity{Kind: "note", Path: "alt.md"}
	data, err := alt.Serialize()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, DirName, BackupName), data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestReplaceIndexPoisonsOnPrimaryWriteFailure(t *testing.T) {
	root := t.TempDir()
	s, fault := openFaultStore(t, root)

	next := cloneIndex(s.Index)
	next.Entities[NewVaultID()] = Entity{Kind: "note", Path: "x.md"}
	fault.armWriteFail(IndexName, errors.New("injected primary rename failure"))

	if err := s.ReplaceIndex(next); !errors.Is(err, ErrPoisoned) {
		t.Fatalf("ReplaceIndex = %v, want ErrPoisoned", err)
	}
	// The in-memory index is unchanged despite the failed write.
	if _, ok := s.FindByPath("x.md"); ok {
		t.Fatal("failed ReplaceIndex leaked the new entity into memory")
	}
	if s.poisoned != true {
		t.Fatal("store not poisoned after a failed primary write")
	}
	// Subsequent mutations refuse until Reload.
	if err := s.AddEntity(NewVaultID(), Entity{Kind: "note", Path: "y.md"}); !errors.Is(err, ErrPoisoned) {
		t.Fatalf("AddEntity on poisoned store = %v, want ErrPoisoned", err)
	}
	if err := s.Reload(); err != nil {
		t.Fatal(err)
	}
	if s.poisoned {
		t.Fatal("Reload did not clear the poison")
	}
	if err := s.AddEntity(NewVaultID(), Entity{Kind: "note", Path: "y.md"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
}

func TestReplaceIndexPoisonsOnBackupWriteFailure(t *testing.T) {
	root := t.TempDir()
	s, fault := openFaultStore(t, root)
	// Force the backup-write step to run (it is skipped while backup == primary).
	divergeBackup(t, root)

	next := cloneIndex(s.Index)
	next.Entities[NewVaultID()] = Entity{Kind: "note", Path: "x.md"}
	fault.armWriteFail(BackupName, errors.New("injected backup write failure"))

	if err := s.ReplaceIndex(next); !errors.Is(err, ErrPoisoned) {
		t.Fatalf("ReplaceIndex = %v, want ErrPoisoned", err)
	}
	if _, ok := s.FindByPath("x.md"); ok {
		t.Fatal("failed ReplaceIndex leaked the new entity into memory")
	}
	if !s.poisoned {
		t.Fatal("store not poisoned after a failed backup write")
	}
	if err := s.Reload(); err != nil {
		t.Fatal(err)
	}
}

func TestReplaceIndexPoisonsOnDirSyncFailure(t *testing.T) {
	root := t.TempDir()
	s, fault := openFaultStore(t, root)

	next := cloneIndex(s.Index)
	next.Entities[NewVaultID()] = Entity{Kind: "note", Path: "x.md"}
	fault.armSyncFail(errors.New("injected directory fsync failure"))

	if err := s.ReplaceIndex(next); !errors.Is(err, ErrPoisoned) {
		t.Fatalf("ReplaceIndex = %v, want ErrPoisoned", err)
	}
	if !s.poisoned {
		t.Fatal("store not poisoned after a failed directory fsync")
	}
	if err := s.Reload(); err != nil {
		t.Fatal(err)
	}
}

func TestSavePoisonsOnWriteFailure(t *testing.T) {
	root := t.TempDir()
	s, fault := openFaultStore(t, root)
	if err := s.AddEntity(NewVaultID(), Entity{Kind: "note", Path: "x.md"}); err != nil {
		t.Fatal(err)
	}
	fault.armWriteFail(IndexName, errors.New("injected primary rename failure"))
	if err := s.Save(); !errors.Is(err, ErrPoisoned) {
		t.Fatalf("Save = %v, want ErrPoisoned", err)
	}
	if !s.poisoned {
		t.Fatal("store not poisoned after a failed Save")
	}
	if err := s.Reload(); err != nil {
		t.Fatal(err)
	}
}

func TestPoisonedStoreRefusesEveryMutation(t *testing.T) {
	root := t.TempDir()
	s, fault := openFaultStore(t, root)
	if err := s.AddEntity(NewVaultID(), Entity{Kind: "note", Path: "a.md"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	aID, _ := s.FindByPath("a.md")

	// Poison the store with a failed primary write.
	fault.armWriteFail(IndexName, errors.New("injected failure"))
	next := cloneIndex(s.Index)
	next.Entities[NewVaultID()] = Entity{Kind: "note", Path: "x.md"}
	if err := s.ReplaceIndex(next); !errors.Is(err, ErrPoisoned) {
		t.Fatalf("ReplaceIndex = %v, want ErrPoisoned", err)
	}

	// Every mutation API refuses a poisoned store.
	if err := s.AddEntity(NewVaultID(), Entity{Kind: "note", Path: "y.md"}); !errors.Is(err, ErrPoisoned) {
		t.Fatalf("AddEntity on poisoned store = %v, want ErrPoisoned", err)
	}
	if err := s.UpdatePath(aID, "renamed.md"); !errors.Is(err, ErrPoisoned) {
		t.Fatalf("UpdatePath on poisoned store = %v, want ErrPoisoned", err)
	}
	if err := s.RemoveEntity(aID); !errors.Is(err, ErrPoisoned) {
		t.Fatalf("RemoveEntity on poisoned store = %v, want ErrPoisoned", err)
	}
	if err := s.ReplaceIndex(cloneIndex(s.Index)); !errors.Is(err, ErrPoisoned) {
		t.Fatalf("ReplaceIndex on poisoned store = %v, want ErrPoisoned", err)
	}
	// A clean Save() (nothing pending) must ALSO refuse: the caller must never
	// mistake the poisoned state for normalcy.
	if err := s.Save(); !errors.Is(err, ErrPoisoned) {
		t.Fatalf("clean Save on poisoned store = %v, want ErrPoisoned", err)
	}
	// None of the above touched the in-memory index.
	if _, ok := s.FindByPath("x.md"); ok {
		t.Fatal("failed ReplaceIndex leaked the new entity into memory")
	}
	if _, ok := s.FindByPath("renamed.md"); ok {
		t.Fatal("UpdatePath on a poisoned store leaked into memory")
	}
	if _, ok := s.FindByPath("a.md"); !ok {
		t.Fatal("RemoveEntity on a poisoned store deleted a.md")
	}

	// Reload restores a usable store.
	if err := s.Reload(); err != nil {
		t.Fatal(err)
	}
	if s.poisoned {
		t.Fatal("Reload did not clear the poison")
	}
	if err := s.AddEntity(NewVaultID(), Entity{Kind: "note", Path: "ok.md"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
}

func TestReplaceIndexValidationFailureLeavesStoreUsable(t *testing.T) {
	root := t.TempDir()
	s, _ := openFaultStore(t, root)

	bad := cloneIndex(s.Index)
	bad.Entities[NewVaultID()] = Entity{Kind: "note", Path: "../escape.md"}
	if err := s.ReplaceIndex(bad); err == nil {
		t.Fatal("invalid index accepted")
	} else if errors.Is(err, ErrPoisoned) {
		t.Fatalf("validation failure must not poison the store: %v", err)
	}
	if s.poisoned {
		t.Fatal("store poisoned after a validation-only failure")
	}
	// The store remains fully usable.
	if err := s.AddEntity(NewVaultID(), Entity{Kind: "note", Path: "ok.md"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	reloaded, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := reloaded.FindByPath("ok.md"); !ok {
		t.Fatal("ok.md not durable after a usable save")
	}
}

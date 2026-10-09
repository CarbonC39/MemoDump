package syncstate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func recoveryStore(t *testing.T) *RecoveryStore {
	t.Helper()
	s, err := NewRecoveryStore(t.TempDir(), testVaultID, testReplicaID)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestRecoveryStoreWriteReadIdempotent(t *testing.T) {
	s := recoveryStore(t)
	syncID := "5d5d8b2c-94f7-4a38-8318-8cd4cb53dfa8"
	hash := strings.Repeat("a", 64)

	if err := s.Write(syncID, hash, "# v1\n"); err != nil {
		t.Fatal(err)
	}
	// The exact write again is a no-op (one file, one rewrite).
	info1, err := os.Stat(filepath.Join(s.dir, syncID, hash+".md"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Write(syncID, hash, "# v1\n"); err != nil {
		t.Fatal(err)
	}
	info2, _ := os.Stat(filepath.Join(s.dir, syncID, hash+".md"))
	if info2.ModTime() != info1.ModTime() || info2.Size() != info1.Size() {
		t.Fatalf("idempotent rewrite changed the file: %v -> %v", info1, info2)
	}

	md, _, ok, err := s.Read(syncID, hash)
	if err != nil || !ok || md != "# v1\n" {
		t.Fatalf("read = %q, %v, %v", md, ok, err)
	}
	if _, _, ok, err := s.Read(syncID, strings.Repeat("b", 64)); err != nil || ok {
		t.Fatalf("missing hash read = %v, %v", ok, err)
	}
}

func TestRecoveryStoreKeepsMultipleStateHashes(t *testing.T) {
	s := recoveryStore(t)
	syncID := "5d5d8b2c-94f7-4a38-8318-8cd4cb53dfa8"
	if err := s.Write(syncID, strings.Repeat("a", 64), "# v1\n"); err != nil {
		t.Fatal(err)
	}
	if err := s.Write(syncID, strings.Repeat("b", 64), "# v2\n"); err != nil {
		t.Fatal(err)
	}
	all, err := s.List(syncID)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("list = %d copies, want 2 (no overwrite of the first)", len(all))
	}
}

func TestRecoveryStoreRejectsUnsafeIDs(t *testing.T) {
	s := recoveryStore(t)
	hash := strings.Repeat("a", 64)
	for _, bad := range []string{"../escape", "not-a-uuid", ""} {
		if err := s.Write(bad, hash, "x"); err == nil {
			t.Errorf("Write(%q) accepted", bad)
		}
	}
	for _, badHash := range []string{"../x", "ABC", "abc"} {
		if err := s.Write("5d5d8b2c-94f7-4a38-8318-8cd4cb53dfa8", badHash, "x"); err == nil {
			t.Errorf("Write with unsafe hash %q accepted", badHash)
		}
	}
	// Nothing escaped the recovery directory.
	if entries, _ := os.ReadDir(s.dir); len(entries) != 0 {
		t.Fatalf("unsafe IDs wrote outside the expected structure: %+v", entries)
	}
}

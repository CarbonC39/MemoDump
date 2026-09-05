package s3

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"memodump/internal/cloudsync"
	"memodump/internal/syncindex"
	"memodump/internal/syncservice"
	"memodump/internal/syncstate"
)

const s3RepoID = "33333333-3333-4333-8333-333333333333"

type s3ReplicaInfo struct {
	svc       *syncservice.Service
	root      string
	stateRoot string
	vaultID   string
	replicaID string
}

// s3Replica enables a vault and returns a sync service wired to the S3
// provider plus the replica identity.
func s3Replica(t *testing.T, root, state string, provider syncservice.Provider, profile string) *s3ReplicaInfo {
	t.Helper()
	idx, err := syncindex.EnableNoteStore(root)
	if err != nil {
		t.Fatal(err)
	}
	_, replicaID, err := syncstate.Resolve(state, root, idx.Index.VaultID)
	if err != nil {
		t.Fatal(err)
	}
	return &s3ReplicaInfo{
		svc: syncservice.New(syncservice.Config{
			RepoRoot: root, StateRoot: state,
			VaultID: idx.Index.VaultID, ReplicaID: string(replicaID),
			RepoID: s3RepoID, Profile: profile, Provider: provider,
		}),
		root: root, stateRoot: state,
		vaultID: idx.Index.VaultID, replicaID: string(replicaID),
	}
}

func readVaultFile(t *testing.T, root, path string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
	if err != nil {
		return ""
	}
	return string(data)
}

func (r *s3ReplicaInfo) recoveryDir() string {
	return filepath.Join(syncstate.StateDir(r.stateRoot, r.vaultID, r.replicaID), syncstate.RecoveryDirName)
}

// conflictNotes lists the deterministic conflict-note files in the vault.
func (r *s3ReplicaInfo) conflictNotes(t *testing.T) []string {
	t.Helper()
	var found []string
	entries, err := os.ReadDir(r.root)
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

// TestS3ReplicasConverge drives two local replicas against one shared S3
// provider (a fake S3 endpoint) for create, edit, and delete: the R4 exit gate
// for a live adapter.
func TestS3ReplicasConverge(t *testing.T) {
	ctx := context.Background()
	cc, _ := newServer(t, newFakeS3())
	provider := func() (cloudsync.RemoteStore, error) { return cc, nil }

	a := s3Replica(t, t.TempDir(), t.TempDir(), provider, cc.Profile())
	b := s3Replica(t, t.TempDir(), t.TempDir(), provider, cc.Profile())

	// Create: A uploads, B pulls.
	if err := os.WriteFile(filepath.Join(a.root, "idea.md"), []byte("# Idea\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if res, err := a.svc.Run(ctx); err != nil || !res.Synced {
		t.Fatalf("A create run = %+v, %v", res, err)
	}
	if res, err := b.svc.Run(ctx); err != nil || !res.Synced {
		t.Fatalf("B create run = %+v, %v", res, err)
	}
	if got := readVaultFile(t, b.root, "idea.md"); got != "# Idea\n" {
		t.Fatalf("B did not pull the created note: %q", got)
	}

	// Edit: A edits, B follows.
	if err := os.WriteFile(filepath.Join(a.root, "idea.md"), []byte("# Idea edited\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if res, err := a.svc.Run(ctx); err != nil || !res.Synced {
		t.Fatalf("A edit run = %+v, %v", res, err)
	}
	if res, err := b.svc.Run(ctx); err != nil || !res.Synced {
		t.Fatalf("B edit run = %+v, %v", res, err)
	}
	if got := readVaultFile(t, b.root, "idea.md"); got != "# Idea edited\n" {
		t.Fatalf("B did not follow the edit: %q", got)
	}

	// Delete: A deletes, B applies the tombstone.
	if err := os.Remove(filepath.Join(a.root, "idea.md")); err != nil {
		t.Fatal(err)
	}
	if res, err := a.svc.Run(ctx); err != nil || !res.Synced {
		t.Fatalf("A delete run = %+v, %v", res, err)
	}
	if res, err := b.svc.Run(ctx); err != nil || !res.Synced {
		t.Fatalf("B delete run = %+v, %v", res, err)
	}
	if _, err := os.Stat(filepath.Join(b.root, "idea.md")); !os.IsNotExist(err) {
		t.Fatal("B did not apply the tombstone")
	}
}

// TestS3ReplicasNestedConflictAndRecovery covers the rest of the R2 scenarios
// applicable to a live adapter: nested paths, concurrent-edit conflicts, the
// reverse delete direction, and recovery copies.
func TestS3ReplicasNestedConflictAndRecovery(t *testing.T) {
	ctx := context.Background()
	cc, _ := newServer(t, newFakeS3())
	provider := func() (cloudsync.RemoteStore, error) { return cc, nil }

	a := s3Replica(t, t.TempDir(), t.TempDir(), provider, cc.Profile())
	b := s3Replica(t, t.TempDir(), t.TempDir(), provider, cc.Profile())

	// Nested create + a shared note: A creates a note under directories B has
	// never seen, and a flat note both will later edit.
	if err := os.MkdirAll(filepath.Join(a.root, "x", "y", "z"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(a.root, "x", "y", "z", "deep.md"), []byte("# deep\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(a.root, "a.md"), []byte("# base\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if res, err := a.svc.Run(ctx); err != nil || !res.Synced {
		t.Fatalf("A nested create = %+v, %v", res, err)
	}
	if res, err := b.svc.Run(ctx); err != nil || !res.Synced {
		t.Fatalf("B nested run = %+v, %v", res, err)
	}
	if got := readVaultFile(t, b.root, "x/y/z/deep.md"); got != "# deep\n" {
		t.Fatalf("B did not pull the nested note: %q", got)
	}
	if got := readVaultFile(t, b.root, "a.md"); got != "# base\n" {
		t.Fatalf("B did not pull the shared note: %q", got)
	}

	// Concurrent edit: both edit the SAME synced note differently; B's edit is
	// preserved as a conflict note and the remote wins the original path.
	if err := os.WriteFile(filepath.Join(a.root, "a.md"), []byte("# A edit\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(b.root, "a.md"), []byte("# B edit\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if res, err := a.svc.Run(ctx); err != nil || !res.Synced {
		t.Fatalf("A edit = %+v, %v", res, err)
	}
	if res, err := b.svc.Run(ctx); err != nil {
		t.Fatalf("B conflict run err = %v", err)
	} else if res.Conflicts != 1 {
		t.Fatalf("B conflict run = %+v, want 1 conflict", res)
	}
	if got := readVaultFile(t, b.root, "a.md"); got != "# A edit\n" {
		t.Fatalf("B original = %q, want A's edit", got)
	}
	conflicts := b.conflictNotes(t)
	if len(conflicts) != 1 || readVaultFile(t, b.root, conflicts[0]) != "# B edit\n" {
		t.Fatalf("B conflict notes = %v with bodies %q", conflicts, readVaultFile(t, b.root, conflicts[0]))
	}
	// A converges on B's conflict note.
	if res, err := a.svc.Run(ctx); err != nil || !res.Synced {
		t.Fatalf("A conflict convergence = %+v, %v", res, err)
	}
	if got := a.conflictNotes(t); len(got) != 1 {
		t.Fatalf("A did not pull B's conflict note: %v", got)
	}

	// Reverse delete: B deletes the nested note; A applies the tombstone and
	// writes a durable recovery copy.
	if err := os.Remove(filepath.Join(b.root, "x", "y", "z", "deep.md")); err != nil {
		t.Fatal(err)
	}
	if res, err := b.svc.Run(ctx); err != nil || !res.Synced {
		t.Fatalf("B reverse delete = %+v, %v", res, err)
	}
	if res, err := a.svc.Run(ctx); err != nil || !res.Synced {
		t.Fatalf("A tombstone apply = %+v, %v", res, err)
	}
	if _, err := os.Stat(filepath.Join(a.root, "x", "y", "z", "deep.md")); !os.IsNotExist(err) {
		t.Fatal("A did not apply the reverse tombstone")
	}
	// A wrote a recovery copy recording the original path.
	var recoveryFiles []string
	_ = filepath.Walk(a.recoveryDir(), func(p string, info os.FileInfo, err error) error {
		if err == nil && info != nil && !info.IsDir() && strings.HasSuffix(p, ".md") {
			recoveryFiles = append(recoveryFiles, p)
		}
		return nil
	})
	if len(recoveryFiles) == 0 {
		t.Fatal("A wrote no recovery copy for the applied tombstone")
	}
}

package s3

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"memodump/internal/cloudsync"
	"memodump/internal/syncindex"
	"memodump/internal/syncservice"
	"memodump/internal/syncstate"
)

const s3RepoID = "33333333-3333-4333-8333-333333333333"

// s3Replica enables a vault and returns a sync service wired to the S3
// provider.
func s3Replica(t *testing.T, root, state string, provider syncservice.Provider, profile string) *syncservice.Service {
	t.Helper()
	idx, err := syncindex.EnableNoteStore(root)
	if err != nil {
		t.Fatal(err)
	}
	_, replicaID, err := syncstate.Resolve(state, root, idx.Index.VaultID)
	if err != nil {
		t.Fatal(err)
	}
	return syncservice.New(syncservice.Config{
		RepoRoot: root, StateRoot: state,
		VaultID: idx.Index.VaultID, ReplicaID: string(replicaID),
		RepoID: s3RepoID, Profile: profile, Provider: provider,
	})
}

func readVaultFile(t *testing.T, root, path string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
	if err != nil {
		return ""
	}
	return string(data)
}

// TestS3ReplicasConverge drives two local replicas against one shared S3
// provider (a fake S3 endpoint) for create, edit, and delete: the R4 exit gate
// for a live adapter.
func TestS3ReplicasConverge(t *testing.T) {
	ctx := context.Background()
	cc, _ := newServer(t, newFakeS3())
	profile := cc.Profile()
	provider := func() (cloudsync.RemoteStore, error) { return cc, nil }

	rootA, rootB := t.TempDir(), t.TempDir()
	stateA, stateB := t.TempDir(), t.TempDir()
	a := s3Replica(t, rootA, stateA, provider, profile)
	b := s3Replica(t, rootB, stateB, provider, profile)

	// Create: A uploads, B pulls.
	if err := os.WriteFile(filepath.Join(rootA, "idea.md"), []byte("# Idea\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if res, err := a.Run(ctx); err != nil || !res.Synced {
		t.Fatalf("A create run = %+v, %v", res, err)
	}
	if res, err := b.Run(ctx); err != nil || !res.Synced {
		t.Fatalf("B create run = %+v, %v", res, err)
	}
	if got := readVaultFile(t, rootB, "idea.md"); got != "# Idea\n" {
		t.Fatalf("B did not pull the created note: %q", got)
	}

	// Edit: A edits, B follows.
	if err := os.WriteFile(filepath.Join(rootA, "idea.md"), []byte("# Idea edited\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if res, err := a.Run(ctx); err != nil || !res.Synced {
		t.Fatalf("A edit run = %+v, %v", res, err)
	}
	if res, err := b.Run(ctx); err != nil || !res.Synced {
		t.Fatalf("B edit run = %+v, %v", res, err)
	}
	if got := readVaultFile(t, rootB, "idea.md"); got != "# Idea edited\n" {
		t.Fatalf("B did not follow the edit: %q", got)
	}

	// Delete: A deletes, B applies the tombstone.
	if err := os.Remove(filepath.Join(rootA, "idea.md")); err != nil {
		t.Fatal(err)
	}
	if res, err := a.Run(ctx); err != nil || !res.Synced {
		t.Fatalf("A delete run = %+v, %v", res, err)
	}
	if res, err := b.Run(ctx); err != nil || !res.Synced {
		t.Fatalf("B delete run = %+v, %v", res, err)
	}
	if _, err := os.Stat(filepath.Join(rootB, "idea.md")); !os.IsNotExist(err) {
		t.Fatal("B did not apply the tombstone")
	}

	// Both converged: no further work produces changes.
	if res, err := a.Run(ctx); err != nil || !res.Synced {
		t.Fatalf("A post-convergence run = %+v, %v", res, err)
	}
	if res, err := b.Run(ctx); err != nil || !res.Synced {
		t.Fatalf("B post-convergence run = %+v, %v", res, err)
	}
}

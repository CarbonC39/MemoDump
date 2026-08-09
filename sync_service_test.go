package main

import (
	"context"
	"os"
	"testing"

	"memodump/internal/cloudsync"
	"memodump/internal/syncprovider/s3"
)

// TestSyncRepoIdentityCreatesAndValidatesRepo: repo.json is created only if
// absent with a fresh Repository ID, read back consistently, and a corrupt
// repository stops.
func TestSyncRepoIdentityCreatesAndValidatesRepo(t *testing.T) {
	ctx := context.Background()
	store := cloudsync.NewMemoryStore()
	repoID, profile, err := syncRepoIdentity(ctx, store, false)
	if err != nil {
		t.Fatal(err)
	}
	if repoID == "" || !cloudsync.IsUUIDv4(repoID) {
		t.Fatalf("repoID = %q, want a v4 UUID", repoID)
	}
	if profile != memoryProfile {
		t.Fatalf("memory profile = %q, want %q", profile, memoryProfile)
	}
	// A second call reads the same repository.
	again, _, err := syncRepoIdentity(ctx, store, true)
	if err != nil || again != repoID {
		t.Fatalf("second read = %q, %v; want %q", again, err, repoID)
	}
	// A corrupt repository is rejected, never treated as absent.
	store2 := cloudsync.NewMemoryStore()
	if err := store2.Seed("repo.json", []byte(`{bad json`), "1"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := syncRepoIdentity(ctx, store2, false); err == nil {
		t.Fatal("corrupt repo.json accepted")
	}
}

// TestSyncRepoIdentityKnownLostStops: when a repository is known (a snapshot
// exists), a missing repo.json is remote damage and must stop with zero writes
// — never replaced with a fresh repository.
func TestSyncRepoIdentityKnownLostStops(t *testing.T) {
	ctx := context.Background()
	store := cloudsync.NewMemoryStore()
	if _, _, err := syncRepoIdentity(ctx, store, true); err == nil {
		t.Fatal("missing repo.json for a known repository must stop")
	}
	// Zero writes: repo.json must not have been created.
	if _, _, err := store.Read(ctx, "repo.json"); !cloudsync.IsStoreError(err, cloudsync.ErrNotFound) {
		t.Fatal("known-repo loss wrote a new repo.json")
	}
}

// TestSyncRepoIdentityCreateRaceAdoptsWinner: losing a concurrent first-create
// re-reads the winner's repo.json instead of failing.
func TestSyncRepoIdentityCreateRaceAdoptsWinner(t *testing.T) {
	ctx := context.Background()
	store := cloudsync.NewMemoryStore()
	winner := cloudsync.RepositoryDescriptor{
		FormatVersion: 1, RepositoryID: "99999999-9999-4999-8999-999999999999",
		CreatedAt: 1, MinimumClientVersion: "2.0.0",
	}
	ser, _ := winner.Serialize()
	if err := store.Seed("repo.json", ser, "1"); err != nil {
		t.Fatal(err)
	}
	// The first read is faulted to not-found (as if the key were absent), then
	// another client's repo.json is already present, so the create collides and
	// the identity is re-read from the winner.
	store.ArmFault("read", &cloudsync.StoreError{Kind: cloudsync.ErrNotFound, Message: "flaky"})
	repoID, _, err := syncRepoIdentity(ctx, store, false)
	if err != nil || repoID != winner.RepositoryID {
		t.Fatalf("race adoption = %q, %v; want the winner %q", repoID, err, winner.RepositoryID)
	}
}

// TestSyncDefaultProviderSelectsS3WhenConfigured: the production provider is an
// S3 client when MEMODUMP_SYNC_* is configured; a partial config is an error,
// and the memory remote is available only behind the explicit dev switch.
func TestSyncDefaultProviderSelectsS3WhenConfigured(t *testing.T) {
	old := syncProvider
	t.Cleanup(func() { syncProvider = old })

	os.Unsetenv("MEMODUMP_SYNC_ENDPOINT")
	os.Unsetenv("MEMODUMP_SYNC_BUCKET")
	os.Unsetenv("MEMODUMP_SYNC_MEMORY")
	os.Unsetenv("MEMODUMP_SYNC_PREFIX")
	t.Cleanup(func() {
		os.Unsetenv("MEMODUMP_SYNC_ENDPOINT")
		os.Unsetenv("MEMODUMP_SYNC_BUCKET")
		os.Unsetenv("MEMODUMP_SYNC_MEMORY")
		os.Unsetenv("MEMODUMP_SYNC_PREFIX")
	})

	// No config at all: an error unless the dev switch is set.
	if _, err := defaultSyncProvider(); err == nil {
		t.Fatal("unconfigured provider must error")
	}
	os.Setenv("MEMODUMP_SYNC_MEMORY", "1")
	if remote, err := defaultSyncProvider(); err != nil || remote != syncMemoryRemote {
		t.Fatalf("memory-switch provider = %T, %v; want the memory remote", remote, err)
	}
	os.Unsetenv("MEMODUMP_SYNC_MEMORY")

	// Partial config: an error, never a silent memory fallback.
	os.Setenv("MEMODUMP_SYNC_ENDPOINT", "http://localhost:9000")
	if _, err := defaultSyncProvider(); err == nil {
		t.Fatal("partial S3 config must error")
	}

	// Full config: an S3 client with a secret-free profile.
	os.Setenv("MEMODUMP_SYNC_BUCKET", "notes")
	os.Setenv("MEMODUMP_SYNC_PREFIX", "vault")
	remote, err := defaultSyncProvider()
	if err != nil {
		t.Fatal(err)
	}
	client, ok := remote.(*s3.Client)
	if !ok {
		t.Fatalf("configured provider = %T, want *s3.Client", remote)
	}
	if providerProfile(client) == memoryProfile {
		t.Fatal("S3 profile collides with the memory profile")
	}
}

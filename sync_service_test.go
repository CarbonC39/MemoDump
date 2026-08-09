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
	repoID, profile, err := syncRepoIdentity(ctx, store)
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
	again, _, err := syncRepoIdentity(ctx, store)
	if err != nil || again != repoID {
		t.Fatalf("second read = %q, %v; want %q", again, err, repoID)
	}
	// A corrupt repository is rejected, never treated as absent.
	store2 := cloudsync.NewMemoryStore()
	if err := store2.Seed("repo.json", []byte(`{bad json`), "1"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := syncRepoIdentity(ctx, store2); err == nil {
		t.Fatal("corrupt repo.json accepted")
	}
}

// TestSyncDefaultProviderSelectsS3WhenConfigured: the production provider is an
// S3 client when MEMODUMP_SYNC_* is configured and the memory remote otherwise.
func TestSyncDefaultProviderSelectsS3WhenConfigured(t *testing.T) {
	old := syncProvider
	t.Cleanup(func() { syncProvider = old })

	os.Unsetenv("MEMODUMP_SYNC_ENDPOINT")
	os.Unsetenv("MEMODUMP_SYNC_BUCKET")
	remote, err := defaultSyncProvider()
	if err != nil {
		t.Fatal(err)
	}
	if remote != syncMemoryRemote {
		t.Fatalf("unconfigured provider = %T, want the memory remote", remote)
	}

	os.Setenv("MEMODUMP_SYNC_ENDPOINT", "http://localhost:9000")
	os.Setenv("MEMODUMP_SYNC_BUCKET", "notes")
	os.Setenv("MEMODUMP_SYNC_PREFIX", "vault")
	t.Cleanup(func() {
		os.Unsetenv("MEMODUMP_SYNC_ENDPOINT")
		os.Unsetenv("MEMODUMP_SYNC_BUCKET")
		os.Unsetenv("MEMODUMP_SYNC_PREFIX")
	})
	remote, err = defaultSyncProvider()
	if err != nil {
		t.Fatal(err)
	}
	client, ok := remote.(*s3.Client)
	if !ok {
		t.Fatalf("configured provider = %T, want *s3.Client", remote)
	}
	// The profile is the secret-free S3 location hash, not the memory profile.
	if providerProfile(client) == memoryProfile {
		t.Fatal("S3 profile collides with the memory profile")
	}
}

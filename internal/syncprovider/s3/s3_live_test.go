package s3

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"memodump/internal/cloudsync"
	"memodump/internal/syncindex"
	"memodump/internal/syncservice"
	"memodump/internal/syncstate"
)

// TestS3Live is an opt-in live test against a real S3-compatible endpoint. Set
// MEMODUMP_S3_LIVE_ENDPOINT, MEMODUMP_S3_LIVE_BUCKET, MEMODUMP_S3_LIVE_ACCESS,
// and MEMODUMP_S3_LIVE_SECRET to run it. It works in a random isolated prefix,
// converges two replicas, and cleans up its objects.
func TestS3Live(t *testing.T) {
	endpoint := os.Getenv("MEMODUMP_S3_LIVE_ENDPOINT")
	bucket := os.Getenv("MEMODUMP_S3_LIVE_BUCKET")
	access := os.Getenv("MEMODUMP_S3_LIVE_ACCESS")
	secret := os.Getenv("MEMODUMP_S3_LIVE_SECRET")
	if endpoint == "" || bucket == "" || access == "" || secret == "" {
		t.Skip("set MEMODUMP_S3_LIVE_ENDPOINT/BUCKET/ACCESS/SECRET to run the live S3 test")
	}
	buf := make([]byte, 6)
	_, _ = rand.Read(buf)
	prefix := "memodump-test-" + hex.EncodeToString(buf)
	ctx := context.Background()
	c, err := New(Config{
		Endpoint: endpoint, Region: "us-east-1", Bucket: bucket, Prefix: prefix,
		AccessKey: access, SecretKey: secret, ForcePathStyle: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	caps, err := c.Test(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !caps.ConditionalWrites {
		t.Fatal("live endpoint does not support conditional writes")
	}
	defer func() {
		page, _ := c.List(ctx, "notes/", "")
		for _, ch := range page.Changes {
			_ = c.deleteObject(context.Background(), c.objectKey(ch.Key))
		}
		_ = c.deleteObject(context.Background(), c.objectKey("repo.json"))
	}()

	// Two replicas converge through the live provider.
	provider := func() (cloudsync.RemoteStore, error) { return c, nil }
	rootA, rootB := t.TempDir(), t.TempDir()
	stateA, stateB := t.TempDir(), t.TempDir()
	a := liveReplica(t, rootA, stateA, provider, c.Profile())
	b := liveReplica(t, rootB, stateB, provider, c.Profile())
	if err := os.WriteFile(filepath.Join(rootA, "idea.md"), []byte("# Live\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if res, err := a.svc.Run(ctx); err != nil || !res.Synced {
		t.Fatalf("A live run = %+v, %v", res, err)
	}
	if res, err := b.svc.Run(ctx); err != nil || !res.Synced {
		t.Fatalf("B live run = %+v, %v", res, err)
	}
	if data, err := os.ReadFile(filepath.Join(rootB, "idea.md")); err != nil || string(data) != "# Live\n" {
		t.Fatalf("B did not pull the live note: %q, %v", data, err)
	}
}

type liveReplicaInfo struct {
	svc *syncservice.Service
}

func liveReplica(t *testing.T, root, state string, provider syncservice.Provider, profile string) *liveReplicaInfo {
	t.Helper()
	idx, err := syncindex.EnableNoteStore(root)
	if err != nil {
		t.Fatal(err)
	}
	_, replicaID, err := syncstate.Resolve(state, root, idx.Index.VaultID)
	if err != nil {
		t.Fatal(err)
	}
	return &liveReplicaInfo{svc: syncservice.New(syncservice.Config{
		RepoRoot: root, StateRoot: state,
		VaultID: idx.Index.VaultID, ReplicaID: string(replicaID),
		RepoID: "33333333-3333-4333-8333-333333333333", Profile: profile, Provider: provider,
	})}
}

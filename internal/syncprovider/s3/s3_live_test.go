package s3

import (
	"context"
	"fmt"
	"os"
	"testing"

	"memodump/internal/cloudsync"
)

// TestS3Live is an opt-in live test against a real S3-compatible endpoint. Set
// MEMODUMP_S3_LIVE_ENDPOINT, MEMODUMP_S3_LIVE_BUCKET, MEMODUMP_S3_LIVE_ACCESS,
// and MEMODUMP_S3_LIVE_SECRET to run it. It works in a random isolated prefix
// and cleans up its objects.
func TestS3Live(t *testing.T) {
	endpoint := os.Getenv("MEMODUMP_S3_LIVE_ENDPOINT")
	bucket := os.Getenv("MEMODUMP_S3_LIVE_BUCKET")
	access := os.Getenv("MEMODUMP_S3_LIVE_ACCESS")
	secret := os.Getenv("MEMODUMP_S3_LIVE_SECRET")
	if endpoint == "" || bucket == "" || access == "" || secret == "" {
		t.Skip("set MEMODUMP_S3_LIVE_ENDPOINT/BUCKET/ACCESS/SECRET to run the live S3 test")
	}
	ctx := context.Background()
	c, err := New(Config{
		Endpoint: endpoint, Region: "us-east-1", Bucket: bucket,
		Prefix:    fmt.Sprintf("memodump-test-%d", os.Getpid()),
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
			_ = c.deleteObject(ctx, c.objectKey(ch.Key))
		}
	}()
	key := cloudsync.NoteKey("11111111-1111-4111-8111-111111111111")
	v, err := c.Create(ctx, key, []byte("live"))
	if err != nil {
		t.Fatal(err)
	}
	data, ver, err := c.Read(ctx, key)
	if err != nil || string(data) != "live" || ver != v {
		t.Fatalf("live read = %q, %q, %v", data, ver, err)
	}
	if _, err := c.Replace(ctx, key, []byte("live2"), v); err != nil {
		t.Fatal(err)
	}
	page, err := c.List(ctx, "notes/", "")
	if err != nil || len(page.Changes) != 1 {
		t.Fatalf("live list = %+v, %v", page, err)
	}
}

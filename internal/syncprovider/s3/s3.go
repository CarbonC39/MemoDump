// Package s3 implements the first real sync provider: S3-compatible object
// storage through minio-go. It is a private-bucket RemoteStore that enforces
// the versioned-note wire layout under a configured prefix, performs
// conditional writes (create-if-absent via If-None-Match: *, replace-if-version
// via If-Match), fully lists notes/ with pagination, and maps S3 errors onto
// normalized StoreError kinds without leaking bodies or credentials. The
// provider profile is secret-free: it hashes only the endpoint, bucket, and
// prefix.
package s3

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"memodump/internal/cloudsync"
)

// Config selects the private bucket/prefix and signing identity. AccessKey and
// SecretKey never leave the process and never appear in any error or status.
type Config struct {
	Endpoint       string // scheme://host[:port]
	Region         string
	Bucket         string
	Prefix         string // objects are stored under <prefix>/; may be ""
	AccessKey      string
	SecretKey      string
	ForcePathStyle bool
	// HTTPClient overrides the default transport (tests inject a fake S3).
	HTTPClient *http.Client
}

// Client is an S3-compatible RemoteStore backed by minio-go.
type Client struct {
	cfg  Config
	core *minio.Core
}

// New validates the config and returns a client backed by minio-go (which
// performs the SigV4 signing and URL/path-style handling). Nothing is
// contacted yet.
func New(cfg Config) (*Client, error) {
	u, err := url.Parse(cfg.Endpoint)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("invalid S3 endpoint")
	}
	if cfg.Bucket == "" {
		return nil, fmt.Errorf("missing S3 bucket")
	}
	region := cfg.Region
	if region == "" {
		region = "us-east-1"
	}
	lookup := minio.BucketLookupDNS
	if cfg.ForcePathStyle {
		lookup = minio.BucketLookupPath
	}
	base := http.DefaultTransport
	if cfg.HTTPClient != nil {
		base = cfg.HTTPClient.Transport
		if base == nil {
			base = http.DefaultTransport
		}
	}
	core, err := minio.NewCore(u.Host, &minio.Options{
		Creds:        credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure:       u.Scheme != "http",
		Region:       region,
		BucketLookup: lookup,
		Transport:    &conditionalTransport{base: base},
	})
	if err != nil {
		return nil, err
	}
	return &Client{cfg: cfg, core: core}, nil
}

// Profile is the secret-free provider fingerprint: a hash of the location
// (endpoint, bucket, prefix), never of credentials.
func (c *Client) Profile() string {
	sum := sha256.Sum256([]byte(c.cfg.Endpoint + "|" + c.cfg.Bucket + "|" + c.cfg.Prefix))
	return hex.EncodeToString(sum[:])
}

func (c *Client) objectKeyPrefix() string {
	if c.cfg.Prefix == "" {
		return ""
	}
	return strings.TrimSuffix(c.cfg.Prefix, "/") + "/"
}

// objectKey maps a sync key ("notes/<id>.json" or "repo.json") to the object
// key under the configured prefix, exactly once.
func (c *Client) objectKey(key string) string {
	return c.objectKeyPrefix() + key
}

// conditionalCtxKey carries the conditional-PUT precondition for the request.
type conditionalCtxKey struct{}

// conditionalTransport injects If-None-Match/If-Match onto PUT requests after
// minio-go signs them. These standard headers are not part of SignedHeaders,
// so S3 accepts them unsigned; they implement the conditional-write contract.
type conditionalTransport struct {
	base http.RoundTripper
}

func (t *conditionalTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method == http.MethodPut {
		if v, ok := req.Context().Value(conditionalCtxKey{}).(string); ok {
			if v == "*" {
				req.Header.Set("If-None-Match", "*")
			} else {
				req.Header.Set("If-Match", v)
			}
		}
	}
	return t.base.RoundTrip(req)
}

// --- RemoteStore operations --------------------------------------------------

// Test probes the provider and returns its capabilities. It verifies the
// bucket is reachable and that the service honors both conditional preconditions
// with a random isolated key (cleaned up immediately); a service that ignores
// either is rejected with ErrUnsupportedCapability.
func (c *Client) Test(ctx context.Context) (cloudsync.Capabilities, error) {
	probeKey := c.objectKey(randomProbeKey())
	defer func() { _ = c.deleteObject(context.Background(), probeKey) }()
	etag, err := c.putObject(ctx, probeKey, []byte("probe"), "")
	if err != nil {
		return cloudsync.Capabilities{}, err
	}
	// Create-if-absent must fail with a precondition error when the key exists.
	if _, err := c.putObject(ctx, probeKey, []byte("probe2"), "*"); !cloudsync.IsStoreError(err, cloudsync.ErrPreconditionFailed) {
		return cloudsync.Capabilities{}, &cloudsync.StoreError{Kind: cloudsync.ErrUnsupportedCapability, Message: "service ignores If-None-Match"}
	}
	// Replace-if-version must fail with a precondition error on a stale version.
	if _, err := c.putObject(ctx, probeKey, []byte("probe3"), etag+"-stale"); !cloudsync.IsStoreError(err, cloudsync.ErrPreconditionFailed) {
		return cloudsync.Capabilities{}, &cloudsync.StoreError{Kind: cloudsync.ErrUnsupportedCapability, Message: "service ignores If-Match"}
	}
	return cloudsync.Capabilities{ConditionalWrites: true, PagedListing: true}, nil
}

// Read returns the bytes and opaque version (ETag) of a key.
func (c *Client) Read(ctx context.Context, key string) ([]byte, string, error) {
	reader, info, _, err := c.core.GetObject(ctx, c.cfg.Bucket, c.objectKey(key), minio.GetObjectOptions{})
	if err != nil {
		return nil, "", classifyS3Error(err)
	}
	defer reader.Close()
	data, rerr := io.ReadAll(reader)
	if rerr != nil {
		return nil, "", classifyS3Error(rerr)
	}
	version := trimETag(info.ETag)
	if version == "" {
		return nil, "", &cloudsync.StoreError{Kind: cloudsync.ErrInvalidResponse, Message: "object read without an ETag"}
	}
	return data, version, nil
}

// List returns one page of changes under prefix. The syncCursor argument is the
// pagination continuation token (the engine uses full listings only). A listing
// truncated without a continuation token is ErrIncompleteList, never a silent
// partial view.
func (c *Client) List(ctx context.Context, prefix, syncCursor string) (cloudsync.ChangePage, error) {
	result, err := c.core.ListObjectsV2(c.cfg.Bucket, c.objectKeyPrefix()+prefix, "", syncCursor, "", 1000)
	if err != nil {
		return cloudsync.ChangePage{}, classifyS3Error(err)
	}
	if result.IsTruncated && result.NextContinuationToken == "" {
		return cloudsync.ChangePage{}, &cloudsync.StoreError{Kind: cloudsync.ErrIncompleteList, Message: "listing truncated without a continuation token"}
	}
	page := cloudsync.ChangePage{}
	for _, obj := range result.Contents {
		rel := strings.TrimPrefix(obj.Key, c.objectKeyPrefix())
		page.Changes = append(page.Changes, cloudsync.Change{
			Key: rel, Type: cloudsync.ChangeCreated, Version: trimETag(obj.ETag),
		})
	}
	if result.IsTruncated {
		page.NextCursor = result.NextContinuationToken
	}
	return page, nil
}

// Create stores bytes under a key that must not already exist.
func (c *Client) Create(ctx context.Context, key string, data []byte) (string, error) {
	return c.putObject(ctx, c.objectKey(key), data, "*")
}

// Replace stores bytes only when expectedVersion matches the current ETag.
func (c *Client) Replace(ctx context.Context, key string, data []byte, expectedVersion string) (string, error) {
	return c.putObject(ctx, c.objectKey(key), data, strings.Trim(expectedVersion, `"`))
}

// putObject performs a conditional PUT. precondition "" = unconditional, "*" =
// create-if-absent (If-None-Match: *), otherwise = replace-if-version
// (If-Match: <etag>).
func (c *Client) putObject(ctx context.Context, key string, data []byte, precondition string) (string, error) {
	pctx := ctx
	if precondition != "" {
		if precondition == "*" {
			pctx = context.WithValue(ctx, conditionalCtxKey{}, "*")
		} else {
			pctx = context.WithValue(ctx, conditionalCtxKey{}, `"`+strings.Trim(precondition, `"`)+`"`)
		}
	}
	info, err := c.core.PutObject(pctx, c.cfg.Bucket, key, bytes.NewReader(data), int64(len(data)),
		"", "", minio.PutObjectOptions{SendContentMd5: true})
	if err != nil {
		return "", classifyS3Error(err)
	}
	version := trimETag(info.ETag)
	if version == "" {
		return "", &cloudsync.StoreError{Kind: cloudsync.ErrInvalidResponse, Message: "object write without an ETag"}
	}
	return version, nil
}

// deleteObject removes an object (used by the probe cleanup only).
func (c *Client) deleteObject(ctx context.Context, key string) (err error) {
	return c.core.RemoveObject(ctx, c.cfg.Bucket, key, minio.RemoveObjectOptions{})
}

func trimETag(etag string) string {
	return strings.Trim(strings.TrimSpace(etag), `"`)
}

func randomProbeKey() string {
	buf := make([]byte, 8)
	_, _ = rand.Read(buf)
	return fmt.Sprintf("_memodump_probe_%d_%s", time.Now().UnixNano(), hex.EncodeToString(buf))
}

// classifyS3Error maps a minio-go error onto a normalized StoreError without
// leaking bodies or credentials.
func classifyS3Error(err error) error {
	var resp minio.ErrorResponse
	if errors.As(err, &resp) {
		code := resp.Code
		switch {
		case resp.StatusCode == http.StatusNotFound:
			return &cloudsync.StoreError{Kind: cloudsync.ErrNotFound, Message: "s3 not-found"}
		case resp.StatusCode == http.StatusPreconditionFailed:
			return &cloudsync.StoreError{Kind: cloudsync.ErrPreconditionFailed, Message: "s3 precondition-failed"}
		case code == "AccessDenied":
			return &cloudsync.StoreError{Kind: cloudsync.ErrPermission, Message: "s3 access-denied"}
		case code == "InvalidAccessKeyId" || code == "SignatureDoesNotMatch" || code == "InvalidToken" || code == "ExpiredToken":
			return &cloudsync.StoreError{Kind: cloudsync.ErrAuth, Message: "s3 auth"}
		case code == "SlowDown":
			return &cloudsync.StoreError{Kind: cloudsync.ErrRateLimit, Message: "s3 slow-down"}
		case code == "QuotaExceeded" || resp.StatusCode == http.StatusInsufficientStorage:
			return &cloudsync.StoreError{Kind: cloudsync.ErrQuota, Message: "s3 quota"}
		case resp.StatusCode >= 500:
			return &cloudsync.StoreError{Kind: cloudsync.ErrRetryableTransport, Message: "s3 " + code}
		default:
			return &cloudsync.StoreError{Kind: cloudsync.ErrInvalidResponse, Message: "s3 " + code}
		}
	}
	// A non-S3 (network) error is retryable transport.
	return &cloudsync.StoreError{Kind: cloudsync.ErrRetryableTransport, Message: err.Error()}
}

// Compile-time check that Client implements RemoteStore.
var _ cloudsync.RemoteStore = (*Client)(nil)

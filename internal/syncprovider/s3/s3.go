// Package s3 implements the first real sync provider: S3-compatible object
// storage over the S3 REST API with AWS Signature V4. It is a private-bucket
// RemoteStore that enforces the versioned-note wire layout under a configured
// prefix, performs conditional writes (create-if-absent via If-None-Match: *,
// replace-if-version via If-Match), fully lists notes/ with pagination, and
// maps S3 errors onto normalized StoreError kinds without leaking bodies or
// credentials. The provider profile is secret-free: it hashes only the
// endpoint, bucket, and prefix.
package s3

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

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

// Client is an S3-compatible RemoteStore.
type Client struct {
	cfg Config
	hc  *http.Client
}

// New validates the config and returns a client. Nothing is contacted yet.
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
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 60 * time.Second}
	}
	return &Client{cfg: Config{Endpoint: cfg.Endpoint, Region: region, Bucket: cfg.Bucket,
		Prefix: cfg.Prefix, AccessKey: cfg.AccessKey, SecretKey: cfg.SecretKey,
		ForcePathStyle: cfg.ForcePathStyle}, hc: hc}, nil
}

// Profile is the secret-free provider fingerprint: a hash of the location
// (endpoint, bucket, prefix), never of credentials. It is the snapshot's
// ProviderProfile.
func (c *Client) Profile() string {
	sum := sha256.Sum256([]byte(c.cfg.Endpoint + "|" + c.cfg.Bucket + "|" + c.cfg.Prefix))
	return hex.EncodeToString(sum[:])
}

// objectKey maps a sync key ("notes/<id>.json" or "repo.json") to the object
// key under the configured prefix.
func (c *Client) objectKey(key string) string {
	if c.cfg.Prefix == "" {
		return key
	}
	return strings.TrimSuffix(c.cfg.Prefix, "/") + "/" + key
}

// baseURL returns the scheme://host for the endpoint.
func (c *Client) baseURL() string {
	u, _ := url.Parse(c.cfg.Endpoint)
	scheme := "https"
	if u.Scheme == "http" {
		scheme = "http"
	}
	return scheme + "://" + u.Host
}

// objectURL returns the request URL for a bucket object (path-style or
// virtual-host style).
func (c *Client) objectURL(key string) string {
	escaped := escapePath(c.objectKey(key))
	if c.cfg.ForcePathStyle {
		return c.baseURL() + "/" + c.cfg.Bucket + "/" + escaped
	}
	return c.baseURL() + "/" + escaped
}

// listURL returns the ListObjectsV2 request URL with query parameters.
func (c *Client) listURL(prefix, continuation string) string {
	q := url.Values{}
	q.Set("list-type", "2")
	q.Set("prefix", c.objectKey(prefix))
	if continuation != "" {
		q.Set("continuation-token", continuation)
	}
	u := c.baseURL()
	if c.cfg.ForcePathStyle {
		u += "/" + c.cfg.Bucket
	}
	return u + "?" + q.Encode()
}

// escapePath percent-encodes each path segment, preserving "/".
func escapePath(p string) string {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.Join(segs, "/")
}

// --- AWS Signature V4 -------------------------------------------------------

var emptySHA256 = hex.EncodeToString(sha256.New().Sum(nil))

type signedRequest struct {
	method  string
	url     string
	body    []byte
	headers http.Header
}

// do sends a signed request and returns the response body (fully read) plus
// the response headers. It returns a normalized StoreError for S3 errors.
func (c *Client) do(ctx context.Context, method, rawURL string, body []byte, headers http.Header) ([]byte, http.Header, error) {
	req, err := http.NewRequestWithContext(ctx, method, rawURL, bytes.NewReader(body))
	if err != nil {
		return nil, nil, err
	}
	for k, vs := range headers {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	if err := c.sign(req, body); err != nil {
		return nil, nil, err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, nil, &cloudsync.StoreError{Kind: cloudsync.ErrRetryableTransport, Message: err.Error()}
	}
	defer resp.Body.Close()
	data, rerr := io.ReadAll(resp.Body)
	if rerr != nil {
		return nil, nil, &cloudsync.StoreError{Kind: cloudsync.ErrRetryableTransport, Message: rerr.Error()}
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return data, resp.Header, nil
	}
	return nil, nil, classifyS3Error(resp.StatusCode, data)
}

// sign applies AWS Signature V4 to the request.
func (c *Client) sign(req *http.Request, body []byte) error {
	now := time.Now().UTC()
	amzDate := now.Format("20060102T150405Z")
	dateStamp := now.Format("20060102")
	payloadHash := sha256Hex(body)
	req.Header.Set("x-amz-date", amzDate)
	req.Header.Set("x-amz-content-sha256", payloadHash)
	req.Header.Set("host", req.URL.Host)

	var signedHeaders []string
	var canonicalHeaders strings.Builder
	for _, k := range []string{"host", "x-amz-content-sha256", "x-amz-date"} {
		val := req.Header.Get(k)
		signedHeaders = append(signedHeaders, k)
		canonicalHeaders.WriteString(k + ":" + strings.TrimSpace(val) + "\n")
	}
	signedHeadersStr := strings.Join(signedHeaders, ";")

	canonicalQuery := canonicalQueryString(req.URL.Query())
	canonicalRequest := strings.Join([]string{
		req.Method,
		canonicalURI(req.URL.Path),
		canonicalQuery,
		canonicalHeaders.String(),
		signedHeadersStr,
		payloadHash,
	}, "\n")

	scope := dateStamp + "/" + c.cfg.Region + "/s3/aws4_request"
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		amzDate,
		scope,
		sha256Hex([]byte(canonicalRequest)),
	}, "\n")

	signingKey := deriveSigningKey(c.cfg.SecretKey, dateStamp, c.cfg.Region)
	signature := hex.EncodeToString(hmacSHA256(signingKey, []byte(stringToSign)))

	auth := "AWS4-HMAC-SHA256 Credential=" + c.cfg.AccessKey + "/" + scope +
		", SignedHeaders=" + signedHeadersStr + ", Signature=" + signature
	req.Header.Set("Authorization", auth)
	return nil
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func hmacSHA256(key, data []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write(data)
	return mac.Sum(nil)
}

func deriveSigningKey(secret, dateStamp, region string) []byte {
	kDate := hmacSHA256([]byte("AWS4"+secret), []byte(dateStamp))
	kRegion := hmacSHA256(kDate, []byte(region))
	kService := hmacSHA256(kRegion, []byte("s3"))
	return hmacSHA256(kService, []byte("aws4_request"))
}

func canonicalQueryString(q url.Values) string {
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		vals := q[k]
		sort.Strings(vals)
		for _, v := range vals {
			parts = append(parts, url.QueryEscape(k)+"="+url.QueryEscape(v))
		}
	}
	return strings.Join(parts, "&")
}

// canonicalURI percent-encodes the path once (S3 requires the canonical URI to
// be the raw path; Go's URL.Path is already decoded).
func canonicalURI(p string) string {
	if p == "" {
		return "/"
	}
	return escapePath(p)
}

// --- RemoteStore operations --------------------------------------------------

// Test probes the provider and returns its capabilities. It verifies the
// bucket is reachable and that the service honors both conditional preconditions
// (create-if-absent and replace-if-version); a service that ignores either is
// rejected with ErrUnsupportedCapability.
func (c *Client) Test(ctx context.Context) (cloudsync.Capabilities, error) {
	probeKey := c.objectKey("_memodump_probe_")
	if _, err := c.putObject(ctx, probeKey, []byte("probe"), ""); err != nil {
		return cloudsync.Capabilities{}, err
	}
	// Create-if-absent must fail when the object exists.
	if _, err := c.putObject(ctx, probeKey, []byte("probe2"), "*"); err == nil {
		return cloudsync.Capabilities{}, &cloudsync.StoreError{Kind: cloudsync.ErrUnsupportedCapability, Message: "service ignores If-None-Match"}
	}
	// Replace-if-version must fail on a stale version.
	if _, err := c.putObject(ctx, probeKey, []byte("probe3"), "wrong-etag"); err == nil {
		return cloudsync.Capabilities{}, &cloudsync.StoreError{Kind: cloudsync.ErrUnsupportedCapability, Message: "service ignores If-Match"}
	}
	// Clean up.
	_ = c.deleteObject(ctx, probeKey)
	return cloudsync.Capabilities{ConditionalWrites: true, PagedListing: true}, nil
}

// Read returns the bytes and opaque version (ETag) of a key.
func (c *Client) Read(ctx context.Context, key string) ([]byte, string, error) {
	headers := http.Header{}
	data, h, err := c.do(ctx, http.MethodGet, c.objectURL(key), nil, headers)
	if err != nil {
		return nil, "", err
	}
	return data, etagVersion(h.Get("ETag")), nil
}

// List returns one page of changes under prefix. The syncCursor argument is the
// pagination continuation token (the engine uses full listings only). The
// SyncCursor field is left empty: the engine persists no cursor.
func (c *Client) List(ctx context.Context, prefix, syncCursor string) (cloudsync.ChangePage, error) {
	headers := http.Header{}
	data, _, err := c.do(ctx, http.MethodGet, c.listURL(prefix, syncCursor), nil, headers)
	if err != nil {
		return cloudsync.ChangePage{}, err
	}
	var result listObjectsV2Result
	if err := xml.Unmarshal(data, &result); err != nil {
		return cloudsync.ChangePage{}, &cloudsync.StoreError{Kind: cloudsync.ErrInvalidResponse, Message: "invalid list response"}
	}
	page := cloudsync.ChangePage{}
	for _, obj := range result.Contents {
		rel := strings.TrimPrefix(obj.Key, c.objectKeyPrefix())
		rel = strings.TrimPrefix(rel, "/")
		page.Changes = append(page.Changes, cloudsync.Change{Key: rel, Type: cloudsync.ChangeCreated, Version: obj.ETag})
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
	return c.putObject(ctx, c.objectKey(key), data, expectedVersion)
}

func (c *Client) objectKeyPrefix() string {
	if c.cfg.Prefix == "" {
		return ""
	}
	return strings.TrimSuffix(c.cfg.Prefix, "/") + "/"
}

// putObject performs a conditional PUT. precondition "" = unconditional, "*" =
// create-if-absent (If-None-Match: *), otherwise = replace-if-version
// (If-Match: <etag>).
func (c *Client) putObject(ctx context.Context, key string, data []byte, precondition string) (string, error) {
	headers := http.Header{}
	if precondition != "" {
		if precondition == "*" {
			headers.Set("If-None-Match", "*")
		} else {
			headers.Set("If-Match", `"`+strings.Trim(precondition, `"`)+`"`)
		}
	}
	_, h, err := c.do(ctx, http.MethodPut, c.objectURL(key), data, headers)
	if err != nil {
		return "", err
	}
	return etagVersion(h.Get("ETag")), nil
}

// deleteObject removes an object (used by the probe cleanup only).
func (c *Client) deleteObject(ctx context.Context, key string) error {
	_, _, err := c.do(ctx, http.MethodDelete, c.objectURL(key), nil, http.Header{})
	return err
}

func etagVersion(etag string) string {
	return strings.Trim(strings.TrimSpace(etag), `"`)
}

// listObjectsV2Result is the ListObjectsV2 XML response.
type listObjectsV2Result struct {
	Contents              []listObject `xml:"Contents"`
	IsTruncated           bool         `xml:"IsTruncated"`
	NextContinuationToken string       `xml:"NextContinuationToken"`
}

type listObject struct {
	Key  string `xml:"Key"`
	ETag string `xml:"ETag"`
}

// classifyS3Error maps an S3 status and error body onto a normalized StoreError
// without echoing the body (which may carry credentials or signed URLs).
func classifyS3Error(status int, body []byte) error {
	code := s3ErrorCode(body)
	message := fmt.Sprintf("s3 %d", status)
	if code != "" {
		message = "s3 " + code
	}
	switch {
	case status == http.StatusNotFound:
		return &cloudsync.StoreError{Kind: cloudsync.ErrNotFound, Message: message}
	case status == http.StatusPreconditionFailed:
		return &cloudsync.StoreError{Kind: cloudsync.ErrPreconditionFailed, Message: message}
	case code == "AccessDenied":
		return &cloudsync.StoreError{Kind: cloudsync.ErrPermission, Message: message}
	case code == "InvalidAccessKeyId" || code == "SignatureDoesNotMatch" || code == "InvalidToken" || code == "ExpiredToken":
		return &cloudsync.StoreError{Kind: cloudsync.ErrAuth, Message: message}
	case code == "SlowDown":
		return &cloudsync.StoreError{Kind: cloudsync.ErrRateLimit, Message: message}
	case code == "QuotaExceeded" || status == http.StatusInsufficientStorage:
		return &cloudsync.StoreError{Kind: cloudsync.ErrQuota, Message: message}
	case status >= 500:
		return &cloudsync.StoreError{Kind: cloudsync.ErrRetryableTransport, Message: message}
	default:
		return &cloudsync.StoreError{Kind: cloudsync.ErrInvalidResponse, Message: message}
	}
}

// s3ErrorCode extracts the S3 Error.Code from an XML error body, if any.
func s3ErrorCode(body []byte) string {
	var e struct {
		Code string `xml:"Code"`
	}
	if err := xml.Unmarshal(body, &e); err != nil {
		return ""
	}
	return e.Code
}

// Compile-time check that Client implements RemoteStore.
var _ cloudsync.RemoteStore = (*Client)(nil)

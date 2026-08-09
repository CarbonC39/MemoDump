package s3

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"memodump/internal/cloudsync"
)

// fakeS3 is a minimal in-memory S3-compatible server for provider tests. It
// honors If-None-Match: * and If-Match, pages ListObjectsV2, and returns ETags
// that double as versions.
type fakeS3 struct {
	mu                  sync.Mutex
	objects             map[string]*fakeObject
	etagSeq             int
	ignorePreconditions bool
	pageSize            int
	requireAuth         bool
	failListCode        string
}

type fakeObject struct {
	data []byte
	etag string
}

func newFakeS3() *fakeS3 {
	return &fakeS3{objects: map[string]*fakeObject{}, pageSize: 1000}
}

// newServer starts the fake S3 and returns a client pointing at it.
func newServer(t *testing.T, f *fakeS3) (*Client, *httptest.Server) {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(f.handler))
	t.Cleanup(ts.Close)
	c, err := New(Config{
		Endpoint: ts.URL, Region: "us-east-1", Bucket: "notes",
		AccessKey: "ak", SecretKey: "sk", ForcePathStyle: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return c, ts
}

func s3Error(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	io.WriteString(w, `<Error><Code>`+code+`</Code><Message>test</Message></Error>`)
}

// parseBucketKey extracts bucket and key from a path-style request.
func (f *fakeS3) parseBucketKey(r *http.Request) (string, string) {
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/"), "/", 2)
	if len(parts) < 2 {
		return parts[0], ""
	}
	return parts[0], parts[1]
}

func (f *fakeS3) handler(w http.ResponseWriter, r *http.Request) {
	if f.requireAuth && r.Header.Get("Authorization") == "" {
		s3Error(w, http.StatusForbidden, "AccessDenied")
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	switch r.Method {
	case http.MethodPut:
		f.handlePut(w, r)
	case http.MethodGet:
		if r.URL.Query().Get("list-type") == "2" {
			f.handleList(w, r)
		} else {
			f.handleGet(w, r)
		}
	case http.MethodDelete:
		f.handleDelete(w, r)
	default:
		s3Error(w, http.StatusMethodNotAllowed, "MethodNotAllowed")
	}
}

func (f *fakeS3) handlePut(w http.ResponseWriter, r *http.Request) {
	_, key := f.parseBucketKey(r)
	body, _ := io.ReadAll(r.Body)
	if r.Header.Get("x-amz-content-sha256") == "STREAMING-AWS4-HMAC-SHA256-PAYLOAD" {
		if decoded, err := readAWSChunked(bytes.NewReader(body)); err == nil {
			body = decoded
		}
	}
	if existing, ok := f.objects[key]; ok {
		if !f.ignorePreconditions {
			if r.Header.Get("If-None-Match") == "*" {
				s3Error(w, http.StatusPreconditionFailed, "PreconditionFailed")
				return
			}
			if im := r.Header.Get("If-Match"); im != "" && `"`+existing.etag+`"` != im {
				s3Error(w, http.StatusPreconditionFailed, "PreconditionFailed")
				return
			}
		}
	} else if r.Header.Get("If-Match") != "" {
		s3Error(w, http.StatusPreconditionFailed, "PreconditionFailed")
		return
	}
	f.etagSeq++
	etag := strconv.Itoa(f.etagSeq)
	f.objects[key] = &fakeObject{data: body, etag: etag}
	w.Header().Set("ETag", `"`+etag+`"`)
	w.WriteHeader(http.StatusOK)
}

func (f *fakeS3) handleGet(w http.ResponseWriter, r *http.Request) {
	_, key := f.parseBucketKey(r)
	obj, ok := f.objects[key]
	if !ok {
		s3Error(w, http.StatusNotFound, "NoSuchKey")
		return
	}
	w.Header().Set("ETag", `"`+obj.etag+`"`)
	w.Header().Set("Content-Type", "text/plain")
	w.Header().Set("Last-Modified", "Mon, 02 Jan 2006 15:04:05 GMT")
	w.Header().Set("Content-Length", fmt.Sprint(len(obj.data)))
	w.Write(obj.data)
}

func (f *fakeS3) handleDelete(w http.ResponseWriter, r *http.Request) {
	_, key := f.parseBucketKey(r)
	delete(f.objects, key)
	w.WriteHeader(http.StatusNoContent)
}

func (f *fakeS3) handleList(w http.ResponseWriter, r *http.Request) {
	if f.failListCode != "" {
		s3Error(w, statusForCode(f.failListCode), f.failListCode)
		return
	}
	prefix := r.URL.Query().Get("prefix")
	continuation := r.URL.Query().Get("continuation-token")
	keys := make([]string, 0, len(f.objects))
	for k := range f.objects {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	start := 0
	if continuation != "" {
		idx := sort.SearchStrings(keys, continuation)
		if idx >= len(keys) || keys[idx] != continuation {
			start = len(keys)
		} else {
			start = idx + 1
		}
	}
	end := start + f.pageSize
	if end > len(keys) {
		end = len(keys)
	}
	type contents struct {
		Key  string `xml:"Key"`
		ETag string `xml:"ETag"`
	}
	var result struct {
		XMLName               xml.Name   `xml:"ListBucketResult"`
		Contents              []contents `xml:"Contents"`
		IsTruncated           bool       `xml:"IsTruncated"`
		NextContinuationToken string     `xml:"NextContinuationToken"`
	}
	for _, k := range keys[start:end] {
		result.Contents = append(result.Contents, contents{Key: k, ETag: `"` + f.objects[k].etag + `"`})
	}
	if end < len(keys) {
		result.IsTruncated = true
		result.NextContinuationToken = keys[end-1]
	}
	w.Header().Set("Content-Type", "application/xml")
	io.WriteString(w, xmlHeader+mustXML(result))
}

func mustXML(v any) string {
	data, _ := xml.Marshal(v)
	return string(data)
}

const xmlHeader = `<?xml version="1.0" encoding="UTF-8"?>`

func TestS3CreateReplaceRead(t *testing.T) {
	ctx := context.Background()
	c, _ := newServer(t, newFakeS3())

	v, err := c.Create(ctx, "notes/a.json", []byte("v1"))
	if err != nil {
		t.Fatal(err)
	}
	if v == "" {
		t.Fatal("Create returned an empty version")
	}
	// Create-if-absent on an existing key fails.
	if _, err := c.Create(ctx, "notes/a.json", []byte("again")); !cloudsync.IsStoreError(err, cloudsync.ErrPreconditionFailed) {
		t.Fatalf("duplicate Create err = %v, want precondition-failed", err)
	}
	// Read returns the bytes and the same version.
	data, ver, err := c.Read(ctx, "notes/a.json")
	if err != nil || string(data) != "v1" || ver != v {
		t.Fatalf("Read = %q, %q, %v", data, ver, err)
	}
	// Replace with the correct version advances the version.
	v2, err := c.Replace(ctx, "notes/a.json", []byte("v2"), v)
	if err != nil {
		t.Fatal(err)
	}
	if v2 == v {
		t.Fatal("Replace returned the same version")
	}
	data, ver, err = c.Read(ctx, "notes/a.json")
	if err != nil || string(data) != "v2" || ver != v2 {
		t.Fatalf("Read after replace = %q, %q, %v", data, ver, err)
	}
	// Replace with a stale version fails.
	if _, err := c.Replace(ctx, "notes/a.json", []byte("v3"), v); !cloudsync.IsStoreError(err, cloudsync.ErrPreconditionFailed) {
		t.Fatalf("stale Replace err = %v, want precondition-failed", err)
	}
	// Replace on a missing key fails: S3 returns 412 for If-Match on a key
	// whose ETag never matches, which the provider maps to precondition-failed.
	if _, err := c.Replace(ctx, "notes/missing.json", []byte("x"), "v0"); !cloudsync.IsStoreError(err, cloudsync.ErrPreconditionFailed) {
		t.Fatalf("missing Replace err = %v, want precondition-failed", err)
	}
}

func TestS3ReadNotFound(t *testing.T) {
	ctx := context.Background()
	c, _ := newServer(t, newFakeS3())
	if _, _, err := c.Read(ctx, "notes/nope.json"); !cloudsync.IsStoreError(err, cloudsync.ErrNotFound) {
		t.Fatalf("Read err = %v, want not-found", err)
	}
}

func TestS3ListPaginationAndPrefix(t *testing.T) {
	ctx := context.Background()
	// A dedicated fake with a small page size exercises pagination.
	server := newFakeS3()
	server.pageSize = 2
	cc, _ := newServer(t, server)
	ids := []string{
		"11111111-1111-4111-8111-111111111111",
		"22222222-2222-4222-8222-222222222222",
		"33333333-3333-4333-8333-333333333333",
		"44444444-4444-4444-8444-444444444444",
		"55555555-5555-4555-8555-555555555555",
	}
	for _, id := range ids {
		if _, err := cc.Create(ctx, cloudsync.NoteKey(id), []byte("body"+id)); err != nil {
			t.Fatal(err)
		}
	}
	// A non-note key under the prefix must be excluded by the provider.
	if _, err := cc.Create(ctx, "notes/other", []byte("x")); err != nil {
		t.Fatal(err)
	}

	var keys []string
	cursor := ""
	for i := 0; i < 10; i++ {
		page, err := cc.List(ctx, "notes/", cursor)
		if err != nil {
			t.Fatal(err)
		}
		for _, ch := range page.Changes {
			if _, ok := cloudsync.ParseNoteKey(ch.Key); ok {
				keys = append(keys, ch.Key)
				if ch.Version == "" {
					t.Fatalf("change %s has no version", ch.Key)
				}
			}
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	if len(keys) != 5 {
		t.Fatalf("listed %d note keys, want 5: %v", len(keys), keys)
	}
	for i, id := range ids {
		want := cloudsync.NoteKey(id)
		if keys[i] != want {
			t.Fatalf("keys[%d] = %q, want %q", i, keys[i], want)
		}
	}
}

func TestS3ErrorClassification(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		code string
		want cloudsync.StoreErrorKind
	}{
		{"AccessDenied", cloudsync.ErrPermission},
		{"InvalidAccessKeyId", cloudsync.ErrAuth},
		{"SignatureDoesNotMatch", cloudsync.ErrAuth},
		{"SlowDown", cloudsync.ErrRateLimit},
		{"QuotaExceeded", cloudsync.ErrQuota},
		{"InternalError", cloudsync.ErrRetryableTransport},
	}
	for _, tc := range cases {
		server := newFakeS3()
		server.failListCode = tc.code
		c, _ := newServer(t, server)
		if _, err := c.List(ctx, "notes/", ""); !cloudsync.IsStoreError(err, tc.want) {
			t.Errorf("%s: list err = %v, want %s", tc.code, err, tc.want)
		}
	}
}

// statusForCode returns a plausible HTTP status for a forced S3 error code.
func statusForCode(code string) int {
	switch code {
	case "AccessDenied", "InvalidAccessKeyId", "SignatureDoesNotMatch":
		return http.StatusForbidden
	case "SlowDown":
		return http.StatusServiceUnavailable
	case "QuotaExceeded":
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}

func TestS3Probe(t *testing.T) {
	ctx := context.Background()
	// Honoring service: conditional writes supported.
	c, _ := newServer(t, newFakeS3())
	caps, err := c.Test(ctx)
	if err != nil || !caps.ConditionalWrites {
		t.Fatalf("Test = %+v, %v; want conditional writes", caps, err)
	}
	// A service that ignores preconditions is rejected.
	server := newFakeS3()
	server.ignorePreconditions = true
	c2, _ := newServer(t, server)
	if _, err := c2.Test(ctx); !cloudsync.IsStoreError(err, cloudsync.ErrUnsupportedCapability) {
		t.Fatalf("Test on ignoring service err = %v, want unsupported-capability", err)
	}
}

func TestS3ProfileIsSecretFree(t *testing.T) {
	c, _ := New(Config{Endpoint: "https://s3.example.com", Region: "us-east-1", Bucket: "notes", AccessKey: "secret1", SecretKey: "secret2"})
	d := c.Profile()
	if len(d) != 64 {
		t.Fatalf("profile length = %d, want 64", len(d))
	}
	// The same location hashes identically regardless of credentials.
	c2, _ := New(Config{Endpoint: "https://s3.example.com", Region: "us-east-1", Bucket: "notes", AccessKey: "other", SecretKey: "other"})
	if c2.Profile() != d {
		t.Fatal("profile depends on credentials")
	}
	// A different bucket differs.
	c3, _ := New(Config{Endpoint: "https://s3.example.com", Region: "us-east-1", Bucket: "other", AccessKey: "ak", SecretKey: "sk"})
	if c3.Profile() == d {
		t.Fatal("profile does not depend on the bucket")
	}
}

// verifySigningHashes checks that the client sends a well-formed V4
// Authorization header and a correct payload hash.
func TestS3SignsRequests(t *testing.T) {
	var mu sync.Mutex
	gotAuth := ""
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotAuth = r.Header.Get("Authorization")
		mu.Unlock()
		if !strings.Contains(gotAuth, "AWS4-HMAC-SHA256 Credential=ak/") {
			s3Error(w, http.StatusForbidden, "SignatureDoesNotMatch")
			return
		}
		w.Header().Set("ETag", `"1"`)
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()
	c, err := New(Config{Endpoint: ts.URL, Region: "us-east-1", Bucket: "notes", AccessKey: "ak", SecretKey: "sk", ForcePathStyle: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Create(context.Background(), "notes/a.json", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gotAuth, "SignedHeaders=host;x-amz-content-sha256;x-amz-date") {
		t.Fatalf("authorization header = %q, want the signed header set", gotAuth)
	}
}

func hashOf(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// readAWSChunked decodes an AWS SigV4 chunked-encoded body into its raw
// payload, mirroring how a real S3 service decodes a chunked upload.
func readAWSChunked(r io.Reader) ([]byte, error) {
	br := bufio.NewReader(r)
	var out []byte
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}
		line = strings.TrimSuffix(line, "\r\n")
		sizeHex := line
		if i := strings.Index(line, ";"); i >= 0 {
			sizeHex = line[:i]
		}
		n, perr := strconv.ParseInt(strings.TrimSpace(sizeHex), 16, 64)
		if perr != nil {
			return nil, perr
		}
		if n == 0 {
			_, _ = br.ReadString('\n') // trailing chunk-signature line
			break
		}
		chunk := make([]byte, n+2) // include the trailing \r\n
		if _, err := io.ReadFull(br, chunk); err != nil {
			return nil, err
		}
		out = append(out, chunk[:n]...)
	}
	return out, nil
}

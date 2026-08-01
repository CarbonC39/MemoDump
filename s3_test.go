package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type imageURLFixture struct {
	Normalize []struct {
		Base   string `json:"base"`
		Prefix string `json:"prefix"`
		Key    string `json:"key"`
		Want   string `json:"want"`
	} `json:"normalize"`
	PublicBaseURL []struct {
		Value         string `json:"value"`
		AllowInsecure bool   `json:"allowInsecure"`
		Valid         bool   `json:"valid"`
	} `json:"publicBaseUrl"`
}

func loadImageURLFixture(t *testing.T) imageURLFixture {
	t.Helper()
	data, err := os.ReadFile("testdata/image-url-fixtures.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture imageURLFixture
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func TestImageURLNormalizationFixtures(t *testing.T) {
	fixture := loadImageURLFixture(t)
	for i, tc := range fixture.Normalize {
		cfg := imageS3Config{PublicBaseURL: tc.Base, Prefix: tc.Prefix}
		if err := normalizeImageURLs(&cfg, true); err != nil {
			t.Fatalf("case %d: normalize failed: %v", i, err)
		}
		if got := buildImageURL(cfg, tc.Key); got != tc.Want {
			t.Errorf("case %d: buildImageURL = %q, want %q", i, got, tc.Want)
		}
	}
}

func TestPublicBaseURLValidationFixtures(t *testing.T) {
	fixture := loadImageURLFixture(t)
	for i, tc := range fixture.PublicBaseURL {
		cfg := imageS3Config{PublicBaseURL: tc.Value}
		err := normalizeImageURLs(&cfg, tc.AllowInsecure)
		if (err == nil) != tc.Valid {
			t.Errorf("case %d: value %q allowInsecure=%v: valid=%v, got err=%v",
				i, tc.Value, tc.AllowInsecure, tc.Valid, err)
		}
	}
}

func TestS3ActiveRequiresAllRequiredFields(t *testing.T) {
	full := imageS3Config{
		Provider: "s3", Endpoint: "https://s3.example.com", Bucket: "b",
		AccessKey: "ak", SecretKey: "sk", PublicBaseURL: "https://cdn.example.com",
	}
	if !s3Active(full) {
		t.Fatal("full config should be active")
	}
	cases := []struct {
		name   string
		mutate func(*imageS3Config)
	}{
		{"no endpoint", func(c *imageS3Config) { c.Endpoint = "" }},
		{"no bucket", func(c *imageS3Config) { c.Bucket = "" }},
		{"no access key", func(c *imageS3Config) { c.AccessKey = "" }},
		{"no secret", func(c *imageS3Config) { c.SecretKey = "" }},
		{"no public url", func(c *imageS3Config) { c.PublicBaseURL = "" }},
		{"provider local", func(c *imageS3Config) { c.Provider = "local" }},
	}
	for _, tc := range cases {
		cfg := full
		tc.mutate(&cfg)
		if s3Active(cfg) {
			t.Errorf("%s: config should be inactive", tc.name)
		}
	}
}

func TestImageConfigSaveSecretRotation(t *testing.T) {
	oldDataDir, oldFile := dataDir, imageConfigFile
	oldNoAuth := noAuth
	dataDir = t.TempDir()
	imageConfigFile = filepath.Join(dataDir, ".image-config.json")
	noAuth = true
	t.Cleanup(func() {
		dataDir = oldDataDir
		imageConfigFile = oldFile
		noAuth = oldNoAuth
	})
	mux := buildAPIMux()

	base := imageS3Config{
		Provider: "s3", Endpoint: "https://s3.example.com", Bucket: "b",
		Prefix: "", PublicBaseURL: "https://cdn.example.com", AccessKey: "ak1",
		SecretKey: "sk1",
	}
	body, _ := json.Marshal(base)
	req := httptest.NewRequest(http.MethodPut, "/api/config/image", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("create status = %d; body=%s", rec.Code, rec.Body.String())
	}

	// Same identity, empty secret → keep the stored secret.
	base.SecretKey = ""
	body, _ = json.Marshal(base)
	req = httptest.NewRequest(http.MethodPut, "/api/config/image", bytes.NewReader(body))
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("update-same status = %d; body=%s", rec.Code, rec.Body.String())
	}
	stored, _ := loadImageConfigFile()
	if stored.SecretKey != "sk1" {
		t.Fatalf("secret was not preserved: %q", stored.SecretKey)
	}

	// Changed access key without a secret → rejected.
	base.AccessKey = "ak2"
	body, _ = json.Marshal(base)
	req = httptest.NewRequest(http.MethodPut, "/api/config/image", bytes.NewReader(body))
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("identity-change status = %d, want 400", rec.Code)
	}

	// Changing to provider local clears the stored config.
	local := imageS3Config{Provider: "local"}
	body, _ = json.Marshal(local)
	req = httptest.NewRequest(http.MethodPut, "/api/config/image", bytes.NewReader(body))
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("local status = %d; body=%s", rec.Code, rec.Body.String())
	}
	stored, _ = loadImageConfigFile()
	if stored.Provider != "local" || stored.SecretKey != "" {
		t.Fatalf("stored config = %#v, want local without secrets", stored)
	}
	var response struct {
		Provider   string `json:"provider"`
		Configured bool   `json:"configured"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Provider != "local" || response.Configured {
		t.Fatalf("local response = %#v", response)
	}
}

// fakeS3Server accepts unauthenticated S3-shaped requests and records the
// objects it has seen. HEAD/GET return 200; DELETE can be configured to fail.
func fakeS3Server(t *testing.T, failDelete bool) (*httptest.Server, *sync.Map) {
	t.Helper()
	objects := &sync.Map{}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPut:
			var buf bytes.Buffer
			_, _ = buf.ReadFrom(r.Body)
			objects.Store(r.URL.Path, buf.String())
			w.WriteHeader(http.StatusOK)
		case http.MethodHead, http.MethodGet:
			if _, ok := objects.Load(r.URL.Path); ok {
				w.Header().Set("Content-Type", "text/plain")
				w.WriteHeader(http.StatusOK)
			} else {
				w.WriteHeader(http.StatusNotFound)
			}
		case http.MethodDelete:
			if failDelete {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			objects.Delete(r.URL.Path)
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, objects
}

func s3ConfigForTestServer(srv *httptest.Server) imageS3Config {
	return imageS3Config{
		Provider: "s3", Endpoint: srv.URL, Bucket: "test-bucket",
		PublicBaseURL: srv.URL + "/test-bucket", AccessKey: "ak", SecretKey: "sk",
		ForcePathStyle: true,
	}
}

func TestImageConfigTestConnection(t *testing.T) {
	srv, _ := fakeS3Server(t, false)
	cfg := s3ConfigForTestServer(srv)
	warnings, err := testImageS3Config(cfg)
	if err != nil {
		t.Fatalf("test connection failed: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
}

func TestImageConfigTestConnectionWithPrefix(t *testing.T) {
	srv, objects := fakeS3Server(t, true)
	cfg := s3ConfigForTestServer(srv)
	cfg.Prefix = "images"
	warnings, err := testImageS3Config(cfg)
	if err != nil {
		t.Fatalf("test connection with prefix failed: %v", err)
	}
	if len(warnings) != 1 {
		t.Fatalf("warnings = %v, want cleanup warning", warnings)
	}
	if _, ok := objects.Load("/test-bucket/images/" + imageProbeKey()); !ok {
		t.Fatal("probe was not uploaded beneath the configured prefix")
	}
}

func TestImageConfigTestConnectionDeleteFailureWarns(t *testing.T) {
	srv, _ := fakeS3Server(t, true)
	cfg := s3ConfigForTestServer(srv)
	warnings, err := testImageS3Config(cfg)
	if err != nil {
		t.Fatalf("test connection failed: %v", err)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "could not be removed") {
		t.Fatalf("warnings = %v, want a delete-failure warning", warnings)
	}
}

func TestImageConfigTestConnectionVerifyFails(t *testing.T) {
	srv, _ := fakeS3Server(t, false)
	cfg := s3ConfigForTestServer(srv)
	cfg.PublicBaseURL = srv.URL + "/wrong-prefix"
	if _, err := testImageS3Config(cfg); err == nil {
		t.Fatal("expected failure when the public URL does not resolve")
	}
}

func TestImagePutInS3ModeProxies(t *testing.T) {
	oldDataDir, oldNoAuth := dataDir, noAuth
	dataDir = t.TempDir()
	noAuth = true
	t.Cleanup(func() {
		dataDir = oldDataDir
		noAuth = oldNoAuth
	})
	srv, objects := fakeS3Server(t, false)

	// Activate S3 via env so effectiveImageS3Config picks it up.
	t.Setenv("MEMODUMP_IMAGE_S3_ENDPOINT", srv.URL)
	t.Setenv("MEMODUMP_IMAGE_S3_BUCKET", "test-bucket")
	t.Setenv("MEMODUMP_IMAGE_S3_PUBLIC_URL", srv.URL+"/test-bucket")
	t.Setenv("MEMODUMP_IMAGE_S3_ACCESS_KEY", "ak")
	t.Setenv("MEMODUMP_IMAGE_S3_SECRET_KEY", "sk")
	t.Setenv("MEMODUMP_IMAGE_S3_FORCE_PATH_STYLE", "true")

	mux := buildAPIMux()
	body := pngBody()
	key := imageHash(body) + ".png"
	if rec := putImage(t, mux, key, body); rec.Code != http.StatusCreated {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	if _, ok := objects.Load("/test-bucket/" + key); !ok {
		t.Fatal("object was not stored on the fake S3 server")
	}
}

func TestImagePutInS3ModeVerifyFailure(t *testing.T) {
	oldDataDir, oldNoAuth := dataDir, noAuth
	dataDir = t.TempDir()
	noAuth = true
	t.Cleanup(func() {
		dataDir = oldDataDir
		noAuth = oldNoAuth
	})
	srv, _ := fakeS3Server(t, false)

	t.Setenv("MEMODUMP_IMAGE_S3_ENDPOINT", srv.URL)
	t.Setenv("MEMODUMP_IMAGE_S3_BUCKET", "test-bucket")
	t.Setenv("MEMODUMP_IMAGE_S3_PUBLIC_URL", srv.URL+"/wrong-prefix")
	t.Setenv("MEMODUMP_IMAGE_S3_ACCESS_KEY", "ak")
	t.Setenv("MEMODUMP_IMAGE_S3_SECRET_KEY", "sk")
	t.Setenv("MEMODUMP_IMAGE_S3_FORCE_PATH_STYLE", "true")

	mux := buildAPIMux()
	body := pngBody()
	key := imageHash(body) + ".png"
	rec := putImage(t, mux, key, body)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "verify_failed") {
		t.Fatalf("body does not mention verify_failed: %s", rec.Body.String())
	}
}

func TestConfigEndpointReportsS3(t *testing.T) {
	oldDataDir := dataDir
	dataDir = t.TempDir()
	t.Cleanup(func() { dataDir = oldDataDir })
	srv, _ := fakeS3Server(t, false)
	t.Setenv("MEMODUMP_IMAGE_S3_ENDPOINT", srv.URL)
	t.Setenv("MEMODUMP_IMAGE_S3_BUCKET", "test-bucket")
	t.Setenv("MEMODUMP_IMAGE_S3_PUBLIC_URL", srv.URL)
	t.Setenv("MEMODUMP_IMAGE_S3_ACCESS_KEY", "ak")
	t.Setenv("MEMODUMP_IMAGE_S3_SECRET_KEY", "sk")

	mux := buildAPIMux()
	req := httptest.NewRequest(http.MethodGet, "/api/config", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var resp struct {
		Image map[string]any `json:"image"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Image["provider"] != "s3" || resp.Image["configured"] != true {
		t.Fatalf("image config = %#v, want s3 configured", resp.Image)
	}
	if _, hasSecret := resp.Image["secretKey"]; hasSecret {
		t.Fatal("config endpoint must not expose secrets")
	}
	if resp.Image["editable"] != false {
		t.Fatalf("editable = %v, want false when env-configured", resp.Image["editable"])
	}
}

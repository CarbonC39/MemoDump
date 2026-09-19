//go:build r2test

// Opt-in integration tests against a real S3-compatible bucket (Cloudflare
// R2). They only run with `go test -tags r2test` and skip unless
// .r2-test/image-config.json exists (the folder is gitignored). Secrets are
// read programmatically and never logged: every piece of output is scrubbed
// through redactR2 before it reaches the test log.
package imagesvc

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"

	"memodump/internal/appstate"
)

const r2ConfigRel = ".r2-test/image-config.json"

func r2TestRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Dir(file)
}

func loadR2Config(t *testing.T) (imageS3Config, bool) {
	t.Helper()
	path := filepath.Join(r2TestRoot(t), r2ConfigRel)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Logf("R2 config %s not present, skipping real-bucket test", path)
		return imageS3Config{}, false
	}
	var cfg imageS3Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("R2 config is invalid JSON: %v", err)
	}
	return cfg, true
}

// redactR2 scrubs credentials from anything that reaches the test log.
func redactR2(cfg imageS3Config, s string) string {
	s = strings.ReplaceAll(s, cfg.AccessKey, "***")
	s = strings.ReplaceAll(s, cfg.SecretKey, "***")
	return s
}

// r2TestEnv points the package globals at the real config and a throwaway data
// dir, and restores them afterwards.
func r2TestEnv(t *testing.T, cfg imageS3Config) {
	t.Helper()
	oldFile, oldDir, oldAuth := ConfigFile, appstate.DataDir, appstate.NoAuth
	ConfigFile = filepath.Join(r2TestRoot(t), r2ConfigRel)
	appstate.DataDir = t.TempDir()
	appstate.NoAuth = true
	t.Cleanup(func() {
		ConfigFile, appstate.DataDir, appstate.NoAuth = oldFile, oldDir, oldAuth
	})
	_ = cfg
}

func TestR2TestConnection(t *testing.T) {
	cfg, ok := loadR2Config(t)
	if !ok {
		t.Skip("no .r2-test/image-config.json")
	}
	if !s3Active(cfg) {
		t.Skip("fill endpoint/bucket/publicBaseUrl/keys in .r2-test/image-config.json first")
	}
	r2TestEnv(t, cfg)

	// Best-effort cleanup of the deterministic probe key, including leftover
	// objects from runs that failed before the server-side DELETE step.
	client, err := newMinioClient(cfg)
	if err != nil {
		t.Fatalf("minio client: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	probeObject := objectNameForImage(cfg, imageProbeKey())
	defer func() {
		if err := client.RemoveObject(ctx, cfg.Bucket, probeObject, minio.RemoveObjectOptions{}); err != nil {
			t.Logf("probe cleanup warning: %s", redactR2(cfg, err.Error()))
		}
	}()

	mux := imageConfigMux()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/config/image/test", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("test connection status = %d: %s", rec.Code, redactR2(cfg, rec.Body.String()))
	}
	var resp struct {
		Warnings []string `json:"warnings"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unexpected response shape: %v", err)
	}
	for _, w := range resp.Warnings {
		t.Logf("warning: %s", redactR2(cfg, w))
	}
	t.Log("test connection OK: probe upload, anonymous read and cleanup accepted")
}

func TestR2ImageUploadRoundTrip(t *testing.T) {
	cfg, ok := loadR2Config(t)
	if !ok {
		t.Skip("no .r2-test/image-config.json")
	}
	if !s3Active(cfg) {
		t.Skip("fill endpoint/bucket/publicBaseUrl/keys in .r2-test/image-config.json first")
	}
	if err := normalizeImageURLs(&cfg, true); err != nil {
		t.Fatalf("normalize config: %v", err)
	}
	r2TestEnv(t, cfg)

	// Upload through the real HTTP API exactly like the web version does.
	body := pngBody()
	key := imageHash(body) + ".png"
	objectName := objectNameForImage(cfg, key)
	client2, err := newMinioClient(cfg)
	if err != nil {
		t.Fatalf("minio client: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// Remove the object even when a later assertion fails.
	defer func() {
		if err := client2.RemoveObject(ctx, cfg.Bucket, objectName, minio.RemoveObjectOptions{}); err != nil {
			t.Logf("cleanup warning: could not remove test object: %s", redactR2(cfg, err.Error()))
		}
	}()

	mux := imageConfigMux()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/images/"+key, bytes.NewReader(body))
	req.Header.Set("X-MemoDump-Image-Target", imageTargetID(cfg))
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("PUT status = %d: %s", rec.Code, redactR2(cfg, rec.Body.String()))
	}

	// Read the object back through the public URL, byte-for-byte, as the
	// browser would render it.
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Get(buildImageURL(cfg, key))
	if err != nil {
		t.Fatalf("public GET failed: %v", redactR2(cfg, err.Error()))
	}
	got, readErr := io.ReadAll(resp.Body)
	resp.Body.Close()
	if readErr != nil {
		t.Fatalf("public GET read failed: %v", readErr)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("public GET status = %d", resp.StatusCode)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("public GET body mismatch: got %d bytes, want %d", len(got), len(body))
	}
	t.Logf("upload + public readback OK (%d bytes, verified identical)", len(got))
}

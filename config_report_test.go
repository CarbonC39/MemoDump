package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"memodump/internal/appstate"
)

// The unauthenticated /api/config endpoint reports the effective image config
// without secrets: it reflects the "editable" flag when S3 is env-configured
// and never leaks the secret key, endpoint, or access-key ID.
func TestConfigEndpointReportsS3(t *testing.T) {
	oldDataDir := appstate.DataDir
	appstate.DataDir = t.TempDir()
	t.Cleanup(func() { appstate.DataDir = oldDataDir })

	// The config endpoint only resolves the effective config; it makes no
	// network calls, so any https endpoint suffices.
	t.Setenv("MEMODUMP_IMAGE_S3_ENDPOINT", "https://s3.example.com")
	t.Setenv("MEMODUMP_IMAGE_S3_BUCKET", "test-bucket")
	t.Setenv("MEMODUMP_IMAGE_S3_PUBLIC_URL", "https://cdn.example.com")
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
	if _, hasEndpoint := resp.Image["endpoint"]; hasEndpoint {
		t.Fatal("unauthenticated config endpoint must not expose the S3 endpoint")
	}
	if _, hasAccessKey := resp.Image["accessKey"]; hasAccessKey {
		t.Fatal("unauthenticated config endpoint must not expose the access-key ID")
	}
	if resp.Image["editable"] != false {
		t.Fatalf("editable = %v, want false when env-configured", resp.Image["editable"])
	}
}

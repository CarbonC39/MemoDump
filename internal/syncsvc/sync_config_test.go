package syncsvc

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

var syncEnvKeys = []string{
	"MEMODUMP_SYNC_ENDPOINT", "MEMODUMP_SYNC_REGION", "MEMODUMP_SYNC_BUCKET",
	"MEMODUMP_SYNC_PREFIX", "MEMODUMP_SYNC_ACCESS_KEY", "MEMODUMP_SYNC_SECRET_KEY",
	"MEMODUMP_SYNC_FORCE_PATH_STYLE",
}

// clearSyncEnv empties every MEMODUMP_SYNC_* variable so tests are deterministic.
func clearSyncEnv(t *testing.T) {
	t.Helper()
	for _, k := range syncEnvKeys {
		t.Setenv(k, "")
	}
}

func withSyncConfigFile(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	old := ConfigFile
	ConfigFile = filepath.Join(dir, "sync-config.json")
	t.Cleanup(func() { ConfigFile = old })
	return ConfigFile
}

func doSyncConfig(t *testing.T, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var rdr *bytes.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		rdr = bytes.NewReader(data)
	}
	var req *http.Request
	if rdr != nil {
		req = httptest.NewRequest(method, path, rdr)
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	rec := httptest.NewRecorder()
	switch method + " " + path {
	case "GET /api/sync/config":
		HandleSyncConfigGet(rec, req)
	case "PUT /api/sync/config":
		HandleSyncConfigSave(rec, req)
	case "POST /api/sync/config/test":
		HandleSyncConfigTest(rec, req)
	}
	return rec
}

func TestSyncConfigSaveRoundTripAndNoSecretLeak(t *testing.T) {
	clearSyncEnv(t)
	withSyncConfigFile(t)

	rec := doSyncConfig(t, "PUT", "/api/sync/config", map[string]any{
		"endpoint": "https://s3.example.com", "region": "us-east-1", "bucket": "notes",
		"prefix": "sync", "accessKey": "ak", "secretKey": "sk", "forcePathStyle": true,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("save status = %d; body=%s", rec.Code, rec.Body.String())
	}
	var saved map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	if _, hasSecret := saved["secretKey"]; hasSecret {
		t.Fatal("save response must not return the secretKey")
	}
	if saved["configured"] != true {
		t.Fatalf("configured = %v, want true", saved["configured"])
	}

	cfg := syncS3Config()
	if cfg.Endpoint != "https://s3.example.com" || cfg.Bucket != "notes" || cfg.SecretKey != "sk" {
		t.Fatalf("syncS3Config = %+v, want the persisted config", cfg)
	}

	get := doSyncConfig(t, "GET", "/api/sync/config", nil)
	var got map[string]any
	if err := json.Unmarshal(get.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if _, hasSecret := got["secretKey"]; hasSecret {
		t.Fatal("GET must never expose the secretKey")
	}
	if got["accessKey"] != "ak" {
		t.Fatalf("accessKey = %v, want ak (panel prefill, not a secret)", got["accessKey"])
	}
}

func TestSyncConfigSecretUnchangedSemantics(t *testing.T) {
	clearSyncEnv(t)
	withSyncConfigFile(t)

	base := map[string]any{
		"endpoint": "https://s3.example.com", "region": "us-east-1", "bucket": "notes",
		"prefix": "sync", "accessKey": "ak", "secretKey": "sk", "forcePathStyle": true,
	}
	if rec := doSyncConfig(t, "PUT", "/api/sync/config", base); rec.Code != http.StatusOK {
		t.Fatalf("create status = %d; body=%s", rec.Code, rec.Body.String())
	}

	// Same identity, empty secretKey -> unchanged secret survives.
	edit := map[string]any{
		"endpoint": "https://s3.example.com", "region": "us-east-1", "bucket": "notes",
		"prefix": "sync", "accessKey": "ak", "secretKey": "", "forcePathStyle": false,
	}
	if rec := doSyncConfig(t, "PUT", "/api/sync/config", edit); rec.Code != http.StatusOK {
		t.Fatalf("edit status = %d; body=%s", rec.Code, rec.Body.String())
	}
	if cfg := syncS3Config(); cfg.SecretKey != "sk" || cfg.ForcePathStyle {
		t.Fatalf("unchanged secret lost or forcePathStyle not applied: %+v", cfg)
	}

	// Changed bucket + empty secretKey -> refused.
	edit["bucket"] = "other"
	if rec := doSyncConfig(t, "PUT", "/api/sync/config", edit); rec.Code != http.StatusBadRequest {
		t.Fatalf("changed-identity-without-secret status = %d, want 400", rec.Code)
	}
}

func TestSyncConfigEnvOverridesFile(t *testing.T) {
	clearSyncEnv(t)
	withSyncConfigFile(t)

	rec := doSyncConfig(t, "PUT", "/api/sync/config", map[string]any{
		"endpoint": "https://file.example.com", "bucket": "file-bucket",
		"accessKey": "ak", "secretKey": "sk", "forcePathStyle": true,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("save status = %d", rec.Code)
	}

	t.Setenv("MEMODUMP_SYNC_ENDPOINT", "https://env.example.com")
	t.Setenv("MEMODUMP_SYNC_BUCKET", "env-bucket")
	cfg := syncS3Config()
	if cfg.Endpoint != "https://env.example.com" || cfg.Bucket != "env-bucket" {
		t.Fatalf("env must override the file: %+v", cfg)
	}
	if cfg.SecretKey != "sk" {
		t.Fatalf("unset env fields keep the file value: %+v", cfg)
	}

	// Env-configured -> the panel reports read-only.
	if !syncConfigHasEnvOverride() {
		t.Fatal("env override must make the panel read-only")
	}
}

func TestSyncConfigTestRejectsUnconfigured(t *testing.T) {
	clearSyncEnv(t)
	withSyncConfigFile(t)
	rec := doSyncConfig(t, "POST", "/api/sync/config/test", nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("test status = %d, want 400 for an unconfigured provider", rec.Code)
	}
	if _, err := os.Stat(ConfigFile); err == nil {
		// A failed probe must not persist anything.
		t.Fatal("probe must not write the config file")
	}
}

func TestSyncConfigSaveRefusedWhileConnected(t *testing.T) {
	clearSyncEnv(t)
	withSyncConfigFile(t)
	dir, state := t.TempDir(), t.TempDir()
	setSyncEnv(t, dir, state, nil)

	if rec := doJSON(t, "POST", "/api/sync/enable", nil); rec.Code != http.StatusOK {
		t.Fatalf("enable status = %d; body=%s", rec.Code, rec.Body.String())
	}

	// Connected: a config save must be refused, otherwise an Enable could pin a
	// connection to a provider that a later save replaces, immediately tripping
	// a provider-changed pause.
	rec := doSyncConfig(t, "PUT", "/api/sync/config", map[string]any{
		"endpoint": "https://s3.example.com", "bucket": "b",
		"accessKey": "ak", "secretKey": "sk", "forcePathStyle": true,
	})
	if rec.Code != http.StatusConflict {
		t.Fatalf("save-while-connected status = %d, want 409", rec.Code)
	}
	if _, err := os.Stat(ConfigFile); err == nil {
		t.Fatal("a refused save must not write the config file")
	}

	// After Disable the same save is accepted.
	if rec := doJSON(t, "POST", "/api/sync/disable", nil); rec.Code != http.StatusOK {
		t.Fatalf("disable status = %d", rec.Code)
	}
	if rec := doSyncConfig(t, "PUT", "/api/sync/config", map[string]any{
		"endpoint": "https://s3.example.com", "bucket": "b",
		"accessKey": "ak", "secretKey": "sk", "forcePathStyle": true,
	}); rec.Code != http.StatusOK {
		t.Fatalf("save-after-disable status = %d; body=%s", rec.Code, rec.Body.String())
	}
}

package main

import (
	"encoding/json"

	"memodump/internal/appstate"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// runtimeMatrixCleanup restores the package globals the runtime-matrix tests
// override, and pins the sync state root to a temp dir so no test ever reads or
// writes the real OS application-data directory.
func runtimeMatrixCleanup(t *testing.T) {
	t.Helper()
	oldDataDir, oldSyncRoot, oldNoAuth, oldCapable := appstate.DataDir, appstate.SyncRoot, appstate.NoAuth, appstate.CloudSyncCapable
	appstate.DataDir, appstate.SyncRoot = t.TempDir(), t.TempDir()
	appstate.NoAuth = true
	appstate.InitRepo() // the note handlers dereference the package-level repo
	t.Cleanup(func() {
		appstate.DataDir, appstate.SyncRoot, appstate.NoAuth, appstate.CloudSyncCapable = oldDataDir, oldSyncRoot, oldNoAuth, oldCapable
		appstate.InitRepo()
	})
}

// TestRuntimeMatrixCLIServerHasNoSyncSurface proves the CLI Web server exposes
// no cloud-sync surface (R6.0): every /api/sync route returns one stable
// unavailable response while the ordinary note/image API keeps working.
func TestRuntimeMatrixCLIServerHasNoSyncSurface(t *testing.T) {
	runtimeMatrixCleanup(t)
	appstate.CloudSyncCapable = false

	mux := buildAPIMux()

	const wantBody = `cloud sync is not available on this runtime`
	for _, tc := range []struct{ method, path string }{
		{"GET", "/api/sync/status"},
		{"POST", "/api/sync/enable"},
		{"POST", "/api/sync/run"},
		{"POST", "/api/sync/disable"},
		{"POST", "/api/sync/reset"},
		{"POST", "/api/sync/test"},
		{"GET", "/api/sync/recovery"},
		{"POST", "/api/sync/recovery/restore"},
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s %s = %d, want 404 unavailable", tc.method, tc.path, rec.Code)
		}
		var body map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body["error"] != wantBody {
			t.Errorf("%s %s body = %q, want stable error %q", tc.method, tc.path, rec.Body.String(), wantBody)
		}
	}

	// Ordinary API routes are unaffected by the sync-surface removal.
	for _, tc := range []struct {
		method, path string
		want         int
	}{
		{"GET", "/api/ping", http.StatusOK},
		{"GET", "/api/config", http.StatusOK},
		{"GET", "/api/notes", http.StatusOK},
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, nil))
		if rec.Code != tc.want {
			t.Errorf("%s %s = %d, want %d", tc.method, tc.path, rec.Code, tc.want)
		}
	}
}

// TestRuntimeMatrixWailsKeepsSyncSurface proves the Wails runtime keeps the
// reviewed R5 sync surface: the routes are registered and status answers with a
// real 200 JSON payload, never the unavailable response.
func TestRuntimeMatrixWailsKeepsSyncSurface(t *testing.T) {
	runtimeMatrixCleanup(t)
	appstate.CloudSyncCapable = true

	mux := buildAPIMux()

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/sync/status", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/sync/status = %d, want 200 (Wails keeps R5 sync)", rec.Code)
	}
	var status map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
		t.Fatalf("status body is not JSON: %v", err)
	}
	if enabled, ok := status["enabled"].(bool); !ok || enabled {
		t.Fatalf("unconfigured Wails status = %+v, want enabled:false", status)
	}
	if body := rec.Body.String(); strings.Contains(body, "cloud sync is not available") {
		t.Fatalf("Wails status must never return the unavailable response: %s", body)
	}

	// The enable route is present too (a 404 would mean the surface is missing).
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/sync/enable", nil))
	if rec.Code == http.StatusNotFound {
		t.Fatal("POST /api/sync/enable is 404 on the Wails runtime; the sync surface must stay registered")
	}
}

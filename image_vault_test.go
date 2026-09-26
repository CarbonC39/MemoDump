package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"memodump/internal/appstate"
	"memodump/internal/vaultfs"
)

// The image vault directory is dot-prefixed so the folder tree never exposes
// it, and reserved so a user folder can never shadow it.
func TestFolderTreeSkipsDotDirectories(t *testing.T) {
	oldDataDir, oldNoAuth := appstate.DataDir, appstate.NoAuth
	appstate.DataDir = t.TempDir()
	appstate.NoAuth = true
	t.Cleanup(func() { appstate.DataDir, appstate.NoAuth = oldDataDir, oldNoAuth })
	appstate.InitRepo()
	mux := buildAPIMux()

	if err := os.MkdirAll(filepath.Join(appstate.DataDir, vaultfs.ReservedVaultDir), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(appstate.DataDir, "notes"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(appstate.DataDir, "notes", "a.md"), []byte("# a"), 0644); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/folders", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	var tree []vaultfs.Folder
	if err := json.Unmarshal(rec.Body.Bytes(), &tree); err != nil {
		t.Fatal(err)
	}
	if len(tree) != 1 || tree[0].Name != "notes" {
		t.Fatalf("folder tree = %#v, want only 'notes'", tree)
	}
}

func TestCreateFolderRejectsReservedName(t *testing.T) {
	oldDataDir, oldNoAuth := appstate.DataDir, appstate.NoAuth
	appstate.DataDir = t.TempDir()
	appstate.NoAuth = true
	t.Cleanup(func() { appstate.DataDir, appstate.NoAuth = oldDataDir, oldNoAuth })
	appstate.InitRepo()
	mux := buildAPIMux()
	for _, path := range []string{".images", "a/.images"} {
		req := httptest.NewRequest(http.MethodPost, "/api/folders",
			bytes.NewBufferString(`{"path":"`+path+`"}`))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("create %q: status = %d, want 400", path, rec.Code)
		}
	}
}

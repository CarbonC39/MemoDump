package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"memodump/internal/cloudsync"
	"memodump/internal/syncindex"
	"memodump/internal/syncservice"
	"memodump/internal/syncstate"
)

// setSyncEnv points the package-level sync globals at a test vault/state root
// and restores them at cleanup. A nil provider keeps the current default.
func setSyncEnv(t *testing.T, dir, stateRoot string, provider syncservice.Provider) {
	t.Helper()
	oldDataDir, oldSyncRoot, oldProvider := dataDir, syncRoot, syncProvider
	if provider == nil {
		provider = oldProvider
	}
	dataDir, syncRoot, syncProvider = dir, stateRoot, provider
	syncLastRunMu.Lock()
	syncLastRun.Result = syncservice.Result{}
	syncLastRun.Completed = time.Time{}
	syncLastRunMu.Unlock()
	t.Cleanup(func() { dataDir, syncRoot, syncProvider = oldDataDir, oldSyncRoot, oldProvider })
}

func doJSON(t *testing.T, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var rdr *bytes.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		rdr = bytes.NewReader(data)
	}
	var req *http.Request
	if rdr != nil {
		req = httptest.NewRequest(method, path, rdr)
		req.Header.Set("Content-Type", "application/json")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	rec := httptest.NewRecorder()
	dispatch := map[string]http.HandlerFunc{
		"GET /api/sync/status":            handleSyncStatus,
		"POST /api/sync/enable":           handleSyncEnable,
		"POST /api/sync/run":              handleSyncRun,
		"POST /api/sync/disable":          handleSyncDisable,
		"POST /api/sync/test":             handleSyncTest,
		"GET /api/sync/recovery":          handleSyncRecoveryList,
		"POST /api/sync/recovery/restore": handleSyncRecoveryRestore,
	}
	dispatch[method+" "+path](rec, req)
	return rec
}

func decodeSync[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
	return out
}

// syncRunJSON mirrors the syncservice.Result wire shape (Go default field
// names) for assertions.
// TestSyncApiRecoveryListReportsRealErrors: a vault that has never enabled sync
// returns an empty recovery list, but a real state error (corrupt recovery
// area) is reported as 500 — never a misleading empty list.
func TestSyncApiRecoveryListReportsRealErrors(t *testing.T) {
	dir, state := t.TempDir(), t.TempDir()
	setSyncEnv(t, dir, state, nil)

	// Not enabled yet: empty list, not an error.
	list := decodeSync[map[string]any](t, doJSON(t, "GET", "/api/sync/recovery", nil))
	if items, _ := list["recovery"].([]any); len(items) != 0 {
		t.Fatalf("not-enabled recovery = %+v, want empty", list)
	}

	// Enabled, then the recovery area becomes corrupt: 500, not [].
	doJSON(t, "POST", "/api/sync/enable", nil)
	vaultID, replicaID, stateRoot, err := syncIdentity()
	if err != nil {
		t.Fatal(err)
	}
	recoveryDir := filepath.Join(syncstate.StateDir(stateRoot, vaultID, replicaID), syncstate.RecoveryDirName)
	if err := os.MkdirAll(filepath.Dir(recoveryDir), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(recoveryDir, []byte("not a directory"), 0644); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	handleSyncRecoveryList(rec, httptest.NewRequest(http.MethodGet, "/api/sync/recovery", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("corrupt recovery list status = %d, want 500 (body %s)", rec.Code, rec.Body.String())
	}
}

// TestSyncApiRecoveryListReportsCorruptIndex: a corrupt sync index is a real
// error, never disguised as "no recovery copies".
func TestSyncApiRecoveryListReportsCorruptIndex(t *testing.T) {
	dir, state := t.TempDir(), t.TempDir()
	setSyncEnv(t, dir, state, nil)
	doJSON(t, "POST", "/api/sync/enable", nil)

	// Corrupt both the primary index and its backup.
	corrupt := []byte(`{not json`)
	for _, name := range []string{"sync-index.json", "sync-index.json.bak"} {
		if err := os.WriteFile(filepath.Join(dir, ".memodump", name), corrupt, 0600); err != nil {
			t.Fatal(err)
		}
	}
	rec := httptest.NewRecorder()
	handleSyncRecoveryList(rec, httptest.NewRequest(http.MethodGet, "/api/sync/recovery", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("corrupt index recovery list status = %d, want 500 (body %s)", rec.Code, rec.Body.String())
	}
}

type syncRunJSON struct {
	Synced            bool
	Scanned           int
	Blocked           int
	Retry             int
	Conflicts         int
	SnapshotCommitted bool
	LastError         string
}

// TestSyncApiEnableRunStatusDisable exercises the lifecycle through the API.
func TestSyncApiEnableRunStatusDisable(t *testing.T) {
	dir, state := t.TempDir(), t.TempDir()
	setSyncEnv(t, dir, state, nil)
	if err := os.WriteFile(filepath.Join(dir, "idea.md"), []byte("# Idea\n"), 0644); err != nil {
		t.Fatal(err)
	}

	st := decodeSync[map[string]any](t, doJSON(t, "GET", "/api/sync/status", nil))
	if st["enabled"] != false {
		t.Fatalf("status = %+v, want disabled", st)
	}

	en := decodeSync[map[string]any](t, doJSON(t, "POST", "/api/sync/enable", nil))
	if en["enabled"] != true || en["vaultId"] == "" {
		t.Fatalf("enable = %+v", en)
	}

	run := decodeSync[syncRunJSON](t, doJSON(t, "POST", "/api/sync/run", nil))
	if !run.Synced || !run.SnapshotCommitted {
		t.Fatalf("run = %+v, want synced", run)
	}

	st = decodeSync[map[string]any](t, doJSON(t, "GET", "/api/sync/status", nil))
	if st["enabled"] != true || st["connected"] != true {
		t.Fatalf("status = %+v, want enabled+connected", st)
	}

	ds := decodeSync[map[string]any](t, doJSON(t, "POST", "/api/sync/disable", nil))
	if ds["enabled"] != false {
		t.Fatalf("disable = %+v", ds)
	}
	st = decodeSync[map[string]any](t, doJSON(t, "GET", "/api/sync/status", nil))
	if st["enabled"] != false || st["connected"] != false {
		t.Fatalf("status after disable = %+v, want disabled", st)
	}
	if _, err := os.Stat(filepath.Join(dir, "idea.md")); err != nil {
		t.Fatal("disable deleted the local note")
	}
	// While disabled, a manual run is refused.
	runAfter := decodeSync[map[string]any](t, doJSON(t, "POST", "/api/sync/run", nil))
	if runAfter["error"] == nil {
		t.Fatalf("run while disabled = %+v, want refused", runAfter)
	}
}

// TestSyncApiTwoReplicasConverge drives two local replicas against one shared
// memory remote through the public API boundary.
func TestSyncApiTwoReplicasConverge(t *testing.T) {
	shared := cloudsync.NewMemoryStore()
	state := t.TempDir()
	dirA, dirB := t.TempDir(), t.TempDir()
	setSyncEnv(t, dirA, state, func() (cloudsync.RemoteStore, error) { return shared, nil })

	// Replica A: enable, write a note, run (uploads).
	doJSON(t, "POST", "/api/sync/enable", nil)
	if err := os.WriteFile(filepath.Join(dirA, "idea.md"), []byte("# Idea\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if run := decodeSync[syncRunJSON](t, doJSON(t, "POST", "/api/sync/run", nil)); !run.Synced {
		t.Fatalf("replica A run = %+v", run)
	}

	// Replica B: a different vault, same state root + shared remote; run pulls.
	oldDataDir, oldSyncRoot, oldProvider := dataDir, syncRoot, syncProvider
	dataDir, syncRoot = dirB, state
	t.Cleanup(func() { dataDir, syncRoot, syncProvider = oldDataDir, oldSyncRoot, oldProvider })
	doJSON(t, "POST", "/api/sync/enable", nil)
	if run := decodeSync[syncRunJSON](t, doJSON(t, "POST", "/api/sync/run", nil)); !run.Synced {
		t.Fatalf("replica B run = %+v", run)
	}
	if data, err := os.ReadFile(filepath.Join(dirB, "idea.md")); err != nil || string(data) != "# Idea\n" {
		t.Fatalf("replica B did not pull the note: %q, %v", data, err)
	}
}

// TestSyncApiRunFatalErrorNeverSynced: a fatal provider error is surfaced as a
// redacted label and never reported "synced".
func TestSyncApiRunFatalErrorNeverSynced(t *testing.T) {
	shared := cloudsync.NewMemoryStore()
	dir, state := t.TempDir(), t.TempDir()
	setSyncEnv(t, dir, state, func() (cloudsync.RemoteStore, error) { return shared, nil })

	rec := &cloudsync.NoteRecord{
		SchemaVersion: cloudsync.NoteSchemaVersion, SyncID: "11111111-1111-4111-8111-111111111111",
		Path: "a.md", Markdown: "# A\n",
	}
	data, _ := rec.Serialize()
	if err := shared.Seed(cloudsync.NoteKey(rec.SyncID), data, "1"); err != nil {
		t.Fatal(err)
	}
	shared.ArmFault("read", &cloudsync.StoreError{Kind: cloudsync.ErrPermission, Message: "denied"})

	doJSON(t, "POST", "/api/sync/enable", nil)
	run := decodeSync[syncRunJSON](t, doJSON(t, "POST", "/api/sync/run", nil))
	if run.Synced {
		t.Fatal("fatal error reported synced")
	}
	if run.LastError != "permission" {
		t.Fatalf("LastError = %q, want the redacted permission label", run.LastError)
	}
	if strings.Contains(run.LastError, "://") || strings.Contains(run.LastError, "secret") {
		t.Fatalf("status leaked provider details: %q", run.LastError)
	}
}

// TestSyncApiRecoveryListRestore lists and restores a recovery copy through the
// API.
func TestSyncApiRecoveryListRestore(t *testing.T) {
	dir, state := t.TempDir(), t.TempDir()
	setSyncEnv(t, dir, state, nil)
	doJSON(t, "POST", "/api/sync/enable", nil)

	// Seed a recovery copy for a sync ID whose indexed path is a.md.
	vaultID, replicaID, _, err := syncIdentity()
	if err != nil {
		t.Fatal(err)
	}
	rec, err := syncstate.NewRecoveryStore(syncRoot, vaultID, replicaID)
	if err != nil {
		t.Fatal(err)
	}
	syncID := "11111111-1111-4111-8111-111111111111"
	stateHash := strings.Repeat("a", 64)
	if err := rec.Write(syncID, stateHash, "# recovered\n"); err != nil {
		t.Fatal(err)
	}
	store, err := syncindex.LoadNoteStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddNote(syncID, "a.md"); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(); err != nil {
		t.Fatal(err)
	}

	list := decodeSync[map[string]any](t, doJSON(t, "GET", "/api/sync/recovery", nil))
	items, _ := list["recovery"].([]any)
	if len(items) != 1 {
		t.Fatalf("recovery list = %+v, want 1 copy", list)
	}
	first := items[0].(map[string]any)
	if first["stateHash"] != stateHash || first["markdown"] != "# recovered\n" {
		t.Fatalf("recovery copy = %+v", first)
	}

	restored := decodeSync[map[string]any](t, doJSON(t, "POST", "/api/sync/recovery/restore", map[string]any{
		"syncId": syncID, "stateHash": stateHash,
	}))
	if restored["ok"] != true {
		t.Fatalf("restore = %+v", restored)
	}
	if data, err := os.ReadFile(filepath.Join(dir, "a.md")); err != nil || string(data) != "# recovered\n" {
		t.Fatalf("restored note = %q, %v", data, err)
	}
}

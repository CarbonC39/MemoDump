package syncsvc

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"memodump/internal/appstate"
	"memodump/internal/cloudsync"
	"memodump/internal/syncindex"
	"memodump/internal/syncservice"
	"memodump/internal/syncstate"
)

// setSyncEnv points the package-level sync globals at a test vault/state root
// and restores them at cleanup. A nil provider selects a fresh memory store.
func setSyncEnv(t *testing.T, dir, stateRoot string, provider syncservice.Provider) {
	t.Helper()
	oldDataDir, oldSyncRoot, oldProvider := appstate.DataDir, appstate.SyncRoot, syncProvider
	if provider == nil {
		store := cloudsync.NewMemoryStore()
		provider = func() (cloudsync.RemoteStore, error) { return store, nil }
	}
	appstate.DataDir, appstate.SyncRoot, syncProvider = dir, stateRoot, provider
	syncLastRunMu.Lock()
	syncLastRun.Result = syncservice.Result{}
	syncLastRun.Completed = time.Time{}
	syncLastRun.Trigger = ""
	syncLastRunMu.Unlock()
	t.Cleanup(func() { appstate.DataDir, appstate.SyncRoot, syncProvider = oldDataDir, oldSyncRoot, oldProvider })
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
		"GET /api/sync/status":            HandleSyncStatus,
		"POST /api/sync/enable":           HandleSyncEnable,
		"POST /api/sync/run":              HandleSyncRun,
		"POST /api/sync/disable":          HandleSyncDisable,
		"POST /api/sync/reset":            HandleSyncReset,
		"POST /api/sync/test":             HandleSyncTest,
		"GET /api/sync/recovery":          HandleSyncRecoveryList,
		"POST /api/sync/recovery/restore": HandleSyncRecoveryRestore,
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
	HandleSyncRecoveryList(rec, httptest.NewRequest(http.MethodGet, "/api/sync/recovery", nil))
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
	HandleSyncRecoveryList(rec, httptest.NewRequest(http.MethodGet, "/api/sync/recovery", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("corrupt index recovery list status = %d, want 500 (body %s)", rec.Code, rec.Body.String())
	}
}

// TestSyncApiEnableRequiresCapabilityProbe: a provider whose capability probe
// fails is refused at enable, so a service that ignores conditional writes is
// never admitted into real sync.
func TestSyncApiEnableRequiresCapabilityProbe(t *testing.T) {
	dir, state := t.TempDir(), t.TempDir()
	failing := &profileStore{RemoteStore: cloudsync.NewMemoryStore(), profile: "failing-probe", probeOK: false}
	setSyncEnv(t, dir, state, func() (cloudsync.RemoteStore, error) { return failing, nil })

	en := decodeSync[map[string]any](t, doJSON(t, "POST", "/api/sync/enable", nil))
	if en["enabled"] == true {
		t.Fatalf("enable = %+v, want the probe to refuse", en)
	}
	if en["error"] == nil {
		t.Fatal("enable accepted a provider without conditional writes")
	}
	// The vault must not be marked connected.
	if connected := syncConnected(); connected {
		t.Fatal("vault marked connected despite the failed probe")
	}
}

// TestSyncApiRunRefusesChangedProvider: a provider swapped after enable is
// refused at run time without updating the connection record — switching
// targets is an explicit reset/reconnect, never something a run decides.
func TestSyncApiRunRefusesChangedProvider(t *testing.T) {
	dir, state := t.TempDir(), t.TempDir()
	storeA := &profileStore{RemoteStore: cloudsync.NewMemoryStore(), profile: "profile-a", probeOK: true}
	storeB := &profileStore{RemoteStore: cloudsync.NewMemoryStore(), profile: "profile-b", probeOK: true}
	cur := storeA
	setSyncEnv(t, dir, state, func() (cloudsync.RemoteStore, error) { return cur, nil })

	en := decodeSync[map[string]any](t, doJSON(t, "POST", "/api/sync/enable", nil))
	if en["enabled"] != true {
		t.Fatalf("enable = %+v", en)
	}

	// Swap the provider: the run must refuse the mismatch.
	cur = storeB
	run := decodeSync[map[string]any](t, doJSON(t, "POST", "/api/sync/run", nil))
	if run["LastError"] == nil {
		t.Fatalf("run = %+v, want a refusal for the changed provider", run)
	}
	// The connection record must be unchanged (still pinned to profile-a).
	rec, err := syncReadConnected()
	if err != nil || rec == nil || rec.Profile != "profile-a" || rec.RepoID == "" {
		t.Fatalf("record after refused run = %+v, %v; want the enable-time identity preserved", rec, err)
	}
}

// TestSyncApiDisablePreservesIdentityThenReconnect: disable keeps the verified
// profile and pinned repository in the connection record, so re-enabling with
// the same provider reconnects cleanly and the first run after it is not
// mistaken for a fresh setup.
func TestSyncApiDisablePreservesIdentityThenReconnect(t *testing.T) {
	dir, state := t.TempDir(), t.TempDir()
	setSyncEnv(t, dir, state, nil)
	if err := os.WriteFile(filepath.Join(dir, "idea.md"), []byte("# Idea\n"), 0644); err != nil {
		t.Fatal(err)
	}
	doJSON(t, "POST", "/api/sync/enable", nil)
	if run := decodeSync[syncRunJSON](t, doJSON(t, "POST", "/api/sync/run", nil)); !run.Synced {
		t.Fatalf("initial run = %+v", run)
	}

	before, err := syncReadConnected()
	if err != nil || before == nil || !before.Connected {
		t.Fatalf("record after enable = %+v, %v", before, err)
	}

	doJSON(t, "POST", "/api/sync/disable", nil)
	after, err := syncReadConnected()
	if err != nil || after == nil {
		t.Fatalf("record after disable = %+v, %v; identity must be preserved", after, err)
	}
	if after.Connected {
		t.Fatal("disable left the record connected")
	}
	if after.Profile != before.Profile || after.RepoID != before.RepoID {
		t.Fatalf("disable dropped identity: before %+v, after %+v", before, after)
	}

	// Re-enable with the same provider reconnects to the same repository.
	doJSON(t, "POST", "/api/sync/enable", nil)
	if run := decodeSync[syncRunJSON](t, doJSON(t, "POST", "/api/sync/run", nil)); !run.Synced {
		t.Fatalf("reconnect run = %+v", run)
	}
}

// TestSyncApiResetAllowsFreshSetup: the explicit reset clears the snapshot and
// the connection identity, so the next enable is a fresh setup against a
// different repository instead of a permanent mismatch stop.
func TestSyncApiResetAllowsFreshSetup(t *testing.T) {
	dir, state := t.TempDir(), t.TempDir()
	setSyncEnv(t, dir, state, nil)
	if err := os.WriteFile(filepath.Join(dir, "idea.md"), []byte("# Idea\n"), 0644); err != nil {
		t.Fatal(err)
	}
	doJSON(t, "POST", "/api/sync/enable", nil)
	if run := decodeSync[syncRunJSON](t, doJSON(t, "POST", "/api/sync/run", nil)); !run.Synced {
		t.Fatalf("initial run = %+v", run)
	}

	// Reset discards the snapshot and identity pin.
	rs := decodeSync[map[string]any](t, doJSON(t, "POST", "/api/sync/reset", nil))
	if rs["reset"] != true {
		t.Fatalf("reset = %+v", rs)
	}
	rec, err := syncReadConnected()
	if err != nil || rec != nil {
		t.Fatalf("record after reset = %+v, %v; want cleared", rec, err)
	}

	// A fresh enable re-establishes against the shared remote and runs cleanly.
	doJSON(t, "POST", "/api/sync/enable", nil)
	if run := decodeSync[syncRunJSON](t, doJSON(t, "POST", "/api/sync/run", nil)); !run.Synced {
		t.Fatalf("run after reset+enable = %+v", run)
	}
}

// TestSyncApiEnableRefusesChangedProvider: enable must also respect the pinned
// provider profile — even when another remote carries a byte-identical
// repo.json, switching providers is refused without touching the record.
func TestSyncApiEnableRefusesChangedProvider(t *testing.T) {
	dir, state := t.TempDir(), t.TempDir()
	memA, memB := cloudsync.NewMemoryStore(), cloudsync.NewMemoryStore()
	storeA := &profileStore{RemoteStore: memA, profile: "profile-a", probeOK: true}
	storeB := &profileStore{RemoteStore: memB, profile: "profile-b", probeOK: true}
	cur := storeA
	setSyncEnv(t, dir, state, func() (cloudsync.RemoteStore, error) { return cur, nil })

	doJSON(t, "POST", "/api/sync/enable", nil)
	before, err := syncReadConnected()
	if err != nil || before == nil || !before.Connected {
		t.Fatalf("record after enable = %+v, %v", before, err)
	}
	doJSON(t, "POST", "/api/sync/disable", nil)

	// Provider B carries a copy of the SAME repo.json but is a different remote.
	data, _, rerr := memA.Read(context.Background(), "repo.json")
	if rerr != nil {
		t.Fatal(rerr)
	}
	if err := memB.Seed("repo.json", data, "1"); err != nil {
		t.Fatal(err)
	}
	cur = storeB

	en := decodeSync[map[string]any](t, doJSON(t, "POST", "/api/sync/enable", nil))
	if en["enabled"] == true {
		t.Fatalf("enable on a changed provider = %+v, want refusal", en)
	}
	after, err := syncReadConnected()
	if err != nil || after == nil {
		t.Fatalf("record after refused enable = %+v, %v", after, err)
	}
	if after.Connected {
		t.Fatal("refused enable still connected the vault")
	}
	if after.Profile != before.Profile || after.RepoID != before.RepoID {
		t.Fatalf("refused enable mutated the record: before %+v, after %+v", before, after)
	}
}

// TestSyncApiResetBlockedByLock: the reset flow holds the replica OS lock, so
// it is refused while a cycle in another process (or another handle) holds it —
// a snapshot delete can never race an in-flight commit.
func TestSyncApiResetBlockedByLock(t *testing.T) {
	dir, state := t.TempDir(), t.TempDir()
	setSyncEnv(t, dir, state, nil)
	doJSON(t, "POST", "/api/sync/enable", nil)
	vaultID, replicaID, stateRoot, err := syncIdentity()
	if err != nil {
		t.Fatal(err)
	}
	lock, err := syncstate.AcquireReplicaLock(stateRoot, vaultID, replicaID)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()

	rs := decodeSync[map[string]any](t, doJSON(t, "POST", "/api/sync/reset", nil))
	if rs["reset"] == true {
		t.Fatalf("reset while a cycle holds the lock = %+v, want refusal", rs)
	}
	if rs["error"] == nil {
		t.Fatal("reset must report the lock contention")
	}
}

// TestSyncApiRunBlockedByLock: a run's connection validation and cycle now share
// one replica-lock critical section, so a run is refused while another process
// holds the lock — a stale run can never sync past a disable/reset.
func TestSyncApiRunBlockedByLock(t *testing.T) {
	dir, state := t.TempDir(), t.TempDir()
	setSyncEnv(t, dir, state, nil)
	if err := os.WriteFile(filepath.Join(dir, "idea.md"), []byte("# Idea\n"), 0644); err != nil {
		t.Fatal(err)
	}
	doJSON(t, "POST", "/api/sync/enable", nil)
	vaultID, replicaID, stateRoot, err := syncIdentity()
	if err != nil {
		t.Fatal(err)
	}
	lock, err := syncstate.AcquireReplicaLock(stateRoot, vaultID, replicaID)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()

	run := decodeSync[map[string]any](t, doJSON(t, "POST", "/api/sync/run", nil))
	if run["LastError"] == nil && run["error"] == nil {
		t.Fatalf("run while another process holds the lock = %+v, want refusal", run)
	}
}

// TestSyncStatusSurfacesCorruptConnectionRecord: a corrupt connection record is
// exposed as connectionError in the status instead of silently reporting
// "disabled", and the record still counts as present so the reset affordance is
// available.
func TestSyncStatusSurfacesCorruptConnectionRecord(t *testing.T) {
	dir, state := t.TempDir(), t.TempDir()
	setSyncEnv(t, dir, state, nil)
	if err := os.WriteFile(filepath.Join(dir, "idea.md"), []byte("# Idea\n"), 0644); err != nil {
		t.Fatal(err)
	}
	doJSON(t, "POST", "/api/sync/enable", nil)
	vaultID, replicaID, stateRoot, err := syncIdentity()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(syncConnectedPath(stateRoot, vaultID, replicaID), []byte(`{bad json`), 0600); err != nil {
		t.Fatal(err)
	}
	st := decodeSync[map[string]any](t, doJSON(t, "GET", "/api/sync/status", nil))
	if st["connectionError"] == nil {
		t.Fatalf("status = %+v, want connectionError surfaced", st)
	}
	if st["connection"] != true {
		t.Fatalf("status = %+v, want connection=true so reset is available", st)
	}
}

// profileStore wraps a store with an explicit secret-free provider profile and
// an optional probe failure, so tests can swap providers and observe re-probing.
type profileStore struct {
	cloudsync.RemoteStore
	profile string
	probeOK bool
}

func (p *profileStore) Profile() string { return p.profile }

func (p *profileStore) Test(ctx context.Context) (cloudsync.Capabilities, error) {
	if !p.probeOK {
		return cloudsync.Capabilities{}, &cloudsync.StoreError{Kind: cloudsync.ErrUnsupportedCapability, Message: "ignores preconditions"}
	}
	return p.RemoteStore.Test(ctx)
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
	oldDataDir, oldSyncRoot, oldProvider := appstate.DataDir, appstate.SyncRoot, syncProvider
	appstate.DataDir, appstate.SyncRoot = dirB, state
	t.Cleanup(func() { appstate.DataDir, appstate.SyncRoot, syncProvider = oldDataDir, oldSyncRoot, oldProvider })
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

	// Enable first: the explicit setup flow establishes repo.json.
	doJSON(t, "POST", "/api/sync/enable", nil)

	// Then seed a remote note whose read will fault, targeting the cycle's read.
	rec := &cloudsync.NoteRecord{
		SchemaVersion: cloudsync.NoteSchemaVersion, SyncID: "11111111-1111-4111-8111-111111111111",
		Path: "a.md", Markdown: "# A\n",
	}
	data, _ := rec.Serialize()
	if err := shared.Seed(cloudsync.NoteKey(rec.SyncID), data, "1"); err != nil {
		t.Fatal(err)
	}
	shared.ArmFault("read", &cloudsync.StoreError{Kind: cloudsync.ErrPermission, Message: "denied"})

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
	// The failure must be recorded so a status refresh never shows a stale run.
	st := decodeSync[map[string]any](t, doJSON(t, "GET", "/api/sync/status", nil))
	if st["lastRun"] == nil {
		t.Fatal("fatal run failure not recorded in syncLastRun")
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
	rec, err := syncstate.NewRecoveryStore(appstate.SyncRoot, vaultID, replicaID)
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

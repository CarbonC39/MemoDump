package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"memodump/internal/syncindex"
	"memodump/internal/syncstate"
	"memodump/internal/vaultfs"
)

// handleSyncStatus reports the current sync state: whether the vault is
// enabled, the last (redacted) run, and the recovery copies. The no-E2EE
// warning is always surfaced for the experimental phase.
func handleSyncStatus(w http.ResponseWriter, r *http.Request) {
	enabled := syncVaultID() != ""
	resp := map[string]any{
		"enabled":       enabled,
		"connected":     syncConnected(),
		"experimental":  true,
		"noE2EE":        true,
		"lastRun":       syncLastRun.Result,
		"lastCompleted": syncLastRun.Completed,
		"recovery":      []any{},
	}
	if enabled {
		if recs, err := listRecovery(); err == nil {
			resp["recovery"] = recs
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleSyncEnable enables sync for the vault: it creates the note-only index
// (assigning a stable Vault ID and Sync IDs) and resolves the replica identity.
// It never modifies existing Markdown.
func handleSyncEnable(w http.ResponseWriter, r *http.Request) {
	store, err := syncindex.EnableNoteStore(dataDir)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	vaultID := store.Index.VaultID
	_, replicaID, err := syncstate.Resolve(syncRoot, dataDir, vaultID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled": true, "vaultId": vaultID, "replicaId": string(replicaID),
		"experimental": true, "noE2EE": true,
	})
}

// handleSyncRun runs one manual note cycle and records the redacted outcome.
func handleSyncRun(w http.ResponseWriter, r *http.Request) {
	svc, err := buildSyncService()
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	res, err := svc.Run(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "sync run failed")
		return
	}
	syncLastRun.Result = *res
	syncLastRun.Completed = time.Now()
	writeJSON(w, http.StatusOK, res)
}

// handleSyncDisable disconnects sync for the replica: it removes the disposable
// snapshot and the recovery area. It never deletes local notes or remote
// records; re-enabling re-uses the existing identities.
func handleSyncDisable(w http.ResponseWriter, r *http.Request) {
	vaultID, replicaID, err := syncIdentity()
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	root, err := syncStateRoot()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	dir := syncstate.StateDir(root, vaultID, replicaID)
	_ = os.Remove(filepath.Join(dir, syncstate.SnapshotName))
	_ = os.RemoveAll(filepath.Join(dir, syncstate.RecoveryDirName))
	writeJSON(w, http.StatusOK, map[string]any{"enabled": false, "disconnected": true})
}

// handleSyncTest probes the configured provider's capabilities.
func handleSyncTest(w http.ResponseWriter, r *http.Request) {
	remote, err := syncProvider()
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	caps, err := remote.Test(r.Context())
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "conditionalWrites": caps.ConditionalWrites, "pagedListing": caps.PagedListing,
	})
}

// handleSyncRecoveryList returns every recoverable-delete copy.
func handleSyncRecoveryList(w http.ResponseWriter, r *http.Request) {
	recs, err := listRecovery()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"recovery": recs})
}

// handleSyncRecoveryRestore writes a recovered copy back to the vault at the
// note's indexed path.
func handleSyncRecoveryRestore(w http.ResponseWriter, r *http.Request) {
	var body struct {
		SyncID    string `json:"syncId"`
		StateHash string `json:"stateHash"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	store, err := recoveryStore()
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	markdown, ok, err := store.Read(body.SyncID, body.StateHash)
	if err != nil || !ok {
		writeErr(w, http.StatusNotFound, "no such recovery copy")
		return
	}
	idx, err := syncindex.LoadNoteStore(dataDir)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	path, ok := idx.PathByID(body.SyncID)
	if !ok {
		writeErr(w, http.StatusNotFound, "no indexed path for the recovery copy")
		return
	}
	repo, err := vaultfs.New(dataDir)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if _, err := repo.Apply(path, markdown, ""); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "path": path})
}

// listRecovery returns the recovery copies as JSON-friendly records.
func listRecovery() ([]map[string]any, error) {
	store, err := recoveryStore()
	if err != nil {
		return nil, err
	}
	copies, err := store.ListAll()
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(copies))
	for _, c := range copies {
		out = append(out, map[string]any{
			"syncId": c.SyncID, "stateHash": c.StateHash,
			"size": len(c.Markdown), "markdown": c.Markdown,
		})
	}
	return out, nil
}

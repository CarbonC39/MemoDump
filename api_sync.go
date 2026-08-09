package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"time"

	"memodump/internal/syncindex"
	"memodump/internal/syncservice"
	"memodump/internal/vaultfs"
)

// syncserviceResult is a read alias so the status handler can expose the
// redacted run result without importing the service package.
type syncserviceResult = syncservice.Result

// handleSyncStatus reports the current sync state. "enabled" and "connected"
// both reflect the persistent connect marker, so a disabled vault is never
// shown as enabled. The last (redacted) run and its completion time are omitted
// until a run has happened, and only the recovery copy COUNT is returned —
// detailed content is served by the recovery endpoint.
func handleSyncStatus(w http.ResponseWriter, r *http.Request) {
	connected := syncConnected()
	resp := map[string]any{
		"enabled":       connected,
		"connected":     connected,
		"experimental":  true,
		"noE2EE":        true,
		"recoveryCount": 0,
	}
	lastRun, lastCompleted := lastRunSnapshot()
	if !lastCompleted.IsZero() {
		resp["lastRun"] = lastRun
		resp["lastCompleted"] = lastCompleted
	}
	if connected {
		if n, err := countRecovery(); err == nil {
			resp["recoveryCount"] = n
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// lastRunSnapshot returns one consistent (result, completed) snapshot under a
// single read lock, so a status never combines fields from different runs.
func lastRunSnapshot() (syncserviceResult, time.Time) {
	syncLastRunMu.RLock()
	defer syncLastRunMu.RUnlock()
	return syncLastRun.Result, syncLastRun.Completed
}

// handleSyncEnable enables sync for the vault: it creates the note-only index
// (assigning a stable Vault ID and Sync IDs), resolves the replica identity,
// and records the connect marker. It never modifies existing Markdown.
func handleSyncEnable(w http.ResponseWriter, r *http.Request) {
	syncOpMu.Lock()
	defer syncOpMu.Unlock()
	store, err := syncindex.EnableNoteStore(dataDir)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	vaultID := store.Index.VaultID
	_, replicaID, stateRoot, err := syncIdentity()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := syncSetConnected(stateRoot, vaultID, replicaID, true); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled": true, "vaultId": vaultID, "replicaId": replicaID,
		"experimental": true, "noE2EE": true,
	})
}

// handleSyncRun runs one manual note cycle and records the redacted outcome. It
// refuses to run while sync is disabled.
func handleSyncRun(w http.ResponseWriter, r *http.Request) {
	syncOpMu.Lock()
	defer syncOpMu.Unlock()
	if !syncConnected() {
		writeErr(w, http.StatusBadRequest, "sync is disabled; enable it first")
		return
	}
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
	syncLastRunMu.Lock()
	syncLastRun.Result = *res
	syncLastRun.Completed = time.Now()
	syncLastRunMu.Unlock()
	writeJSON(w, http.StatusOK, res)
}

// handleSyncDisable disconnects sync for the replica: it clears only the
// connect marker. It never deletes local notes, remote records, the identity
// index, the snapshot, or recovery copies; re-enabling re-uses the existing
// identities.
func handleSyncDisable(w http.ResponseWriter, r *http.Request) {
	syncOpMu.Lock()
	defer syncOpMu.Unlock()
	vaultID, replicaID, stateRoot, err := syncIdentity()
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := syncSetConnected(stateRoot, vaultID, replicaID, false); err != nil && !os.IsNotExist(err) {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
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

// handleSyncRecoveryList returns every recoverable-delete copy, including the
// original note path recorded when the copy was made. A vault that has not
// enabled sync returns an empty list; real I/O and state errors are reported.
func handleSyncRecoveryList(w http.ResponseWriter, r *http.Request) {
	recs, err := listRecovery()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"recovery": recs})
}

// handleSyncRecoveryRestore writes a recovered copy back to the vault. The
// restore target is the original path recorded with the copy (safe-guarded
// against traversal), so it works even after the index mapping was cleaned up.
// It is serialized with the other lifecycle operations.
func handleSyncRecoveryRestore(w http.ResponseWriter, r *http.Request) {
	syncOpMu.Lock()
	defer syncOpMu.Unlock()
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
	markdown, path, ok, err := store.Read(body.SyncID, body.StateHash)
	if err != nil || !ok {
		writeErr(w, http.StatusNotFound, "no such recovery copy")
		return
	}
	if path == "" {
		// A copy predating path recording falls back to the indexed mapping.
		idx, lerr := syncindex.LoadNoteStore(dataDir)
		if lerr != nil {
			writeErr(w, http.StatusInternalServerError, lerr.Error())
			return
		}
		path, ok = idx.PathByID(body.SyncID)
		if !ok {
			writeErr(w, http.StatusNotFound, "no indexed path for the recovery copy")
			return
		}
	}
	if _, err := vaultfs.SafePath(dataDir, path); err != nil {
		writeErr(w, http.StatusBadRequest, "unsafe recovery path")
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

// listRecovery returns the recovery copies as JSON-friendly records, including
// the original note path. A vault that has never enabled sync yields an empty
// list; a corrupt index or any real state/I/O error is reported.
func listRecovery() ([]map[string]any, error) {
	store, err := recoveryStore()
	if err != nil {
		if errors.Is(err, syncindex.ErrNotEnabled) {
			return []map[string]any{}, nil // never enabled: no copies
		}
		return nil, err
	}
	copies, err := store.ListAll()
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(copies))
	for _, c := range copies {
		out = append(out, map[string]any{
			"syncId": c.SyncID, "stateHash": c.StateHash, "path": c.Path,
			"size": len(c.Markdown), "markdown": c.Markdown,
		})
	}
	return out, nil
}

// countRecovery returns only the number of recovery copies (no content). A
// vault that has never enabled sync counts 0; a corrupt index or any real
// state/I/O error is reported.
func countRecovery() (int, error) {
	store, err := recoveryStore()
	if err != nil {
		if errors.Is(err, syncindex.ErrNotEnabled) {
			return 0, nil
		}
		return 0, err
	}
	copies, err := store.ListAll()
	if err != nil {
		return 0, err
	}
	return len(copies), nil
}

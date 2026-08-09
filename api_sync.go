package main

import (
	"encoding/json"
	"errors"
	"net/http"
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

// handleSyncEnable enables sync for the vault through the EXPLICIT setup flow:
// it creates the note-only index (assigning a stable Vault ID and Sync IDs),
// resolves the replica identity, verifies the provider's capabilities (a
// service that ignores conditional writes is refused), establishes or re-adopts
// the remote repository — the only place repo.json is ever created — and records
// the connection with the verified provider profile and pinned repository ID.
// It never modifies existing Markdown.
func handleSyncEnable(w http.ResponseWriter, r *http.Request) {
	syncOpMu.Lock()
	defer syncOpMu.Unlock()
	if _, err := syncindex.EnableNoteStore(dataDir); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	vaultID, replicaID, _, err := syncIdentity()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	prev, err := syncReadConnected()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	remote, err := syncProvider()
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	caps, err := remote.Test(r.Context())
	if err != nil || !caps.ConditionalWrites || !caps.PagedListing {
		writeErr(w, http.StatusBadRequest, "sync provider probe failed; sync not enabled")
		return
	}
	repoID, profile, err := syncRepoSetup(r.Context(), remote, prev)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := syncWriteConnected(&syncConnectionRecord{Connected: true, Profile: profile, RepoID: repoID}); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled": true, "vaultId": vaultID, "replicaId": replicaID, "repoId": repoID,
		"experimental": true, "noE2EE": true,
	})
}

// handleSyncRun runs one manual note cycle and records the redacted outcome.
// It refuses to run while the connection record is absent or disconnected, and
// re-verifies the provider whenever its profile differs from the one recorded
// at enable (a changed provider is probed again before running). repo.json is
// only ever read here — never created. Every outcome, including build and run
// failures, is written to syncLastRun so a refresh never surfaces a stale one.
func handleSyncRun(w http.ResponseWriter, r *http.Request) {
	syncOpMu.Lock()
	defer syncOpMu.Unlock()
	rec, err := syncReadConnected()
	if err != nil {
		recordLastRunError(err)
		writeJSON(w, http.StatusOK, &syncservice.Result{Synced: false, LastError: syncservice.ClassifyError(err)})
		return
	}
	if rec == nil || !rec.Connected {
		recErr := errors.New("sync is disabled; enable it first")
		recordLastRunError(recErr)
		writeErr(w, http.StatusBadRequest, recErr.Error())
		return
	}
	remote, err := syncProvider()
	if err != nil {
		recordLastRunError(err)
		writeJSON(w, http.StatusOK, &syncservice.Result{Synced: false, LastError: syncservice.ClassifyError(err)})
		return
	}
	profile := providerProfile(remote)
	if rec.Profile != profile {
		// The provider changed since enable: re-verify before running.
		caps, perr := remote.Test(r.Context())
		if perr != nil || !caps.ConditionalWrites || !caps.PagedListing {
			recErr := errors.New("sync provider changed and failed its probe; disable and re-enable sync")
			recordLastRunError(recErr)
			writeJSON(w, http.StatusOK, &syncservice.Result{Synced: false, LastError: syncservice.ClassifyError(recErr)})
			return
		}
		rec.Profile = profile
		if werr := syncWriteConnected(rec); werr != nil {
			recordLastRunError(werr)
			writeJSON(w, http.StatusOK, &syncservice.Result{Synced: false, LastError: syncservice.ClassifyError(werr)})
			return
		}
	}
	repoID, _, err := syncRepoIdentity(r.Context(), remote)
	if err != nil {
		recordLastRunError(err)
		writeJSON(w, http.StatusOK, &syncservice.Result{Synced: false, LastError: syncservice.ClassifyError(err)})
		return
	}
	if rec.RepoID != "" && repoID != rec.RepoID {
		recErr := errors.New("remote repository changed since enable; disable and re-enable sync")
		recordLastRunError(recErr)
		writeJSON(w, http.StatusOK, &syncservice.Result{Synced: false, LastError: syncservice.ClassifyError(recErr)})
		return
	}
	if rec.RepoID == "" {
		// A legacy marker without a pinned repository adopts the verified one.
		rec.RepoID = repoID
		if werr := syncWriteConnected(rec); werr != nil {
			recordLastRunError(werr)
			writeJSON(w, http.StatusOK, &syncservice.Result{Synced: false, LastError: syncservice.ClassifyError(werr)})
			return
		}
	}
	svc, err := buildSyncService(r.Context(), repoID, profile, remote)
	if err != nil {
		recordLastRunError(err)
		writeJSON(w, http.StatusOK, &syncservice.Result{Synced: false, LastError: syncservice.ClassifyError(err)})
		return
	}
	res, err := svc.Run(r.Context())
	if err != nil {
		recordLastRunError(err)
		writeJSON(w, http.StatusOK, &syncservice.Result{Synced: false, LastError: syncservice.ClassifyError(err)})
		return
	}
	syncLastRunMu.Lock()
	syncLastRun.Result = *res
	syncLastRun.Completed = time.Now()
	syncLastRunMu.Unlock()
	writeJSON(w, http.StatusOK, res)
}

// recordLastRunError records a failed outcome in syncLastRun so the status
// endpoint never surfaces a stale earlier result.
func recordLastRunError(err error) {
	syncLastRunMu.Lock()
	syncLastRun.Result = syncservice.Result{Synced: false, LastError: syncservice.ClassifyError(err)}
	syncLastRun.Completed = time.Now()
	syncLastRunMu.Unlock()
}

// clearLastRun clears the recorded run outcome (e.g. on disable).
func clearLastRun() {
	syncLastRunMu.Lock()
	syncLastRun.Result = syncservice.Result{}
	syncLastRun.Completed = time.Time{}
	syncLastRunMu.Unlock()
}

// handleSyncDisable disconnects sync for the replica: it clears only the
// connection record. It never deletes local notes, remote records, the identity
// index, the snapshot, or recovery copies; re-enabling re-uses the existing
// identities.
func handleSyncDisable(w http.ResponseWriter, r *http.Request) {
	syncOpMu.Lock()
	defer syncOpMu.Unlock()
	if err := syncWriteConnected(nil); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	clearLastRun()
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

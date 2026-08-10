package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"

	"memodump/internal/syncindex"
	"memodump/internal/syncservice"
	"memodump/internal/syncstate"
	"memodump/internal/vaultfs"
)

// syncserviceResult is a read alias so the status handler can expose the
// redacted run result without importing the service package.
type syncserviceResult = syncservice.Result

// handleSyncStatus reports the current sync state. "enabled" and "connected"
// both reflect the persistent connection record, so a disabled vault is never
// shown as enabled. A corrupt or unreadable connection record is surfaced as
// connectionError rather than silently reported "disabled". The last (redacted)
// run and its completion time are omitted until a run has happened, and only
// the recovery copy COUNT is returned — detailed content is served by the
// recovery endpoint.
func handleSyncStatus(w http.ResponseWriter, r *http.Request) {
	rec, cerr := syncReadConnected()
	connected := rec != nil && rec.Connected
	resp := map[string]any{
		"enabled":       connected,
		"connected":     connected,
		"connection":    syncConnectionExists(),
		"experimental":  true,
		"noE2EE":        true,
		"recoveryCount": 0,
	}
	if cerr != nil {
		resp["connectionError"] = cerr.Error()
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

// handleSyncEnable enables sync for the vault through the EXPLICIT setup flow.
// The empty index identity (a fresh Vault ID, no note mappings) is created
// outside the replica lock — it never scans or assigns Sync IDs — so two
// first-time enables cannot race each other into mutating an existing index
// outside the lock. The scan and Sync ID assignment then ALWAYS run inside the
// replica lock (EnableNoteStore), so a concurrent cycle in another process can
// never assign different identities to the same new note. The enable then
// verifies the provider's capabilities, establishes or re-adopts the remote
// repository — the only place repo.json is ever created — and records the
// connection with the verified provider profile and pinned repository ID. It
// never modifies existing Markdown.
func handleSyncEnable(w http.ResponseWriter, r *http.Request) {
	syncOpMu.Lock()
	defer syncOpMu.Unlock()
	if _, cerr := syncindex.CreateNoteStore(dataDir, uuid.NewString()); cerr != nil && !errors.Is(cerr, syncindex.ErrAlreadyEnabled) {
		writeErr(w, http.StatusBadRequest, cerr.Error())
		return
	}
	var vaultID, replicaID, repoID string
	err := withSyncLifecycleLock(func(v, rep, stateRoot string, lock *syncstate.Lock) error {
		vaultID, replicaID = v, rep
		// Scan and assign stable Sync IDs to every note under the replica lock.
		if _, err := syncindex.EnableNoteStore(dataDir); err != nil {
			return err
		}
		prev, err := syncReadConnected()
		if err != nil {
			return err
		}
		remote, err := syncProvider()
		if err != nil {
			return err
		}
		caps, err := remote.Test(r.Context())
		if err != nil || !caps.ConditionalWrites || !caps.PagedListing {
			return errors.New("sync provider probe failed; sync not enabled")
		}
		repoID, profile, err := syncRepoSetup(r.Context(), remote, prev)
		if err != nil {
			return err
		}
		return syncWriteConnected(&syncConnectionRecord{Connected: true, Profile: profile, RepoID: repoID})
	})
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled": true, "vaultId": vaultID, "replicaId": replicaID, "repoId": repoID,
		"experimental": true, "noE2EE": true,
	})
}

// errSyncDisabled marks a run refusal because sync is disabled; the handler
// surfaces it as a 400 rather than a redacted run result.
var errSyncDisabled = errors.New("sync is disabled; enable it first")

// handleSyncRun runs one manual note cycle and records the redacted outcome.
// The connection validation (record, provider profile, repository ID) happens
// INSIDE the replica OS lock critical section that the cycle itself runs under,
// so a disable/reset in another process can never leave a stale run syncing or
// re-writing a reset snapshot. Every outcome, including build and run failures,
// is written to syncLastRun so a refresh never surfaces a stale one.
func handleSyncRun(w http.ResponseWriter, r *http.Request) {
	syncOpMu.Lock()
	defer syncOpMu.Unlock()
	var res *syncservice.Result
	err := withSyncLifecycleLock(func(vaultID, replicaID, stateRoot string, lock *syncstate.Lock) error {
		rec, err := syncReadConnected()
		if err != nil {
			return err
		}
		if rec == nil || !rec.Connected {
			return errSyncDisabled
		}
		remote, err := syncProvider()
		if err != nil {
			return err
		}
		profile := providerProfile(remote)
		if rec.Profile == "" || rec.Profile != profile {
			return errors.New("sync provider changed since enable; disable and reset sync to reconnect")
		}
		repoID, _, err := syncRepoIdentity(r.Context(), remote)
		if err != nil {
			return err
		}
		if rec.RepoID == "" || repoID != rec.RepoID {
			return errors.New("remote repository changed since enable; disable and reset sync to reconnect")
		}
		svc, err := buildSyncService(r.Context(), repoID, profile, remote, lock)
		if err != nil {
			return err
		}
		res, err = svc.Run(r.Context())
		return err
	})
	if err != nil {
		recordLastRunError(err)
		if errors.Is(err, errSyncDisabled) || errors.Is(err, syncindex.ErrNotEnabled) {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
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

// handleSyncDisable disconnects sync for the replica: it flips only the
// Connected flag in the connection record, under the replica OS lock so it
// never interleaves with a cycle in another process. The verified provider
// profile and pinned repository ID are preserved, so re-enabling with the same
// provider reconnects cleanly and a normal run never sees an identity mismatch.
// It never deletes local notes, remote records, the identity index, the
// snapshot, or recovery copies. Switching repositories is the explicit reset
// flow.
func handleSyncDisable(w http.ResponseWriter, r *http.Request) {
	syncOpMu.Lock()
	defer syncOpMu.Unlock()
	err := withSyncLifecycleLock(func(v, rep, stateRoot string, lock *syncstate.Lock) error {
		rec, err := syncReadConnected()
		if err != nil {
			return err
		}
		if rec == nil {
			return nil
		}
		rec.Connected = false
		return syncWriteConnected(rec)
	})
	if err != nil {
		if errors.Is(err, syncindex.ErrNotEnabled) {
			clearLastRun()
			writeJSON(w, http.StatusOK, map[string]any{"enabled": false, "disconnected": true})
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	clearLastRun()
	writeJSON(w, http.StatusOK, map[string]any{"enabled": false, "disconnected": true})
}

// handleSyncReset is the explicit reconnect/reset flow, serialized with any
// running cycle by the replica OS lock: it discards the replica's disposable
// snapshot and clears the connection record (identity pin), so the next enable
// starts a fresh setup. This is the ONLY deliberate way to switch repositories
// or recreate a lost one. Local notes and recovery copies are preserved.
func handleSyncReset(w http.ResponseWriter, r *http.Request) {
	syncOpMu.Lock()
	defer syncOpMu.Unlock()
	err := withSyncLifecycleLock(func(v, rep, stateRoot string, lock *syncstate.Lock) error {
		return syncReplicaResetAt(v, rep, stateRoot)
	})
	if err != nil {
		if errors.Is(err, syncindex.ErrNotEnabled) {
			clearLastRun()
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "reset": true})
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	clearLastRun()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "reset": true})
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

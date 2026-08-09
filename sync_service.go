package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"memodump/internal/cloudsync"
	"memodump/internal/syncindex"
	"memodump/internal/syncservice"
	"memodump/internal/syncstate"
)

// Sync identity constants for the experimental memory-remote phase. A later
// provider phase derives the Repository ID and provider profile from the real
// remote repo.json.
const (
	memoryRepoID  = "99999999-9999-4999-8999-999999999999"
	memoryProfile = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
)

// syncProvider selects the remote store for a manual run. It is a package-level
// seam: tests inject a shared memory store so two replicas can converge, and a
// later provider phase replaces the default. The default is a process-local
// memory remote for the experimental phase.
var syncProvider syncservice.Provider

// syncMemoryRemote is the process-local demo remote used until a real provider
// exists. The server's own sync state is therefore not durable across restarts
// in this experimental phase.
var syncMemoryRemote = cloudsync.NewMemoryStore()

// syncOpMu serializes the state-mutating sync API operations (enable, run,
// disable) within the process. Each request builds its own Service, so the
// service's instance mutex cannot serialize lifecycle operations; this
// package-level lock does. The replica OS lock still serializes across
// processes.
var syncOpMu sync.Mutex

// syncLastRun is the most recent manual-run outcome (redacted) and when it
// completed, guarded by syncLastRunMu because Status reads it concurrently with
// Run writing it.
var (
	syncLastRunMu sync.RWMutex
	syncLastRun   struct {
		Result    syncservice.Result
		Completed time.Time
	}
)

func init() {
	syncProvider = func() (cloudsync.RemoteStore, error) { return syncMemoryRemote, nil }
}

// syncStateRoot resolves the empty default state root to the OS app-data
// location, matching the other syncstate helpers. All stores must receive the
// resolved path so the lock, index, snapshot, and recovery agree on one root.
func syncStateRoot() (string, error) {
	if syncRoot != "" {
		return syncRoot, nil
	}
	return syncstate.DefaultStateRoot()
}

// syncVaultID returns the vault ID from the enabled note-only index, or an
// error when sync is not enabled, the index is corrupt, or reading it fails.
// Callers must distinguish ErrNotEnabled (never enabled → benign) from corrupt
// and I/O errors (which must be reported, never treated as "no sync").
func syncVaultID() (string, error) {
	store, err := syncindex.LoadNoteStore(dataDir)
	if err != nil {
		return "", err
	}
	return store.Index.VaultID, nil
}

// syncIdentity returns the vault and replica IDs plus the resolved state root
// for the current data dir, resolving the replica through the device registry.
func syncIdentity() (vaultID, replicaID, stateRoot string, err error) {
	stateRoot, err = syncStateRoot()
	if err != nil {
		return "", "", "", err
	}
	vaultID, err = syncVaultID()
	if err != nil {
		return "", "", "", err
	}
	_, replica, err := syncstate.Resolve(stateRoot, dataDir, vaultID)
	if err != nil {
		return "", "", "", err
	}
	return vaultID, string(replica), stateRoot, nil
}

// syncConnectedPath is the persistent connect marker file in a replica's state
// directory. Its presence means the user has enabled the connection.
func syncConnectedPath(stateRoot, vaultID, replicaID string) string {
	return filepath.Join(syncstate.StateDir(stateRoot, vaultID, replicaID), "connected.json")
}

// syncSetConnected durably records whether sync is enabled (connected) for the
// replica. Disable clears only this marker — the index, identity, snapshot,
// and recovery copies are all preserved.
func syncSetConnected(stateRoot, vaultID, replicaID string, connected bool) error {
	path := syncConnectedPath(stateRoot, vaultID, replicaID)
	if connected {
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return err
		}
		data, _ := json.Marshal(map[string]bool{"connected": true})
		return os.WriteFile(path, data, 0600)
	}
	return os.Remove(path)
}

// syncConnected reports whether the user has enabled sync (the connect marker
// exists).
func syncConnected() bool {
	vaultID, replicaID, stateRoot, err := syncIdentity()
	if err != nil {
		return false
	}
	_, err = os.Stat(syncConnectedPath(stateRoot, vaultID, replicaID))
	return err == nil
}

// buildSyncService assembles the sync service for the current data dir, with
// the resolved state root passed to every store. The vault must be enabled.
func buildSyncService() (*syncservice.Service, error) {
	vaultID, replicaID, stateRoot, err := syncIdentity()
	if err != nil {
		return nil, err
	}
	return syncservice.New(syncservice.Config{
		RepoRoot: dataDir, StateRoot: stateRoot,
		VaultID: vaultID, ReplicaID: replicaID,
		RepoID: memoryRepoID, Profile: memoryProfile,
		Provider: syncProvider,
	}), nil
}

// recoveryStore builds the recovery store for the current replica.
func recoveryStore() (*syncstate.RecoveryStore, error) {
	vaultID, replicaID, stateRoot, err := syncIdentity()
	if err != nil {
		return nil, err
	}
	return syncstate.NewRecoveryStore(stateRoot, vaultID, replicaID)
}

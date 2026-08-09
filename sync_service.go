package main

import (
	"fmt"
	"os"
	"path/filepath"
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

// syncLastRun is the most recent manual-run outcome (redacted) and when it
// completed. Runs are serialized by the sync service itself.
var syncLastRun = struct {
	Result    syncservice.Result
	Completed time.Time
}{}

func init() {
	syncProvider = func() (cloudsync.RemoteStore, error) { return syncMemoryRemote, nil }
}

// syncStateRoot resolves the empty default state root to the OS app-data
// location, matching the other syncstate helpers.
func syncStateRoot() (string, error) {
	if syncRoot != "" {
		return syncRoot, nil
	}
	return syncstate.DefaultStateRoot()
}

// syncVaultID returns the vault ID from the enabled note-only index, or "" when
// sync is not enabled.
func syncVaultID() string {
	store, err := syncindex.LoadNoteStore(dataDir)
	if err != nil {
		return ""
	}
	return store.Index.VaultID
}

// syncIdentity returns the vault and replica IDs for the current data dir,
// resolving the replica through the device registry.
func syncIdentity() (vaultID, replicaID string, err error) {
	vaultID = syncVaultID()
	if vaultID == "" {
		return "", "", fmt.Errorf("sync is not enabled")
	}
	_, replica, err := syncstate.Resolve(syncRoot, dataDir, vaultID)
	if err != nil {
		return "", "", err
	}
	return vaultID, string(replica), nil
}

// syncConnected reports whether a completed sync connection exists for the
// replica (a disposable snapshot on disk). Disable removes it, so the vault
// stays enabled but disconnected.
func syncConnected() bool {
	vaultID, replicaID, err := syncIdentity()
	if err != nil {
		return false
	}
	root, err := syncStateRoot()
	if err != nil {
		return false
	}
	_, err = os.Stat(filepath.Join(syncstate.StateDir(root, vaultID, replicaID), syncstate.SnapshotName))
	return err == nil
}

// buildSyncService assembles the sync service for the current data dir. The
// vault must already be enabled; the caller holds no lock (the service
// acquires the replica lock for each run).
func buildSyncService() (*syncservice.Service, error) {
	vaultID, replicaID, err := syncIdentity()
	if err != nil {
		return nil, err
	}
	return syncservice.New(syncservice.Config{
		RepoRoot: dataDir, StateRoot: syncRoot,
		VaultID: vaultID, ReplicaID: replicaID,
		RepoID: memoryRepoID, Profile: memoryProfile,
		Provider: syncProvider,
	}), nil
}

// recoveryStore builds the recovery store for the current replica.
func recoveryStore() (*syncstate.RecoveryStore, error) {
	vaultID, replicaID, err := syncIdentity()
	if err != nil {
		return nil, err
	}
	return syncstate.NewRecoveryStore(syncRoot, vaultID, replicaID)
}

// Package syncstate holds one replica's durable device identity: the Device
// ID, the path-scoped Replica registry, and the OS-level replica lock. Device
// state lives outside the vault, under a configurable state root; nothing is
// created there until sync is enabled.
package syncstate

import (
	"os"
	"path/filepath"
)

// DefaultStateRoot is the OS application-data location for one installation's
// sync device state: <os.UserConfigDir>/memodump/sync. The Wails build uses it
// by default; the CLI/server can override it with --sync-root,
// MEMODUMP_SYNC_ROOT, or SYNC_ROOT= (the path a container must persist).
func DefaultStateRoot() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "memodump", "sync"), nil
}

// StateDir returns the replica state directory <stateRoot>/<vaultId>/<replicaId>.
// It creates nothing. vaultId and replicaId must be validated UUIDs before any
// path is built from them.
func StateDir(stateRoot, vaultID, replicaID string) string {
	return filepath.Join(stateRoot, vaultID, replicaID)
}

// LockPath is the OS lock file inside a replica's state directory.
func LockPath(stateRoot, vaultID, replicaID string) string {
	return filepath.Join(StateDir(stateRoot, vaultID, replicaID), "replica.lock")
}

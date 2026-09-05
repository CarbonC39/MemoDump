// Package syncstate holds one replica's durable device identity and the single
// disposable device snapshot: the Device ID, the path-scoped Replica registry,
// the OS-level replica lock, and the atomic state.json snapshot store. Device
// state lives outside the vault, under a configurable state root; nothing is
// created there until sync is enabled.
package syncstate

import (
	"os"
	"path/filepath"
)

// DefaultStateRoot is the OS application-data location for one installation's
// sync device state: <os.UserConfigDir>/memodump/sync. It is the default when
// the caller (package main) has not supplied an explicit state root, which the
// Wails build and tests can do. The CLI Web server never reaches it — it has no
// cloud-sync surface (R6.0).
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

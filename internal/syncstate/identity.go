package syncstate

import (
	"fmt"

	"github.com/google/uuid"

	"memodump/internal/cloudsync"
)

// DeviceID identifies one MemoDump installation/profile. It is stored outside
// the vault (device.json) and used only for attribution and conflict names.
type DeviceID string

// ReplicaID identifies one local checkout/copy of a vault on a device. Two
// paths containing copies of the same Vault ID must never share device state,
// cursors, or a WAL, so each path-scoped copy gets its own Replica ID.
type ReplicaID string

func newDeviceID() DeviceID   { return DeviceID(uuid.NewString()) }
func newReplicaID() ReplicaID { return ReplicaID(uuid.NewString()) }

// validateReplicaArgs rejects vault/replica IDs that are not version-4 UUIDs,
// so a malicious or corrupt value can never escape the state root via
// StateDir/LockPath.
func validateReplicaArgs(vaultID, replicaID string) error {
	if !cloudsync.IsUUIDv4(vaultID) {
		return fmt.Errorf("invalid vaultId %q", vaultID)
	}
	if !cloudsync.IsUUIDv4(replicaID) {
		return fmt.Errorf("invalid replicaId %q", replicaID)
	}
	return nil
}

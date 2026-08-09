package syncstate

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// SnapshotStoreV2 owns one replica's disposable note snapshot file. There is no
// backup, append, partial update, compactor, or background goroutine: Replace
// rewrites the whole file once and Load reads it whole. The store is bound to
// one Vault/Replica pair at construction and refuses to persist a snapshot
// declaring any other identity.
type SnapshotStoreV2 struct {
	dir       string
	vaultID   string
	replicaID string
	io        fsIO
}

// NewSnapshotStoreV2 returns a store for the replica state directory
// <stateRoot>/<vaultId>/<replicaId>. The IDs must be valid version-4 UUIDs so
// an untrusted value can never escape the state root through a path. It creates
// nothing until Replace.
func NewSnapshotStoreV2(stateRoot, vaultID, replicaID string) (*SnapshotStoreV2, error) {
	if err := validateReplicaArgs(vaultID, replicaID); err != nil {
		return nil, err
	}
	return &SnapshotStoreV2{
		dir:       StateDir(stateRoot, vaultID, replicaID),
		vaultID:   vaultID,
		replicaID: replicaID,
		io:        osFsIO{},
	}, nil
}

// Path is the snapshot file location.
func (s *SnapshotStoreV2) Path() string {
	return filepath.Join(s.dir, SnapshotName)
}

// Load reads and validates the note snapshot against the expected identity. It
// returns a usable snapshot with NoDiscard, or a non-nil discard reason for
// missing/corrupt/unsupported-prototype/mismatched state (never an error), or a
// real I/O error — such as a permission failure — which stops the cycle and is
// never reported as "missing snapshot". A schema-v1 prototype snapshot is
// DiscardUnsupportedPrototype, never a baseline and never ordinary corruption.
func (s *SnapshotStoreV2) Load(exp ExpectedIdentity) (*SnapshotV2, DiscardReason, error) {
	data, err := s.io.ReadFile(s.Path())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, DiscardMissing, nil
		}
		return nil, NoDiscard, fmt.Errorf("read snapshot: %w", err)
	}
	snap, err := ParseSnapshotV2(data)
	if err != nil {
		if errors.Is(err, ErrUnsupportedPrototype) {
			return nil, DiscardUnsupportedPrototype, nil
		}
		return nil, DiscardCorrupt, nil
	}
	if snap.VaultID != exp.VaultID || snap.ReplicaID != exp.ReplicaID {
		return nil, DiscardCorrupt, nil
	}
	if snap.ProviderProfile != exp.ProviderProfile {
		return nil, DiscardProfileMismatch, nil
	}
	if snap.RepositoryID != exp.RepositoryID {
		return nil, DiscardRepositoryMismatch, nil
	}
	return snap, NoDiscard, nil
}

// Replace atomically installs snap as the replica's snapshot: one unique temp
// file in the same directory, full write, file sync, atomic rename, and a
// directory sync where the platform supports it. A snapshot declaring a
// different Vault or Replica ID than this store was bound to is rejected. A
// failure before the rename leaves the prior snapshot loadable; a failure after
// the rename (for example the directory sync) may expose either the old or the
// new snapshot, but never a partially written file.
func (s *SnapshotStoreV2) Replace(snap *SnapshotV2) error {
	if snap.VaultID != s.vaultID || snap.ReplicaID != s.replicaID {
		return fmt.Errorf("replace snapshot: snapshot identity (%s/%s) does not match store (%s/%s)",
			snap.VaultID, snap.ReplicaID, s.vaultID, s.replicaID)
	}
	if err := snap.Validate(); err != nil {
		return fmt.Errorf("replace snapshot: %w", err)
	}
	data, err := snap.Serialize()
	if err != nil {
		return fmt.Errorf("replace snapshot: %w", err)
	}
	if err := os.MkdirAll(s.dir, 0755); err != nil {
		return fmt.Errorf("replace snapshot: %w", err)
	}
	tmp, err := s.io.CreateTemp(s.dir, ".state-*.tmp")
	if err != nil {
		return fmt.Errorf("replace snapshot: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = s.io.Remove(tmpPath) }()

	if err := s.io.WriteAll(tmp, data); err != nil {
		tmp.Close()
		return fmt.Errorf("replace snapshot: %w", err)
	}
	if err := s.io.Sync(tmp); err != nil {
		tmp.Close()
		return fmt.Errorf("replace snapshot: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("replace snapshot: %w", err)
	}
	if err := s.io.Rename(tmpPath, s.Path()); err != nil {
		return fmt.Errorf("replace snapshot: %w", err)
	}
	if err := s.io.SyncDir(s.dir); err != nil {
		return fmt.Errorf("replace snapshot: %w", err)
	}
	return nil
}

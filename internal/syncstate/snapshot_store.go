package syncstate

import (
	"fmt"
	"os"
	"path/filepath"
)

// ExpectedIdentity is the set of identities a snapshot must match to be
// usable for the current cycle: the Vault and Replica IDs plus the selected
// provider profile and the Repository ID discovered from the remote repo.json.
type ExpectedIdentity struct {
	VaultID         string
	ReplicaID       string
	ProviderProfile string
	RepositoryID    string
}

// DiscardReason explains why a snapshot cannot be used. The zero value means
// the snapshot is usable; every discard outcome is an explicit classification
// so the coordinator never guesses about durable state.
type DiscardReason int

const (
	// NoDiscard: the snapshot loaded and matches the expected identity.
	NoDiscard DiscardReason = iota
	// DiscardMissing: no state.json exists (never synced, or AppData lost).
	DiscardMissing
	// DiscardCorrupt: malformed JSON, an invalid field, an unknown schema, or
	// a wrong Vault/Replica ID. The coordinator ignores it and performs
	// conservative onboarding with a full remote listing.
	DiscardCorrupt
	// DiscardProfileMismatch: the provider fingerprint differs. This is a
	// reconnect/provider switch and requires explicit confirmation; it is
	// never silently reinterpreted as an empty repository.
	DiscardProfileMismatch
	// DiscardRepositoryMismatch: the Repository ID differs. Always stops.
	DiscardRepositoryMismatch
)

func (d DiscardReason) String() string {
	switch d {
	case NoDiscard:
		return "usable"
	case DiscardMissing:
		return "missing"
	case DiscardCorrupt:
		return "corrupt"
	case DiscardProfileMismatch:
		return "provider-profile-mismatch"
	case DiscardRepositoryMismatch:
		return "repository-id-mismatch"
	default:
		return "unknown"
	}
}

// SnapshotStore owns one replica's disposable snapshot file. There is no
// backup, append, partial update, compactor, or background goroutine: Replace
// rewrites the whole file once and Load reads it whole.
type SnapshotStore struct {
	dir string
	io  fsIO
}

// NewSnapshotStore returns a store for the replica state directory
// <stateRoot>/<vaultId>/<replicaId>. It creates nothing until Replace.
func NewSnapshotStore(stateRoot, vaultID, replicaID string) *SnapshotStore {
	return &SnapshotStore{dir: StateDir(stateRoot, vaultID, replicaID), io: osFsIO{}}
}

// Path is the snapshot file location.
func (s *SnapshotStore) Path() string {
	return filepath.Join(s.dir, SnapshotName)
}

// Load reads and validates the snapshot against the expected identity. It
// returns a usable snapshot with NoDiscard, or a non-nil discard reason for
// missing/corrupt/mismatched state (never an error), or a real I/O error —
// such as a permission failure — which stops the cycle and is never reported
// as "missing snapshot".
func (s *SnapshotStore) Load(exp ExpectedIdentity) (*Snapshot, DiscardReason, error) {
	data, err := s.io.ReadFile(s.Path())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, DiscardMissing, nil
		}
		return nil, NoDiscard, fmt.Errorf("read snapshot: %w", err)
	}
	snap, err := ParseSnapshot(data)
	if err != nil {
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
// directory sync where the platform supports it. A failure leaves the prior
// snapshot loadable (the platform's rename guarantees determine the exact
// durability), and no partial state is ever observed.
func (s *SnapshotStore) Replace(snap *Snapshot) error {
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

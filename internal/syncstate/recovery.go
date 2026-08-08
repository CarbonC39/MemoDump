package syncstate

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"memodump/internal/cloudsync"
)

// RecoveryDirName is the recoverable-delete subdirectory inside a replica's
// state directory.
const RecoveryDirName = "recovery"

// RecoveryStore is the device-local recoverable-delete area:
// <replica-state>/recovery/<syncId>/<stateHash>.md. A pulled tombstone copies
// the complete Markdown here idempotently before the note is deleted. Recovery
// data is content, never sync state: it is never uploaded, never used for
// reconciliation, and never garbage-collected automatically.
type RecoveryStore struct {
	dir string
	io  fsIO
}

// NewRecoveryStore returns a store under the replica state directory. The IDs
// are validated so an untrusted value can never escape the state root.
func NewRecoveryStore(stateRoot, vaultID, replicaID string) (*RecoveryStore, error) {
	if err := validateReplicaArgs(vaultID, replicaID); err != nil {
		return nil, err
	}
	return &RecoveryStore{
		dir: filepath.Join(StateDir(stateRoot, vaultID, replicaID), RecoveryDirName),
		io:  osFsIO{},
	}, nil
}

// pathFor resolves the recovery file for a (Sync ID, state hash) pair. The
// state hash is validated as lowercase 64-hex so it can never escape the
// directory.
func (s *RecoveryStore) pathFor(syncID, stateHash string) (string, error) {
	if !cloudsync.IsSyncID(syncID) {
		return "", fmt.Errorf("invalid syncId %q", syncID)
	}
	if !hex64Re.MatchString(stateHash) {
		return "", fmt.Errorf("invalid state hash %q", stateHash)
	}
	return filepath.Join(s.dir, syncID, stateHash+".md"), nil
}

// Write stores markdown for (syncID, stateHash) atomically and idempotently: a
// unique temp file, file sync, atomic rename, and a directory sync. Writing the
// same content twice is a no-op (no rewrite), and an old copy for a different
// state hash is never overwritten or removed.
func (s *RecoveryStore) Write(syncID, stateHash, markdown string) error {
	path, err := s.pathFor(syncID, stateHash)
	if err != nil {
		return err
	}
	if existing, rerr := os.ReadFile(path); rerr == nil && string(existing) == markdown {
		return nil // idempotent
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".recovery-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if err := s.io.WriteAll(tmp, []byte(markdown)); err != nil {
		tmp.Close()
		return err
	}
	if err := s.io.Sync(tmp); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := s.io.Rename(tmpPath, path); err != nil {
		return err
	}
	return s.io.SyncDir(dir)
}

// Read returns the recovered Markdown for a (Sync ID, state hash) pair.
func (s *RecoveryStore) Read(syncID, stateHash string) (string, bool, error) {
	path, err := s.pathFor(syncID, stateHash)
	if err != nil {
		return "", false, err
	}
	data, rerr := os.ReadFile(path)
	if rerr != nil {
		if os.IsNotExist(rerr) {
			return "", false, nil
		}
		return "", false, rerr
	}
	return string(data), true, nil
}

// List returns every recovered copy for a Sync ID as state hash -> markdown.
func (s *RecoveryStore) List(syncID string) (map[string]string, error) {
	if !cloudsync.IsSyncID(syncID) {
		return nil, fmt.Errorf("invalid syncId %q", syncID)
	}
	out := make(map[string]string)
	entries, err := os.ReadDir(filepath.Join(s.dir, syncID))
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return nil, err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		stateHash := strings.TrimSuffix(e.Name(), ".md")
		if !hex64Re.MatchString(stateHash) {
			continue
		}
		if data, rerr := os.ReadFile(filepath.Join(s.dir, syncID, e.Name())); rerr == nil {
			out[stateHash] = string(data)
		}
	}
	return out, nil
}

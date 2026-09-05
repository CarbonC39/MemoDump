package syncstate

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
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
// state hash is never overwritten or removed. The original note path is not
// recorded; use WriteWithPath when the path must survive index cleanup.
func (s *RecoveryStore) Write(syncID, stateHash, markdown string) error {
	return s.WriteWithPath(syncID, stateHash, "", markdown)
}

// pathPath returns the sidecar path for a recovery copy's original note path.
func (s *RecoveryStore) pathPath(syncID, stateHash string) (string, error) {
	mdPath, err := s.pathFor(syncID, stateHash)
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(mdPath, ".md") + ".path", nil
}

// WriteWithPath stores markdown plus the original note path for (syncID,
// stateHash), atomically and idempotently. The path sidecar lets a restore find
// the note's location even after the coordinator cleans up the index mapping on
// a converged deletion.
func (s *RecoveryStore) WriteWithPath(syncID, stateHash, notePath, markdown string) error {
	path, err := s.pathFor(syncID, stateHash)
	if err != nil {
		return err
	}
	if existing, rerr := os.ReadFile(path); rerr == nil && string(existing) == markdown {
		// Content is idempotent; still ensure the path sidecar is durable.
		return s.writePathSidecar(syncID, stateHash, notePath)
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
	if err := s.io.SyncDir(dir); err != nil {
		return err
	}
	return s.writePathSidecar(syncID, stateHash, notePath)
}

// writePathSidecar durably records the original note path for a recovery copy.
func (s *RecoveryStore) writePathSidecar(syncID, stateHash, notePath string) error {
	side, err := s.pathPath(syncID, stateHash)
	if err != nil {
		return err
	}
	if notePath == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(side), 0755); err != nil {
		return err
	}
	if existing, rerr := os.ReadFile(side); rerr == nil && string(existing) == notePath {
		return nil // idempotent
	}
	tmp, err := os.CreateTemp(filepath.Dir(side), ".recovery-path-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if _, err := tmp.WriteString(notePath); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, side); err != nil {
		return err
	}
	return s.io.SyncDir(filepath.Dir(side))
}

// Read returns the recovered Markdown and the original note path ("" when the
// copy predates path recording) for a (Sync ID, state hash) pair.
func (s *RecoveryStore) Read(syncID, stateHash string) (markdown, notePath string, ok bool, err error) {
	path, err := s.pathFor(syncID, stateHash)
	if err != nil {
		return "", "", false, err
	}
	data, rerr := os.ReadFile(path)
	if rerr != nil {
		if os.IsNotExist(rerr) {
			return "", "", false, nil
		}
		return "", "", false, rerr
	}
	side, serr := s.pathPath(syncID, stateHash)
	if serr == nil {
		if b, e := os.ReadFile(side); e == nil {
			notePath = string(b)
		}
	}
	return string(data), notePath, true, nil
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

// RecoveryCopy is one recoverable-delete copy: the Sync ID it belongs to, the
// state hash it was saved under, the original note path (when recorded), and
// the recovered Markdown.
type RecoveryCopy struct {
	SyncID    string
	StateHash string
	Path      string
	Markdown  string
}

// ListAll returns every recovery copy across all Sync IDs, deterministically
// ordered by Sync ID then state hash.
func (s *RecoveryStore) ListAll() ([]RecoveryCopy, error) {
	ids, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []RecoveryCopy
	for _, id := range ids {
		if !id.IsDir() || !cloudsync.IsSyncID(id.Name()) {
			continue
		}
		copies, err := s.List(id.Name())
		if err != nil {
			return nil, err
		}
		hashes := make([]string, 0, len(copies))
		for h := range copies {
			hashes = append(hashes, h)
		}
		sort.Strings(hashes)
		for _, h := range hashes {
			_, notePath, ok, err := s.Read(id.Name(), h)
			if err != nil {
				return nil, err
			}
			if !ok {
				continue
			}
			out = append(out, RecoveryCopy{SyncID: id.Name(), StateHash: h, Path: notePath, Markdown: copies[h]})
		}
	}
	return out, nil
}

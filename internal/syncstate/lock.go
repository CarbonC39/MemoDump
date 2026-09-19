package syncstate

import (
	"errors"
	"os"
	"path/filepath"
)

// ErrLocked reports that another process already owns a replica's state
// directory. The caller must disable sync for that replica while continuing to
// serve note edits.
var ErrLocked = errors.New("replica state directory is locked by another process")

// Lock is a held exclusive OS lock on a replica's state directory. It is not
// safe to copy.
type Lock struct {
	f         *os.File
	stateRoot string
	vaultID   string
	replicaID string
}

// AcquireReplicaLock takes the exclusive lock for a replica's state directory,
// creating the directory and lock file as needed. A second process (or a second
// unclosed handle) fails with ErrLocked. The lock is released on Close or when
// the process exits.
func AcquireReplicaLock(stateRoot, vaultID, replicaID string) (*Lock, error) {
	if err := validateReplicaArgs(vaultID, replicaID); err != nil {
		return nil, err
	}
	if stateRoot == "" {
		root, err := DefaultStateRoot()
		if err != nil {
			return nil, err
		}
		stateRoot = root
	}
	dir := StateDir(stateRoot, vaultID, replicaID)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, "replica.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := tryLock(f); err != nil {
		f.Close()
		return nil, err
	}
	return &Lock{f: f, stateRoot: normalizedRoot(stateRoot), vaultID: vaultID, replicaID: replicaID}, nil
}

// normalizedRoot canonicalizes a state root for identity comparison: absolute
// and clean, with symlinks resolved so two spellings of the same directory
// compare equal.
func normalizedRoot(stateRoot string) string {
	if stateRoot == "" {
		if root, err := DefaultStateRoot(); err == nil {
			stateRoot = root
		}
	}
	abs, err := filepath.Abs(stateRoot)
	if err != nil {
		return filepath.Clean(stateRoot)
	}
	abs = filepath.Clean(abs)
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	return abs
}

// Close releases the lock and closes the lock file. It is idempotent and safe
// on a nil Lock.
func (l *Lock) Close() error {
	if l == nil || l.f == nil {
		return nil
	}
	unlockErr := unlock(l.f)
	closeErr := l.f.Close()
	l.f = nil
	if unlockErr != nil {
		return unlockErr
	}
	return closeErr
}

// Held reports whether the lock is currently owned by this process. It probes a
// fresh non-blocking handle on the same lock file: flock/LockFileEx treat each
// open description independently, so a second handle from the same process
// contends with the held one. Only a probe failing with ErrLocked means the
// lock is held; any other probe error means the state is unknown.
func (l *Lock) Held() bool {
	if l == nil || l.f == nil {
		return false
	}
	f, err := os.OpenFile(l.f.Name(), os.O_RDWR, 0)
	if err != nil {
		return false
	}
	defer f.Close()
	return errors.Is(tryLock(f), ErrLocked)
}

// For reports whether the lock belongs to the given replica's state directory.
// A lock acquired for another vault, replica, or state root must never guard
// this one's index and snapshot.
func (l *Lock) For(vaultID, replicaID, stateRoot string) bool {
	if l == nil {
		return false
	}
	return l.vaultID == vaultID && l.replicaID == replicaID && l.stateRoot == normalizedRoot(stateRoot)
}

// acquireRegistryLock takes the short-lived cross-process lock guarding the
// state-root registry/device read-modify-write in Resolve, so two processes
// first-enabling the same vault cannot generate and persist two different
// Replica IDs. It is blocking: a process waits for the other's resolve to
// finish. The in-process mutex in Resolve serializes the same-process case,
// where a second flock on a new descriptor could otherwise behave differently
// across platforms.
func acquireRegistryLock(stateRoot string) (*Lock, error) {
	if err := os.MkdirAll(stateRoot, 0755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(stateRoot, "registry.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := lockBlocking(f); err != nil {
		f.Close()
		return nil, err
	}
	return &Lock{f: f}, nil
}

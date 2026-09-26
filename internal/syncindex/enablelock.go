package syncindex

import (
	"os"
	"path/filepath"
)

// enableLock is a held exclusive OS lock serializing index creation, so two
// concurrent first-enables cannot mint different Vault IDs and Sync IDs. It is
// also acquired by Create and Rebuild to keep them out of each other's way.
type enableLock struct {
	f *os.File
}

// acquireEnableLock takes the blocking exclusive lock for a vault's sync
// metadata directory, creating the directory (and the lock file) as needed.
func acquireEnableLock(root string) (*enableLock, error) {
	dir := filepath.Join(root, DirName)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, ".enable.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := lockExclusive(f); err != nil {
		f.Close()
		return nil, err
	}
	return &enableLock{f: f}, nil
}

// Close releases the lock and closes the file.
func (l *enableLock) Close() error {
	if l == nil || l.f == nil {
		return nil
	}
	unlockErr := unlockLock(l.f)
	closeErr := l.f.Close()
	l.f = nil
	if unlockErr != nil {
		return unlockErr
	}
	return closeErr
}

package syncindex

import (
	"os"
	"sync"
)

// indexIO abstracts the durability-relevant filesystem operations so tests can
// inject a failure at any numbered step of the portable-index durability
// sequence (primary rename, backup write, directory fsync).
type indexIO interface {
	// WriteFileAtomic writes data to dir/name via a unique temp file, fsync,
	// and atomic rename.
	WriteFileAtomic(dir, name string, data []byte) error
	ReadFile(path string) ([]byte, error)
	SyncDir(dir string) error
}

// osIndexIO is the production implementation.
type osIndexIO struct{}

func (osIndexIO) WriteFileAtomic(dir, name string, data []byte) error {
	return writeFileAtomic(dir, name, data)
}
func (osIndexIO) ReadFile(path string) ([]byte, error) { return os.ReadFile(path) }
func (osIndexIO) SyncDir(dir string) error             { return syncDir(dir) }

// faultIndexIO wraps an indexIO and injects failures at named durability steps.
// A test arms a one-shot write failure for a target file name (the primary
// index or the backup) or a persistent directory-sync failure.
type faultIndexIO struct {
	inner indexIO
	mu    sync.Mutex

	failWrite map[string]error // target name → fail the NEXT write to it
	failSync  error            // every SyncDir fails with this
}

func newFaultIndexIO(inner indexIO) *faultIndexIO {
	return &faultIndexIO{inner: inner, failWrite: make(map[string]error)}
}

// armWriteFail makes the next WriteFileAtomic to name fail with err.
func (f *faultIndexIO) armWriteFail(name string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failWrite[name] = err
}

// armSyncFail makes every SyncDir fail with err until cleared.
func (f *faultIndexIO) armSyncFail(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failSync = err
}

func (f *faultIndexIO) WriteFileAtomic(dir, name string, data []byte) error {
	f.mu.Lock()
	if err, ok := f.failWrite[name]; ok {
		delete(f.failWrite, name)
		f.mu.Unlock()
		return err
	}
	f.mu.Unlock()
	return f.inner.WriteFileAtomic(dir, name, data)
}

func (f *faultIndexIO) ReadFile(path string) ([]byte, error) { return f.inner.ReadFile(path) }

func (f *faultIndexIO) SyncDir(dir string) error {
	f.mu.Lock()
	err := f.failSync
	f.mu.Unlock()
	if err != nil {
		return err
	}
	return f.inner.SyncDir(dir)
}

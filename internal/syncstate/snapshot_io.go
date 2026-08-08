package syncstate

import (
	"fmt"
	"os"
	"sync"
)

// fsIO abstracts the filesystem operations the snapshot store depends on, so
// tests can inject failures (failed create, short write, failed fsync, failed
// rename, failed directory sync) at any numbered step. The default
// implementation uses the os package directly.
type fsIO interface {
	CreateTemp(dir, pattern string) (*os.File, error)
	OpenRead(name string) (*os.File, error)
	ReadFile(name string) ([]byte, error)
	WriteAll(f *os.File, b []byte) error
	Sync(f *os.File) error
	Rename(oldpath, newpath string) error
	Remove(name string) error
	SyncDir(dir string) error
}

// osFsIO is the real filesystem implementation.
type osFsIO struct{}

func (osFsIO) CreateTemp(dir, pattern string) (*os.File, error) {
	return os.CreateTemp(dir, pattern)
}

func (osFsIO) OpenRead(name string) (*os.File, error) { return os.Open(name) }

func (osFsIO) ReadFile(name string) ([]byte, error) { return os.ReadFile(name) }

func (osFsIO) Rename(oldpath, newpath string) error { return atomicReplace(oldpath, newpath) }

func (osFsIO) Remove(name string) error { return os.Remove(name) }

func (osFsIO) Sync(f *os.File) error { return f.Sync() }

func (osFsIO) SyncDir(dir string) error { return syncDir(dir) }

// WriteAll writes every byte; a short write is an error so a partially written
// snapshot is never acknowledged as durable.
func (osFsIO) WriteAll(f *os.File, b []byte) error {
	n, err := f.Write(b)
	if err != nil {
		return err
	}
	if n != len(b) {
		return fmt.Errorf("short write: wrote %d of %d bytes", n, len(b))
	}
	return nil
}

// faultFsIO wraps an fsIO and can be armed to fail the Nth call to a named
// operation (1-based) with an error, or to return a short write from WriteAll.
type faultFsIO struct {
	fsIO
	mu     sync.Mutex
	failOn map[string]int // op -> 1-based call number to fail
	calls  map[string]int // op -> calls so far
	err    error
	short  bool // a write failure returns a short write instead of err
}

func newFaultFsIO(under fsIO) *faultFsIO {
	if under == nil {
		under = osFsIO{}
	}
	return &faultFsIO{fsIO: under, failOn: map[string]int{}, calls: map[string]int{}}
}

// arm makes the call-th call to op fail with err.
func (f *faultFsIO) arm(op string, call int, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failOn[op] = call
	f.err = err
}

// armNext makes the NEXT call to op fail with err.
func (f *faultFsIO) armNext(op string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failOn[op] = f.calls[op] + 1
	f.err = err
}

// armShortWrite makes the call-th call to WriteAll return a short write.
func (f *faultFsIO) armShortWrite(op string, call int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failOn[op] = call
	f.short = true
}

// armNextShortWrite makes the NEXT call to WriteAll return a short write.
func (f *faultFsIO) armNextShortWrite(op string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failOn[op] = f.calls[op] + 1
	f.short = true
}

func (f *faultFsIO) shouldFail(op string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls[op]++
	return f.failOn[op] == f.calls[op]
}

func (f *faultFsIO) injected(op string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	return fmt.Errorf("injected %s failure", op)
}

func (f *faultFsIO) isShort() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.short
}

func (f *faultFsIO) CreateTemp(dir, pattern string) (*os.File, error) {
	if f.shouldFail("create") {
		return nil, f.injected("create")
	}
	return f.fsIO.CreateTemp(dir, pattern)
}

func (f *faultFsIO) OpenRead(name string) (*os.File, error) {
	if f.shouldFail("read") {
		return nil, f.injected("read")
	}
	return f.fsIO.OpenRead(name)
}

func (f *faultFsIO) ReadFile(name string) ([]byte, error) {
	if f.shouldFail("read") {
		return nil, f.injected("read")
	}
	return f.fsIO.ReadFile(name)
}

func (f *faultFsIO) Rename(oldpath, newpath string) error {
	if f.shouldFail("rename") {
		return f.injected("rename")
	}
	return f.fsIO.Rename(oldpath, newpath)
}

func (f *faultFsIO) Remove(name string) error {
	if f.shouldFail("remove") {
		return f.injected("remove")
	}
	return f.fsIO.Remove(name)
}

func (f *faultFsIO) Sync(file *os.File) error {
	if f.shouldFail("sync") {
		return f.injected("sync")
	}
	return f.fsIO.Sync(file)
}

func (f *faultFsIO) SyncDir(dir string) error {
	if f.shouldFail("sync-dir") {
		return f.injected("sync-dir")
	}
	return f.fsIO.SyncDir(dir)
}

func (f *faultFsIO) WriteAll(file *os.File, b []byte) error {
	if f.shouldFail("write") {
		if f.isShort() {
			half := len(b) / 2
			if half == 0 {
				half = 1
			}
			n, _ := file.Write(b[:half])
			return fmt.Errorf("injected short write: %d of %d bytes", n, len(b))
		}
		return f.injected("write")
	}
	return f.fsIO.WriteAll(file, b)
}

package syncstate

import (
	"fmt"
	"os"
	"sync"
)

// walIO abstracts the filesystem operations the device-state WAL depends on, so
// tests can inject failures (short writes, failed fsync, failed rename, failed
// open/remove) at any numbered step. The default implementation uses the os
// package directly.
type walIO interface {
	OpenAppend(name string) (*os.File, error)
	ReadFile(name string) ([]byte, error)
	Rename(oldpath, newpath string) error
	Remove(name string) error
	ReadDir(name string) ([]os.DirEntry, error)
	Sync(f *os.File) error
	WriteAll(f *os.File, b []byte) error
}

// osWalIO is the real filesystem implementation.
type osWalIO struct{}

func (osWalIO) OpenAppend(name string) (*os.File, error) {
	return os.OpenFile(name, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
}

func (osWalIO) ReadFile(name string) ([]byte, error) { return os.ReadFile(name) }

func (osWalIO) Rename(oldpath, newpath string) error { return os.Rename(oldpath, newpath) }

func (osWalIO) Remove(name string) error { return os.Remove(name) }

func (osWalIO) ReadDir(name string) ([]os.DirEntry, error) { return os.ReadDir(name) }

func (osWalIO) Sync(f *os.File) error { return f.Sync() }

// WriteAll writes every byte; a short write is an error so a partially written
// record is never acknowledged as durable.
func (osWalIO) WriteAll(f *os.File, b []byte) error {
	n, err := f.Write(b)
	if err != nil {
		return err
	}
	if n != len(b) {
		return fmt.Errorf("short write: wrote %d of %d bytes", n, len(b))
	}
	return nil
}

// faultWalIO wraps a walIO and can be armed to fail the Nth call to a named
// operation (1-based) with an error, or to return a short write from WriteAll.
type faultWalIO struct {
	walIO
	mu     sync.Mutex
	failOn map[string]int // op -> 1-based call number to fail
	calls  map[string]int // op -> calls so far
	err    error
	short  bool // a write failure returns a short write instead of err
}

func newFaultWalIO(under walIO) *faultWalIO {
	if under == nil {
		under = osWalIO{}
	}
	return &faultWalIO{walIO: under, failOn: map[string]int{}, calls: map[string]int{}}
}

// arm makes the call-th call to op fail with err.
func (f *faultWalIO) arm(op string, call int, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failOn[op] = call
	f.err = err
}

// armNext makes the NEXT call to op fail with err.
func (f *faultWalIO) armNext(op string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failOn[op] = f.calls[op] + 1
	f.err = err
}

// armShortWrite makes the call-th call to op return a short write.
func (f *faultWalIO) armShortWrite(op string, call int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failOn[op] = call
	f.short = true
}

// armNextShortWrite makes the NEXT call to op return a short write.
func (f *faultWalIO) armNextShortWrite(op string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failOn[op] = f.calls[op] + 1
	f.short = true
}

func (f *faultWalIO) shouldFail(op string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls[op]++
	return f.failOn[op] == f.calls[op]
}

func (f *faultWalIO) injected(op string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	return fmt.Errorf("injected %s failure", op)
}

func (f *faultWalIO) isShort() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.short
}

func (f *faultWalIO) OpenAppend(name string) (*os.File, error) {
	if f.shouldFail("open") {
		return nil, f.injected("open")
	}
	return f.walIO.OpenAppend(name)
}

func (f *faultWalIO) ReadFile(name string) ([]byte, error) {
	if f.shouldFail("read") {
		return nil, f.injected("read")
	}
	return f.walIO.ReadFile(name)
}

func (f *faultWalIO) Rename(oldpath, newpath string) error {
	if f.shouldFail("rename") {
		return f.injected("rename")
	}
	return f.walIO.Rename(oldpath, newpath)
}

func (f *faultWalIO) Remove(name string) error {
	if f.shouldFail("remove") {
		return f.injected("remove")
	}
	return f.walIO.Remove(name)
}

func (f *faultWalIO) ReadDir(name string) ([]os.DirEntry, error) {
	if f.shouldFail("readdir") {
		return nil, f.injected("readdir")
	}
	return f.walIO.ReadDir(name)
}

func (f *faultWalIO) Sync(file *os.File) error {
	if f.shouldFail("sync") {
		return f.injected("sync")
	}
	return f.walIO.Sync(file)
}

func (f *faultWalIO) WriteAll(file *os.File, b []byte) error {
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
	return f.walIO.WriteAll(file, b)
}

package vaultfs

import (
	"sort"
	"sync"
)

// lockManager serializes file operations per repository-relative path. Multi-
// path operations acquire their locks in sorted order, so two operations that
// touch the same pair of paths can never deadlock.
//
// Locks are advisory within the process: they serialize MemoDump's own
// concurrent requests, which is the correctness requirement for the revision
// CAS. They cannot serialize an external editor that races the final
// verification-and-rename window — that remains a platform boundary.
type lockManager struct {
	mu    sync.Mutex
	locks map[string]*lockRef
}

type lockRef struct {
	mu  sync.Mutex
	ref int
}

func newLockManager() *lockManager {
	return &lockManager{locks: make(map[string]*lockRef)}
}

// acquire takes the lock for path and returns a release function. Locks are
// reference-counted so a long-idle path's lock is reclaimed.
func (m *lockManager) acquire(path string) func() {
	m.mu.Lock()
	lr, ok := m.locks[path]
	if !ok {
		lr = &lockRef{}
		m.locks[path] = lr
	}
	lr.ref++
	m.mu.Unlock()

	lr.mu.Lock()
	return func() {
		lr.mu.Unlock()
		m.mu.Lock()
		lr.ref--
		if lr.ref == 0 {
			delete(m.locks, path)
		}
		m.mu.Unlock()
	}
}

// withLock holds the locks for all paths (sorted, de-duplicated) while running
// fn. It returns fn's error.
func (m *lockManager) withLock(paths []string, fn func() error) error {
	unique := make([]string, 0, len(paths))
	seen := make(map[string]bool, len(paths))
	for _, p := range paths {
		if !seen[p] {
			seen[p] = true
			unique = append(unique, p)
		}
	}
	sort.Strings(unique)

	releases := make([]func(), 0, len(unique))
	for _, p := range unique {
		releases = append(releases, m.acquire(p))
	}
	defer func() {
		for i := len(releases) - 1; i >= 0; i-- {
			releases[i]()
		}
	}()
	return fn()
}

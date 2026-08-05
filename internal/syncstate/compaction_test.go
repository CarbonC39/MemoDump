package syncstate

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

var errInjected = errors.New("injected failure")

func putN(t *testing.T, s *Store, n int) {
	t.Helper()
	for i := 1; i <= n; i++ {
		if _, err := s.Put(fmt.Sprintf("k%d", i), i); err != nil {
			t.Fatal(err)
		}
	}
}

// TestCompactFailureAtEveryStepRecovers drives the rotation/compaction steps
// with a fault injected at each numbered boundary, then reopens the store
// ("crash") and verifies every acked record is durably present.
func TestCompactFailureAtEveryStepRecovers(t *testing.T) {
	scenarios := []struct {
		name string
		run  func(t *testing.T, s *Store, fault *faultWalIO)
	}{
		{"rotate-sync", func(t *testing.T, s *Store, f *faultWalIO) {
			f.armNext("sync", errInjected)
			if err := s.rotate(); err == nil {
				t.Fatal("rotate sync failure not surfaced")
			}
		}},
		{"rotate-rename", func(t *testing.T, s *Store, f *faultWalIO) {
			f.armNext("rename", errInjected)
			if err := s.rotate(); err == nil {
				t.Fatal("rotate rename failure not surfaced")
			}
			// The active WAL must have been reopened so the store stays writable.
			if _, err := s.Put("extra", 99); err != nil {
				t.Fatalf("store not writable after failed rename: %v", err)
			}
		}},
		{"rotate-open-new-active", func(t *testing.T, s *Store, f *faultWalIO) {
			f.armNext("open", errInjected)
			if err := s.rotate(); err == nil {
				t.Fatal("rotate open failure not surfaced")
			}
		}},
		{"snapshot-read", func(t *testing.T, s *Store, f *faultWalIO) {
			if err := s.rotate(); err != nil {
				t.Fatal(err)
			}
			f.armNext("read", errInjected)
			if _, err := s.buildSnapshot(context.Background()); err == nil {
				t.Fatal("snapshot read failure not surfaced")
			}
		}},
		{"snapshot-fsync", func(t *testing.T, s *Store, f *faultWalIO) {
			if err := s.rotate(); err != nil {
				t.Fatal(err)
			}
			f.armNext("sync", errInjected)
			if _, err := s.buildSnapshot(context.Background()); err == nil {
				t.Fatal("snapshot fsync failure not surfaced")
			}
		}},
		{"snapshot-install-rename", func(t *testing.T, s *Store, f *faultWalIO) {
			if err := s.rotate(); err != nil {
				t.Fatal(err)
			}
			f.armNext("rename", errInjected)
			if _, err := s.buildSnapshot(context.Background()); err == nil {
				t.Fatal("snapshot install failure not surfaced")
			}
		}},
		{"prune-remove", func(t *testing.T, s *Store, f *faultWalIO) {
			if err := s.rotate(); err != nil {
				t.Fatal(err)
			}
			covered, err := s.buildSnapshot(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			f.armNext("remove", errInjected)
			if err := s.pruneFrozen(context.Background(), covered); err == nil {
				t.Fatal("prune remove failure not surfaced")
			}
		}},
	}

	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			dir := t.TempDir()
			fault := newFaultWalIO(osWalIO{})
			s, err := Open(dir, Options{FS: fault})
			if err != nil {
				t.Fatal(err)
			}
			putN(t, s, 5)
			sc.run(t, s, fault)

			// "Crash": abandon s and recover from disk.
			s2, err := Open(dir, Options{})
			if err != nil {
				t.Fatalf("reopen after %s failure: %v", sc.name, err)
			}
			for i := 1; i <= 5; i++ {
				if _, ok := s2.Get(fmt.Sprintf("k%d", i)); !ok {
					t.Fatalf("acked key k%d lost after %s failure", i, sc.name)
				}
			}
			s2.Close()
		})
	}
}

// TestCompactionRoundTrip verifies a successful compaction produces a snapshot
// whose data matches the pre-compaction state, then that a restart replays
// snapshot + new active WAL together.
func TestCompactionRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	putN(t, s, 20)
	if err := s.Compact(); err != nil {
		t.Fatal(err)
	}
	// After compaction the snapshot exists and no frozen generations remain.
	if _, err := filepathGlob(dir, "state.wal.*.frozen.ndjson"); err != nil {
		t.Fatalf("frozen gens remain after compaction: %v", err)
	}
	if s.Metrics().Compactions == 0 {
		t.Fatal("compaction not counted")
	}
	// Records appended after the compaction live in the new active WAL.
	if _, err := s.Put("late", 99); err != nil {
		t.Fatal(err)
	}
	s.Close()

	s2, err := Open(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	for i := 1; i <= 20; i++ {
		if _, ok := s2.Get(fmt.Sprintf("k%d", i)); !ok {
			t.Fatalf("key k%d lost across compaction", i)
		}
	}
	if _, ok := s2.Get("late"); !ok {
		t.Fatal("post-compaction append lost")
	}
}

// TestAppendersDuringRepeatedCompaction runs appenders concurrently with a
// continuously-triggering compactor and verifies afterwards that every acked
// append is durable exactly once and the sequence space is contiguous.
func TestAppendersDuringRepeatedCompaction(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, Options{
		WALBytesThreshold:      4096,
		SnapshotRatioThreshold: 0.01,
		RecordsThreshold:       50,
	})
	if err != nil {
		t.Fatal(err)
	}

	const appenders = 4
	const perAppender = 400
	const total = appenders * perAppender

	ctx, cancel := context.WithCancel(context.Background())
	compErr := make(chan error, 1)
	go func() { compErr <- s.RunCompactor(ctx) }()

	var wg sync.WaitGroup
	for a := 0; a < appenders; a++ {
		wg.Add(1)
		go func(a int) {
			defer wg.Done()
			for j := 0; j < perAppender; j++ {
				key := fmt.Sprintf("k%d_%d", a, j)
				if _, err := s.Put(key, map[string]any{"a": int64(a), "j": int64(j)}); err != nil {
					t.Errorf("append %s: %v", key, err)
					return
				}
			}
		}(a)
	}
	wg.Wait()
	cancel()
	if err := <-compErr; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("compactor: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s2, err := Open(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	for a := 0; a < appenders; a++ {
		for j := 0; j < perAppender; j++ {
			key := fmt.Sprintf("k%d_%d", a, j)
			v, ok := s2.Get(key)
			if !ok {
				t.Fatalf("acked key %s missing after compaction stress", key)
			}
			if want := fmt.Sprintf(`{"a":%d,"j":%d}`, a, j); string(v) != want {
				t.Fatalf("key %s = %s, want %s", key, v, want)
			}
		}
	}
	// Every append acked a distinct sequence; the recovered watermark proves the
	// durable sequence space is exactly 1..total (no gap, no reuse).
	if got := s2.LastAppliedSeq(); got != total {
		t.Fatalf("last applied seq = %d, want %d (no missing/duplicate durable seq)", got, total)
	}
}

// TestFrozenGenerationNeverOverwritten forces nextGen to collide with an
// existing frozen generation (as a stale or corrupt counter would) and verifies
// the no-replace rename refuses instead of overwriting.
func TestFrozenGenerationNeverOverwritten(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	putN(t, s, 3)
	if err := s.rotate(); err != nil {
		t.Fatal(err)
	}
	frozen1 := filepath.Join(dir, frozenName(1))
	data1, err := os.ReadFile(frozen1)
	if err != nil {
		t.Fatal(err)
	}
	s.nextGen = 1 // force a collision
	if err := s.rotate(); !errors.Is(err, os.ErrExist) {
		t.Fatalf("rotate with a colliding generation = %v, want os.ErrExist", err)
	}
	data2, err := os.ReadFile(frozen1)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data1, data2) {
		t.Fatal("existing frozen generation was overwritten")
	}
	// The refused rotation must leave the store writable (the active fd was
	// reopened), not silently degraded.
	if _, err := s.Put("still-writable", 99); err != nil {
		t.Fatalf("store lost write capability after a refused rotation: %v", err)
	}
}

// TestPoisonedStoreRefusesRotationAndCompact proves a failed append cannot be
// rotated into a frozen generation: the torn tail stays in the active WAL where
// recovery can truncate it.
func TestPoisonedStoreRefusesRotationAndCompact(t *testing.T) {
	dir := t.TempDir()
	fault := newFaultWalIO(osWalIO{})
	s, err := Open(dir, Options{FS: fault})
	if err != nil {
		t.Fatal(err)
	}
	putN(t, s, 1)
	fault.armNextShortWrite("write")
	if _, err := s.Put("b", 2); err == nil {
		t.Fatal("short write did not fail")
	}
	if err := s.rotate(); err == nil {
		t.Fatal("rotate on a poisoned store succeeded")
	}
	if err := s.Compact(); err == nil {
		t.Fatal("compact on a poisoned store succeeded")
	}
	s.Close()

	// Recovery is clean: the torn tail is truncated, the failed append absent.
	s2, err := Open(dir, Options{})
	if err != nil {
		t.Fatalf("reopen after poisoned rotation refusal: %v", err)
	}
	defer s2.Close()
	if _, ok := s2.Get("k1"); !ok {
		t.Fatal("valid record lost")
	}
	if _, ok := s2.Get("b"); ok {
		t.Fatal("failed append recovered as durable")
	}
}

// TestCompactionHandlesLargeRecords verifies a legal record larger than the
// old 8 MiB scanner cap remains compactable (no fixed line-size limit).
func TestCompactionHandlesLargeRecords(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, Options{WALBytesThreshold: 1024, SnapshotRatioThreshold: 0.01, RecordsThreshold: 1})
	if err != nil {
		t.Fatal(err)
	}
	big := strings.Repeat("x", 8<<20+1)
	if _, err := s.Put("big", big); err != nil {
		t.Fatal(err)
	}
	if err := s.Compact(); err != nil {
		t.Fatalf("compact of a >8 MiB record failed: %v", err)
	}
	s.Close()

	s2, err := Open(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	v, ok := s2.Get("big")
	if !ok || string(v) != `"`+big+`"` {
		t.Fatal("large record lost across compaction")
	}
}

func TestDirSyncCapabilityMatchesPlatform(t *testing.T) {
	// The capability must be explicit per platform, never silently assumed.
	if runtime.GOOS == "windows" {
		if DirSyncSupported() {
			t.Fatal("DirSyncSupported() = true on windows")
		}
	} else if !DirSyncSupported() {
		t.Fatal("DirSyncSupported() = false on a POSIX platform")
	}
}

func TestCompactCoalescesWhileActive(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	// Simulate an active compactor holding the single-flight lock.
	s.compMu.Lock()
	// A concurrent request coalesces instead of running.
	if err := s.Compact(); err != nil {
		t.Fatal(err)
	}
	if !s.pendingCompact.Load() {
		t.Fatal("concurrent Compact did not mark work pending")
	}
	s.compMu.Unlock()

	// The next Compact consumes the pending request (one extra pass) and clears
	// the flag.
	if err := s.Compact(); err != nil {
		t.Fatal(err)
	}
	if s.pendingCompact.Load() {
		t.Fatal("pending work not consumed")
	}
	if s.Metrics().Compactions < 2 {
		t.Fatalf("expected the coalesced pass to run, compactions = %d", s.Metrics().Compactions)
	}
}

// filepathGlob reports an error if any path matches, so callers can assert that
// a pattern matched nothing.
func filepathGlob(dir, pattern string) ([]string, error) {
	matches, err := filepath.Glob(filepath.Join(dir, pattern))
	if err != nil {
		return nil, err
	}
	if len(matches) > 0 {
		return matches, errors.New("unexpected matches")
	}
	return nil, nil
}

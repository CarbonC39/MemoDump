package syncstate

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// shouldCompact reports whether there is compaction work: the active WAL has
// reached the byte threshold AND either the ratio of the snapshot size or the
// record count threshold (the plan's defaults: >=1 MiB and >=25% of snapshot or
// >=10k records), OR un-consumed frozen generations recovered from a previous
// run have reached the byte threshold.
func (s *Store) shouldCompact() bool {
	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	if s.frozenBytes >= s.opts.WALBytesThreshold {
		return true
	}
	if s.walBytes < s.opts.WALBytesThreshold {
		return false
	}
	recordsMet := s.walRecords >= s.opts.RecordsThreshold
	ratioMet := s.snapshotSize == 0 ||
		float64(s.walBytes) >= float64(s.snapshotSize)*s.opts.SnapshotRatioThreshold
	return recordsMet || ratioMet
}

// Compact runs one full compaction (not cancellable; manual call), coalescing
// concurrent requests: only one compactor executes at a time; a request that
// arrives while one is running is marked pending and re-evaluated after the
// current snapshot is durable.
func (s *Store) Compact() error {
	return s.compact(context.Background())
}

func (s *Store) compact(ctx context.Context) error {
	if !s.compMu.TryLock() {
		s.pendingCompact.Store(true)
		return nil
	}
	defer s.compMu.Unlock()
	for {
		start := time.Now()
		if err := s.doCompact(ctx); err != nil {
			return err
		}
		s.metricsMu.Lock()
		s.metrics.Compactions++
		s.metrics.CompactionDurationNs += time.Since(start).Nanoseconds()
		s.metricsMu.Unlock()
		// A request that arrived while this compaction was running is consumed
		// here and triggers another pass after the snapshot is durable.
		if !s.pendingCompact.Swap(false) {
			return nil
		}
	}
}

// doCompact rotates the active WAL into a frozen generation, builds a new
// snapshot from the snapshot plus all frozen generations, and prunes the
// covered frozen generations. It checks ctx between generations so a cancelled
// compactor stops at a safe boundary.
func (s *Store) doCompact(ctx context.Context) error {
	if err := s.rotate(); err != nil {
		return err
	}
	covered, err := s.buildSnapshot(ctx)
	if err != nil {
		return err
	}
	if err := s.pruneFrozen(ctx, covered); err != nil {
		return err
	}
	// Every frozen generation was consumed by the new snapshot.
	s.stateMu.Lock()
	s.frozenBytes = 0
	s.stateMu.Unlock()
	return nil
}

// rotate moves the active WAL into a uniquely named frozen generation and opens
// a fresh active WAL, all while holding the writer actor.
func (s *Store) rotate() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("store is closed")
	}
	// 1. Sync and close the active WAL descriptor.
	if err := s.io.Sync(s.f); err != nil {
		return fmt.Errorf("rotate: sync active wal: %w", err)
	}
	if err := s.f.Close(); err != nil {
		return fmt.Errorf("rotate: close active wal: %w", err)
	}
	// 2. Rename to a unique frozen generation with no-replace semantics and
	// sync the directory. An existing generation is never overwritten.
	gen := s.nextGen
	s.nextGen++
	active := filepath.Join(s.dir, activeWalName)
	frozen := filepath.Join(s.dir, frozenName(gen))
	if err := s.io.RenameNoClobber(active, frozen); err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("rotate: frozen generation %d already exists; refusing to overwrite: %w", gen, os.ErrExist)
		}
		// The active descriptor is closed but the file is still active; reopen
		// it so a failed rotation leaves the store writable.
		f, oerr := s.io.OpenAppend(active)
		if oerr != nil {
			return fmt.Errorf("rotate: rename to frozen %d: %w (reopen active: %v)", gen, err, oerr)
		}
		s.f = f
		return fmt.Errorf("rotate: rename to frozen %d: %w", gen, err)
	}
	if err := syncDir(s.dir); err != nil {
		return fmt.Errorf("rotate: sync dir: %w", err)
	}
	// 3. Open a new active WAL in append mode and sync the directory again.
	f, err := s.io.OpenAppend(active)
	if err != nil {
		return fmt.Errorf("rotate: open new active wal: %w", err)
	}
	s.f = f
	s.stateMu.Lock()
	s.walBytes = 0
	s.walRecords = 0
	s.stateMu.Unlock()
	if err := syncDir(s.dir); err != nil {
		return fmt.Errorf("rotate: sync new active dir: %w", err)
	}
	return nil
}

// buildSnapshot constructs the compacted state by replaying the durable
// snapshot plus every frozen generation (never the live map, never the new
// active WAL), streams it to a temporary file, and installs it with a durable
// replace. It returns the frozen generations fully covered by the new snapshot.
// It runs without the writer lock: frozen files are immutable after rotation.
// Frozen generations are decoded incrementally through a buffered scanner (one
// buffer at a time, released per generation) and ctx is checked between
// generations so a cancelled compactor stops at a safe boundary.
func (s *Store) buildSnapshot(ctx context.Context) ([]int64, error) {
	state := make(map[string]json.RawMessage)
	lastApplied := int64(0)

	if data, err := s.io.ReadFile(filepath.Join(s.dir, snapshotName)); err == nil {
		snap, perr := parseSnapshot(data)
		if perr != nil {
			return nil, fmt.Errorf("compact: snapshot: %v", perr)
		}
		for k, v := range snap.Data {
			state[k] = v
		}
		lastApplied = snap.LastAppliedSeq
	} else if !os.IsNotExist(err) {
		return nil, err
	}

	gens, err := s.frozenGens()
	if err != nil {
		return nil, err
	}
	for _, gen := range gens {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := s.replayFrozen(ctx, gen, state, &lastApplied); err != nil {
			return nil, err
		}
	}

	// Stream the compacted state through the durable-replace helper: unique
	// temp file, fsync, atomic rename, and a directory sync.
	var snapshotSize int64
	err = s.durableReplaceSnapshot(func(w io.Writer) error {
		cw := &countingWriter{w: w}
		if _, werr := writeSnapshotFile(cw, lastApplied, state); werr != nil {
			return werr
		}
		snapshotSize = cw.n
		return nil
	})
	if err != nil {
		return nil, err
	}

	s.stateMu.Lock()
	s.snapshotSize = snapshotSize
	s.stateMu.Unlock()
	return gens, nil
}

// replayFrozen decodes one frozen generation incrementally, applying records
// with seq above the snapshot watermark.
func (s *Store) replayFrozen(ctx context.Context, gen int64, state map[string]json.RawMessage, lastApplied *int64) error {
	f, err := s.io.OpenRead(filepath.Join(s.dir, frozenName(gen)))
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8<<20)
	for sc.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		rec, perr := parseWalRecord(line)
		if perr != nil {
			return fmt.Errorf("compact: frozen %d: %v", gen, perr)
		}
		if rec.Seq <= *lastApplied {
			continue // already in the snapshot watermark
		}
		if aerr := applyTo(state, rec); aerr != nil {
			return aerr
		}
		*lastApplied = rec.Seq
	}
	return sc.Err()
}

// countingWriter counts the bytes written through it.
type countingWriter struct {
	w io.Writer
	n int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

// durableReplaceSnapshot atomically installs a new snapshot: a uniquely named
// temp file written by write, fsynced, renamed over the snapshot, and the
// directory synced. The fsync and rename go through s.io so tests can inject
// failures; a failed fsync or atomic replacement stops the compaction commit.
func (s *Store) durableReplaceSnapshot(write func(io.Writer) error) error {
	tmp, err := os.CreateTemp(s.dir, ".state-snapshot-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if err := write(tmp); err != nil {
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
	if err := s.io.Rename(tmpPath, filepath.Join(s.dir, snapshotName)); err != nil {
		return err
	}
	return syncDir(s.dir)
}

// pruneFrozen deletes the frozen generations fully covered by the durable
// snapshot, then syncs the directory.
func (s *Store) pruneFrozen(ctx context.Context, covered []int64) error {
	for _, gen := range covered {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.io.Remove(filepath.Join(s.dir, frozenName(gen))); err != nil {
			return err
		}
	}
	return syncDir(s.dir)
}

// RunCompactor runs the background compaction loop until ctx is cancelled. Only
// one compactor runs per replica; concurrent threshold requests are coalesced,
// and an in-flight compaction checks ctx between generations.
func (s *Store) RunCompactor(ctx context.Context) error {
	t := time.NewTicker(s.opts.PollInterval)
	defer t.Stop()
	for {
		if s.shouldCompact() {
			if err := s.compact(ctx); err != nil {
				return err
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
}

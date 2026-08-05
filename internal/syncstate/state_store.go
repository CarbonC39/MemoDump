package syncstate

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	snapshotName  = "state.snapshot.json"
	activeWalName = "state.wal.ndjson"
)

// ErrStateCorrupt reports device state that automatic recovery must not guess
// through: a corrupt snapshot, a complete bad-checksum WAL record, or
// corruption before a torn tail. The caller surfaces a repair action.
var ErrStateCorrupt = errors.New("device state corrupt")

// Options configures the device-state store and its background compactor.
type Options struct {
	FS walIO

	// WALBytesThreshold triggers compaction when the active WAL reaches this
	// size AND one of the ratio/records thresholds also holds. Default 1 MiB.
	WALBytesThreshold int64
	// SnapshotRatioThreshold: the active WAL must be at least this fraction of
	// the snapshot size. Default 0.25.
	SnapshotRatioThreshold float64
	// RecordsThreshold: at least this many records in the active WAL.
	// Default 10000.
	RecordsThreshold int64
	// PollInterval for the background compactor. Default 1s.
	PollInterval time.Duration
}

// Metrics exposes cumulative counters for benchmarks and diagnostics.
type Metrics struct {
	Appends              int64
	BytesWritten         int64
	Compactions          int64
	CompactionDurationNs int64
	FsyncCount           int64
	FsyncTotalNs         int64
	WriterLockHoldNs     int64
}

// Store is the durable device-state store for one replica: an in-memory applied
// state backed by a rotating, fsynced NDJSON WAL and a compacted snapshot. It
// must be opened only after the replica's OS lock is held.
type Store struct {
	dir  string
	io   walIO
	opts Options

	mu      sync.Mutex // writer actor: serializes append, rotate, close
	f       *os.File   // active WAL fd (nil after Close)
	nextSeq int64
	nextGen int64

	stateMu      sync.RWMutex
	state        map[string]json.RawMessage
	lastApplied  int64
	walBytes     int64
	walRecords   int64
	snapshotSize int64

	closed         bool
	pendingCompact atomic.Bool
	compMu         sync.Mutex

	metricsMu sync.Mutex
	metrics   Metrics
}

// Open recovers and opens the device-state store for a replica. The caller must
// hold the replica lock first. Recovery replays the snapshot, every frozen WAL
// generation in sequence, then the active WAL; a syntactically partial
// unterminated tail of the active WAL is truncated, while a complete
// bad-checksum record or earlier corruption stops recovery.
func Open(dir string, opts Options) (*Store, error) {
	if opts.FS == nil {
		opts.FS = osWalIO{}
	}
	if opts.WALBytesThreshold == 0 {
		opts.WALBytesThreshold = 1 << 20 // 1 MiB
	}
	if opts.SnapshotRatioThreshold == 0 {
		opts.SnapshotRatioThreshold = 0.25
	}
	if opts.RecordsThreshold == 0 {
		opts.RecordsThreshold = 10000
	}
	if opts.PollInterval == 0 {
		opts.PollInterval = time.Second
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}

	s := &Store{
		dir:   dir,
		io:    opts.FS,
		opts:  opts,
		state: make(map[string]json.RawMessage),
	}

	truncateTo, nextSeq, err := s.recover()
	if err != nil {
		return nil, err
	}
	s.nextSeq = nextSeq
	s.lastApplied = nextSeq - 1 // the recovered watermark (one below the next seq)
	s.nextGen = s.maxFrozenGen() + 1

	f, err := s.io.OpenAppend(filepath.Join(dir, activeWalName))
	if err != nil {
		return nil, fmt.Errorf("open active wal: %w", err)
	}
	if truncateTo >= 0 {
		if err := f.Truncate(truncateTo); err != nil {
			f.Close()
			return nil, fmt.Errorf("truncate torn wal tail: %w", err)
		}
	}
	s.f = f
	return s, nil
}

// recover replays durable state and returns the byte offset to truncate the
// active WAL to (-1 = no truncation) and the next sequence number to allocate
// (one past the maximum durable sequence).
func (s *Store) recover() (truncateTo, nextSeq int64, err error) {
	// Snapshot base.
	if data, rerr := s.io.ReadFile(filepath.Join(s.dir, snapshotName)); rerr == nil {
		snap, perr := parseSnapshot(data)
		if perr != nil {
			return 0, 0, fmt.Errorf("%w: snapshot: %v", ErrStateCorrupt, perr)
		}
		s.state = snap.Data
		s.lastApplied = snap.LastAppliedSeq
		s.snapshotSize = int64(len(data))
	} else if !os.IsNotExist(rerr) {
		return 0, 0, rerr
	}
	nextSeq = s.lastApplied

	// Frozen generations, in sequence.
	gens, err := s.frozenGens()
	if err != nil {
		return 0, 0, err
	}
	for _, gen := range gens {
		data, rerr := s.io.ReadFile(filepath.Join(s.dir, frozenName(gen)))
		if rerr != nil {
			return 0, 0, rerr
		}
		maxSeq, perr := s.replayRecords(data)
		if perr != nil {
			return 0, 0, fmt.Errorf("%w: frozen %d: %v", ErrStateCorrupt, gen, perr)
		}
		if maxSeq > nextSeq {
			nextSeq = maxSeq
		}
	}

	// Active WAL: replay complete lines and locate the torn tail.
	data, rerr := s.io.ReadFile(filepath.Join(s.dir, activeWalName))
	if rerr != nil {
		if os.IsNotExist(rerr) {
			return -1, nextSeq + 1, nil
		}
		return 0, 0, rerr
	}
	lastValidEnd, maxSeq, perr := s.replayActive(data)
	if perr != nil {
		return 0, 0, fmt.Errorf("%w: active wal: %v", ErrStateCorrupt, perr)
	}
	if maxSeq > nextSeq {
		nextSeq = maxSeq
	}
	return lastValidEnd, nextSeq + 1, nil
}

// replayRecords parses every complete line of a frozen WAL generation. Any bad
// record stops recovery.
func (s *Store) replayRecords(data []byte) (maxSeq int64, err error) {
	maxSeq = s.lastApplied
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		rec, perr := parseWalRecord(line)
		if perr != nil {
			return 0, perr
		}
		if err := applyTo(s.state, rec); err != nil {
			return 0, err
		}
		if rec.Seq > maxSeq {
			maxSeq = rec.Seq
		}
	}
	return maxSeq, nil
}

// replayActive parses the active WAL. Every complete line must verify; a
// non-empty final fragment without a newline is a torn tail, truncated to the
// end of the last valid complete line. It returns the truncation offset (or
// len(data) when the file ends cleanly), the max valid seq, and an error for a
// complete bad record or corruption before the tail.
func (s *Store) replayActive(data []byte) (truncateTo, maxSeq int64, err error) {
	maxSeq = s.lastApplied
	offset := int64(0)
	truncateTo = int64(len(data))
	lines := bytes.Split(data, []byte{'\n'})
	for i, line := range lines {
		if i == len(lines)-1 {
			if len(line) > 0 {
				truncateTo = offset // partial tail after the last complete line
			}
			return truncateTo, maxSeq, nil
		}
		if len(line) > 0 {
			rec, perr := parseWalRecord(line)
			if perr != nil {
				return 0, 0, perr
			}
			if err := applyTo(s.state, rec); err != nil {
				return 0, 0, err
			}
			if rec.Seq > maxSeq {
				maxSeq = rec.Seq
			}
		}
		offset += int64(len(line) + 1)
	}
	return truncateTo, maxSeq, nil
}

func applyTo(state map[string]json.RawMessage, rec *walRecord) error {
	p, err := decodePayload(rec.Payload)
	if err != nil {
		return err
	}
	switch rec.Op {
	case walOpPut:
		state[p.Key] = p.Value
	case walOpDelete:
		delete(state, p.Key)
	default:
		return fmt.Errorf("unknown wal op %q", rec.Op)
	}
	return nil
}

// Put durably records that key now holds value, returning its sequence number.
func (s *Store) Put(key string, value any) (int64, error) {
	payload, err := putPayload(key, value)
	if err != nil {
		return 0, err
	}
	return s.append(walOpPut, payload)
}

// Delete durably records that key is gone, returning its sequence number.
func (s *Store) Delete(key string) (int64, error) {
	payload, err := deletePayload(key)
	if err != nil {
		return 0, err
	}
	return s.append(walOpDelete, payload)
}

func (s *Store) append(op string, payload json.RawMessage) (int64, error) {
	start := time.Now()
	s.mu.Lock()
	defer func() {
		s.metricsMu.Lock()
		s.metrics.WriterLockHoldNs += time.Since(start).Nanoseconds()
		s.metricsMu.Unlock()
	}()
	defer s.mu.Unlock()
	if s.closed {
		return 0, errors.New("store is closed")
	}
	seq := s.nextSeq
	rec := &walRecord{SchemaVersion: WalSchemaVersion, Seq: seq, Op: op, Payload: payload}
	line, err := rec.Serialize()
	if err != nil {
		return 0, err
	}
	if err := s.io.WriteAll(s.f, line); err != nil {
		return 0, fmt.Errorf("wal append %d: %w", seq, err)
	}
	syncStart := time.Now()
	if err := s.io.Sync(s.f); err != nil {
		return 0, fmt.Errorf("wal fsync %d: %w", seq, err)
	}
	s.metricsMu.Lock()
	s.metrics.Appends++
	s.metrics.BytesWritten += int64(len(line))
	s.metrics.FsyncCount++
	s.metrics.FsyncTotalNs += time.Since(syncStart).Nanoseconds()
	s.metricsMu.Unlock()
	s.nextSeq = seq + 1

	// Apply to the in-memory state only after the record is durable.
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	if err := applyTo(s.state, rec); err != nil {
		return 0, err
	}
	s.lastApplied = seq
	s.walBytes += int64(len(line))
	s.walRecords++
	return seq, nil
}

// Get returns the current durable value for key, if any.
func (s *Store) Get(key string) ([]byte, bool) {
	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	v, ok := s.state[key]
	return v, ok
}

// Snapshot returns a copy of the applied state.
func (s *Store) Snapshot() map[string]json.RawMessage {
	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	out := make(map[string]json.RawMessage, len(s.state))
	for k, v := range s.state {
		out[k] = v
	}
	return out
}

// LastAppliedSeq returns the greatest durable sequence applied to the state.
func (s *Store) LastAppliedSeq() int64 {
	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	return s.lastApplied
}

// Metrics returns a copy of the cumulative counters.
func (s *Store) Metrics() Metrics {
	s.metricsMu.Lock()
	defer s.metricsMu.Unlock()
	return s.metrics
}

// Close syncs and closes the active WAL. Further appends fail.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	if s.f == nil {
		return nil
	}
	syncErr := s.io.Sync(s.f)
	closeErr := s.f.Close()
	s.f = nil
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}

// --- frozen generation helpers ---------------------------------------------

func frozenName(gen int64) string {
	return fmt.Sprintf("state.wal.%d.frozen.ndjson", gen)
}

func parseFrozenName(name string) (int64, bool) {
	const prefix = "state.wal."
	const suffix = ".frozen.ndjson"
	if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, suffix) {
		return 0, false
	}
	n, err := strconv.ParseInt(strings.TrimSuffix(strings.TrimPrefix(name, prefix), suffix), 10, 64)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

// frozenGens returns the frozen generation numbers in ascending order. Files
// that do not match the frozen pattern (replica.lock, stale temp files, the
// active WAL) are ignored.
func (s *Store) frozenGens() ([]int64, error) {
	entries, err := s.io.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	var gens []int64
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if n, ok := parseFrozenName(e.Name()); ok {
			gens = append(gens, n)
		}
	}
	sort.Slice(gens, func(i, j int) bool { return gens[i] < gens[j] })
	return gens, nil
}

func (s *Store) maxFrozenGen() int64 {
	gens, err := s.frozenGens()
	if err != nil || len(gens) == 0 {
		return 0
	}
	return gens[len(gens)-1]
}

package cloudsync

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// MemoryStore is an in-process RemoteStore used by tests and the engine's
// deterministic simulation harness. It is backed by an append-only change log
// so the sync cursor behaves like a real provider's delta cursor: a valid
// cursor resumes exactly the changes after it (including physical deletions),
// and an invalid or empty cursor falls back to a full baseline scan. Keys
// modified while a scan is paginated simply appear in a later page.
type MemoryStore struct {
	mu      sync.Mutex
	objects map[string]*memoryObject
	log     []logEntry
	seq     int64
	faults  []Fault
}

type memoryObject struct {
	data    []byte
	version string
}

type logEntry struct {
	seq     int64
	key     string
	typ     ChangeType
	version string
}

// Fault fails the next matching operation with err. Op is one of
// "create", "replace", "remove", "read", "list". Faults are consumed in order.
// The mode flags shape the failure:
//   - UncertainWrite: the create/replace SUCCEEDS but returns Error, so the
//     caller cannot know whether the write landed and must re-read;
//   - CursorReject: List ignores even a valid cursor and returns a full
//     baseline (as if the cursor were rejected by the provider);
//   - IncompleteSkip: List omits the last N keys from an otherwise complete
//     listing, simulating an incomplete scan.
type Fault struct {
	Op             string
	Error          *StoreError
	UncertainWrite bool
	CursorReject   bool
	IncompleteSkip int
}

// NewMemoryStore creates an empty in-memory store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{objects: make(map[string]*memoryObject)}
}

// ArmFault queues a deterministic fault to fail the next matching operation.
func (s *MemoryStore) ArmFault(op string, err *StoreError) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.faults = append(s.faults, Fault{Op: op, Error: err})
}

// ArmUncertainWrite queues a fault where the next create/replace performs the
// write but returns err, so the caller must re-read to learn the outcome.
func (s *MemoryStore) ArmUncertainWrite(op string, err *StoreError) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.faults = append(s.faults, Fault{Op: op, Error: err, UncertainWrite: true})
}

// ArmCursorReject queues a list fault that ignores a valid cursor and returns
// a full baseline.
func (s *MemoryStore) ArmCursorReject() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.faults = append(s.faults, Fault{Op: "list", CursorReject: true})
}

// ArmIncompleteList queues a list fault that omits the last skip keys.
func (s *MemoryStore) ArmIncompleteList(skip int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.faults = append(s.faults, Fault{Op: "list", IncompleteSkip: skip})
}

func (s *MemoryStore) takeFault(op string) *Fault {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, f := range s.faults {
		if f.Op == op {
			s.faults = append(s.faults[:i], s.faults[i+1:]...)
			return &f
		}
	}
	return nil
}

func (s *MemoryStore) nextSeq() int64 {
	s.seq++
	return s.seq
}

func (s *MemoryStore) versionString(seq int64) string {
	return strconv.FormatInt(seq, 10)
}

// Test reports the in-memory provider's capabilities.
func (s *MemoryStore) Test(ctx context.Context) (Capabilities, error) {
	if err := ctx.Err(); err != nil {
		return Capabilities{}, err
	}
	return Capabilities{
		ConditionalWrites: true,
		PagedListing:      true,
		DeltaCursor:       true,
	}, nil
}

// Create stores data under key, failing if the key already exists.
func (s *MemoryStore) Create(ctx context.Context, key string, data []byte) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	fault := s.takeFault("create")
	if fault != nil && !fault.UncertainWrite {
		return "", fault.Error
	}
	s.mu.Lock()
	if _, ok := s.objects[key]; ok {
		s.mu.Unlock()
		return "", &StoreError{Kind: ErrPreconditionFailed, Message: fmt.Sprintf("key %q exists", key)}
	}
	seq := s.nextSeq()
	s.objects[key] = &memoryObject{data: append([]byte(nil), data...), version: s.versionString(seq)}
	s.log = append(s.log, logEntry{seq: seq, key: key, typ: ChangeCreated, version: s.versionString(seq)})
	version := s.versionString(seq)
	s.mu.Unlock()
	if fault != nil {
		// The write landed but the response was lost: the caller re-reads.
		return "", fault.Error
	}
	return version, nil
}

// Replace stores data only when expectedVersion matches the current version.
func (s *MemoryStore) Replace(ctx context.Context, key string, data []byte, expectedVersion string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	fault := s.takeFault("replace")
	if fault != nil && !fault.UncertainWrite {
		return "", fault.Error
	}
	s.mu.Lock()
	obj, ok := s.objects[key]
	if !ok {
		s.mu.Unlock()
		return "", &StoreError{Kind: ErrNotFound, Message: fmt.Sprintf("key %q missing", key)}
	}
	if obj.version != expectedVersion {
		s.mu.Unlock()
		return "", &StoreError{Kind: ErrPreconditionFailed, Message: "stale expected version"}
	}
	seq := s.nextSeq()
	obj.data = append([]byte(nil), data...)
	obj.version = s.versionString(seq)
	s.log = append(s.log, logEntry{seq: seq, key: key, typ: ChangeUpdated, version: s.versionString(seq)})
	version := s.versionString(seq)
	s.mu.Unlock()
	if fault != nil {
		// The write landed but the response was lost: the caller re-reads.
		return "", fault.Error
	}
	return version, nil
}

// Remove physically deletes a key, recording a ChangeDeleted in the log. This
// is not part of the RemoteStore contract (V1 propagates deletions as entity
// tombstones) but exists so the change log can express physical removals — the
// engine treats a key reported physically removed after a known baseline as
// repository damage, never as a deletion.
func (s *MemoryStore) Remove(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.objects[key]; !ok {
		return &StoreError{Kind: ErrNotFound, Message: fmt.Sprintf("key %q missing", key)}
	}
	delete(s.objects, key)
	s.log = append(s.log, logEntry{seq: s.nextSeq(), key: key, typ: ChangeDeleted})
	return nil
}

// Read returns the bytes and version of a key.
func (s *MemoryStore) Read(ctx context.Context, key string) ([]byte, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	if f := s.takeFault("read"); f != nil {
		return nil, "", f.Error
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	obj, ok := s.objects[key]
	if !ok {
		return nil, "", &StoreError{Kind: ErrNotFound, Message: fmt.Sprintf("key %q missing", key)}
	}
	return append([]byte(nil), obj.data...), obj.version, nil
}

// Seed installs an object at an explicit version, used by the scenario runner
// to rebuild durable remote state across restarts with stable versions. It is
// not part of the RemoteStore contract.
func (s *MemoryStore) Seed(key string, data []byte, version string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.objects[key]; ok {
		return &StoreError{Kind: ErrPreconditionFailed, Message: fmt.Sprintf("key %q exists", key)}
	}
	seq, err := strconv.ParseInt(version, 10, 64)
	if err != nil || seq <= 0 {
		return fmt.Errorf("seed: bad version %q", version)
	}
	s.objects[key] = &memoryObject{data: append([]byte(nil), data...), version: version}
	s.log = append(s.log, logEntry{seq: seq, key: key, typ: ChangeCreated, version: version})
	if seq > s.seq {
		s.seq = seq
	}
	return nil
}

// pendingChange carries the change plus the log seq used for delta pagination.
type pendingChange struct {
	seq    int64
	change Change
}

// snapshotAt reconstructs the set of keys and versions present at the given log
// position (inclusive). The log is append-only, so this is deterministic: a
// baseline paginated with the same watermark reproduces the same snapshot on
// every page.
func (s *MemoryStore) snapshotAt(watermark int64) map[string]string {
	state := make(map[string]string)
	for _, e := range s.log {
		if e.seq > watermark {
			break
		}
		if e.typ == ChangeDeleted {
			delete(state, e.key)
		} else {
			state[e.key] = e.version
		}
	}
	return state
}

// buildBaseline returns the baseline pending changes at watermark, resuming
// after `after` ("" = from the start). ok is false when the continuation key is
// absent from the snapshot at that watermark.
func (s *MemoryStore) buildBaseline(prefix string, watermark int64, after string) ([]pendingChange, bool) {
	snapshot := s.snapshotAt(watermark)
	keys := sortedPrefixedKeys(snapshot, prefix)
	start := 0
	if after != "" {
		idx := sort.SearchStrings(keys, after)
		if idx >= len(keys) || keys[idx] != after {
			return nil, false
		}
		start = idx + 1
	}
	changes := make([]pendingChange, 0, len(keys)-start)
	for _, k := range keys[start:] {
		changes = append(changes, pendingChange{change: Change{Key: k, Type: ChangeCreated, Version: snapshot[k]}})
	}
	return changes, true
}

// resetBaseline returns a full baseline scan at the current log position.
func (s *MemoryStore) resetBaseline(prefix string) ([]pendingChange, int64) {
	changes, _ := s.buildBaseline(prefix, s.seq, "")
	return changes, s.seq
}

// List returns a page of changes under prefix. The cursor may be:
//
//   - "" or garbage: a full baseline scan at the CURRENT log position;
//   - "base:<watermark>:<key>": the continuation of a paginated baseline scan;
//   - an integer: the delta position to resume changes after it.
//
// A baseline scan pins a watermark (its SyncCursor) for ALL its pages, so a key
// modified while pagination is in flight stays out of the baseline and is
// reported by the next delta round. NextCursor continues the current scan;
// SyncCursor is the position to persist and pass to the next List. A full
// listing enumerates the complete key set; a faulted incomplete listing is
// damage the caller must detect.
func (s *MemoryStore) List(ctx context.Context, prefix, cursor string) (ChangePage, error) {
	if err := ctx.Err(); err != nil {
		return ChangePage{}, err
	}
	fault := s.takeFault("list")
	if fault != nil && fault.Error != nil {
		return ChangePage{}, fault.Error
	}
	if fault != nil && fault.CursorReject {
		// The provider rejects even a valid cursor: fall back to a full
		// baseline scan so no event is ever skipped.
		cursor = ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	var pending []pendingChange
	var syncCursor int64
	delta := false

	switch {
	case strings.HasPrefix(cursor, "base:"):
		// Continue a paginated baseline scan. The token carries the watermark
		// captured by the FIRST page. The watermark and the continuation key are
		// validated: a malformed, out-of-range, or stale token resets to a full
		// baseline at the current position rather than emitting a future cursor.
		parts := strings.SplitN(cursor, ":", 3)
		var watermark int64
		valid := len(parts) == 3
		if valid {
			parsed, err := strconv.ParseInt(parts[1], 10, 64)
			valid = err == nil && parsed >= 0 && parsed <= s.seq
			if valid {
				watermark = parsed
			}
		}
		if !valid {
			pending, syncCursor = s.resetBaseline(prefix)
			break
		}
		changes, ok := s.buildBaseline(prefix, watermark, parts[2])
		if !ok {
			// Continuation key is not in the snapshot at that watermark: reset.
			pending, syncCursor = s.resetBaseline(prefix)
			break
		}
		pending = changes
		syncCursor = watermark

	case cursor != "":
		seq, err := strconv.ParseInt(cursor, 10, 64)
		if err != nil || seq < 0 || seq > s.seq {
			// Invalid or out-of-range delta cursor: reset to a full baseline so
			// no later event is ever skipped.
			pending, syncCursor = s.resetBaseline(prefix)
			break
		}
		delta = true
		for _, e := range s.log {
			if e.seq <= seq || !strings.HasPrefix(e.key, prefix) {
				continue
			}
			pending = append(pending, pendingChange{seq: e.seq, change: Change{Key: e.key, Type: e.typ, Version: e.version}})
			syncCursor = e.seq
		}
		if syncCursor == 0 {
			syncCursor = seq // nothing new: resume from the same position
		}

	default:
		// Empty cursor: full baseline scan at the current position.
		pending, syncCursor = s.resetBaseline(prefix)
	}

	if fault != nil && fault.IncompleteSkip > 0 && !delta {
		// Damage: the listing silently omits the last keys. Never report this
		// as "everything is fine" — the caller treats an incomplete full list
		// as suspect and re-lists.
		if len(pending) > fault.IncompleteSkip {
			pending = pending[:len(pending)-fault.IncompleteSkip]
		} else {
			pending = nil
		}
	}

	const pageSize = 100
	page := pending
	next := ""
	if len(pending) > pageSize {
		page = pending[:pageSize]
		if delta {
			next = strconv.FormatInt(page[len(page)-1].seq, 10)
		} else {
			next = fmt.Sprintf("base:%d:%s", syncCursor, page[len(page)-1].change.Key)
		}
	}
	out := make([]Change, len(page))
	for i, pc := range page {
		out[i] = pc.change
	}
	return ChangePage{Changes: out, NextCursor: next, SyncCursor: strconv.FormatInt(syncCursor, 10)}, nil
}

func sortedPrefixedKeys(m map[string]string, prefix string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys
}

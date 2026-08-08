package cloudsync

import (
	"fmt"
	"sort"
	"strings"
)

// The pure reconciliation engine. DecideEntity implements the known-baseline
// and no-baseline tables from the lite spec exactly once, as a pure function
// with no I/O and no retry loops; DecideRepository orders the resulting
// decisions repository-wide. The Go and TypeScript implementations must emit
// identical decisions for every shared scenario trace (testdata/sync/scenarios).

// LocalState classifies one Sync ID's local observation.
type LocalState int

const (
	// LocalLive: the indexed path exists and is readable; Entity carries the
	// canonical entity.
	LocalLive LocalState = iota
	// LocalAbsent: the indexed path is gone. Absence is an observation, not a
	// deletion, until a usable baseline proves the entity existed remotely.
	LocalAbsent
	// LocalUnknown: blocked (symlink/kind flip), unstable, or unreadable. It is
	// never treated as absent.
	LocalUnknown
)

func (s LocalState) String() string {
	switch s {
	case LocalLive:
		return "live"
	case LocalAbsent:
		return "absent"
	case LocalUnknown:
		return "unknown"
	}
	return "unknown"
}

// RemoteState classifies one Sync ID's remote observation.
type RemoteState int

const (
	// RemoteLive: a valid live entity record at the expected version.
	RemoteLive RemoteState = iota
	// RemoteTombstone: a valid entity record with deleted=true. A physically
	// removed key is RemoteMissing, never a tombstone.
	RemoteTombstone
	// RemoteMissing: the key is physically absent. That is repository damage,
	// not a deletion signal.
	RemoteMissing
	// RemoteInvalid: an unreadable, malformed, or invalid record. It is never
	// materialized.
	RemoteInvalid
)

func (s RemoteState) String() string {
	switch s {
	case RemoteLive:
		return "live"
	case RemoteTombstone:
		return "tombstone"
	case RemoteMissing:
		return "missing"
	case RemoteInvalid:
		return "invalid"
	}
	return "unknown"
}

// LocalObservation is the immutable local input for one Sync ID.
type LocalObservation struct {
	SyncID   string
	Kind     string
	State    LocalState
	Entity   *Entity // canonical entity when State == LocalLive
	Revision string  // opaque local CAS token; "" when absent/unknown
}

// RemoteObservation is the immutable remote input for one Sync ID.
type RemoteObservation struct {
	SyncID    string
	Kind      string
	State     RemoteState
	Entity    *Entity // when State is RemoteLive or RemoteTombstone
	Version   string  // opaque provider version
	Retryable bool    // when Invalid: a retryable outcome vs a hard error
}

// Baseline is the usable snapshot baseline for one Sync ID, when present.
type Baseline struct {
	ContentHash   string
	Deleted       bool
	RemoteVersion string
}

// Annotations are pre-computed structural conflicts that force a block.
type Annotations struct {
	PathConflict       bool // two entities want the same representable path
	ParentCycle        bool // the folder parent graph contains a cycle
	StructuralConflict bool // folder/subtree structural ambiguity
}

// DecisionKind is a normalized sync decision.
type DecisionKind int

const (
	// DecisionNoop: nothing to do this cycle.
	DecisionNoop DecisionKind = iota
	// DecisionEstablishBaseline: local and remote are known equal; record the
	// baseline (contentHash, deleted, remote version).
	DecisionEstablishBaseline
	// DecisionPullLive: create/replace the local entity from the remote live
	// entity using the local revision CAS.
	DecisionPullLive
	// DecisionPushLive: create-if-absent or replace-if-version the remote
	// entity from local content.
	DecisionPushLive
	// DecisionPushTombstone: replace the remote live record with a tombstone
	// using its current version CAS.
	DecisionPushTombstone
	// DecisionApplyTombstone: write a recovery copy, then delete the local
	// entity using the local revision CAS.
	DecisionApplyTombstone
	// DecisionCreateConflict: preserve one divergent side as a deterministic
	// conflict entity and handle the original (keep remote live, or tombstone).
	DecisionCreateConflict
	// DecisionRepairIndex: an index/identity repair (ambiguous absence, or
	// cleaning a live path mapping after a converged deletion).
	DecisionRepairIndex
	// DecisionBlock: a structural/path conflict or invalid data; no mutation.
	DecisionBlock
	// DecisionRetry: a retryable transport outcome; no decision was reached.
	DecisionRetry
)

func (k DecisionKind) String() string {
	switch k {
	case DecisionNoop:
		return "noop"
	case DecisionEstablishBaseline:
		return "establish-baseline"
	case DecisionPullLive:
		return "pull-live"
	case DecisionPushLive:
		return "push-live"
	case DecisionPushTombstone:
		return "push-tombstone"
	case DecisionApplyTombstone:
		return "apply-tombstone"
	case DecisionCreateConflict:
		return "create-conflict"
	case DecisionRepairIndex:
		return "repair-index"
	case DecisionBlock:
		return "block"
	case DecisionRetry:
		return "retry"
	}
	return "unknown"
}

// ConflictInfo carries the deterministic conflict-copy plan for
// DecisionCreateConflict. The conflict identity is derived from the fixed-role
// state hashes (local, then remote), so a crash or lost response repeats the
// same conflict copy rather than producing a second one.
type ConflictInfo struct {
	SourceSyncID            string // the original Sync ID
	ConflictSyncID          string // the derived UUID v5 conflict identity
	ConflictEntity          *Entity
	OriginalTombstone       bool    // the original Sync ID becomes (or stays) a tombstone
	OriginalVersion         string  // remote CAS version to tombstone the original ("" when already tombstoned)
	AcceptRemoteOriginal    bool    // accept the remote live entity onto the original Sync ID
	OriginalEntity          *Entity // the entity to apply to the original Sync ID locally (case A pull)
	OriginalTombstoneEntity *Entity // the tombstone record to push to the original remotely (case C)
	LocalStateHash          string
	RemoteStateHash         string
}

// Decision is the normalized output for one Sync ID.
type Decision struct {
	SyncID        string
	Kind          DecisionKind
	Reason        string
	ParentID      string
	ContentHash   string
	Deleted       bool
	Version       string // remote version: CAS for pushes, target for pulls/establishes
	LocalRevision string // expected local CAS token for local mutations
	Entity        *Entity
	Conflict      *ConflictInfo
}

// newDecision seeds a decision with the Sync ID and its parent (when known).
func newDecision(syncID, parentID string) Decision {
	return Decision{SyncID: syncID, ParentID: parentID}
}

// entityKind returns the entity kind from whichever observation carries it.
func entityKind(l LocalObservation, r RemoteObservation) string {
	if l.Kind != "" {
		return l.Kind
	}
	if l.Entity != nil {
		return l.Entity.Kind
	}
	if r.Kind != "" {
		return r.Kind
	}
	if r.Entity != nil {
		return r.Entity.Kind
	}
	return ""
}

// entityHash returns the canonical content hash of an entity.
func entityHash(e *Entity) string {
	if e == nil {
		return ""
	}
	return e.ContentHash
}

// DecideEntity computes the normalized decision for one Sync ID from its
// immutable local observation, remote observation, optional usable baseline,
// and structural annotations. It performs no I/O. Blocked/unknown and invalid
// inputs always produce block/retry, never a deletion.
func DecideEntity(l LocalObservation, r RemoteObservation, b *Baseline, ann Annotations) Decision {
	kind := entityKind(l, r)
	d := newDecision(l.SyncID, parentOf(l, r))
	if d.SyncID == "" {
		d.SyncID = r.SyncID
	}

	if ann.PathConflict || ann.ParentCycle || ann.StructuralConflict {
		return d.block("path/graph structural conflict")
	}
	if l.State == LocalUnknown {
		return d.block("local unknown (blocked/unstable/unreadable)")
	}
	if r.State == RemoteInvalid {
		if r.Retryable {
			return d.retry("invalid remote record, retryable")
		}
		return d.block("invalid remote record")
	}

	// A physically missing remote object is damage, never a tombstone: with
	// local content we re-create it (create-if-absent heals the loss); without
	// it the absence is ambiguous and needs repair.
	if r.State == RemoteMissing {
		if l.State == LocalLive {
			return d.pushLive(l.Entity, "", l.Revision)
		}
		return d.repairIndex("indexed local absence plus physically absent remote object is ambiguous")
	}

	if b == nil {
		return decideNoBaseline(l, r, kind)
	}
	return decideWithBaseline(l, r, b, kind)
}

func parentOf(l LocalObservation, r RemoteObservation) string {
	if l.Entity != nil {
		return l.Entity.ParentID
	}
	if r.Entity != nil {
		return r.Entity.ParentID
	}
	return ""
}

// decideNoBaseline implements spec §7.2.
func decideNoBaseline(l LocalObservation, r RemoteObservation, kind string) Decision {
	d := newDecision(l.SyncID, parentOf(l, r))
	lHash := entityHash(l.Entity)

	switch l.State {
	case LocalAbsent:
		switch r.State {
		case RemoteLive:
			// Remote-only live content: reserve the Sync ID/path in the index,
			// then create it locally only-if-absent.
			return d.pullLive(r.Entity, r.Version, "")
		case RemoteTombstone:
			// Local absence plus a remote tombstone establishes a deleted
			// baseline and removes no user content.
			return d.establishBaseline(entityHash(r.Entity), true, r.Version)
		}
	case LocalLive:
		switch r.State {
		case RemoteLive:
			if lHash == r.Entity.ContentHash {
				return d.establishBaseline(lHash, false, r.Version)
			}
			if kind == KindNote {
				return d.createConflict(l, r, false, "")
			}
			return d.block("structural divergence without a baseline")
		case RemoteTombstone:
			if kind == KindNote {
				return d.createConflict(l, r, true, "")
			}
			return d.block("folder live vs remote tombstone without a baseline")
		case RemoteMissing:
			// Local-only content creates the remote object only-if-absent.
			return d.pushLive(l.Entity, "", l.Revision)
		}
	}
	return d.block("no usable baseline and no matching rule")
}

// decideWithBaseline implements spec §7.1.
func decideWithBaseline(l LocalObservation, r RemoteObservation, b *Baseline, kind string) Decision {
	d := newDecision(l.SyncID, parentOf(l, r))
	lHash := entityHash(l.Entity)

	switch l.State {
	case LocalLive:
		switch r.State {
		case RemoteLive:
			rHash := r.Entity.ContentHash
			switch {
			case lHash == rHash:
				// L == R: establish/refresh the baseline; no-op when it already
				// matches.
				if !b.Deleted && b.ContentHash == lHash {
					return d.noop("local and remote unchanged")
				}
				return d.establishBaseline(lHash, false, r.Version)
			case b.Deleted:
				// Baseline deleted but local is live: the entity was recreated
				// locally. The baseline state is (bHash, deleted=true), so a
				// remote LIVE record never equals it regardless of the hash.
				if lHash == rHash {
					// L == R (both live with the same content): establish a
					// live baseline.
					return d.establishBaseline(lHash, false, r.Version)
				}
				// Divergent: keep both; the remote live entity stays on the
				// original Sync ID.
				if kind == KindNote {
					return d.createConflict(l, r, false, "")
				}
				return d.block("folder structural conflict over a deleted baseline")
			case lHash == b.ContentHash:
				// L == B and R != B: pull the remote change with the local
				// revision CAS.
				return d.pullLive(r.Entity, r.Version, l.Revision)
			case rHash == b.ContentHash:
				// R == B and L != B: push the local change with the baseline
				// remote version CAS.
				return d.pushLive(l.Entity, b.RemoteVersion, l.Revision)
			default:
				// L != B, R != B, L != R: divergent live edits.
				if kind == KindNote {
					return d.createConflict(l, r, false, "")
				}
				return d.block("folder structural conflict")
			}
		case RemoteTombstone:
			rHash := r.Entity.ContentHash
			switch {
			case b.Deleted:
				// Baseline deleted; local recreated; remote tombstone. When the
				// remote tombstone matches the baseline, push the recreation.
				if rHash == b.ContentHash {
					return d.pushLive(l.Entity, b.RemoteVersion, l.Revision)
				}
				if kind == KindNote {
					return d.createConflict(l, r, true, "")
				}
				return d.block("folder recreated over a divergent tombstone")
			case lHash == b.ContentHash:
				// Local unchanged live baseline vs remote tombstone: write a
				// recovery copy, then delete locally with the revision CAS.
				return d.applyTombstone(r.Version, l.Revision)
			default:
				// Locally edited live vs remote tombstone: preserve the local
				// edit as a conflict entity, then recover/delete the original.
				if kind == KindNote {
					return d.createConflict(l, r, true, "")
				}
				return d.block("folder edited over a remote tombstone")
			}
		}
	case LocalAbsent:
		switch r.State {
		case RemoteLive:
			rHash := r.Entity.ContentHash
			switch {
			case b.Deleted:
				// L == B (deleted) and R != B: pull the recreated remote entity.
				return d.pullLive(r.Entity, r.Version, "")
			case !b.Deleted && rHash == b.ContentHash:
				// Local absent, remote unchanged live baseline: replace the
				// remote with a tombstone using its version CAS. The tombstone
				// is the live entity with deleted=true, so its content hash is
				// preserved.
				return d.pushTombstone(r.Entity, b.RemoteVersion, "")
			case kind == KindNote:
				// Local absent, remote edited live: preserve the remote edit as
				// a deterministic conflict entity, then tombstone the original
				// with its current version.
				return d.createConflictRemote(l, r, rHash)
			default:
				return d.block("folder absent vs divergent remote live")
			}
		case RemoteTombstone:
			// Local absent + remote tombstone: the deletion has converged. When
			// the baseline is already deleted and matches, nothing to do;
			// otherwise record the deleted baseline. The coordinator removes the
			// live path mapping.
			if b.Deleted && b.ContentHash == r.Entity.ContentHash {
				return d.noop("converged deletion")
			}
			return d.establishBaseline(r.Entity.ContentHash, true, r.Version)
		}
	}
	return d.block("no matching rule")
}

// --- decision builders -------------------------------------------------------

func (d Decision) noop(reason string) Decision {
	d.Kind = DecisionNoop
	d.Reason = reason
	return d
}

func (d Decision) establishBaseline(hash string, deleted bool, version string) Decision {
	d.Kind = DecisionEstablishBaseline
	d.ContentHash = hash
	d.Deleted = deleted
	d.Version = version
	d.Reason = "local and remote known equal"
	return d
}

func (d Decision) pullLive(entity *Entity, version, localRevision string) Decision {
	d.Kind = DecisionPullLive
	d.Entity = entity
	d.ContentHash = entityHash(entity)
	d.Version = version
	d.LocalRevision = localRevision
	d.Reason = "remote changed"
	return d
}

func (d Decision) pushLive(entity *Entity, version, localRevision string) Decision {
	d.Kind = DecisionPushLive
	d.Entity = entity
	d.ContentHash = entityHash(entity)
	d.Version = version // "" = create-if-absent
	d.LocalRevision = localRevision
	if version == "" {
		d.Reason = "local-only; create remote if-absent"
	} else {
		d.Reason = "local changed"
	}
	return d
}

func (d Decision) pushTombstone(live *Entity, version, localRevision string) Decision {
	// The tombstone is the live entity with deleted=true, so its content hash
	// (over kind/parent/name/markdown) is preserved and the record stays valid.
	e := *live
	e.Deleted = true
	d.Kind = DecisionPushTombstone
	d.Entity = &e
	d.ContentHash = e.ContentHash
	d.Deleted = true
	d.Version = version
	d.LocalRevision = localRevision
	d.Reason = "locally deleted; replace remote with tombstone"
	return d
}

func (d Decision) applyTombstone(version, localRevision string) Decision {
	d.Kind = DecisionApplyTombstone
	d.Deleted = true
	d.Version = version
	d.LocalRevision = localRevision
	d.Reason = "remote tombstone; write recovery and delete locally"
	return d
}

func (d Decision) repairIndex(reason string) Decision {
	d.Kind = DecisionRepairIndex
	d.Reason = reason
	return d
}

func (d Decision) block(reason string) Decision {
	d.Kind = DecisionBlock
	d.Reason = reason
	return d
}

func (d Decision) retry(reason string) Decision {
	d.Kind = DecisionRetry
	d.Reason = reason
	return d
}

// createConflict preserves the LOCAL side as the conflict entity for a
// divergent note. originalTombstone reports whether the original Sync ID
// becomes (or stays) a tombstone; when false the remote live entity stays on
// the original (AcceptRemoteOriginal).
func (d Decision) createConflict(l LocalObservation, r RemoteObservation, originalTombstone bool, originalVersion string) Decision {
	d.Kind = DecisionCreateConflict
	lHash := entityHash(l.Entity)
	rHash := entityHash(r.Entity)
	localStateHash := StateHash(lHash, false)
	remoteStateHash := StateHash(rHash, r.State == RemoteTombstone)
	conflictID, err := DeriveConflictSyncID(d.SyncID, localStateHash, remoteStateHash)
	if err != nil {
		return d.block("cannot derive conflict identity")
	}
	stem := l.Entity.Name
	conflictEntity := cloneAsConflict(l.Entity, conflictID, stem)
	var originalEntity *Entity
	if !originalTombstone {
		originalEntity = r.Entity // accept the remote live entity onto the original
	}
	d.Conflict = &ConflictInfo{
		SourceSyncID:         d.SyncID,
		ConflictSyncID:       conflictID,
		ConflictEntity:       conflictEntity,
		OriginalTombstone:    originalTombstone,
		OriginalVersion:      originalVersion,
		AcceptRemoteOriginal: !originalTombstone,
		OriginalEntity:       originalEntity,
		LocalStateHash:       localStateHash,
		RemoteStateHash:      remoteStateHash,
	}
	d.ContentHash = entityHash(conflictEntity)
	d.Reason = "divergent edits; keep both via a deterministic conflict copy"
	return d
}

// createConflictRemote preserves the REMOTE live side as the conflict entity
// (local absent vs remote edited live), then tombstones the original with its
// current version.
func (d Decision) createConflictRemote(l LocalObservation, r RemoteObservation, rHash string) Decision {
	d.Kind = DecisionCreateConflict
	lHash := ""
	if l.Entity != nil {
		lHash = entityHash(l.Entity)
	}
	localStateHash := StateHash(lHash, true) // local absent side is deleted
	remoteStateHash := StateHash(rHash, false)
	conflictID, err := DeriveConflictSyncID(d.SyncID, localStateHash, remoteStateHash)
	if err != nil {
		return d.block("cannot derive conflict identity")
	}
	stem := r.Entity.Name
	conflictEntity := cloneAsConflict(r.Entity, conflictID, stem)
	tomb := *r.Entity
	tomb.Deleted = true // the tombstone keeps the remote edit's identity and hash
	d.Conflict = &ConflictInfo{
		SourceSyncID:            d.SyncID,
		ConflictSyncID:          conflictID,
		ConflictEntity:          conflictEntity,
		OriginalTombstone:       true,
		OriginalVersion:         r.Version,
		AcceptRemoteOriginal:    false,
		OriginalTombstoneEntity: &tomb,
		LocalStateHash:          localStateHash,
		RemoteStateHash:         remoteStateHash,
	}
	d.ContentHash = entityHash(conflictEntity)
	d.Reason = "local absent vs remote edit; keep remote edit as conflict, tombstone original"
	return d
}

// cloneAsConflict renames a note copy into a deterministic conflict entity
// while preserving its parent, kind, markdown, and attribution.
func cloneAsConflict(src *Entity, conflictID, stem string) *Entity {
	cp := *src
	cp.SyncID = conflictID
	cp.Name = strings.TrimSuffix(ConflictFilename(stem, conflictID), ".md")
	cp.Deleted = false
	cp.ContentHash = cp.ComputeContentHash()
	return &cp
}

// --- repository-wide planning order ------------------------------------------

// DecideRepository orders the per-entity decisions repository-wide:
//
//  1. conflict creation before any destructive action on the original;
//  2. parents before live children (pull/push/establish);
//  3. tombstones child-first (children before their folders);
//  4. bookkeeping (baselines, index repair, noops) after actions;
//  5. blocks and retries last, sorted deterministically.
//
// The result is deterministic so Go and TypeScript emit the identical
// normalized decision sequence for the same input set.
func DecideRepository(decisions []Decision) []Decision {
	live := make([]Decision, 0, len(decisions))
	tombstones := make([]Decision, 0, len(decisions))
	conflicts := make([]Decision, 0, len(decisions))
	bookkeeping := make([]Decision, 0, len(decisions))
	blocked := make([]Decision, 0, len(decisions))
	for _, d := range decisions {
		switch d.Kind {
		case DecisionCreateConflict:
			conflicts = append(conflicts, d)
		case DecisionPullLive, DecisionPushLive, DecisionEstablishBaseline:
			live = append(live, d)
		case DecisionPushTombstone, DecisionApplyTombstone:
			tombstones = append(tombstones, d)
		case DecisionNoop, DecisionRepairIndex:
			bookkeeping = append(bookkeeping, d)
		case DecisionBlock, DecisionRetry:
			blocked = append(blocked, d)
		}
	}
	order := make([]Decision, 0, len(decisions))
	order = append(order, stableParentsFirst(conflicts)...)
	order = append(order, stableParentsFirst(live)...)
	order = append(order, stableChildrenFirst(tombstones)...)
	order = append(order, stableParentsFirst(bookkeeping)...)
	order = append(order, stableBySyncID(blocked)...)
	return order
}

// stableParentsFirst orders a slice so parents precede their children. It is a
// deterministic Kahn-style topological sort: each round emits the earliest
// remaining node (by original position) whose parent is not still pending. A
// cycle (or a self-parent) cannot hang planning — the remaining nodes are
// emitted in their original order and the engine surfaces the cycle as a block.
func stableParentsFirst(in []Decision) []Decision {
	if len(in) < 2 {
		return in
	}
	pos := make(map[string]int, len(in))
	remaining := make(map[string]Decision, len(in))
	for i, d := range in {
		pos[d.SyncID] = i
		remaining[d.SyncID] = d
	}
	out := make([]Decision, 0, len(in))
	for len(remaining) > 0 {
		bestPos, best := -1, ""
		for _, d := range in {
			if _, ok := remaining[d.SyncID]; !ok {
				continue
			}
			if d.ParentID != "" {
				if _, ok := remaining[d.ParentID]; ok {
					continue // parent not yet emitted
				}
			}
			if bestPos == -1 || pos[d.SyncID] < bestPos {
				bestPos, best = pos[d.SyncID], d.SyncID
			}
		}
		if best == "" {
			// Cycle: emit the remainder in original order so planning never
			// hangs; the affected cycle is surfaced separately as a block.
			for _, d := range in {
				if _, ok := remaining[d.SyncID]; ok {
					out = append(out, d)
					delete(remaining, d.SyncID)
				}
			}
			break
		}
		out = append(out, remaining[best])
		delete(remaining, best)
	}
	return out
}

// stableChildrenFirst is stableParentsFirst in reverse: children precede their
// parents, which is the required order for tombstone application.
func stableChildrenFirst(in []Decision) []Decision {
	if len(in) < 2 {
		return in
	}
	reversed := append([]Decision(nil), in...)
	for i, j := 0, len(reversed)-1; i < j; i, j = i+1, j-1 {
		reversed[i], reversed[j] = reversed[j], reversed[i]
	}
	out := stableParentsFirst(reversed)
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

func stableBySyncID(in []Decision) []Decision {
	out := append([]Decision(nil), in...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].SyncID < out[j].SyncID })
	return out
}

// String renders a decision for fixtures and diagnostics.
func (d Decision) String() string {
	s := fmt.Sprintf("%s %s", d.SyncID, d.Kind)
	if d.Reason != "" {
		s += " (" + d.Reason + ")"
	}
	return s
}

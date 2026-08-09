package cloudsync

import (
	"fmt"
	"strings"
)

// The note-only reconciliation engine. DecideNote implements the known-baseline
// and no-baseline tables from spec §7 exactly once, as a pure function with no
// I/O, no folder branch, and no multi-entity action graph. Conflict preservation
// is a named compound outcome whose execution order is fixed by its name (see
// spec §8). NoteLocalObservation carries the same canonical content hash as the
// remote note record, so local/remote equality is a direct hash comparison.

// NoteLocalObservation is the immutable local input for one Sync ID. It
// describes a Markdown note only.
type NoteLocalObservation struct {
	SyncID      string
	State       LocalState
	Path        string // indexed local path ("" when unknown)
	Markdown    string // canonical LF body when State == LocalLive
	ContentHash string // NoteRecord content hash when State == LocalLive
	Revision    string // opaque local filesystem CAS token; "" when absent/unknown
}

// NoteRemoteObservation is the immutable remote input for one Sync ID.
type NoteRemoteObservation struct {
	SyncID      string
	State       RemoteState
	Path        string // portable remote path
	Markdown    string // when State == RemoteLive
	ContentHash string // when State is RemoteLive or RemoteTombstone
	Version     string // opaque provider version
	Retryable   bool   // when Invalid: a retryable outcome vs a hard error
}

// NoteDecisionKind is a normalized per-note sync decision. The compound
// preservation outcomes are named in execution order: preserve the divergent
// side as a deterministic conflict note FIRST, then act on the original.
type NoteDecisionKind int

const (
	// NoteNoop: nothing to do this cycle.
	NoteNoop NoteDecisionKind = iota
	// NoteEstablishBaseline: local and remote are known equal; record the
	// baseline (contentHash, deleted, remote version).
	NoteEstablishBaseline
	// NotePushLive: create-if-absent or replace-if-version the remote note from
	// local content.
	NotePushLive
	// NotePullLive: create/replace the local note from the remote note using the
	// local revision CAS.
	NotePullLive
	// NotePushTombstone: replace the remote live note with a tombstone using its
	// current version CAS (local side deleted).
	NotePushTombstone
	// NoteApplyTombstone: write a recovery copy, then delete the local note
	// using the local revision CAS (remote side tombstoned).
	NoteApplyTombstone
	// NotePreserveLocalThenPull: preserve the local edit as a conflict note,
	// then accept the remote note at the original Sync ID.
	NotePreserveLocalThenPull
	// NotePreserveLocalThenDelete: preserve the local edit as a conflict note,
	// then accept the remote tombstone (recovery + local revision-CAS delete).
	NotePreserveLocalThenDelete
	// NotePreserveRemoteThenTombstone: preserve the remote edit as a conflict
	// note, then tombstone the original with its current version CAS.
	NotePreserveRemoteThenTombstone
	// NoteBlock: a path conflict, unknown local state, invalid remote data, or
	// remote damage; no mutation.
	NoteBlock
	// NoteRetry: a retryable transport outcome; no decision was reached.
	NoteRetry
)

func (k NoteDecisionKind) String() string {
	switch k {
	case NoteNoop:
		return "noop"
	case NoteEstablishBaseline:
		return "establish_baseline"
	case NotePushLive:
		return "push_live"
	case NotePullLive:
		return "pull_live"
	case NotePushTombstone:
		return "push_tombstone"
	case NoteApplyTombstone:
		return "apply_tombstone"
	case NotePreserveLocalThenPull:
		return "preserve_local_then_pull"
	case NotePreserveLocalThenDelete:
		return "preserve_local_then_delete"
	case NotePreserveRemoteThenTombstone:
		return "preserve_remote_then_tombstone"
	case NoteBlock:
		return "block"
	case NoteRetry:
		return "retry"
	}
	return "unknown"
}

// NoteConflictInfo carries the deterministic conflict-copy plan for the three
// compound preservation outcomes. The conflict Sync ID is a UUID v5 derived
// from the source ID and the fixed-role local/remote state hashes, so a crash
// or lost response repeats the same conflict note instead of allocating another
// one (spec §8).
type NoteConflictInfo struct {
	SourceSyncID     string
	ConflictSyncID   string
	ConflictPath     string
	ConflictMarkdown string // the preserved side's markdown
	LocalStateHash   string
	RemoteStateHash  string
	// OriginalTombstone reports whether the original Sync ID becomes (or stays)
	// a tombstone; OriginalVersion is the remote CAS to push that tombstone
	// ("" when the remote is already tombstoned).
	OriginalTombstone bool
	OriginalVersion   string
}

// NoteDecision is the normalized output for one Sync ID. The top-level fields
// describe the action on the original Sync ID; Conflict, when set, describes
// the preservation copy that must be created first.
type NoteDecision struct {
	SyncID        string
	Kind          NoteDecisionKind
	Reason        string
	ContentHash   string // content hash of the record the decision acts on
	Deleted       bool
	Path          string // portable note path
	Markdown      string // markdown to transfer (pull/push) or accept at the original
	Version       string // remote version: CAS for pushes/tombstones, target for pulls/establishes
	LocalRevision string // expected local filesystem CAS for local mutations
	Conflict      *NoteConflictInfo
}

// DecideNote computes the normalized decision for one Sync ID from its
// immutable local observation, remote observation, optional usable baseline,
// and precomputed path-conflict flag. It performs no I/O. A path conflict,
// unknown local state, and invalid remote data always produce block/retry,
// never a deletion.
func DecideNote(local NoteLocalObservation, remote NoteRemoteObservation, baseline *Baseline, pathConflict bool) NoteDecision {
	d := NoteDecision{SyncID: local.SyncID}
	if d.SyncID == "" {
		d.SyncID = remote.SyncID
	}

	if pathConflict {
		return d.block("path conflict")
	}
	if local.State == LocalUnknown {
		return d.block("local unknown (blocked/unstable/unreadable)")
	}
	if remote.State == RemoteInvalid {
		if remote.Retryable {
			return d.retry("invalid remote record, retryable")
		}
		return d.block("invalid remote record")
	}

	// A physically missing remote object is repository damage, never a
	// tombstone. With a baseline the object was synced before, so report the
	// damage and leave local content untouched; without one a live local side
	// heals the absence by creating the record only-if-absent.
	if remote.State == RemoteMissing {
		if baseline != nil {
			return d.block("remote record physically missing though a baseline expected it")
		}
		if local.State == LocalLive {
			return d.pushLive(local, "", local.Revision)
		}
		return d.block("indexed local absence plus physically absent remote object is ambiguous")
	}

	if baseline == nil {
		return decideNoteNoBaseline(local, remote)
	}
	return decideNoteWithBaseline(local, remote, baseline)
}

// decideNoteNoBaseline implements spec §7's no-baseline table.
func decideNoteNoBaseline(local NoteLocalObservation, remote NoteRemoteObservation) NoteDecision {
	d := NoteDecision{SyncID: local.SyncID}
	if d.SyncID == "" {
		d.SyncID = remote.SyncID
	}
	switch local.State {
	case LocalAbsent:
		switch remote.State {
		case RemoteLive:
			// Remote-only live note: create the local file only-if-absent.
			return d.pullLive(remote, "", remote.Version)
		case RemoteTombstone:
			// Local absence plus a remote tombstone establishes a deleted
			// baseline and removes no user content.
			return d.establishBaseline(remote.ContentHash, true, remote.Version)
		}
	case LocalLive:
		switch remote.State {
		case RemoteLive:
			if local.ContentHash == remote.ContentHash {
				return d.establishBaseline(local.ContentHash, false, remote.Version)
			}
			// Divergent live states with the same ID: keep both; the remote
			// stays on the original.
			return d.preserveLocalThenPull(local, remote)
		case RemoteTombstone:
			// Preserve the local note as a conflict copy; the original remains
			// deleted.
			return d.preserveLocalThenDelete(local, remote)
		}
	}
	return d.block("no usable baseline and no matching rule")
}

// decideNoteWithBaseline implements spec §7's known-baseline table.
func decideNoteWithBaseline(local NoteLocalObservation, remote NoteRemoteObservation, b *Baseline) NoteDecision {
	d := NoteDecision{SyncID: local.SyncID}
	if d.SyncID == "" {
		d.SyncID = remote.SyncID
	}
	switch local.State {
	case LocalLive:
		switch remote.State {
		case RemoteLive:
			switch {
			case local.ContentHash == remote.ContentHash:
				// L == R: refresh the baseline; noop only when the baseline
				// already holds the same content AND the current remote version
				// (an equal-content rewrite with a new version must refresh the
				// version, or a later CAS would use a stale token).
				if !b.Deleted && b.ContentHash == local.ContentHash && b.RemoteVersion == remote.Version {
					return d.noop("local and remote unchanged")
				}
				return d.establishBaseline(local.ContentHash, false, remote.Version)
			case b.Deleted:
				// Recreated over a deleted baseline; the sides diverge.
				return d.preserveLocalThenPull(local, remote)
			case local.ContentHash == b.ContentHash:
				// L == B and R != B: pull the remote change with the local
				// revision CAS.
				return d.pullLive(remote, local.Revision, remote.Version)
			case remote.ContentHash == b.ContentHash:
				// R == B and L != B: push the local change with the CURRENT
				// remote version CAS. The baseline's version may be stale if
				// the provider rewrote equal content since the baseline.
				return d.pushLive(local, remote.Version, local.Revision)
			default:
				// L != B, R != B, L != R: divergent live edits.
				return d.preserveLocalThenPull(local, remote)
			}
		case RemoteTombstone:
			switch {
			case b.Deleted:
				// Local recreated over a deleted baseline. A remote tombstone
				// matching the baseline means the recreation is the new truth.
				if remote.ContentHash == b.ContentHash {
					return d.pushLive(local, remote.Version, local.Revision)
				}
				return d.preserveLocalThenDelete(local, remote)
			case local.ContentHash == b.ContentHash:
				// Local unchanged live baseline vs remote tombstone: recovery
				// copy, then local revision-CAS delete.
				return d.applyTombstone(remote.Version, local.Revision)
			default:
				// Local edit versus remote tombstone.
				return d.preserveLocalThenDelete(local, remote)
			}
		}
	case LocalAbsent:
		switch remote.State {
		case RemoteLive:
			switch {
			case b.Deleted:
				// L == B (deleted) and R != B: pull the recreated remote note.
				return d.pullLive(remote, "", remote.Version)
			case remote.ContentHash == b.ContentHash:
				// Local absent, remote unchanged live baseline: conditional
				// remote tombstone with the CURRENT remote version.
				return d.pushTombstone(remote, remote.Version)
			default:
				// Local absent versus remote edit: preserve the remote edit as a
				// conflict note, then tombstone the original.
				return d.preserveRemoteThenTombstone(local, remote, b)
			}
		case RemoteTombstone:
			// Converged only when the baseline matches content AND version; an
			// equal tombstone with a new version refreshes the baseline.
			if b.Deleted && b.ContentHash == remote.ContentHash && b.RemoteVersion == remote.Version {
				return d.noop("converged deletion")
			}
			return d.establishBaseline(remote.ContentHash, true, remote.Version)
		}
	}
	return d.block("no matching rule")
}

// --- decision builders -------------------------------------------------------

func (d NoteDecision) noop(reason string) NoteDecision {
	d.Kind = NoteNoop
	d.Reason = reason
	return d
}

func (d NoteDecision) establishBaseline(hash string, deleted bool, version string) NoteDecision {
	d.Kind = NoteEstablishBaseline
	d.ContentHash = hash
	d.Deleted = deleted
	d.Version = version
	d.Reason = "local and remote known equal"
	return d
}

func (d NoteDecision) pushLive(local NoteLocalObservation, version, localRevision string) NoteDecision {
	d.Kind = NotePushLive
	d.ContentHash = local.ContentHash
	d.Path = local.Path
	d.Markdown = local.Markdown
	d.Version = version // "" = create-if-absent
	d.LocalRevision = localRevision
	if version == "" {
		d.Reason = "local-only; create remote if-absent"
	} else {
		d.Reason = "local changed"
	}
	return d
}

func (d NoteDecision) pullLive(remote NoteRemoteObservation, localRevision, version string) NoteDecision {
	d.Kind = NotePullLive
	d.ContentHash = remote.ContentHash
	d.Path = remote.Path
	d.Markdown = remote.Markdown
	d.Version = version
	d.LocalRevision = localRevision
	d.Reason = "remote changed"
	return d
}

func (d NoteDecision) pushTombstone(remote NoteRemoteObservation, version string) NoteDecision {
	d.Kind = NotePushTombstone
	d.Deleted = true
	d.ContentHash = noteTombstoneHash(d.SyncID, remote.Path)
	d.Path = remote.Path
	d.Version = version
	d.Reason = "locally deleted; replace remote with tombstone"
	return d
}

func (d NoteDecision) applyTombstone(version, localRevision string) NoteDecision {
	d.Kind = NoteApplyTombstone
	d.Deleted = true
	d.Version = version
	d.LocalRevision = localRevision
	d.Reason = "remote tombstone; write recovery and delete locally"
	return d
}

func (d NoteDecision) block(reason string) NoteDecision {
	d.Kind = NoteBlock
	d.Reason = reason
	return d
}

func (d NoteDecision) retry(reason string) NoteDecision {
	d.Kind = NoteRetry
	d.Reason = reason
	return d
}

// preserveLocalThenPull keeps the local live edit as the conflict note and
// accepts the remote live note at the original Sync ID (both live diverge).
func (d NoteDecision) preserveLocalThenPull(local NoteLocalObservation, remote NoteRemoteObservation) NoteDecision {
	lState := StateHash(local.ContentHash, false)
	rState := StateHash(remote.ContentHash, false)
	conflictID, err := DeriveConflictSyncID(d.SyncID, lState, rState)
	if err != nil {
		return d.block("cannot derive conflict identity")
	}
	d.Kind = NotePreserveLocalThenPull
	d.ContentHash = remote.ContentHash
	d.Path = remote.Path
	d.Markdown = remote.Markdown
	d.Version = remote.Version
	d.LocalRevision = local.Revision
	d.Conflict = &NoteConflictInfo{
		SourceSyncID:     d.SyncID,
		ConflictSyncID:   conflictID,
		ConflictPath:     ConflictPath(local.Path, conflictID),
		ConflictMarkdown: local.Markdown,
		LocalStateHash:   lState,
		RemoteStateHash:  rState,
	}
	d.Reason = "divergent edits; preserve local as conflict, accept remote at original"
	return d
}

// preserveLocalThenDelete keeps the local live edit as the conflict note and
// accepts the remote tombstone (the original stays deleted).
func (d NoteDecision) preserveLocalThenDelete(local NoteLocalObservation, remote NoteRemoteObservation) NoteDecision {
	lState := StateHash(local.ContentHash, false)
	rState := StateHash(remote.ContentHash, true)
	conflictID, err := DeriveConflictSyncID(d.SyncID, lState, rState)
	if err != nil {
		return d.block("cannot derive conflict identity")
	}
	d.Kind = NotePreserveLocalThenDelete
	d.Deleted = true
	d.Version = remote.Version
	d.LocalRevision = local.Revision
	d.Conflict = &NoteConflictInfo{
		SourceSyncID:      d.SyncID,
		ConflictSyncID:    conflictID,
		ConflictPath:      ConflictPath(local.Path, conflictID),
		ConflictMarkdown:  local.Markdown,
		LocalStateHash:    lState,
		RemoteStateHash:   rState,
		OriginalTombstone: true,
	}
	d.Reason = "local edit versus remote tombstone; preserve local as conflict, accept deletion"
	return d
}

// preserveRemoteThenTombstone keeps the remote live edit as the conflict note
// and tombstones the original with its current version CAS (local absent).
func (d NoteDecision) preserveRemoteThenTombstone(local NoteLocalObservation, remote NoteRemoteObservation, b *Baseline) NoteDecision {
	// The local absent side is a deletion of the last-known baseline content.
	lState := StateHash(b.ContentHash, true)
	rState := StateHash(remote.ContentHash, false)
	conflictID, err := DeriveConflictSyncID(d.SyncID, lState, rState)
	if err != nil {
		return d.block("cannot derive conflict identity")
	}
	d.Kind = NotePreserveRemoteThenTombstone
	d.Deleted = true
	d.ContentHash = noteTombstoneHash(d.SyncID, remote.Path)
	d.Path = remote.Path
	d.Version = remote.Version
	d.Conflict = &NoteConflictInfo{
		SourceSyncID:      d.SyncID,
		ConflictSyncID:    conflictID,
		ConflictPath:      ConflictPath(remote.Path, conflictID),
		ConflictMarkdown:  remote.Markdown,
		LocalStateHash:    lState,
		RemoteStateHash:   rState,
		OriginalTombstone: true,
		OriginalVersion:   remote.Version,
	}
	d.Reason = "local absent versus remote edit; preserve remote as conflict, tombstone original"
	return d
}

// noteTombstoneHash returns the canonical content hash of the tombstone record
// for a Sync ID and path (markdown dropped, deleted=true).
func noteTombstoneHash(syncID, path string) string {
	return (&NoteRecord{
		SchemaVersion: NoteSchemaVersion,
		SyncID:        syncID,
		Path:          path,
		Deleted:       true,
	}).ComputeContentHash()
}

// ConflictPath returns the deterministic conflict-note path for a derived
// conflict identity: the original note's directory plus the conflict filename.
// It contains no clock and no device label, so a crash or lost response repeats
// the same conflict copy.
func ConflictPath(originalPath, conflictSyncID string) string {
	dir, base := splitNotePath(originalPath)
	name := ConflictFilename(strings.TrimSuffix(base, ".md"), conflictSyncID)
	if dir == "" {
		return name
	}
	return dir + "/" + name
}

func splitNotePath(path string) (dir, base string) {
	if i := strings.LastIndex(path, "/"); i >= 0 {
		return path[:i], path[i+1:]
	}
	return "", path
}

// String renders a decision for diagnostics.
func (d NoteDecision) String() string {
	s := fmt.Sprintf("%s %s", d.SyncID, d.Kind)
	if d.Reason != "" {
		s += " (" + d.Reason + ")"
	}
	return s
}

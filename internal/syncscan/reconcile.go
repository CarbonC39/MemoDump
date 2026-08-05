// Package syncscan reconciles a vault scan with the portable index and the
// durable baselines, producing the complete deterministic local inputs for the
// sync engine. It performs no cloud I/O and schedules no destructive action; it
// only describes what the engine must account for.
package syncscan

import (
	"fmt"
	"sort"
	"strings"

	"memodump/internal/cloudsync"
	"memodump/internal/syncindex"
	"memodump/internal/syncstate"
	"memodump/internal/vaultfs"
)

// State classifies one indexed entity against its durable baseline.
type State int

const (
	// StateUnchanged: present and its content matches the baseline.
	StateUnchanged State = iota
	// StateModified: present but its content differs from the baseline.
	StateModified
	// StateLocalOnly: present with no baseline (the replica knows other
	// baselines); the engine conditionally creates it remotely.
	StateLocalOnly
	// StateLocallyDeleted: the path is gone and a baseline existed; the engine
	// may publish a tombstone.
	StateLocallyDeleted
	// StateRenamed: the path is gone and exactly one new unindexed note has the
	// same content; the old Sync ID moves to it.
	StateRenamed
	// StateAmbiguous: the path is gone with no baseline; probe the remote and
	// require an explicit deletion/recovery decision — never auto-delete.
	StateAmbiguous
	// StateBlocked: the path is a symlink or its kind flipped; never touched.
	StateBlocked
	// StateUnstable: present but still being written; defer to the next scan.
	StateUnstable
	// StateBaselineUnknown: the replica has no durable baseline knowledge
	// (missing AppData, or never synced); probe before any action.
	StateBaselineUnknown
)

func (s State) String() string {
	switch s {
	case StateUnchanged:
		return "unchanged"
	case StateModified:
		return "modified"
	case StateLocalOnly:
		return "local-only"
	case StateLocallyDeleted:
		return "locally-deleted"
	case StateRenamed:
		return "renamed"
	case StateAmbiguous:
		return "ambiguous"
	case StateBlocked:
		return "blocked"
	case StateUnstable:
		return "unstable"
	case StateBaselineUnknown:
		return "baseline-unknown"
	}
	return "unknown"
}

// Entity is one indexed entity's reconciled local state — the deterministic
// local input the engine diffs against the remote.
type Entity struct {
	SyncID    string
	Kind      string
	Path      string // indexed path (for Renamed: the old path)
	State     State
	LocalHash string // current local digest when the note is readable
	NewPath   string // StateRenamed: the observed destination path
	Probe     bool   // requires a remote probe before any action
}

// NewEntity is an observed path with no Sync ID yet; it needs identity.
type NewEntity struct {
	Path      string
	Kind      string
	LocalHash string
}

// Reconciliation is the complete deterministic local input for the engine: the
// current scan reconciled with indexed identity and durable baselines. It
// schedules nothing — it only describes.
type Reconciliation struct {
	Entities        []Entity
	New             []NewEntity
	BaselineUnknown bool // the replica has no durable baseline knowledge
}

// Reconcile derives per-entity local state from a scan, the portable index,
// and the durable baselines. It is read-only: ordinary note changes are
// inferred from Markdown bytes and never append dirty WAL rows, and no index
// mutation happens here (ApplyIdentity applies the identity decisions).
func Reconcile(res *vaultfs.ScanResult, idx *syncindex.Store, st *syncstate.Store) (*Reconciliation, error) {
	notes := make(map[string]vaultfs.Observation, len(res.Notes))
	for _, n := range res.Notes {
		notes[n.Path] = n
	}
	folders := make(map[string]vaultfs.Observation, len(res.Folders))
	for _, f := range res.Folders {
		folders[f.Path] = f
	}
	blocked := make(map[string]bool, len(res.Blocked))
	for _, p := range res.Blocked {
		blocked[p] = true
	}
	unstable := make(map[string]bool, len(res.Unstable))
	for _, p := range res.Unstable {
		unstable[p] = true
	}
	indexed := make(map[string]bool, len(idx.Index.Entities))
	for _, e := range idx.Index.Entities {
		indexed[e.Path] = true
	}

	// A replica with no durable baselines has no baseline knowledge: either it
	// has never synced or its AppData was lost. Every indexed entity must be
	// probed before any action (spec §5.4); nothing is ever auto-uploaded,
	// deleted, or tombstoned in this state. Non-baseline durable state (a
	// cursor or config key) does not count as baseline knowledge.
	baselineUnknown := !st.HasAnyBaseline()
	r := &Reconciliation{BaselineUnknown: baselineUnknown}

	type missingRef struct {
		syncID  string
		kind    string
		path    string
		hasBase bool
		hash    string // baseline local hash ("" when none)
	}
	var missing []missingRef

	syncIDs := make([]string, 0, len(idx.Index.Entities))
	for id := range idx.Index.Entities {
		syncIDs = append(syncIDs, id)
	}
	sort.Strings(syncIDs)

	for _, syncID := range syncIDs {
		ent := idx.Index.Entities[syncID]
		if pathBlocked(blocked, ent.Path) {
			// The path or an ancestor became a symlink: it must not be mistaken
			// for a deletion, and its contents must never be synced. A symlinked
			// directory blocks its whole subtree.
			r.Entities = append(r.Entities, Entity{SyncID: syncID, Kind: ent.Kind, Path: ent.Path, State: StateBlocked})
			continue
		}
		obs, ok := observe(notes, folders, ent.Path)
		if !ok {
			if unstable[ent.Path] {
				// Present but mid-write: defer, never treat as deleted.
				r.Entities = append(r.Entities, Entity{SyncID: syncID, Kind: ent.Kind, Path: ent.Path, State: StateUnstable})
				continue
			}
			m := missingRef{syncID: syncID, kind: ent.Kind, path: ent.Path}
			if !baselineUnknown {
				base, has, err := syncstate.GetBaseline(st, syncID)
				if err != nil {
					return nil, err
				}
				m.hasBase = has
				if has {
					m.hash = base.LocalHash
				}
			}
			missing = append(missing, m)
			continue
		}
		if obs.Kind != ent.Kind {
			// The index and the filesystem disagree about what this path is.
			r.Entities = append(r.Entities, Entity{SyncID: syncID, Kind: ent.Kind, Path: ent.Path, State: StateBlocked})
			continue
		}
		e := Entity{SyncID: syncID, Kind: ent.Kind, Path: ent.Path}
		if obs.Kind == cloudsync.KindNote {
			e.LocalHash = obs.LocalHash
		}
		if baselineUnknown {
			e.State = StateBaselineUnknown
			e.Probe = true
			r.Entities = append(r.Entities, e)
			continue
		}
		base, has, err := syncstate.GetBaseline(st, syncID)
		if err != nil {
			return nil, err
		}
		switch {
		case !has:
			e.State = StateLocalOnly
		case obs.Kind == cloudsync.KindNote && obs.LocalHash != base.LocalHash:
			e.State = StateModified
		default:
			e.State = StateUnchanged
		}
		r.Entities = append(r.Entities, e)
	}

	// Offline rename inference: after downtime, a missing note whose baseline
	// content hash matches EXACTLY ONE new unindexed note may be a move. Any
	// ambiguity — two missing entities with the same hash, or two identical
	// new files — is not used for rename inference; those new files are copies
	// (new Sync IDs) and the missing originals are plain deletions.
	renameTo := make(map[string]string)
	if len(missing) > 0 {
		newByHash := make(map[string][]string)
		for _, n := range res.Notes {
			if !indexed[n.Path] && n.LocalHash != "" {
				newByHash[n.LocalHash] = append(newByHash[n.LocalHash], n.Path)
			}
		}
		missingByHash := make(map[string]missingRef)
		missingCountByHash := make(map[string]int)
		for _, m := range missing {
			if m.hasBase && m.hash != "" {
				missingCountByHash[m.hash]++
				missingByHash[m.hash] = m
			}
		}
		for hash, paths := range newByHash {
			if len(paths) != 1 || missingCountByHash[hash] != 1 {
				continue
			}
			renameTo[missingByHash[hash].syncID] = paths[0]
		}
	}

	for _, m := range missing {
		switch {
		case baselineUnknown:
			r.Entities = append(r.Entities, Entity{
				SyncID: m.syncID, Kind: m.kind, Path: m.path,
				State: StateBaselineUnknown, Probe: true,
			})
		case !m.hasBase:
			// Index entry present but local path absent and no baseline is
			// ambiguous (spec §5.4): probe remote, require an explicit
			// deletion/recovery decision. A folder's baseline has an empty
			// LocalHash by nature, so baseline presence is judged by hasBase
			// alone; the hash only participates in note rename inference.
			r.Entities = append(r.Entities, Entity{
				SyncID: m.syncID, Kind: m.kind, Path: m.path,
				State: StateAmbiguous, Probe: true,
			})
		default:
			e := Entity{SyncID: m.syncID, Kind: m.kind, Path: m.path, State: StateLocallyDeleted}
			if newPath, ok := renameTo[m.syncID]; ok {
				e.State = StateRenamed
				e.NewPath = newPath
				e.LocalHash = m.hash // the new file's digest equals the baseline
			}
			r.Entities = append(r.Entities, e)
		}
	}

	// New entities: unindexed observations that need Sync IDs. Paths claimed by
	// an inferred rename take the old Sync ID and are not new.
	claimed := make(map[string]bool)
	for _, e := range r.Entities {
		if e.State == StateRenamed {
			claimed[e.NewPath] = true
		}
	}
	// res.Notes and res.Folders are path-sorted, so r.New is deterministic.
	for _, n := range res.Notes {
		if !indexed[n.Path] && !claimed[n.Path] {
			r.New = append(r.New, NewEntity{Path: n.Path, Kind: cloudsync.KindNote, LocalHash: n.LocalHash})
		}
	}
	for _, f := range res.Folders {
		if !indexed[f.Path] && !claimed[f.Path] {
			r.New = append(r.New, NewEntity{Path: f.Path, Kind: cloudsync.KindFolder})
		}
	}

	return r, nil
}

// ApplyIdentity applies the index-only identity decisions of a reconciliation:
// inferred renames move the old Sync ID to the new path, and every unindexed
// observation receives a fresh Sync ID. It never touches the WAL or the remote
// and never deletes anything. The whole batch is built on a clone and committed
// through ReplaceIndex only when every change validates, so a failing batch
// leaves the store's index untouched — never a half-applied identity.
func ApplyIdentity(r *Reconciliation, idx *syncindex.Store) error {
	next := syncindex.New(idx.Index.VaultID)
	for id, e := range idx.Index.Entities {
		next.Entities[id] = e
	}
	byPath := make(map[string]string, len(next.Entities))
	for id, e := range next.Entities {
		byPath[e.Path] = id
	}

	claimed := make(map[string]bool)
	for _, e := range r.Entities {
		if e.State != StateRenamed {
			continue
		}
		ent, ok := next.Entities[e.SyncID]
		if !ok {
			return fmt.Errorf("unknown syncId %s", e.SyncID)
		}
		if prev, ok := byPath[e.NewPath]; ok && prev != e.SyncID {
			return fmt.Errorf("path %q already indexed as %s", e.NewPath, prev)
		}
		delete(byPath, ent.Path)
		ent.Path = e.NewPath
		next.Entities[e.SyncID] = ent
		byPath[e.NewPath] = e.SyncID
		claimed[e.NewPath] = true
	}
	for _, ne := range r.New {
		if claimed[ne.Path] {
			continue
		}
		if prev, ok := byPath[ne.Path]; ok {
			return fmt.Errorf("path %q already indexed as %s", ne.Path, prev)
		}
		id := syncindex.NewVaultID()
		next.Entities[id] = syncindex.Entity{Kind: ne.Kind, Path: ne.Path}
		byPath[ne.Path] = id
	}
	return idx.ReplaceIndex(next)
}

// observe returns the observation at a path, if any. A path is a note or a
// folder, never both; the scan guarantees one filesystem entry per path.
func observe(notes, folders map[string]vaultfs.Observation, path string) (vaultfs.Observation, bool) {
	if n, ok := notes[path]; ok {
		return n, true
	}
	f, ok := folders[path]
	return f, ok
}

// pathBlocked reports whether path is itself blocked or lies under a blocked
// directory, so a symlinked directory blocks its whole subtree.
func pathBlocked(blocked map[string]bool, path string) bool {
	for {
		if blocked[path] {
			return true
		}
		i := strings.LastIndex(path, "/")
		if i < 0 {
			return false
		}
		path = path[:i]
	}
}

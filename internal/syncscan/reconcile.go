// Package syncscan derives deterministic local observations for the sync
// engine from a vault scan and the portable index: which indexed entities are
// present, missing, blocked, or unstable, which unindexed observations need a
// Sync ID, and which unique rename/repair candidates exist. It performs no
// remote, snapshot, or baseline decision — the engine compares these
// observations against remote and snapshot state.
package syncscan

import (
	"fmt"
	"sort"
	"strings"

	"memodump/internal/cloudsync"
	"memodump/internal/syncindex"
	"memodump/internal/vaultfs"
)

// State classifies one indexed entity as a local observation. None of these
// imply a deletion, a modification, or a baseline — they describe what the
// filesystem shows, nothing more.
type State int

const (
	// StatePresent: the indexed path exists with the indexed kind and is
	// readable; notes carry their current LocalHash.
	StatePresent State = iota
	// StateMissing: the indexed path is absent. Absence is an observation, not
	// a deletion, until a usable baseline proves the entity existed remotely.
	StateMissing
	// StateBlocked: the path is a symlink, sits under a symlinked directory, or
	// its kind flipped; it is never reported absent.
	StateBlocked
	// StateUnstable: present but still being written; defer to the next scan.
	StateUnstable
)

func (s State) String() string {
	switch s {
	case StatePresent:
		return "present"
	case StateMissing:
		return "missing"
	case StateBlocked:
		return "blocked"
	case StateUnstable:
		return "unstable"
	}
	return "unknown"
}

// Entity is one indexed entity's local observation.
type Entity struct {
	SyncID    string
	Kind      string
	Path      string // indexed path
	State     State
	LocalHash string // current local digest when the note is present and readable
}

// NewEntity is an observed path with no Sync ID yet; it needs identity.
type NewEntity struct {
	Path      string
	Kind      string
	LocalHash string
}

// RepairHint is a unique crash/rename repair candidate: a missing indexed note
// whose last-known content digest matches exactly one unindexed note. The
// engine decides whether to apply it; anything ambiguous stays separate.
type RepairHint struct {
	SyncID    string // missing indexed note
	Path      string // old indexed path
	NewPath   string // the unique unindexed note with identical content
	LocalHash string // the shared content digest
}

// Reconciliation is the complete local input for the engine. It schedules
// nothing — it only describes observations and repair candidates.
type Reconciliation struct {
	Entities []Entity
	New      []NewEntity
	Repairs  []RepairHint
}

// Reconcile derives per-entity local observations from a scan and the portable
// index. lastKnown maps a Sync ID to its last-known local content digest
// (vaultfs.LocalHash) and is used only to produce unique rename/repair hints;
// when nil or empty, missing notes are plain missing observations and the
// engine applies a lossless delete-plus-create interpretation or reports a
// path conflict. It is read-only: no index mutation happens here.
func Reconcile(res *vaultfs.ScanResult, idx *syncindex.Store, lastKnown map[string]string) (*Reconciliation, error) {
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

	r := &Reconciliation{}

	type missingRef struct {
		syncID string
		kind   string
		path   string
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
			missing = append(missing, missingRef{syncID: syncID, kind: ent.Kind, path: ent.Path})
			continue
		}
		if obs.Kind != ent.Kind {
			// The index and the filesystem disagree about what this path is.
			r.Entities = append(r.Entities, Entity{SyncID: syncID, Kind: ent.Kind, Path: ent.Path, State: StateBlocked})
			continue
		}
		e := Entity{SyncID: syncID, Kind: ent.Kind, Path: ent.Path, State: StatePresent}
		if obs.Kind == cloudsync.KindNote {
			e.LocalHash = obs.LocalHash
		}
		r.Entities = append(r.Entities, e)
	}

	// Unique rename/repair inference: a missing indexed note whose last-known
	// content digest matches EXACTLY ONE new unindexed note may be a move. Any
	// ambiguity — two missing entities with the same last-known hash, or two
	// identical new files — is not used for repair inference; those new files
	// are copies (new Sync IDs) and the missing originals remain missing.
	repairTo := make(map[string]string)
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
			if hash, ok := lastKnown[m.syncID]; ok && hash != "" {
				missingCountByHash[hash]++
				missingByHash[hash] = m
			}
		}
		for hash, paths := range newByHash {
			if len(paths) != 1 || missingCountByHash[hash] != 1 {
				continue
			}
			repairTo[missingByHash[hash].syncID] = paths[0]
		}
	}
	for _, m := range missing {
		r.Entities = append(r.Entities, Entity{SyncID: m.syncID, Kind: m.kind, Path: m.path, State: StateMissing})
		if newPath, ok := repairTo[m.syncID]; ok {
			r.Repairs = append(r.Repairs, RepairHint{
				SyncID: m.syncID, Path: m.path, NewPath: newPath, LocalHash: lastKnown[m.syncID],
			})
		}
	}

	// New entities: unindexed observations that need Sync IDs. Paths claimed by
	// a repair candidate take the old Sync ID and are not new.
	claimed := make(map[string]bool)
	for _, h := range r.Repairs {
		claimed[h.NewPath] = true
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
// repair candidates move the old Sync ID to the new path, and every unindexed
// observation receives a fresh Sync ID. It never touches the remote and never
// deletes anything. The whole batch is built on a clone and committed through
// ReplaceIndex only when every change validates, so a failing batch leaves the
// store's index untouched — never a half-applied identity.
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
	for _, hint := range r.Repairs {
		ent, ok := next.Entities[hint.SyncID]
		if !ok {
			return fmt.Errorf("unknown syncId %s", hint.SyncID)
		}
		if prev, ok := byPath[hint.NewPath]; ok && prev != hint.SyncID {
			return fmt.Errorf("path %q already indexed as %s", hint.NewPath, prev)
		}
		delete(byPath, ent.Path)
		ent.Path = hint.NewPath
		next.Entities[hint.SyncID] = ent
		byPath[hint.NewPath] = hint.SyncID
		claimed[hint.NewPath] = true
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

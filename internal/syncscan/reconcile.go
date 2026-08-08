// Package syncscan derives deterministic local observations for the sync
// engine from a vault scan and the portable index: which indexed entities are
// present, missing, blocked, or unstable, and which unindexed observations
// need a Sync ID. It performs no remote, snapshot, or baseline decision — the
// engine compares these observations against remote and snapshot state.
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

// NewEntity is an observed path with no Sync ID yet; it needs identity. Every
// unindexed note and folder appears here — identity and rename decisions belong
// to the engine, never to the scanner.
type NewEntity struct {
	Path      string
	Kind      string
	LocalHash string
}

// Reconciliation is the complete local input for the engine. It schedules
// nothing — it only describes observations.
type Reconciliation struct {
	Entities []Entity
	New      []NewEntity
}

// Reconcile derives per-entity local observations from a scan and the portable
// index. It is read-only: no index mutation happens here, and no repair or
// rename inference is performed — the engine decides identity.
func Reconcile(res *vaultfs.ScanResult, idx *syncindex.Store) (*Reconciliation, error) {
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
			r.Entities = append(r.Entities, Entity{SyncID: syncID, Kind: ent.Kind, Path: ent.Path, State: StateMissing})
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

	// New entities: every unindexed observation that needs identity. res.Notes
	// and res.Folders are path-sorted, so r.New is deterministic.
	for _, n := range res.Notes {
		if !indexed[n.Path] {
			r.New = append(r.New, NewEntity{Path: n.Path, Kind: cloudsync.KindNote, LocalHash: n.LocalHash})
		}
	}
	for _, f := range res.Folders {
		if !indexed[f.Path] {
			r.New = append(r.New, NewEntity{Path: f.Path, Kind: cloudsync.KindFolder})
		}
	}

	return r, nil
}

// ApplyIdentity gives every unindexed observation a fresh Sync ID. It never
// touches the remote and never deletes anything, and it performs no repair or
// rename inference — approved identity changes come from the engine. The whole
// batch is built on a clone and committed through ReplaceIndex only when every
// change validates, so a failing batch leaves the store's index untouched —
// never a half-applied identity.
func ApplyIdentity(r *Reconciliation, idx *syncindex.Store) error {
	next := syncindex.New(idx.Index.VaultID)
	for id, e := range idx.Index.Entities {
		next.Entities[id] = e
	}
	byPath := make(map[string]string, len(next.Entities))
	for id, e := range next.Entities {
		byPath[e.Path] = id
	}

	for _, ne := range r.New {
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

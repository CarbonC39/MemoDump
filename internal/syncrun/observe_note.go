// Note-only observation assembly. These pure-and-thin helpers turn a vault
// scan, the note-only index, the disposable snapshot baselines, and a complete
// remote listing into the immutable per-Sync-ID local and remote observations
// that DecideNote consumes. They make no decisions themselves: blocked and
// unstable paths are unknown (never absent), a physically missing remote record
// is not a tombstone, and an incomplete remote listing never drives decisions.
package syncrun

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"memodump/internal/cloudsync"
	"memodump/internal/syncindex"
	"memodump/internal/syncstate"
	"memodump/internal/vaultfs"
)

// ReadNoteFn reads a present note's raw Markdown and its local filesystem
// revision fresh, under the vault's lock. The coordinator wires it to
// vaultfs.ReadVerbatim so the observation layer stays testable without a full
// repository. An unreadable path is a caller-visible observation error.
type ReadNoteFn func(path string) (markdown, revision string, err error)

// unionNoteIDs returns the sorted set of Sync IDs that make up this cycle:
// everything in the index, every snapshot baseline, and every listed remote
// note key. Processing order is deterministic.
func unionNoteIDs(idx *syncindex.NoteStore, baselines map[string]syncstate.SnapshotEntity, remoteKeys map[string]bool) []string {
	set := make(map[string]bool)
	for id := range idx.Index.Notes {
		set[id] = true
	}
	for id := range baselines {
		set[id] = true
	}
	for key := range remoteKeys {
		if id, ok := cloudsync.ParseNoteKey(key); ok {
			set[id] = true
		}
	}
	ids := make([]string, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// addNewNoteIDs assigns a fresh Sync ID to every scanned note that is not yet
// indexed, so definite new notes are observable in this cycle and can be saved
// before any upload. A scanned note the note-only contract cannot represent
// (unportable path) is never indexed and stays unknown — the coordinator's
// observation classification surfaces it as such.
func addNewNoteIDs(idx *syncindex.NoteStore, res *vaultfs.ScanResult) error {
	for _, n := range res.Notes {
		if !cloudsync.ValidNotePath(n.Path) {
			continue // unrepresentable: blocked, never indexed
		}
		if _, ok := idx.IDByPath(n.Path); ok {
			continue
		}
		if err := idx.AddNote(syncindex.NewVaultID(), n.Path); err != nil {
			return fmt.Errorf("assign identity to %q: %w", n.Path, err)
		}
	}
	return nil
}

// noteLocalObservations derives the immutable local observation for every union
// Sync ID from the scan and index. A present indexed note is read fresh and
// given its canonical NoteRecord content hash; an indexed path absent from the
// scan is LocalAbsent; blocked, unstable, kind-flipped, unreadable, or
// unrepresentable paths are LocalUnknown — never absent.
func noteLocalObservations(res *vaultfs.ScanResult, idx *syncindex.NoteStore, ids []string, read ReadNoteFn) map[string]cloudsync.NoteLocalObservation {
	notes := make(map[string]vaultfs.Observation, len(res.Notes))
	for _, n := range res.Notes {
		notes[n.Path] = n
	}
	folders := make(map[string]bool, len(res.Folders))
	for _, f := range res.Folders {
		folders[f.Path] = true
	}
	unstable := stringSet(res.Unstable)
	blocked := stringSet(res.Blocked)

	obs := make(map[string]cloudsync.NoteLocalObservation, len(ids))
	for _, id := range ids {
		path, indexed := idx.PathByID(id)
		if !indexed {
			// Present only in the baseline or the remote listing: no local file.
			obs[id] = cloudsync.NoteLocalObservation{SyncID: id, State: cloudsync.LocalAbsent}
			continue
		}
		if !cloudsync.ValidNotePath(path) {
			obs[id] = cloudsync.NoteLocalObservation{SyncID: id, State: cloudsync.LocalUnknown, Path: path}
			continue
		}
		if folders[path] || pathBlocked(blocked, path) || unstable[path] {
			// Kind flip, symlink/subtree, or a mid-scan write: unknown, never
			// absent.
			obs[id] = cloudsync.NoteLocalObservation{SyncID: id, State: cloudsync.LocalUnknown, Path: path}
			continue
		}
		if _, ok := notes[path]; !ok {
			obs[id] = cloudsync.NoteLocalObservation{SyncID: id, State: cloudsync.LocalAbsent, Path: path}
			continue
		}
		markdown, revision, err := read(path)
		if err != nil {
			// Readable at scan time but unreadable now: unknown, never absent.
			obs[id] = cloudsync.NoteLocalObservation{SyncID: id, State: cloudsync.LocalUnknown, Path: path}
			continue
		}
		canonical := cloudsync.NormalizeMarkdown(markdown)
		hash := (&cloudsync.NoteRecord{
			SchemaVersion: cloudsync.NoteSchemaVersion,
			SyncID:        id,
			Path:          path,
			Markdown:      canonical,
		}).ComputeContentHash()
		obs[id] = cloudsync.NoteLocalObservation{
			SyncID: id, State: cloudsync.LocalLive, Path: path,
			Markdown: canonical, ContentHash: hash, Revision: revision,
		}
	}
	return obs
}

// pathBlocked reports whether path is itself blocked or lies under a blocked
// (symlinked) directory, so a symlinked directory blocks its whole subtree.
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

func stringSet(in []string) map[string]bool {
	out := make(map[string]bool, len(in))
	for _, s := range in {
		out[s] = true
	}
	return out
}

// notePathConflicts returns the set of Sync IDs blocked by a portable path
// collision: two LIVE note records with DIFFERENT Sync IDs whose portable
// paths collide (case/Unicode folding). The same Sync ID appearing both locally
// and remotely at colliding paths is one note, not a conflict — the decision
// resolves its path change. Parent directories are implementation details, not
// entities, so only note paths participate.
func notePathConflicts(local map[string]cloudsync.NoteLocalObservation, remote map[string]cloudsync.NoteRemoteObservation) map[string]bool {
	byKey := make(map[string][]string)
	add := func(id, path string) {
		if path == "" {
			return
		}
		key := cloudsync.PortablePathKey(path)
		byKey[key] = append(byKey[key], id)
	}
	for id, l := range local {
		if l.State == cloudsync.LocalLive {
			add(id, l.Path)
		}
	}
	for id, r := range remote {
		if r.State == cloudsync.RemoteLive {
			add(id, r.Path)
		}
	}
	blocked := make(map[string]bool)
	for _, ids := range byKey {
		distinct := make(map[string]bool, len(ids))
		for _, id := range ids {
			distinct[id] = true
		}
		if len(distinct) > 1 {
			for id := range distinct {
				blocked[id] = true
			}
		}
	}
	return blocked
}

// listNoteKeys enumerates the complete set of remote note keys. A transport or
// listing error stops the cycle: a partial remote view must never drive
// decisions. Unrelated valid notes synchronize only when the listing is
// complete.
func listNoteKeys(ctx context.Context, remote cloudsync.RemoteStore) (map[string]bool, error) {
	keys := make(map[string]bool)
	page, err := remote.List(ctx, cloudsync.NoteKeyPrefix, "")
	if err != nil {
		return nil, err
	}
	for {
		for _, ch := range page.Changes {
			if _, ok := cloudsync.ParseNoteKey(ch.Key); ok {
				keys[ch.Key] = true
			}
		}
		if page.NextCursor == "" {
			break
		}
		page, err = remote.List(ctx, cloudsync.NoteKeyPrefix, page.NextCursor)
		if err != nil {
			return nil, err
		}
	}
	return keys, nil
}

// noteRemoteObservations reads and parses the listed note records, classifying
// every union Sync ID's remote state. A listed key that cannot be read or
// parsed is RemoteInvalid (retryable when the transport error is retryable); a
// key absent from the listing is RemoteMissing — never a tombstone. An
// incomplete listing therefore surfaces as RemoteMissing and, for a note a
// baseline expected, blocks the cycle rather than deleting anything.
func noteRemoteObservations(ctx context.Context, remote cloudsync.RemoteStore, keys map[string]bool, ids []string) map[string]cloudsync.NoteRemoteObservation {
	obs := make(map[string]cloudsync.NoteRemoteObservation, len(ids))
	for _, id := range ids {
		key := cloudsync.NoteKey(id)
		if !keys[key] {
			obs[id] = cloudsync.NoteRemoteObservation{SyncID: id, State: cloudsync.RemoteMissing}
			continue
		}
		data, version, err := remote.Read(ctx, key)
		if err != nil {
			obs[id] = cloudsync.NoteRemoteObservation{
				SyncID: id, State: cloudsync.RemoteInvalid,
				Retryable: cloudsync.IsStoreError(err, cloudsync.ErrRetryableTransport),
			}
			continue
		}
		rec, perr := cloudsync.ParseNoteRecord(data)
		if perr != nil {
			obs[id] = cloudsync.NoteRemoteObservation{SyncID: id, State: cloudsync.RemoteInvalid}
			continue
		}
		state := cloudsync.RemoteLive
		if rec.Deleted {
			state = cloudsync.RemoteTombstone
		}
		obs[id] = cloudsync.NoteRemoteObservation{
			SyncID: id, State: state, Path: rec.Path,
			Markdown: rec.Markdown, ContentHash: rec.ComputeContentHash(), Version: version,
		}
	}
	return obs
}

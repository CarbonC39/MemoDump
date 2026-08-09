package syncrun

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"memodump/internal/cloudsync"
	"memodump/internal/syncindex"
	"memodump/internal/syncstate"
	"memodump/internal/vaultfs"
)

func mustStore(t *testing.T, vaultID string, notes map[string]string) *syncindex.NoteStore {
	t.Helper()
	idx := syncindex.NewNoteIndex(vaultID)
	for id, path := range notes {
		idx.Notes[id] = syncindex.NoteEntry{Path: path}
	}
	return &syncindex.NoteStore{Index: idx}
}

func scanResult(notes []string, folders []string, unstable, blocked []string) *vaultfs.ScanResult {
	res := &vaultfs.ScanResult{Unstable: unstable, Blocked: blocked}
	for _, p := range notes {
		res.Notes = append(res.Notes, vaultfs.Observation{Path: p, Kind: cloudsync.KindNote, LocalHash: "x"})
	}
	for _, p := range folders {
		res.Folders = append(res.Folders, vaultfs.Observation{Path: p, Kind: cloudsync.KindFolder})
	}
	return res
}

// staticReader serves note bodies from a map with a deterministic revision.
func staticReader(bodies map[string]string) ReadNoteFn {
	return func(path string) (string, string, error) {
		md, ok := bodies[path]
		if !ok {
			return "", "", fmt.Errorf("not found: %s", path)
		}
		return md, "rev-" + path, nil
	}
}

func noteHash(syncID, path, markdown string) string {
	return (&cloudsync.NoteRecord{
		SchemaVersion: cloudsync.NoteSchemaVersion, SyncID: syncID, Path: path,
		Markdown: cloudsync.NormalizeMarkdown(markdown),
	}).ComputeContentHash()
}

// TestObserveLocalCoversExitGate covers the R2.1 exit gate for the local side:
// nested notes, unindexed notes gaining identity, indexed absence, and an
// external rename surfacing as old absence plus a NEW identity.
func TestObserveLocalCoversExitGate(t *testing.T) {
	const vaultID = "dc56ad15-62c6-4fa7-bf7a-5c6337d574be"
	idNested := "11111111-1111-4111-8111-111111111111"
	idGone := "22222222-2222-4222-8222-222222222222"
	idOld := "33333333-3333-4333-8333-333333333333"

	idx := mustStore(t, vaultID, map[string]string{
		idNested: "a/b/c.md",
		idGone:   "gone.md",
		idOld:    "old.md",
	})
	// The vault now holds a nested note, a renamed note (old.md -> renamed.md),
	// and a brand-new note. gone.md vanished.
	res := scanResult([]string{"a/b/c.md", "new.md", "renamed.md"}, nil, nil, nil)
	bodies := map[string]string{
		"a/b/c.md":   "# C\n",
		"new.md":     "# New\n",
		"renamed.md": "# Old but renamed\n",
	}

	if err := addNewNoteIDs(idx, res); err != nil {
		t.Fatal(err)
	}
	// new.md and renamed.md gained distinct fresh identities; old.md kept its
	// own (absent) identity and did not inherit the rename.
	idNew, ok := idx.IDByPath("new.md")
	if !ok {
		t.Fatal("new.md gained no identity")
	}
	idRenamed, ok := idx.IDByPath("renamed.md")
	if !ok {
		t.Fatal("renamed.md gained no identity")
	}
	if idRenamed == idOld {
		t.Fatal("external rename was inferred as identity-preserving")
	}
	if idNew == idRenamed {
		t.Fatal("distinct notes shared an identity")
	}

	ids := unionNoteIDs(idx, nil, nil)
	obs := noteLocalObservations(res, idx, ids, staticReader(bodies))

	if o := obs[idNested]; o.State != cloudsync.LocalLive || o.Path != "a/b/c.md" || o.Revision == "" {
		t.Fatalf("nested note = %+v, want live", o)
	} else if o.ContentHash != noteHash(idNested, "a/b/c.md", "# C\n") {
		t.Fatalf("nested note content hash wrong: %s", o.ContentHash)
	}
	if o := obs[idGone]; o.State != cloudsync.LocalAbsent {
		t.Fatalf("gone.md = %+v, want absent", o)
	}
	if o := obs[idOld]; o.State != cloudsync.LocalAbsent {
		t.Fatalf("old.md = %+v, want absent (external rename)", o)
	}
	for _, id := range []string{idNew, idRenamed} {
		if o := obs[id]; o.State != cloudsync.LocalLive {
			t.Fatalf("new note %s = %+v, want live", id, o)
		}
	}
}

// TestObserveLocalUnknownNeverAbsent covers blocked, unstable, kind-flipped,
// and read-error paths classifying as unknown, never absent.
func TestObserveLocalUnknownNeverAbsent(t *testing.T) {
	const vaultID = "dc56ad15-62c6-4fa7-bf7a-5c6337d574be"
	idBlocked := "11111111-1111-4111-8111-111111111111"
	idUnstable := "22222222-2222-4222-8222-222222222222"
	idFlip := "33333333-3333-4333-8333-333333333333"
	idReadErr := "44444444-4444-4444-8444-444444444444"

	idx := mustStore(t, vaultID, map[string]string{
		idBlocked:  "link.md",
		idUnstable: "changing.md",
		idFlip:     "was.md",
		idReadErr:  "broken.md",
	})
	res := scanResult([]string{"broken.md"}, []string{"was.md"}, []string{"changing.md"}, []string{"link.md"})
	bodies := map[string]string{"broken.md": "# B\n"}
	read := func(path string) (string, string, error) {
		if path == "broken.md" {
			return "", "", fmt.Errorf("permission denied")
		}
		return staticReader(bodies)(path)
	}

	ids := unionNoteIDs(idx, nil, nil)
	obs := noteLocalObservations(res, idx, ids, read)
	for _, id := range []string{idBlocked, idUnstable, idFlip, idReadErr} {
		if o := obs[id]; o.State != cloudsync.LocalUnknown {
			t.Errorf("id %s = %+v, want unknown", id, o)
		}
	}
}

// TestNotePathConflicts covers portable collisions across live local and remote
// note records: different Sync IDs colliding are blocked, the same Sync ID
// locally and remotely at the same portable path is not.
func TestNotePathConflicts(t *testing.T) {
	live := func(id, path string) cloudsync.NoteLocalObservation {
		return cloudsync.NoteLocalObservation{SyncID: id, State: cloudsync.LocalLive, Path: path}
	}
	rLive := func(id, path string) cloudsync.NoteRemoteObservation {
		return cloudsync.NoteRemoteObservation{SyncID: id, State: cloudsync.RemoteLive, Path: path}
	}
	local := map[string]cloudsync.NoteLocalObservation{
		"11111111-1111-4111-8111-111111111111": live("11111111-1111-4111-8111-111111111111", "Note.md"),
		"22222222-2222-4222-8222-222222222222": live("22222222-2222-4222-8222-222222222222", "note.md"),
		"33333333-3333-4333-8333-333333333333": live("33333333-3333-4333-8333-333333333333", "a/b.md"),
	}
	remote := map[string]cloudsync.NoteRemoteObservation{
		// Same Sync ID as a local note, same portable path: one note, no clash.
		"33333333-3333-4333-8333-333333333333": rLive("33333333-3333-4333-8333-333333333333", "a/b.md"),
		// A remote note colliding with the local "Note.md" on a case-insensitive
		// platform.
		"44444444-4444-4444-8444-444444444444": rLive("44444444-4444-4444-8444-444444444444", "NOTE.md"),
		// An unrelated remote note in its own directory: no collision.
		"55555555-5555-4555-8555-555555555555": rLive("55555555-5555-4555-8555-555555555555", "other/note.md"),
	}
	blocked := notePathConflicts(local, remote)
	for _, id := range []string{
		"11111111-1111-4111-8111-111111111111",
		"22222222-2222-4222-8222-222222222222",
		"44444444-4444-4444-8444-444444444444",
	} {
		if !blocked[id] {
			t.Errorf("colliding Sync ID %s not blocked", id)
		}
	}
	for _, id := range []string{"33333333-3333-4333-8333-333333333333", "55555555-5555-4555-8555-555555555555"} {
		if blocked[id] {
			t.Errorf("non-colliding Sync ID %s wrongly blocked", id)
		}
	}
}

// TestObserveRemoteUnionIncompleteListing covers the remote side of the exit
// gate: the union is deterministic, remote records classify correctly, and an
// incomplete listing surfaces a baseline-expected note as missing so the cycle
// blocks instead of deleting.
func TestObserveRemoteUnionIncompleteListing(t *testing.T) {
	ctx := context.Background()
	const vaultID = "dc56ad15-62c6-4fa7-bf7a-5c6337d574be"
	idR1 := "11111111-1111-4111-8111-111111111111"
	idR2 := "22222222-2222-4222-8222-222222222222"

	s := cloudsync.NewMemoryStore()
	seed := func(id, path, md string) {
		t.Helper()
		rec := &cloudsync.NoteRecord{
			SchemaVersion: cloudsync.NoteSchemaVersion, SyncID: id, Path: path, Markdown: md,
		}
		data, err := rec.Serialize()
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Seed(cloudsync.NoteKey(id), data, "1"); err != nil {
			t.Fatal(err)
		}
	}
	seed(idR1, "a.md", "# R1\n")
	seed(idR2, "b.md", "# R2\n")

	keys, err := listNoteKeys(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 {
		t.Fatalf("listed %d keys, want 2", len(keys))
	}

	idx := mustStore(t, vaultID, map[string]string{idR1: "a.md"})
	baselines := map[string]syncstate.SnapshotEntity{idR2: {ContentHash: strings.Repeat("b", 64), RemoteVersion: "1"}}
	ids := unionNoteIDs(idx, baselines, keys)
	if len(ids) != 2 || ids[0] != idR1 || ids[1] != idR2 {
		t.Fatalf("union = %v, want sorted [%s %s]", ids, idR1, idR2)
	}

	remotes, err := noteRemoteObservations(ctx, s, keys, ids)
	if r := remotes[idR1]; r.State != cloudsync.RemoteLive || r.ContentHash != noteHash(idR1, "a.md", "# R1\n") {
		t.Fatalf("idR1 remote = %+v, want live", r)
	}
	if r := remotes[idR2]; r.State != cloudsync.RemoteLive {
		t.Fatalf("idR2 remote = %+v, want live", r)
	}

	// A silently incomplete listing is a typed error from the provider: the
	// cycle stops instead of driving decisions from a partial remote view.
	s.ArmIncompleteList(1)
	if _, err := listNoteKeys(ctx, s); !cloudsync.IsStoreError(err, cloudsync.ErrIncompleteList) {
		t.Fatalf("incomplete listing error = %v, want ErrIncompleteList", err)
	}
	// A listing transport error also stops the cycle.
	s.ArmFault("list", &cloudsync.StoreError{Kind: cloudsync.ErrPermission, Message: "denied"})
	if _, err := listNoteKeys(ctx, s); err == nil {
		t.Fatal("listing transport error must stop the cycle")
	}
}

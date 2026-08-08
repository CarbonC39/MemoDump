package cloudsync

import (
	"reflect"
	"strings"
	"testing"
)

// Hand-written decision assertions for the critical reconciliation branches, so
// the self-generated scenario fixtures are not the only oracle.

const (
	tID_S = "5d5d8b2c-94f7-4a38-8318-8cd4cb53dfa8"
	tID_A = "6e6e9c3d-a5b8-4c49-9409-9de566677770"
	tID_F = "7f7f0d4e-b6c9-4d5a-a51a-0ef677788881"
)

func tnote(id, name, parent, markdown string) *Entity {
	e := &Entity{
		SchemaVersion: SchemaVersion, SyncID: id, Kind: KindNote,
		ParentID: parent, Name: name, Markdown: markdown,
		UpdatedBy: "1a2b3c4d-1111-4222-8333-444455556666", UpdatedAt: 1785800000000,
	}
	e.ContentHash = e.ComputeContentHash()
	return e
}

func live(syncID string, e *Entity) LocalObservation {
	return LocalObservation{SyncID: syncID, Kind: e.Kind, State: LocalLive, Entity: e, Revision: "r1"}
}

func rlive(syncID string, e *Entity) RemoteObservation {
	return RemoteObservation{SyncID: syncID, Kind: e.Kind, State: RemoteLive, Entity: e, Version: "v3"}
}

func rtomb(syncID string, e *Entity) RemoteObservation {
	e.Deleted = true
	return RemoteObservation{SyncID: syncID, Kind: e.Kind, State: RemoteTombstone, Entity: e, Version: "v3"}
}

func TestDecideEntityKnownBaselineTable(t *testing.T) {
	v1 := tnote(tID_S, "idea", "", "# v1\n")
	v2 := tnote(tID_S, "idea", "", "# v2\n")
	liveBase := &Baseline{ContentHash: v1.ContentHash, Deleted: false, RemoteVersion: "v2"}
	delBase := &Baseline{ContentHash: v1.ContentHash, Deleted: true, RemoteVersion: "v2"}

	cases := []struct {
		name    string
		l       LocalObservation
		r       RemoteObservation
		b       *Baseline
		want    DecisionKind
		wantDel bool
	}{
		{"unchanged noop", live(tID_S, v1), rlive(tID_S, v1), liveBase, DecisionNoop, false},
		{"local edit push", live(tID_S, v2), rlive(tID_S, v1), liveBase, DecisionPushLive, false},
		{"remote edit pull", live(tID_S, v1), rlive(tID_S, v2), liveBase, DecisionPullLive, false},
		{"identical edit establish", live(tID_S, v2), rlive(tID_S, v2), liveBase, DecisionEstablishBaseline, false},
		{"local delete push tombstone", LocalObservation{SyncID: tID_S, Kind: KindNote, State: LocalAbsent}, rlive(tID_S, v1), liveBase, DecisionPushTombstone, true},
		{"remote tombstone apply", live(tID_S, v1), rtomb(tID_S, tnote(tID_S, "idea", "", "# v1\n")), liveBase, DecisionApplyTombstone, true},
		{"absent plus tombstone establishes deleted baseline", LocalObservation{SyncID: tID_S, Kind: KindNote, State: LocalAbsent}, rtomb(tID_S, tnote(tID_S, "idea", "", "# v1\n")), liveBase, DecisionEstablishBaseline, true},
		{"converged delete noop", LocalObservation{SyncID: tID_S, Kind: KindNote, State: LocalAbsent}, rtomb(tID_S, tnote(tID_S, "idea", "", "# v1\n")), delBase, DecisionNoop, false},
		{"local unknown block", LocalObservation{SyncID: tID_S, Kind: KindNote, State: LocalUnknown}, rlive(tID_S, v1), liveBase, DecisionBlock, false},
		{"remote invalid block", live(tID_S, v1), RemoteObservation{SyncID: tID_S, State: RemoteInvalid}, liveBase, DecisionBlock, false},
		{"remote invalid retry", live(tID_S, v1), RemoteObservation{SyncID: tID_S, State: RemoteInvalid, Retryable: true}, liveBase, DecisionRetry, false},
		{"physical missing local present repush", live(tID_S, v1), RemoteObservation{SyncID: tID_S, State: RemoteMissing}, liveBase, DecisionPushLive, false},
		{"physical missing local absent repair", LocalObservation{SyncID: tID_S, Kind: KindNote, State: LocalAbsent}, RemoteObservation{SyncID: tID_S, State: RemoteMissing}, liveBase, DecisionRepairIndex, false},
	}
	for _, tc := range cases {
		got := DecideEntity(tc.l, tc.r, tc.b, Annotations{})
		if got.Kind != tc.want {
			t.Errorf("%s: kind = %v, want %v (reason %q)", tc.name, got.Kind, tc.want, got.Reason)
		}
		if tc.wantDel && !got.Deleted {
			t.Errorf("%s: expected a deleted-state decision", tc.name)
		}
	}
}

func TestDecideEntityNoBaselineTable(t *testing.T) {
	same := tnote(tID_S, "idea", "", "# s\n")
	cases := []struct {
		name string
		l    LocalObservation
		r    RemoteObservation
		want DecisionKind
	}{
		{"local only push create", live(tID_S, same), RemoteObservation{SyncID: tID_S, State: RemoteMissing}, DecisionPushLive},
		{"remote only pull", LocalObservation{SyncID: tID_S, Kind: KindNote, State: LocalAbsent}, rlive(tID_S, same), DecisionPullLive},
		{"identical establish", live(tID_S, same), rlive(tID_S, same), DecisionEstablishBaseline},
		{"remote tombstone with local absent establish", LocalObservation{SyncID: tID_S, Kind: KindNote, State: LocalAbsent}, rtomb(tID_S, tnote(tID_S, "idea", "", "# v1\n")), DecisionEstablishBaseline},
		{"divergent note conflict", live(tID_S, tnote(tID_S, "idea", "", "# local\n")), rlive(tID_S, tnote(tID_S, "idea", "", "# remote\n")), DecisionCreateConflict},
	}
	for _, tc := range cases {
		got := DecideEntity(tc.l, tc.r, nil, Annotations{})
		if got.Kind != tc.want {
			t.Errorf("%s: kind = %v, want %v", tc.name, got.Kind, tc.want)
		}
	}
}

func TestDecideEntityDivergentNotesCreatesDeterministicConflict(t *testing.T) {
	local := tnote(tID_S, "idea", "", "# local version\n")
	remote := tnote(tID_S, "idea", "", "# remote version\n")
	got := DecideEntity(
		live(tID_S, local), rlive(tID_S, remote),
		&Baseline{ContentHash: tnote(tID_S, "idea", "", "# base\n").ContentHash, RemoteVersion: "v1"},
		Annotations{},
	)
	if got.Kind != DecisionCreateConflict || got.Conflict == nil {
		t.Fatalf("kind = %v", got.Kind)
	}
	c := got.Conflict
	wantID, err := DeriveConflictSyncID(tID_S,
		StateHash(local.ContentHash, false), StateHash(remote.ContentHash, false))
	if err != nil {
		t.Fatal(err)
	}
	if c.ConflictSyncID != wantID {
		t.Errorf("conflict id = %s, want %s", c.ConflictSyncID, wantID)
	}
	if !c.AcceptRemoteOriginal || c.OriginalTombstone {
		t.Errorf("note conflict must keep the remote original live: %+v", c)
	}
	if c.ConflictEntity.Name != strings.TrimSuffix(ConflictFilename("idea", wantID), ".md") {
		t.Errorf("conflict name = %q", c.ConflictEntity.Name)
	}
	if c.ConflictEntity.Markdown != local.Markdown {
		t.Errorf("conflict entity must preserve the local content")
	}
	if c.ConflictEntity.ContentHash != c.ConflictEntity.ComputeContentHash() {
		t.Errorf("conflict entity hash mismatch")
	}
	// Deterministic: repeating the derivation yields the same identity.
	again := DecideEntity(live(tID_S, local), rlive(tID_S, remote),
		&Baseline{ContentHash: tnote(tID_S, "idea", "", "# base\n").ContentHash, RemoteVersion: "v1"}, Annotations{})
	if again.Conflict.ConflictSyncID != wantID {
		t.Errorf("conflict derivation not deterministic")
	}
}

func TestDecideEntityDeletedBaselineRebuild(t *testing.T) {
	x := tnote(tID_S, "idea", "", "# x\n")
	a := tnote(tID_S, "idea", "", "# a\n")
	b := tnote(tID_S, "idea", "", "# b\n")
	del := &Baseline{ContentHash: x.ContentHash, Deleted: true, RemoteVersion: "v2"}

	// Both sides rebuild the same content: establish a live baseline.
	got := DecideEntity(live(tID_S, x), rlive(tID_S, x), del, Annotations{})
	if got.Kind != DecisionEstablishBaseline || got.Deleted {
		t.Errorf("identical rebuild = %v, want live establish-baseline", got)
	}
	// Divergent rebuilds: keep both, the remote live entity stays on the
	// original (never tombstoned).
	got = DecideEntity(live(tID_S, a), rlive(tID_S, b), del, Annotations{})
	if got.Kind != DecisionCreateConflict || got.Conflict == nil || got.Conflict.OriginalTombstone {
		t.Errorf("divergent rebuild = %+v, want keep-both with the remote original live", got)
	}
}

func TestDecideEntityAnnotationsForceBlock(t *testing.T) {
	v1 := tnote(tID_S, "idea", "", "# v1\n")
	for _, ann := range []Annotations{
		{PathConflict: true},
		{ParentCycle: true},
		{StructuralConflict: true},
	} {
		got := DecideEntity(live(tID_S, v1), rlive(tID_S, v1), nil, ann)
		if got.Kind != DecisionBlock {
			t.Errorf("annotated %+v = %v, want block", ann, got.Kind)
		}
	}
}

func TestStableParentsFirstOrdering(t *testing.T) {
	child := Decision{SyncID: tID_A, ParentID: tID_F}
	parent := Decision{SyncID: tID_F}
	// The pathological input: the child precedes its parent in the slice.
	got := stableParentsFirst([]Decision{child, parent})
	if !reflect.DeepEqual([]string{got[0].SyncID, got[1].SyncID}, []string{tID_F, tID_A}) {
		t.Errorf("order = %s %s, want parent then child", got[0].SyncID, got[1].SyncID)
	}
}

func TestStableParentsFirstThreeLevelAndTrees(t *testing.T) {
	root := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	mid := "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	leaf := "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
	other := "dddddddd-dddd-4ddd-8ddd-dddddddddddd"
	// Interleaved so a naive stable sort is not enough: three-level chain plus
	// an unrelated tree whose root sorts before the chain's leaf.
	in := []Decision{
		{SyncID: leaf, ParentID: mid, Kind: DecisionPullLive},
		{SyncID: other, Kind: DecisionPullLive},
		{SyncID: mid, ParentID: root, Kind: DecisionPullLive},
		{SyncID: root, Kind: DecisionPullLive},
	}
	out := stableParentsFirst(in)
	pos := map[string]int{}
	for i, d := range out {
		pos[d.SyncID] = i
	}
	for _, d := range out {
		if d.ParentID == "" {
			continue
		}
		if pos[d.ParentID] > pos[d.SyncID] {
			t.Errorf("parent %s (%d) after child %s (%d)", d.ParentID, pos[d.ParentID], d.SyncID, pos[d.SyncID])
		}
	}
	if len(out) != len(in) {
		t.Fatalf("lost decisions: %d -> %d", len(in), len(out))
	}
}

func TestStableParentsFirstCycleDoesNotHang(t *testing.T) {
	a := Decision{SyncID: tID_F, ParentID: tID_A}
	b := Decision{SyncID: tID_A, ParentID: tID_F}
	out := stableParentsFirst([]Decision{a, b}) // must terminate
	if len(out) != 2 {
		t.Fatalf("cycle produced %d decisions, want 2", len(out))
	}
}

func TestDecideRepositoryTombstonesChildFirst(t *testing.T) {
	// Folders are tombstoned after their children, so [folder, child] input
	// must be emitted as [child, folder].
	child := Decision{SyncID: tID_A, ParentID: tID_F, Kind: DecisionPushTombstone, Entity: tnote(tID_A, "a", tID_F, "")}
	folder := Decision{SyncID: tID_F, Kind: DecisionPushTombstone, Entity: tnote(tID_F, "F", "", "")}
	out := DecideRepository([]Decision{folder, child})
	pos := map[string]int{}
	for i, d := range out {
		pos[d.SyncID] = i
	}
	if pos[tID_F] < pos[tID_A] {
		t.Errorf("folder tombstone (%d) before child (%d); want child-first", pos[tID_F], pos[tID_A])
	}
}

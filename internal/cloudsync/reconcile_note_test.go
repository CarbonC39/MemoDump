package cloudsync

import (
	"strings"
	"testing"
)

const testNoteSyncID = "5d5d8b2c-94f7-4a38-8318-8cd4cb53dfa8"

// Note: content hashes in these tests are opaque 64-hex strings; the pure
// decision only compares them, it never validates or derives from them.

func noteLocal(state LocalState, hash, path string) NoteLocalObservation {
	o := NoteLocalObservation{SyncID: testNoteSyncID, State: state, Path: path}
	if state == LocalLive {
		o.ContentHash = hash
		o.Markdown = "body\n"
		o.Revision = "local-rev"
	}
	return o
}

func noteRemote(state RemoteState, hash, path, version string) NoteRemoteObservation {
	o := NoteRemoteObservation{SyncID: testNoteSyncID, State: state, Path: path, Version: version}
	switch state {
	case RemoteLive, RemoteTombstone:
		o.ContentHash = hash
		if state == RemoteLive {
			o.Markdown = "body\n"
		}
	case RemoteInvalid:
		o.Retryable = true
	}
	return o
}

func noteRemoteHard(state RemoteState, hash, path, version string) NoteRemoteObservation {
	o := noteRemote(state, hash, path, version)
	o.Retryable = false
	return o
}

func baseline(hash string, deleted bool, version string) *Baseline {
	return &Baseline{ContentHash: hash, Deleted: deleted, RemoteVersion: version}
}

// TestDecideNoteTables covers every row of spec §7 in one table: onboarding,
// one-sided edits, concurrent identical/different edits, local/remote deletes,
// both edit/delete directions, unknown local state, physical remote absence,
// invalid remote input, and the precomputed path-conflict flag.
func TestDecideNoteTables(t *testing.T) {
	h0 := strings.Repeat("a", 64) // baseline content
	h1 := strings.Repeat("b", 64) // one side's content
	h2 := strings.Repeat("c", 64) // the other side's content
	th := strings.Repeat("d", 64) // a tombstone's content hash
	th2 := strings.Repeat("e", 64)
	path := "Projects/idea.md"

	cases := []struct {
		name         string
		local        NoteLocalObservation
		remote       NoteRemoteObservation
		baseline     *Baseline
		pathConflict bool
		want         NoteDecisionKind
	}{
		// --- no baseline (onboarding) ---
		{"no-baseline local-only", noteLocal(LocalLive, h1, path), noteRemote(RemoteMissing, "", path, ""), nil, false, NotePushLive},
		{"no-baseline remote-only", noteLocal(LocalAbsent, "", ""), noteRemote(RemoteLive, h1, path, "v1"), nil, false, NotePullLive},
		{"no-baseline identical", noteLocal(LocalLive, h1, path), noteRemote(RemoteLive, h1, path, "v1"), nil, false, NoteEstablishBaseline},
		{"no-baseline divergent", noteLocal(LocalLive, h1, path), noteRemote(RemoteLive, h2, path, "v1"), nil, false, NotePreserveLocalThenPull},
		{"no-baseline live vs tombstone", noteLocal(LocalLive, h1, path), noteRemote(RemoteTombstone, th, path, "v1"), nil, false, NotePreserveLocalThenDelete},
		{"no-baseline absent + tombstone", noteLocal(LocalAbsent, "", ""), noteRemote(RemoteTombstone, th, path, "v1"), nil, false, NoteEstablishBaseline},
		{"no-baseline absent + missing", noteLocal(LocalAbsent, "", ""), noteRemote(RemoteMissing, "", path, ""), nil, false, NoteBlock},
		{"no-baseline invalid remote", noteLocal(LocalLive, h1, path), noteRemoteHard(RemoteInvalid, "", path, ""), nil, false, NoteBlock},
		{"no-baseline invalid remote retryable", noteLocal(LocalLive, h1, path), noteRemote(RemoteInvalid, "", path, ""), nil, false, NoteRetry},

		// --- with baseline ---
		{"L==R baseline matches", noteLocal(LocalLive, h0, path), noteRemote(RemoteLive, h0, path, "v0"), baseline(h0, false, "v0"), false, NoteNoop},
		{"L==R baseline version stale", noteLocal(LocalLive, h0, path), noteRemote(RemoteLive, h0, path, "v1"), baseline(h0, false, "v0"), false, NoteEstablishBaseline},
		{"L==R baseline stale", noteLocal(LocalLive, h0, path), noteRemote(RemoteLive, h0, path, "v2"), baseline(h1, false, "v0"), false, NoteEstablishBaseline},
		{"remote changed only", noteLocal(LocalLive, h0, path), noteRemote(RemoteLive, h1, path, "v2"), baseline(h0, false, "v0"), false, NotePullLive},
		{"local changed only", noteLocal(LocalLive, h1, path), noteRemote(RemoteLive, h0, path, "v1"), baseline(h0, false, "v0"), false, NotePushLive},
		{"both changed differently", noteLocal(LocalLive, h1, path), noteRemote(RemoteLive, h2, path, "v2"), baseline(h0, false, "v0"), false, NotePreserveLocalThenPull},
		{"local absent remote unchanged", noteLocal(LocalAbsent, "", ""), noteRemote(RemoteLive, h0, path, "v1"), baseline(h0, false, "v0"), false, NotePushTombstone},
		{"local absent remote edited", noteLocal(LocalAbsent, "", ""), noteRemote(RemoteLive, h1, path, "v2"), baseline(h0, false, "v0"), false, NotePreserveRemoteThenTombstone},
		{"local absent remote recreated", noteLocal(LocalAbsent, "", ""), noteRemote(RemoteLive, h1, path, "v2"), baseline(th, true, "v1"), false, NotePullLive},
		{"local unchanged remote tombstone", noteLocal(LocalLive, h0, path), noteRemote(RemoteTombstone, th, path, "v2"), baseline(h0, false, "v0"), false, NoteApplyTombstone},
		{"local edited vs remote tombstone", noteLocal(LocalLive, h1, path), noteRemote(RemoteTombstone, th, path, "v2"), baseline(h0, false, "v0"), false, NotePreserveLocalThenDelete},
		{"converged deletion", noteLocal(LocalAbsent, "", ""), noteRemote(RemoteTombstone, th, path, "v1"), baseline(th, true, "v1"), false, NoteNoop},
		{"converged deletion version stale", noteLocal(LocalAbsent, "", ""), noteRemote(RemoteTombstone, th, path, "v2"), baseline(th, true, "v1"), false, NoteEstablishBaseline},
		{"absent + divergent tombstone baseline", noteLocal(LocalAbsent, "", ""), noteRemote(RemoteTombstone, th, path, "v2"), baseline(h0, false, "v0"), false, NoteEstablishBaseline},
		{"recreated identical over deleted baseline", noteLocal(LocalLive, h1, path), noteRemote(RemoteLive, h1, path, "v2"), baseline(th, true, "v1"), false, NoteEstablishBaseline},
		{"recreated divergent over deleted baseline", noteLocal(LocalLive, h1, path), noteRemote(RemoteLive, h2, path, "v2"), baseline(th, true, "v1"), false, NotePreserveLocalThenPull},
		{"recreated over matching tombstone", noteLocal(LocalLive, h1, path), noteRemote(RemoteTombstone, th, path, "v1"), baseline(th, true, "v1"), false, NotePushLive},
		{"recreated over divergent tombstone", noteLocal(LocalLive, h1, path), noteRemote(RemoteTombstone, th2, path, "v2"), baseline(th, true, "v1"), false, NotePreserveLocalThenDelete},
		{"remote missing with baseline", noteLocal(LocalLive, h1, path), noteRemote(RemoteMissing, "", path, ""), baseline(h1, false, "v1"), false, NoteBlock},

		// --- guards ---
		{"path conflict", noteLocal(LocalLive, h1, path), noteRemote(RemoteLive, h2, path, "v1"), baseline(h0, false, "v0"), true, NoteBlock},
		{"local unknown", noteLocal(LocalUnknown, "", ""), noteRemote(RemoteLive, h1, path, "v1"), baseline(h0, false, "v0"), false, NoteBlock},
		{"invalid remote with baseline", noteLocal(LocalLive, h0, path), noteRemoteHard(RemoteInvalid, "", path, ""), baseline(h0, false, "v0"), false, NoteBlock},
	}
	for _, tc := range cases {
		got := DecideNote(tc.local, tc.remote, tc.baseline, tc.pathConflict)
		if got.Kind != tc.want {
			t.Errorf("%s: DecideNote = %s (%s), want %s", tc.name, got.Kind, got.Reason, tc.want)
		}
	}
}

// TestDecideNoteUsesCurrentRemoteVersion pins the CAS-version rule the
// reviewer flagged: every remote conditional write uses the CURRENT cycle's
// remote version, never the baseline's possibly-stale one, and equal content
// with a new version refreshes the baseline. This is what makes a failed CAS
// followed by a re-read converge instead of replaying a stale token forever.
func TestDecideNoteUsesCurrentRemoteVersion(t *testing.T) {
	h0 := strings.Repeat("a", 64)
	h1 := strings.Repeat("b", 64)
	th := strings.Repeat("d", 64)
	path := "idea.md"
	// The provider rewrote equal content: remote advanced v0 -> v1 while the
	// baseline still records v0.
	stale := baseline(h0, false, "v0")

	// push_live: local edit upload uses the current remote token v1, not v0.
	d := DecideNote(noteLocal(LocalLive, h1, path), noteRemote(RemoteLive, h0, path, "v1"), stale, false)
	if d.Kind != NotePushLive || d.Version != "v1" {
		t.Fatalf("push_live = %s (version %q), want version v1", d.Kind, d.Version)
	}

	// push_tombstone: local deletion upload uses the current remote token v1.
	d = DecideNote(noteLocal(LocalAbsent, "", ""), noteRemote(RemoteLive, h0, path, "v1"), stale, false)
	if d.Kind != NotePushTombstone || d.Version != "v1" {
		t.Fatalf("push_tombstone = %s (version %q), want version v1", d.Kind, d.Version)
	}

	// recreated-over-tombstone push_live uses the current remote token.
	delStale := baseline(th, true, "v0")
	d = DecideNote(noteLocal(LocalLive, h1, path), noteRemote(RemoteTombstone, th, path, "v1"), delStale, false)
	if d.Kind != NotePushLive || d.Version != "v1" {
		t.Fatalf("recreated push_live = %s (version %q), want version v1", d.Kind, d.Version)
	}

	// Equal content with a new version refreshes the baseline instead of noop.
	d = DecideNote(noteLocal(LocalLive, h0, path), noteRemote(RemoteLive, h0, path, "v1"), stale, false)
	if d.Kind != NoteEstablishBaseline || d.Version != "v1" {
		t.Fatalf("equal-with-new-version = %s (version %q), want establish_baseline v1", d.Kind, d.Version)
	}
	d = DecideNote(noteLocal(LocalAbsent, "", ""), noteRemote(RemoteTombstone, th, path, "v1"), delStale, false)
	if d.Kind != NoteEstablishBaseline || d.Version != "v1" {
		t.Fatalf("converged-deletion-with-new-version = %s (version %q), want establish_baseline v1", d.Kind, d.Version)
	}

	// After a failed CAS and re-read, the new decision advances to the current
	// token instead of replaying the stale one.
	d1 := DecideNote(noteLocal(LocalLive, h1, path), noteRemote(RemoteLive, h0, path, "v0"), stale, false)
	if d1.Kind != NotePushLive || d1.Version != "v0" {
		t.Fatalf("first push = %s (version %q), want v0", d1.Kind, d1.Version)
	}
	// Simulated re-read after the precondition failure: remote still h0 but now
	// at v1. The decision must use v1.
	d2 := DecideNote(noteLocal(LocalLive, h1, path), noteRemote(RemoteLive, h0, path, "v1"), stale, false)
	if d2.Kind != NotePushLive || d2.Version != "v1" {
		t.Fatalf("re-read push = %s (version %q), want the current v1", d2.Kind, d2.Version)
	}
}

// TestDecideNoteCompoundOutcomes pins the conflict plan carried by the three
// compound outcomes: the derived conflict identity/path are deterministic and
// the original action fields are set.
func TestDecideNoteCompoundOutcomes(t *testing.T) {
	h0 := strings.Repeat("a", 64)
	h1 := strings.Repeat("b", 64)
	h2 := strings.Repeat("c", 64)
	th := strings.Repeat("d", 64)
	path := "Projects/idea.md"

	t.Run("preserve-local-then-pull", func(t *testing.T) {
		d := DecideNote(noteLocal(LocalLive, h1, path), noteRemote(RemoteLive, h2, path, "v2"), baseline(h0, false, "v0"), false)
		if d.Kind != NotePreserveLocalThenPull {
			t.Fatalf("kind = %s", d.Kind)
		}
		c := d.Conflict
		if c == nil || c.ConflictSyncID == "" || c.ConflictPath == "" {
			t.Fatalf("missing conflict plan: %+v", c)
		}
		if !strings.HasPrefix(c.ConflictPath, "Projects/idea (conflict ") || !strings.HasSuffix(c.ConflictPath, ").md") {
			t.Fatalf("conflict path %q not in the derived form", c.ConflictPath)
		}
		if c.ConflictMarkdown != "body\n" {
			t.Fatalf("conflict markdown = %q, want the local body", c.ConflictMarkdown)
		}
		// The original accepts the remote live note: content/markdown/version.
		if d.ContentHash != h2 || d.Markdown != "body\n" || d.Version != "v2" || d.Deleted {
			t.Fatalf("original action not the remote accept: %+v", d)
		}
	})

	t.Run("preserve-local-then-delete", func(t *testing.T) {
		d := DecideNote(noteLocal(LocalLive, h1, path), noteRemote(RemoteTombstone, th, path, "v2"), baseline(h0, false, "v0"), false)
		if d.Kind != NotePreserveLocalThenDelete {
			t.Fatalf("kind = %s", d.Kind)
		}
		if !d.Deleted || d.LocalRevision != "local-rev" || !d.Conflict.OriginalTombstone {
			t.Fatalf("original must be deleted locally, remote already tombstoned: %+v", d)
		}
	})

	t.Run("preserve-remote-then-tombstone", func(t *testing.T) {
		d := DecideNote(noteLocal(LocalAbsent, "", ""), noteRemote(RemoteLive, h1, path, "v2"), baseline(h0, false, "v0"), false)
		if d.Kind != NotePreserveRemoteThenTombstone {
			t.Fatalf("kind = %s", d.Kind)
		}
		c := d.Conflict
		if c == nil || c.ConflictMarkdown != "body\n" || !c.OriginalTombstone || c.OriginalVersion != "v2" {
			t.Fatalf("conflict plan wrong: %+v", c)
		}
		if !d.Deleted || d.Version != "v2" {
			t.Fatalf("original must be tombstoned remotely with v2: %+v", d)
		}
	})
}

// TestDecideNoteIsDeterministic repeats the same inputs and requires identical
// decisions, including the derived conflict identity and path.
func TestDecideNoteIsDeterministic(t *testing.T) {
	h0 := strings.Repeat("a", 64)
	h1 := strings.Repeat("b", 64)
	path := "a.md"
	first := DecideNote(noteLocal(LocalLive, h1, path), noteRemote(RemoteLive, h0, path, "v2"), baseline(h0, false, "v0"), false)
	for i := 0; i < 5; i++ {
		again := DecideNote(noteLocal(LocalLive, h1, path), noteRemote(RemoteLive, h0, path, "v2"), baseline(h0, false, "v0"), false)
		if again.Kind != first.Kind {
			t.Fatalf("kind changed across repeats: %s vs %s", again.Kind, first.Kind)
		}
		if again.Conflict != nil {
			if again.Conflict.ConflictSyncID != first.Conflict.ConflictSyncID {
				t.Fatalf("conflict identity not deterministic")
			}
			if again.Conflict.ConflictPath != first.Conflict.ConflictPath {
				t.Fatalf("conflict path not deterministic")
			}
		}
	}
}

// TestDecideNoteUnknownOrInvalidNeverDeletes is the invariant every guard row
// shares: unknown local state, invalid remote input, remote damage, and path
// conflicts always produce block/retry and never a tombstone.
func TestDecideNoteUnknownOrInvalidNeverDeletes(t *testing.T) {
	h1 := strings.Repeat("b", 64)
	path := "a.md"
	inputs := []struct {
		name   string
		local  NoteLocalObservation
		remote NoteRemoteObservation
		base   *Baseline
		pc     bool
	}{
		{"unknown local, remote tombstone", noteLocal(LocalUnknown, "", ""), noteRemote(RemoteTombstone, h1, path, "v1"), baseline(h1, false, "v0"), false},
		{"invalid remote, local absent", noteLocal(LocalAbsent, "", ""), noteRemoteHard(RemoteInvalid, "", path, ""), baseline(h1, false, "v0"), false},
		{"remote missing with baseline, local absent", noteLocal(LocalAbsent, "", ""), noteRemote(RemoteMissing, "", path, ""), baseline(h1, true, "v1"), false},
		{"path conflict, local absent remote live", noteLocal(LocalAbsent, "", ""), noteRemote(RemoteLive, h1, path, "v1"), baseline(h1, false, "v0"), true},
	}
	for _, in := range inputs {
		d := DecideNote(in.local, in.remote, in.base, in.pc)
		if d.Kind != NoteBlock && d.Kind != NoteRetry {
			t.Errorf("%s: kind = %s, want block/retry", in.name, d.Kind)
		}
		if d.Deleted {
			t.Errorf("%s: decision authorizes a deletion", in.name)
		}
	}
}

// --- R1.2: deterministic preservation helpers ---------------------------------

// TestConflictPathDerivation pins the conflict-path contract: the derived path
// is exactly "dir/stem (conflict <12 hex digits of the ID>).md", deterministic,
// and never carries a timestamp or numeric suffix (spec §8).
func TestConflictPathDerivation(t *testing.T) {
	id := "04b2cbe6-19cf-584f-bad4-55fa03d9c05a"
	cases := []struct {
		path   string
		expect string
	}{
		{"idea.md", "idea (conflict 04b2cbe619cf).md"},
		{"Projects/idea.md", "Projects/idea (conflict 04b2cbe619cf).md"},
		{"a/b/c/deep.md", "a/b/c/deep (conflict 04b2cbe619cf).md"},
		{"你好/笔记.md", "你好/笔记 (conflict 04b2cbe619cf).md"},
	}
	for _, tc := range cases {
		got := ConflictPath(tc.path, id)
		if got != tc.expect {
			t.Errorf("ConflictPath(%q, %q) = %q, want %q", tc.path, id, got, tc.expect)
		}
		if again := ConflictPath(tc.path, id); again != got {
			t.Errorf("ConflictPath not deterministic: %q vs %q", again, got)
		}
	}
	// The filename derives from the conflict ID only — no clock, device label,
	// or numeric suffix is ever appended.
	if strings.Contains(ConflictPath("idea.md", id), "2 (conflict") || strings.Contains(ConflictPath("idea.md", id), "2026") {
		t.Fatalf("conflict path carries a suffix or timestamp: %q", ConflictPath("idea.md", id))
	}
}

// TestConflictIdentitySwappedRoles tests that retries and role swaps behave
// intentionally: the same divergence derives exactly one identity and path, and
// swapping the local/remote role hashes produces a DIFFERENT identity.
func TestConflictIdentitySwappedRoles(t *testing.T) {
	h1 := strings.Repeat("b", 64)
	h2 := strings.Repeat("c", 64)
	path := "idea.md"
	local := noteLocal(LocalLive, h1, path)
	remote := noteRemote(RemoteLive, h2, path, "v2")
	base := baseline(strings.Repeat("a", 64), false, "v0")

	d := DecideNote(local, remote, base, false)
	if d.Kind != NotePreserveLocalThenPull || d.Conflict == nil {
		t.Fatalf("decision = %s, want preserve-local-then-pull with a conflict plan", d.Kind)
	}
	// Retry: the identical inputs repeat exactly one identity and path.
	for i := 0; i < 5; i++ {
		again := DecideNote(local, remote, base, false)
		if again.Conflict.ConflictSyncID != d.Conflict.ConflictSyncID ||
			again.Conflict.ConflictPath != d.Conflict.ConflictPath {
			t.Fatalf("retry changed the conflict identity or path")
		}
	}
	// Swapped role hashes produce a different identity (fixed-role ordering).
	swapped, err := DeriveConflictSyncID(testNoteSyncID, d.Conflict.RemoteStateHash, d.Conflict.LocalStateHash)
	if err != nil {
		t.Fatal(err)
	}
	if swapped == d.Conflict.ConflictSyncID {
		t.Fatal("swapped role hashes produced the same conflict identity")
	}
	// A different divergence derives a different identity and path.
	other := DecideNote(noteLocal(LocalLive, h1, path), noteRemote(RemoteTombstone, h1, path, "v2"), base, false)
	if other.Conflict == nil || other.Conflict.ConflictSyncID == d.Conflict.ConflictSyncID {
		t.Fatal("different divergence produced the same conflict identity")
	}
	if other.Conflict.ConflictPath == d.Conflict.ConflictPath {
		t.Fatal("different divergence produced the same conflict path")
	}
}

// TestDecideNoteConflictPathCollisionBlocks defines the collision handling for
// a compound outcome: when the desired conflict path is already taken, the
// precomputed path-conflict flag makes the decision block — the derivation is
// never suffixed with a timestamp or number.
func TestDecideNoteConflictPathCollisionBlocks(t *testing.T) {
	h0 := strings.Repeat("a", 64)
	h1 := strings.Repeat("b", 64)
	h2 := strings.Repeat("c", 64)
	path := "idea.md"
	base := baseline(h0, false, "v0")

	// The divergence would derive a conflict path; if that path were free the
	// decision is a compound outcome, but the collision flag forces a block.
	d := DecideNote(noteLocal(LocalLive, h1, path), noteRemote(RemoteLive, h2, path, "v2"), base, false)
	if d.Kind != NotePreserveLocalThenPull {
		t.Fatalf("setup: kind = %s", d.Kind)
	}
	blocked := DecideNote(noteLocal(LocalLive, h1, path), noteRemote(RemoteLive, h2, path, "v2"), base, true)
	if blocked.Kind != NoteBlock {
		t.Fatalf("colliding conflict path must block, got %s", blocked.Kind)
	}
	if blocked.Conflict != nil {
		t.Fatal("a blocked decision must not carry a conflict plan")
	}
	// The derivation itself is unchanged even under the collision: nothing is
	// appended or shifted.
	again := ConflictPath(path, d.Conflict.ConflictSyncID)
	if again != d.Conflict.ConflictPath {
		t.Fatalf("conflict path changed under collision: %q vs %q", again, d.Conflict.ConflictPath)
	}
}

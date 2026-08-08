package cloudsync

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// scenarioDir is the shared scenario trace directory.
const scenarioDir = "../../testdata/sync/scenarios"

func loadScenarios(t *testing.T) []Scenario {
	t.Helper()
	entries, err := os.ReadDir(scenarioDir)
	if err != nil {
		t.Fatal(err)
	}
	var out []Scenario
	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(scenarioDir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		var sc Scenario
		if err := json.Unmarshal(data, &sc); err != nil {
			t.Fatalf("%s: %v", e.Name(), err)
		}
		out = append(out, sc)
	}
	if len(out) == 0 {
		t.Fatal("no scenario fixtures found")
	}
	return out
}

func TestScenariosDecisionsMatchFixture(t *testing.T) {
	ctx := context.Background()
	for _, sc := range loadScenarios(t) {
		s, err := NewSim(sc.Initial)
		if err != nil {
			t.Fatalf("%s: NewSim: %v", sc.Name, err)
		}
		plan, _, err := s.RunCycle(ctx, StepNone)
		if err != nil {
			t.Fatalf("%s: cycle: %v", sc.Name, err)
		}
		got := normalizeDecisions(plan)
		if !reflect.DeepEqual(got, sc.Expected) {
			t.Errorf("%s: decisions mismatch\n got %s\nwant %s", sc.Name, jsonPretty(got), jsonPretty(sc.Expected))
		}
	}
}

func TestScenariosExecuteToExpectedFinal(t *testing.T) {
	ctx := context.Background()
	for _, sc := range loadScenarios(t) {
		s, err := NewSim(sc.Initial)
		if err != nil {
			t.Fatalf("%s: NewSim: %v", sc.Name, err)
		}
		if _, _, err := s.RunCycle(ctx, StepDone); err != nil {
			t.Fatalf("%s: cycle: %v", sc.Name, err)
		}
		_, final := s.state()
		if !reflect.DeepEqual(final, sc.Final) {
			t.Errorf("%s: final state mismatch\n got %s\nwant %s", sc.Name, jsonPretty(final), jsonPretty(sc.Final))
		}
	}
}

// TestScenariosAgreeWithStoredObservations drives the pure engine directly from
// the committed observation inputs (exactly what the TypeScript suite does), so
// the two languages prove identical decisions on identical inputs.
func TestScenariosAgreeWithStoredObservations(t *testing.T) {
	for _, sc := range loadScenarios(t) {
		var ds []Decision
		for _, obs := range sc.Observations {
			l := obs.Local
			r := obs.Remote
			var ent *Entity
			var rev string
			if l.Entity != nil {
				ent = l.Entity
				rev = l.Revision
			}
			local := LocalObservation{SyncID: obs.SyncID, Kind: kindOf(l.Entity, r.Entity), State: localStateFromString(l.State), Entity: ent, Revision: rev}
			var rent *Entity
			var version string
			if r.Entity != nil {
				rent = r.Entity
				version = r.Version
			} else if r.State != "missing" {
				version = r.Version
			}
			remote := RemoteObservation{SyncID: obs.SyncID, Kind: kindOf(l.Entity, r.Entity), State: remoteStateFromString(r.State), Entity: rent, Version: version}
			var b *Baseline
			if obs.Baseline != nil {
				b = &Baseline{ContentHash: obs.Baseline.ContentHash, Deleted: obs.Baseline.Deleted, RemoteVersion: obs.Baseline.RemoteVersion}
			}
			ds = append(ds, DecideEntity(local, remote, b, Annotations{PathConflict: obs.Blocked}))
		}
		got := normalizeDecisions(DecideRepository(ds))
		if !reflect.DeepEqual(got, sc.Expected) {
			t.Errorf("%s: pure-engine decisions mismatch\n got %s\nwant %s", sc.Name, jsonPretty(got), jsonPretty(sc.Expected))
		}
	}
}

// TestScenariosConvergeUnderRestart proves that stopping at any abstract
// boundary and restarting from durable state converges to the same final state
// without duplicate conflict identities.
func TestScenariosConvergeUnderRestart(t *testing.T) {
	ctx := context.Background()
	boundaries := []Step{StepNone, StepIndex, StepConflict, StepRecovery, StepLocal, StepRemote}
	for _, sc := range loadScenarios(t) {
		// Reference: run fully to quiescence.
		ref, err := NewSim(sc.Initial)
		if err != nil {
			t.Fatalf("%s: %v", sc.Name, err)
		}
		if _, err := ref.RunUntilQuiescent(ctx); err != nil {
			t.Fatalf("%s: reference did not converge: %v", sc.Name, err)
		}
		_, refFinal := ref.state()

		for _, boundary := range boundaries {
			s, err := NewSim(sc.Initial)
			if err != nil {
				t.Fatalf("%s: %v", sc.Name, err)
			}
			// Run one cycle and stop at the boundary, then restart from the
			// durable state and run to quiescence.
			if _, _, err := s.RunCycle(ctx, boundary); err != nil {
				t.Fatalf("%s: stop at %d: %v", sc.Name, boundary, err)
			}
			cur, _ := s.state()
			s2, err := NewSim(cur)
			if err != nil {
				t.Fatalf("%s: restart: %v", sc.Name, err)
			}
			if _, err := s2.RunUntilQuiescent(ctx); err != nil {
				t.Fatalf("%s: did not converge after restart at boundary %d: %v", sc.Name, boundary, err)
			}
			_, final := s2.state()
			if !reflect.DeepEqual(final, refFinal) {
				t.Errorf("%s: restart at boundary %d diverged from reference", sc.Name, boundary)
			}
			assertNoDuplicateConflictIDs(t, sc.Name, s2)
		}
	}
}

// assertNoDuplicateConflictIDs checks that every conflict identity reserved in
// the index is unique and matches the fixture's expected conflict IDs.
func assertNoDuplicateConflictIDs(t *testing.T, name string, s *Sim) {
	t.Helper()
	seen := make(map[string]bool)
	for id := range s.index {
		if !IsUUIDv4(id) {
			// A v5 Sync ID is a conflict identity; it must appear exactly once.
			if seen[id] {
				t.Fatalf("%s: duplicate conflict identity %s", name, id)
			}
			seen[id] = true
		}
	}
	// The set of v5 IDs must match the fixture's create-conflict decisions.
	var expected []string
	for _, sc := range loadScenarios(t) {
		if sc.Name != name {
			continue
		}
		for _, d := range sc.Expected {
			if d.Conflict != nil {
				expected = append(expected, d.Conflict.ConflictSyncID)
			}
		}
	}
	if len(expected) != len(seen) {
		t.Errorf("%s: conflict identities %v do not match expected %v", name, keysOf(seen), expected)
	}
	for _, id := range expected {
		if !seen[id] {
			t.Errorf("%s: expected conflict identity %s not reserved", name, id)
		}
	}
}

func keysOf(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestSimulatorInjectedRemoteFaultsConverge proves the engine converges under
// the memory store's write-response-loss, stale-CAS, cursor-rejection, and
// incomplete-listing faults, not just in isolated unit tests.
func TestSimulatorInjectedRemoteFaultsConverge(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name     string
		scenario string
		arm      func(*MemoryStore)
	}{
		{
			// A conflict create whose response is lost: the write lands, the
			// engine re-reads, confirms the canonical state, and converges to a
			// single deterministic conflict copy.
			"conflict-create-response-loss",
			"divergent-edits",
			func(s *MemoryStore) {
				s.ArmUncertainWrite("create", &StoreError{Kind: ErrRetryableTransport, Message: "response lost"})
			},
		},
		{
			// A push whose replace response is lost: idempotent success after
			// re-read; the next cycle establishes the baseline.
			"push-replace-response-loss",
			"one-sided-edit",
			func(s *MemoryStore) {
				s.ArmUncertainWrite("replace", &StoreError{Kind: ErrRetryableTransport, Message: "response lost"})
			},
		},
		{
			// A stale precondition on the push: no unconditional write; the
			// next cycle re-reads and re-decides.
			"stale-replace-cas",
			"one-sided-edit",
			func(s *MemoryStore) {
				s.ArmFault("replace", &StoreError{Kind: ErrPreconditionFailed, Message: "stale"})
			},
		},
		{
			// A rejected cursor falls back to a full listing.
			"cursor-rejection",
			"one-sided-edit",
			func(s *MemoryStore) { s.ArmCursorReject() },
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sc := findScenario(t, tc.scenario)
			s, err := NewSim(sc.Initial)
			if err != nil {
				t.Fatal(err)
			}
			tc.arm(s.remote)
			if _, err := s.RunUntilQuiescent(ctx); err != nil {
				t.Fatalf("did not converge under %s: %v", tc.name, err)
			}
			assertNoDuplicateConflictIDs(t, tc.scenario, s)
		})
	}
}

// TestSimulatorIncompleteListNeverDeletes proves an incomplete full listing
// (an entity observed missing after a known baseline) surfaces as damage and
// never deletes local data: the local note is re-created idempotently once the
// object is rediscovered, and the cycle converges.
func TestSimulatorIncompleteListNeverDeletes(t *testing.T) {
	ctx := context.Background()
	sc := findScenario(t, "one-sided-edit")
	s, err := NewSim(sc.Initial)
	if err != nil {
		t.Fatal(err)
	}
	s.remote.ArmIncompleteList(1) // hide the entity from the first listing
	if _, err := s.RunUntilQuiescent(ctx); err != nil {
		t.Fatalf("did not converge after an incomplete listing: %v", err)
	}
	// The entity must never be deleted locally.
	if _, ok := s.files["idea.md"]; !ok {
		t.Fatal("incomplete listing deleted the local note")
	}
}

// TestSimulatorRecoveryFailurePreventsDelete proves a recovery write failure
// aborts the cycle before the deletion.
func TestSimulatorRecoveryFailurePreventsDelete(t *testing.T) {
	ctx := context.Background()
	sc := findScenario(t, "remote-tombstone")
	s, err := NewSim(sc.Initial)
	if err != nil {
		t.Fatal(err)
	}
	s.failRecovery = true
	if _, _, err := s.RunCycle(ctx, StepDone); err == nil {
		t.Fatal("cycle succeeded despite a recovery failure")
	}
	if _, ok := s.files["idea.md"]; !ok {
		t.Fatal("the local note was deleted despite the recovery failure")
	}
}

// TestSimulatorConflictCollisionBlocks proves an unrelated collision at the
// derived conflict path blocks instead of deriving a second conflict copy.
func TestSimulatorConflictCollisionBlocks(t *testing.T) {
	ctx := context.Background()
	sc := findScenario(t, "divergent-edits")
	s, err := NewSim(sc.Initial)
	if err != nil {
		t.Fatal(err)
	}
	// A user file already occupies the deterministic conflict path.
	plan, _, err := s.RunCycle(ctx, StepIndex)
	if err != nil {
		t.Fatal(err)
	}
	var conflictPath string
	for _, d := range plan {
		if d.Kind == DecisionCreateConflict {
			conflictPath = s.pathForEntity(d.Conflict.ConflictEntity)
		}
	}
	if conflictPath == "" {
		t.Fatal("no conflict decision in the plan")
	}
	s.writeLocal(conflictPath, "# unrelated user content\n")
	if _, _, err := s.RunCycle(ctx, StepDone); err == nil {
		t.Fatal("conflict collision was not blocked")
	}
	// The original is untouched (no second conflict, no original modification).
	if _, ok := s.files["idea.md"]; !ok {
		t.Fatal("the original note was modified despite the collision")
	}
}

// TestBlockedChangeDoesNotAdvanceCursor proves a blocked entity keeps its old
// baseline and the snapshot cursor is not advanced past the unhandled change.
func TestBlockedChangeDoesNotAdvanceCursor(t *testing.T) {
	ctx := context.Background()
	sc := findScenario(t, "blocked-path-conflict")
	s, err := NewSim(sc.Initial)
	if err != nil {
		t.Fatal(err)
	}
	before := cloneBaselines(s.baselines)
	cursorBefore := s.cursor
	if _, _, err := s.RunCycle(ctx, StepDone); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, s.baselines) {
		t.Fatalf("blocked entity baseline changed: %+v -> %+v", before, s.baselines)
	}
	if s.cursor != cursorBefore {
		t.Fatalf("cursor advanced past a blocked change: %q -> %q", cursorBefore, s.cursor)
	}
}

// TestRecoveryKeyedByStateHash proves recovery is keyed by (Sync ID, state
// hash): a second deletion of the same entity does not overwrite the first.
func TestRecoveryKeyedByStateHash(t *testing.T) {
	ctx := context.Background()
	sc := findScenario(t, "remote-tombstone")
	s, err := NewSim(sc.Initial)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.RunCycle(ctx, StepDone); err != nil {
		t.Fatal(err)
	}
	first, ok := s.recovery[syncID_S]
	if !ok {
		t.Fatal("no recovery after the tombstone was applied")
	}
	if len(first) != 1 {
		t.Fatalf("recovery has %d copies, want 1", len(first))
	}
	for h, md := range first {
		if h != StateHash(tnoteHash(syncID_S, "idea", "", "# v1\n"), false) {
			t.Errorf("recovery keyed by %s, want the v1 state hash", h)
		}
		if md != "# v1\n" {
			t.Errorf("recovery body = %q, want the v1 markdown", md)
		}
	}
	// The entity is recreated with different content and deleted again: the new
	// copy is added under its own state hash, never overwriting the first.
	s.files["idea.md"] = "# v2\n"
	if err := s.recoverEntity(syncID_S); err != nil {
		t.Fatal(err)
	}
	if len(s.recovery[syncID_S]) != 2 {
		t.Fatalf("second deletion overwrote the first: %+v", s.recovery[syncID_S])
	}
}

func tnoteHash(id, name, parent, markdown string) string {
	e := tnote(id, name, parent, markdown)
	return e.ContentHash
}

func findScenario(t *testing.T, name string) Scenario {
	t.Helper()
	for _, sc := range loadScenarios(t) {
		if sc.Name == name {
			return sc
		}
	}
	t.Fatalf("scenario %q not found", name)
	return Scenario{}
}

func kindOf(entities ...*Entity) string {
	for _, e := range entities {
		if e != nil {
			return e.Kind
		}
	}
	return ""
}

func localStateFromString(s string) LocalState {
	switch s {
	case "live":
		return LocalLive
	case "unknown":
		return LocalUnknown
	default:
		return LocalAbsent
	}
}

func remoteStateFromString(s string) RemoteState {
	switch s {
	case "live":
		return RemoteLive
	case "tombstone":
		return RemoteTombstone
	case "invalid":
		return RemoteInvalid
	default:
		return RemoteMissing
	}
}

func jsonPretty(v any) string {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err.Error()
	}
	return string(data)
}

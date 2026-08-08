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
	boundaries := []Step{StepNone, StepIndex, StepLocal, StepRemote, StepRecovery}
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

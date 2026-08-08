package cloudsync

// Generates testdata/sync/scenarios/*.json from this package's simulator and
// decision engine. Run `GEN_FIXTURES=1 go test -run TestGenerateScenarios`,
// then the committed traces pin the contract that the Go and TypeScript suites
// both assert against.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"testing"
)

// Shared fixture identities.
const (
	scenarioVaultID   = "11111111-1111-4111-8111-111111111111"
	scenarioReplicaID = "22222222-2222-4222-8222-222222222222"
	scenarioRepoID    = "33333333-3333-4333-8333-333333333333"
	scenarioProfile   = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	syncID_S          = "5d5d8b2c-94f7-4a38-8318-8cd4cb53dfa8"
	syncID_A          = "6e6e9c3d-a5b8-4c49-9409-9de566677770"
	syncID_F          = "7f7f0d4e-b6c9-4d5a-a51a-0ef677788881"
)

func mkNote(id, name, parent string, markdown string) *Entity {
	e := &Entity{
		SchemaVersion: SchemaVersion,
		SyncID:        id,
		Kind:          KindNote,
		ParentID:      parent,
		Name:          name,
		Markdown:      markdown,
		UpdatedBy:     "1a2b3c4d-1111-4222-8333-444455556666",
		UpdatedAt:     1785800000000,
	}
	e.ContentHash = e.ComputeContentHash()
	return e
}

func mkFolder(id, name, parent string) *Entity {
	e := &Entity{
		SchemaVersion: SchemaVersion,
		SyncID:        id,
		Kind:          KindFolder,
		ParentID:      parent,
		Name:          name,
		UpdatedBy:     "1a2b3c4d-1111-4222-8333-444455556666",
		UpdatedAt:     1785800000000,
	}
	e.ContentHash = e.ComputeContentHash()
	return e
}

// scenarioSeed builds the scenario's durable state. Snapshot baseline versions
// are patched to the memory store's actual versions after NewSim.
type scenarioSeed struct {
	name      string
	files     map[string]string
	index     map[string]IndexEntry
	baselines map[string]ScenarioBaseline
	remote    map[string]*Entity
	rawRemote map[string][]byte
	blocked   map[string]bool
}

func TestGenerateScenarios(t *testing.T) {
	if os.Getenv("GEN_FIXTURES") == "" {
		t.Skip("set GEN_FIXTURES=1 to regenerate testdata/sync/scenarios")
	}
	out := filepath.Join("..", "..", "testdata", "sync", "scenarios")
	if err := os.MkdirAll(out, 0755); err != nil {
		t.Fatal(err)
	}
	for _, seed := range scenarioSeeds() {
		sc := buildScenario(t, seed)
		data, err := json.MarshalIndent(sc, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(out, seed.name+".json"), append(data, '\n'), 0644); err != nil {
			t.Fatal(err)
		}
	}
}

func scenarioSeeds() []scenarioSeed {
	indexNote := func(id, path string) map[string]IndexEntry {
		return map[string]IndexEntry{id: {Kind: KindNote, Path: path}}
	}
	return []scenarioSeed{
		{
			name:      "first-local-upload",
			files:     map[string]string{"idea.md": "# Local\n"},
			index:     indexNote(syncID_S, "idea.md"),
			baselines: nil,
			remote:    nil,
		},
		{
			name:      "first-remote-download",
			files:     nil,
			index:     indexNote(syncID_S, "idea.md"),
			baselines: nil,
			remote:    map[string]*Entity{syncID_S: mkNote(syncID_S, "idea", "", "# Remote\n")},
		},
		{
			name:      "identical-onboarding",
			files:     map[string]string{"idea.md": "# Same\n"},
			index:     indexNote(syncID_S, "idea.md"),
			baselines: nil,
			remote:    map[string]*Entity{syncID_S: mkNote(syncID_S, "idea", "", "# Same\n")},
		},
		{
			name:  "one-sided-edit",
			files: map[string]string{"idea.md": "# v2\n"},
			index: indexNote(syncID_S, "idea.md"),
			baselines: map[string]ScenarioBaseline{
				syncID_S: {ContentHash: mkNote(syncID_S, "idea", "", "# v1\n").ContentHash, Deleted: false},
			},
			remote: map[string]*Entity{syncID_S: mkNote(syncID_S, "idea", "", "# v1\n")},
		},
		{
			name:  "one-sided-rename",
			files: map[string]string{"b.md": "# x\n"},
			index: indexNote(syncID_S, "b.md"),
			baselines: map[string]ScenarioBaseline{
				syncID_S: {ContentHash: mkNote(syncID_S, "a", "", "# x\n").ContentHash, Deleted: false},
			},
			remote: map[string]*Entity{syncID_S: mkNote(syncID_S, "a", "", "# x\n")},
		},
		{
			name:  "recreate-from-deleted-baseline",
			files: map[string]string{"idea.md": "# x\n"},
			index: indexNote(syncID_S, "idea.md"),
			baselines: map[string]ScenarioBaseline{
				syncID_S: {ContentHash: mkNote(syncID_S, "idea", "", "# x\n").ContentHash, Deleted: true},
			},
			remote: map[string]*Entity{syncID_S: mkNote(syncID_S, "idea", "", "# x\n")},
		},
		{
			name:  "recreate-divergent-from-deleted-baseline",
			files: map[string]string{"idea.md": "# a\n"},
			index: indexNote(syncID_S, "idea.md"),
			baselines: map[string]ScenarioBaseline{
				syncID_S: {ContentHash: mkNote(syncID_S, "idea", "", "# x\n").ContentHash, Deleted: true},
			},
			remote: map[string]*Entity{syncID_S: mkNote(syncID_S, "idea", "", "# b\n")},
		},
		{
			name:  "remote-edit",
			files: map[string]string{"idea.md": "# v1\n"},
			index: indexNote(syncID_S, "idea.md"),
			baselines: map[string]ScenarioBaseline{
				syncID_S: {ContentHash: mkNote(syncID_S, "idea", "", "# v1\n").ContentHash, Deleted: false},
			},
			remote: map[string]*Entity{syncID_S: mkNote(syncID_S, "idea", "", "# v2\n")},
		},
		{
			name:  "identical-edit-both",
			files: map[string]string{"idea.md": "# v2\n"},
			index: indexNote(syncID_S, "idea.md"),
			baselines: map[string]ScenarioBaseline{
				syncID_S: {ContentHash: mkNote(syncID_S, "idea", "", "# v1\n").ContentHash, Deleted: false},
			},
			remote: map[string]*Entity{syncID_S: mkNote(syncID_S, "idea", "", "# v2\n")},
		},
		{
			name:  "divergent-edits",
			files: map[string]string{"idea.md": "# local version\n"},
			index: indexNote(syncID_S, "idea.md"),
			baselines: map[string]ScenarioBaseline{
				syncID_S: {ContentHash: mkNote(syncID_S, "idea", "", "# base\n").ContentHash, Deleted: false},
			},
			remote: map[string]*Entity{syncID_S: mkNote(syncID_S, "idea", "", "# remote version\n")},
		},
		{
			name:      "local-delete",
			files:     nil,
			index:     indexNote(syncID_S, "idea.md"),
			baselines: map[string]ScenarioBaseline{syncID_S: {ContentHash: mkNote(syncID_S, "idea", "", "# v1\n").ContentHash}},
			remote:    map[string]*Entity{syncID_S: mkNote(syncID_S, "idea", "", "# v1\n")},
		},
		{
			name:  "remote-tombstone",
			files: map[string]string{"idea.md": "# v1\n"},
			index: indexNote(syncID_S, "idea.md"),
			baselines: map[string]ScenarioBaseline{
				syncID_S: {ContentHash: mkNote(syncID_S, "idea", "", "# v1\n").ContentHash, Deleted: false},
			},
			remote: map[string]*Entity{syncID_S: func() *Entity {
				e := mkNote(syncID_S, "idea", "", "# v1\n")
				e.Deleted = true
				return e
			}()},
		},
		{
			name:  "local-edit-vs-tombstone",
			files: map[string]string{"idea.md": "# local edit\n"},
			index: indexNote(syncID_S, "idea.md"),
			baselines: map[string]ScenarioBaseline{
				syncID_S: {ContentHash: mkNote(syncID_S, "idea", "", "# v1\n").ContentHash, Deleted: false},
			},
			remote: map[string]*Entity{syncID_S: func() *Entity {
				e := mkNote(syncID_S, "idea", "", "# v1\n")
				e.Deleted = true
				return e
			}()},
		},
		{
			name:  "remote-edit-vs-local-delete",
			files: nil,
			index: indexNote(syncID_S, "idea.md"),
			baselines: map[string]ScenarioBaseline{
				syncID_S: {ContentHash: mkNote(syncID_S, "idea", "", "# v1\n").ContentHash, Deleted: false},
			},
			remote: map[string]*Entity{syncID_S: mkNote(syncID_S, "idea", "", "# remote edit\n")},
		},
		{
			name:  "converged-delete",
			files: nil,
			index: indexNote(syncID_S, "idea.md"),
			baselines: map[string]ScenarioBaseline{
				syncID_S: {ContentHash: mkNote(syncID_S, "idea", "", "# v1\n").ContentHash, Deleted: true},
			},
			remote: map[string]*Entity{syncID_S: func() *Entity {
				e := mkNote(syncID_S, "idea", "", "# v1\n")
				e.Deleted = true
				return e
			}()},
		},
		{
			name:  "physical-missing-live",
			files: map[string]string{"idea.md": "# v1\n"},
			index: indexNote(syncID_S, "idea.md"),
			baselines: map[string]ScenarioBaseline{
				syncID_S: {ContentHash: mkNote(syncID_S, "idea", "", "# v1\n").ContentHash, Deleted: false},
			},
			remote: nil, // the object was physically removed (damage)
		},
		{
			name:  "physical-missing-absent",
			files: nil,
			index: indexNote(syncID_S, "idea.md"),
			baselines: map[string]ScenarioBaseline{
				syncID_S: {ContentHash: mkNote(syncID_S, "idea", "", "# v1\n").ContentHash, Deleted: false},
			},
			remote: nil,
		},
		{
			name:  "invalid-remote",
			files: map[string]string{"idea.md": "# v1\n"},
			index: indexNote(syncID_S, "idea.md"),
			baselines: map[string]ScenarioBaseline{
				syncID_S: {ContentHash: mkNote(syncID_S, "idea", "", "# v1\n").ContentHash, Deleted: false},
			},
			rawRemote: map[string][]byte{syncID_S: []byte(`{"schemaVersion":1,"not":"an entity"}`)},
		},
		{
			name:  "blocked-path-conflict",
			files: map[string]string{"idea.md": "# v1\n"},
			index: indexNote(syncID_S, "idea.md"),
			baselines: map[string]ScenarioBaseline{
				syncID_S: {ContentHash: mkNote(syncID_S, "idea", "", "# v1\n").ContentHash, Deleted: false},
			},
			remote:  map[string]*Entity{syncID_S: mkNote(syncID_S, "idea", "", "# v1\n")},
			blocked: map[string]bool{syncID_S: true},
		},
		{
			name: "folder-structural-conflict",
			files: map[string]string{
				"Projects/a.md": "# child\n",
			},
			index: map[string]IndexEntry{
				syncID_F: {Kind: KindFolder, Path: "Projects"},
				syncID_A: {Kind: KindNote, Path: "Projects/a.md"},
			},
			baselines: map[string]ScenarioBaseline{
				syncID_F: {ContentHash: mkFolder(syncID_F, "Projects", "").ContentHash, Deleted: false},
				syncID_A: {ContentHash: mkNote(syncID_A, "a", syncID_F, "# base\n").ContentHash, Deleted: false},
			},
			remote: map[string]*Entity{
				syncID_F: mkFolder(syncID_F, "Projects", ""),
				syncID_A: mkNote(syncID_A, "a", syncID_F, "# child\n"),
			},
			blocked: map[string]bool{syncID_F: true, syncID_A: true},
		},
		{
			name:  "parent-cycle",
			files: nil,
			index: map[string]IndexEntry{
				syncID_F: {Kind: KindFolder, Path: "F"},
				syncID_A: {Kind: KindFolder, Path: "A"},
			},
			baselines: map[string]ScenarioBaseline{
				syncID_F: {ContentHash: mkFolder(syncID_F, "F", syncID_A).ContentHash, Deleted: false},
				syncID_A: {ContentHash: mkFolder(syncID_A, "A", syncID_F).ContentHash, Deleted: false},
			},
			remote: map[string]*Entity{
				syncID_F: mkFolder(syncID_F, "F", syncID_A),
				syncID_A: mkFolder(syncID_A, "A", syncID_F),
			},
			blocked: map[string]bool{syncID_F: true, syncID_A: true},
		},
	}
}

// buildScenario runs one cycle through the simulator and captures the trace.
func buildScenario(t *testing.T, seed scenarioSeed) Scenario {
	t.Helper()
	init := ScenarioInitial{
		VaultID:         scenarioVaultID,
		ReplicaID:       scenarioReplicaID,
		RepositoryID:    scenarioRepoID,
		ProviderProfile: scenarioProfile,
		Local:           ScenarioLocalFiles{Files: seed.files, Index: seed.index},
		Remote:          ScenarioRemote{Entities: map[string]ScenarioRemoteEntity{}},
	}
	// Assign deterministic remote versions (sorted by Sync ID) so the snapshot
	// baselines and CAS preconditions line up.
	var remoteIDs []string
	for id := range seed.remote {
		if seed.remote[id] != nil {
			remoteIDs = append(remoteIDs, id)
		}
	}
	for id := range seed.rawRemote {
		remoteIDs = append(remoteIDs, id)
	}
	sort.Strings(remoteIDs)
	for i, id := range remoteIDs {
		version := strconv.Itoa(i + 1)
		if e, ok := seed.remote[id]; ok && e != nil {
			init.Remote.Entities[id] = ScenarioRemoteEntity{Version: version, Entity: e}
		} else if raw, ok := seed.rawRemote[id]; ok {
			init.Remote.Entities[id] = ScenarioRemoteEntity{Version: version, RawBase64: base64.StdEncoding.EncodeToString(raw)}
		}
	}
	if seed.baselines != nil {
		snap := &ScenarioSnapshot{Entities: map[string]ScenarioBaseline{}}
		for id, b := range seed.baselines {
			if re, ok := init.Remote.Entities[id]; ok {
				b.RemoteVersion = re.Version
			} else {
				b.RemoteVersion = "1"
			}
			snap.Entities[id] = b
		}
		init.Snapshot = snap
	}
	init.Blocked = blockedList(seed.blocked)

	s, err := NewSim(init)
	if err != nil {
		t.Fatalf("%s: NewSim: %v", seed.name, err)
	}
	initial, _ := s.state()
	obs := s.observations(context.Background())
	if seed.blocked != nil {
		for i := range obs {
			if seed.blocked[obs[i].SyncID] {
				obs[i].Blocked = true
			}
		}
	}
	plan, _, err := s.RunCycle(context.Background(), StepDone)
	if err != nil {
		t.Fatalf("%s: cycle: %v", seed.name, err)
	}
	_, final := s.state()
	return Scenario{
		Name:         seed.name,
		Initial:      initial,
		Observations: obs,
		Expected:     normalizeDecisions(plan),
		Final:        final,
	}
}

func blockedList(m map[string]bool) []string {
	var out []string
	for id := range m {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

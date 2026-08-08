package cloudsync

// The shared scenario runner: it executes the pure reconciliation decisions
// against in-memory local files, a portable index, a memory remote, and the
// snapshot baselines, so the engine's behavior is proven without filesystem,
// IndexedDB, HTTP, or providers. Both the Go and TypeScript suites consume the
// same traces under testdata/sync/scenarios.

import (
	"context"
	"encoding/base64"
	"fmt"
	"sort"
	"strings"
)

// EntityKeyPrefix is the remote key prefix for entity records.
const EntityKeyPrefix = "entities/"

func entityKey(syncID string) string { return EntityKeyPrefix + syncID + ".json" }

// IndexEntry is the scenario's portable-index shape (kind + path).
type IndexEntry struct {
	Kind string `json:"kind"`
	Path string `json:"path"`
}

// ScenarioBaseline is one entity's snapshot baseline in a scenario.
type ScenarioBaseline struct {
	ContentHash   string `json:"contentHash"`
	RemoteVersion string `json:"remoteVersion"`
	Deleted       bool   `json:"deleted"`
}

// ScenarioRemoteEntity is one remote object in a scenario. RawBase64, when set,
// stores those bytes verbatim instead of a serialized entity, so an invalid
// record can be injected.
type ScenarioRemoteEntity struct {
	Version   string  `json:"version"`
	Entity    *Entity `json:"entity,omitempty"`
	RawBase64 string  `json:"rawBase64,omitempty"`
}

// ScenarioLocalFiles is the scenario's local vault: path -> markdown plus the
// portable index.
type ScenarioLocalFiles struct {
	Files map[string]string     `json:"files"`
	Index map[string]IndexEntry `json:"index"`
}

// ScenarioSnapshot is the scenario's durable snapshot.
type ScenarioSnapshot struct {
	Entities map[string]ScenarioBaseline `json:"entities"`
	Cursor   string                      `json:"cursor"`
}

// ScenarioRemote is the scenario's remote repository contents.
type ScenarioRemote struct {
	Entities map[string]ScenarioRemoteEntity `json:"entities"`
}

// ScenarioInitial is the scenario's initial durable state.
type ScenarioInitial struct {
	VaultID         string             `json:"vaultId"`
	ReplicaID       string             `json:"replicaId"`
	RepositoryID    string             `json:"repositoryId"`
	ProviderProfile string             `json:"providerProfile"`
	Local           ScenarioLocalFiles `json:"local"`
	Snapshot        *ScenarioSnapshot  `json:"snapshot"`
	Remote          ScenarioRemote     `json:"remote"`
	// Recovery holds recoverable delete copies keyed by Sync ID.
	Recovery map[string]ScenarioRecovery `json:"recovery"`
	// Blocked lists Sync IDs carrying a pre-computed path/graph conflict
	// annotation (a path collision, parent cycle, or structural conflict).
	Blocked []string `json:"blocked,omitempty"`
}

// ScenarioRecovery is one recoverable delete copy (content, never sync state).
type ScenarioRecovery struct {
	StateHash string `json:"stateHash"`
	Markdown  string `json:"markdown"`
}

// ScenarioLocalObs is one entity's derived local input.
type ScenarioLocalObs struct {
	State    string  `json:"state"` // live / absent / unknown
	Entity   *Entity `json:"entity,omitempty"`
	Revision string  `json:"revision,omitempty"`
}

// ScenarioRemoteObs is one entity's derived remote input.
type ScenarioRemoteObs struct {
	State   string  `json:"state"` // live / tombstone / missing / invalid
	Entity  *Entity `json:"entity,omitempty"`
	Version string  `json:"version,omitempty"`
}

// ScenarioObservation is one entity's derived pure engine inputs.
type ScenarioObservation struct {
	SyncID   string            `json:"syncId"`
	Local    ScenarioLocalObs  `json:"local"`
	Remote   ScenarioRemoteObs `json:"remote"`
	Baseline *ScenarioBaseline `json:"baseline,omitempty"`
	Blocked  bool              `json:"blocked,omitempty"`
}

// ScenarioConflict is the normalized conflict payload of an expected decision.
type ScenarioConflict struct {
	SourceSyncID            string  `json:"sourceSyncId"`
	ConflictSyncID          string  `json:"conflictSyncId"`
	ConflictEntity          *Entity `json:"conflictEntity"`
	OriginalTombstone       bool    `json:"originalTombstone"`
	OriginalVersion         string  `json:"originalVersion,omitempty"`
	AcceptRemoteOriginal    bool    `json:"acceptRemoteOriginal"`
	OriginalEntity          *Entity `json:"originalEntity,omitempty"`
	OriginalTombstoneEntity *Entity `json:"originalTombstoneEntity,omitempty"`
	LocalStateHash          string  `json:"localStateHash"`
	RemoteStateHash         string  `json:"remoteStateHash"`
}

// ScenarioDecision is the normalized expected decision for one entity.
type ScenarioDecision struct {
	SyncID        string            `json:"syncId"`
	Kind          string            `json:"kind"`
	Reason        string            `json:"reason,omitempty"`
	ParentID      string            `json:"parentId,omitempty"`
	ContentHash   string            `json:"contentHash,omitempty"`
	Deleted       *bool             `json:"deleted"`
	Version       string            `json:"version,omitempty"`
	LocalRevision string            `json:"localRevision,omitempty"`
	Conflict      *ScenarioConflict `json:"conflict,omitempty"`
}

// ScenarioFinal is the durable state after one executed cycle.
type ScenarioFinal struct {
	Local    ScenarioLocalFiles              `json:"local"`
	Remote   map[string]ScenarioRemoteEntity `json:"remote"`
	Snapshot *ScenarioSnapshot               `json:"snapshot"`
	Recovery map[string]ScenarioRecovery     `json:"recovery"`
}

// Scenario is one full shared trace.
type Scenario struct {
	Name         string                `json:"name"`
	Initial      ScenarioInitial       `json:"initial"`
	Observations []ScenarioObservation `json:"observations"`
	Expected     []ScenarioDecision    `json:"expected"`
	Final        ScenarioFinal         `json:"final"`
}

// Sim executes decision cycles against in-memory durable state. Restarting is
// just constructing a new Sim from the current durable state; the cycle
// reconstructs current local and remote truth from scratch.
type Sim struct {
	VaultID      string
	ReplicaID    string
	RepositoryID string
	Profile      string

	files        map[string]string     // path -> markdown
	index        map[string]IndexEntry // syncID -> entry
	dirs         map[string]bool       // vault directories (folder paths)
	remote       *MemoryStore
	baselines    map[string]ScenarioBaseline // durable snapshot
	cursor       string
	recovery     map[string]ScenarioRecovery // syncID -> recoverable delete copy
	revisions    map[string]string           // path -> local CAS token
	blocked      map[string]bool             // sync IDs with a path/graph conflict annotation
	failRecovery bool                        // inject a recovery write failure
	seq          int
}

// NewSim builds a simulator from the scenario's initial durable state. Remote
// entities are installed at their recorded versions (stable across restarts),
// and the snapshot baselines are applied as given.
func NewSim(init ScenarioInitial) (*Sim, error) {
	s := &Sim{
		VaultID:      init.VaultID,
		ReplicaID:    init.ReplicaID,
		RepositoryID: init.RepositoryID,
		Profile:      init.ProviderProfile,
		files:        make(map[string]string, len(init.Local.Files)),
		index:        make(map[string]IndexEntry, len(init.Local.Index)),
		dirs:         make(map[string]bool),
		remote:       NewMemoryStore(),
		baselines:    make(map[string]ScenarioBaseline),
		recovery:     make(map[string]ScenarioRecovery, len(init.Recovery)),
		revisions:    make(map[string]string),
		blocked:      make(map[string]bool, len(init.Blocked)),
	}
	for p, md := range init.Local.Files {
		s.files[p] = md
	}
	for id, e := range init.Local.Index {
		s.index[id] = e
		if e.Kind == KindFolder {
			s.dirs[e.Path] = true
		}
	}
	for p := range init.Local.Files {
		dir := p
		for {
			i := strings.LastIndex(dir, "/")
			if i < 0 {
				break
			}
			dir = dir[:i]
			if dir == "" {
				break
			}
			s.dirs[dir] = true
		}
	}
	for id, rec := range init.Recovery {
		s.recovery[id] = rec
	}
	for _, id := range init.Blocked {
		s.blocked[id] = true
	}
	// Remote objects are installed at their recorded version so a restart from
	// durable state reproduces identical versions.
	for syncID, re := range init.Remote.Entities {
		var data []byte
		if re.RawBase64 != "" {
			dec, err := base64.StdEncoding.DecodeString(re.RawBase64)
			if err != nil {
				return nil, err
			}
			data = dec
		} else if re.Entity != nil {
			ser, err := re.Entity.Serialize()
			if err != nil {
				return nil, err
			}
			data = ser
		} else {
			continue // a physically absent remote object
		}
		if re.Version != "" {
			if err := s.remote.Seed(entityKey(syncID), data, re.Version); err != nil {
				return nil, err
			}
		} else if _, err := s.remote.Create(context.Background(), entityKey(syncID), data); err != nil {
			return nil, err
		}
	}
	if init.Snapshot != nil {
		for id, b := range init.Snapshot.Entities {
			s.baselines[id] = b
		}
		s.cursor = init.Snapshot.Cursor
	}
	return s, nil
}

// state captures the current durable state for fixture writing and assertions.
func (s *Sim) state() (ScenarioInitial, ScenarioFinal) {
	blocked := make([]string, 0, len(s.blocked))
	for id := range s.blocked {
		blocked = append(blocked, id)
	}
	sort.Strings(blocked)
	init := ScenarioInitial{
		VaultID:         s.VaultID,
		ReplicaID:       s.ReplicaID,
		RepositoryID:    s.RepositoryID,
		ProviderProfile: s.Profile,
		Local:           ScenarioLocalFiles{Files: cloneMap(s.files), Index: cloneIndex(s.index)},
		Remote:          ScenarioRemote{Entities: s.remoteEntities()},
		Recovery:        cloneRecovery(s.recovery),
		Blocked:         blocked,
	}
	snap := &ScenarioSnapshot{Entities: cloneBaselines(s.baselines), Cursor: s.cursor}
	if len(s.baselines) == 0 && s.cursor == "" {
		snap = nil
	}
	init.Snapshot = snap
	final := ScenarioFinal{
		Local:    ScenarioLocalFiles{Files: cloneMap(s.files), Index: cloneIndex(s.index)},
		Remote:   s.remoteEntities(),
		Snapshot: snap,
		Recovery: cloneRecovery(s.recovery),
	}
	return init, final
}

// remoteEntities reads the current remote contents back through the memory
// store (full listing + reads), so the capture reflects real versions.
func (s *Sim) remoteEntities() map[string]ScenarioRemoteEntity {
	out := make(map[string]ScenarioRemoteEntity)
	ctx := context.Background()
	page, err := s.remote.List(ctx, EntityKeyPrefix, "")
	for err == nil {
		for _, c := range page.Changes {
			data, version, rerr := s.remote.Read(ctx, c.Key)
			if rerr != nil {
				continue
			}
			id := strings.TrimSuffix(strings.TrimPrefix(c.Key, EntityKeyPrefix), ".json")
			if ent, perr := ParseEntity(data); perr == nil {
				out[id] = ScenarioRemoteEntity{Version: version, Entity: ent}
			} else {
				out[id] = ScenarioRemoteEntity{Version: version, RawBase64: base64.StdEncoding.EncodeToString(data)}
			}
		}
		if page.NextCursor == "" {
			break
		}
		page, err = s.remote.List(ctx, EntityKeyPrefix, page.NextCursor)
	}
	return out
}

// unionIDs is the sorted set of Sync IDs found in the index, the snapshot, or
// the remote listing.
func (s *Sim) unionIDs() []string {
	set := make(map[string]bool)
	for id := range s.index {
		set[id] = true
	}
	for id := range s.baselines {
		set[id] = true
	}
	for id := range s.remoteEntities() {
		set[id] = true
	}
	ids := make([]string, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// folderIDAt returns the indexed folder Sync ID for a vault path ("" for root
// or an unindexed folder).
func (s *Sim) folderIDAt(path string) string {
	if path == "" {
		return ""
	}
	for id, e := range s.index {
		if e.Kind == KindFolder && e.Path == path {
			return id
		}
	}
	return ""
}

// buildLocalEntity constructs the canonical local entity for an indexed path.
func (s *Sim) buildLocalEntity(syncID, path, kind string) *Entity {
	parentPath := ""
	if i := strings.LastIndex(path, "/"); i >= 0 {
		parentPath = path[:i]
	}
	name := path
	if i := strings.LastIndex(path, "/"); i >= 0 {
		name = path[i+1:]
	}
	e := &Entity{
		SchemaVersion: SchemaVersion,
		SyncID:        syncID,
		Kind:          kind,
		ParentID:      s.folderIDAt(parentPath),
		Name:          name,
		UpdatedBy:     "1a2b3c4d-1111-4222-8333-444455556666",
		UpdatedAt:     1785800000000,
	}
	if kind == KindNote {
		e.Name = strings.TrimSuffix(name, ".md")
		e.Markdown = s.files[path]
	}
	e.ContentHash = e.ComputeContentHash()
	return e
}

// observeLocal derives the local observation for every union Sync ID.
func (s *Sim) observeLocal() map[string]LocalObservation {
	obs := make(map[string]LocalObservation)
	for _, id := range s.unionIDs() {
		entry, indexed := s.index[id]
		if !indexed {
			obs[id] = LocalObservation{SyncID: id, State: LocalAbsent}
			continue
		}
		if entry.Kind == KindFolder {
			if s.dirs[entry.Path] {
				e := s.buildLocalEntity(id, entry.Path, entry.Kind)
				obs[id] = LocalObservation{
					SyncID: id, Kind: entry.Kind, State: LocalLive, Entity: e,
					Revision: s.revisions[entry.Path],
				}
			} else {
				obs[id] = LocalObservation{SyncID: id, Kind: entry.Kind, State: LocalAbsent}
			}
			continue
		}
		if _, ok := s.files[entry.Path]; ok {
			e := s.buildLocalEntity(id, entry.Path, entry.Kind)
			obs[id] = LocalObservation{
				SyncID: id, Kind: entry.Kind, State: LocalLive, Entity: e,
				Revision: s.revisions[entry.Path],
			}
		} else {
			obs[id] = LocalObservation{SyncID: id, Kind: entry.Kind, State: LocalAbsent}
		}
	}
	return obs
}

// observeRemote derives the remote observation for every union Sync ID from a
// full listing followed by reads.
func (s *Sim) observeRemote(ctx context.Context) map[string]RemoteObservation {
	keys := make(map[string]bool)
	page, err := s.remote.List(ctx, EntityKeyPrefix, "")
	for err == nil {
		for _, c := range page.Changes {
			keys[c.Key] = true
		}
		if page.NextCursor == "" {
			break
		}
		page, err = s.remote.List(ctx, EntityKeyPrefix, page.NextCursor)
	}
	obs := make(map[string]RemoteObservation)
	for _, id := range s.unionIDs() {
		key := entityKey(id)
		if !keys[key] {
			obs[id] = RemoteObservation{SyncID: id, State: RemoteMissing}
			continue
		}
		data, version, rerr := s.remote.Read(ctx, key)
		if rerr != nil {
			obs[id] = RemoteObservation{
				SyncID: id, State: RemoteInvalid,
				Retryable: IsStoreError(rerr, ErrRetryableTransport),
			}
			continue
		}
		ent, perr := ParseEntity(data)
		if perr != nil {
			obs[id] = RemoteObservation{SyncID: id, State: RemoteInvalid}
			continue
		}
		st := RemoteLive
		if ent.Deleted {
			st = RemoteTombstone
		}
		obs[id] = RemoteObservation{SyncID: id, Kind: ent.Kind, State: st, Entity: ent, Version: version}
	}
	return obs
}

// observations derives the pure engine inputs for every union Sync ID.
func (s *Sim) observations(ctx context.Context) []ScenarioObservation {
	locals := s.observeLocal()
	remotes := s.observeRemote(ctx)
	var out []ScenarioObservation
	for _, id := range s.unionIDs() {
		l := locals[id]
		r := remotes[id]
		lo := ScenarioLocalObs{State: l.State.String()}
		if l.Entity != nil {
			lo.Entity = l.Entity
			lo.Revision = l.Revision
		}
		ro := ScenarioRemoteObs{State: r.State.String()}
		if r.Entity != nil {
			ro.Entity = r.Entity
			ro.Version = r.Version
		}
		var b *ScenarioBaseline
		if bl, ok := s.baselines[id]; ok {
			cp := bl
			b = &cp
		}
		out = append(out, ScenarioObservation{SyncID: id, Local: lo, Remote: ro, Baseline: b})
	}
	return out
}

// decide computes the planned decisions for every union Sync ID.
func (s *Sim) decide(ctx context.Context) []Decision {
	locals := s.observeLocal()
	remotes := s.observeRemote(ctx)
	ds := make([]Decision, 0, len(s.unionIDs()))
	for _, id := range s.unionIDs() {
		var b *Baseline
		if bl, ok := s.baselines[id]; ok {
			b = &Baseline{ContentHash: bl.ContentHash, Deleted: bl.Deleted, RemoteVersion: bl.RemoteVersion}
		}
		ds = append(ds, DecideEntity(locals[id], remotes[id], b, Annotations{PathConflict: s.blocked[id]}))
	}
	return DecideRepository(ds)
}

// Step identifies an execution boundary where a cycle may stop (a crash point).
type Step int

const (
	// StepNone: stop before any execution (observe/decide only).
	StepNone Step = iota
	// StepIndex: stop after conflict/identity reservation in the index.
	StepIndex
	// StepConflict: stop after the conflict copies are created and verified
	// locally and remotely, before the original is touched.
	StepConflict
	// StepRecovery: stop after recovery copies for deletions.
	StepRecovery
	// StepLocal: stop after local file mutations.
	StepLocal
	// StepRemote: stop after remote writes.
	StepRemote
	// StepSnapshot: stop after the snapshot baseline commit (cycle complete).
	StepSnapshot
	// StepDone: run every step.
	StepDone
)

// RunCycle executes one cycle up to (but not including) stop. It returns the
// planned decisions and the number of side-effecting decisions in the plan.
func (s *Sim) RunCycle(ctx context.Context, stop Step) ([]Decision, int, error) {
	plan := s.decide(ctx)
	actions := 0
	for _, d := range plan {
		switch d.Kind {
		case DecisionNoop, DecisionBlock, DecisionRetry:
			continue
		}
		actions++
	}
	if stop == StepNone {
		return plan, 0, nil
	}

	// 1. Index reservations (conflict identities) before the original changes.
	if err := s.applyIndex(plan); err != nil {
		return plan, actions, err
	}
	if stop == StepIndex {
		return plan, actions, nil
	}
	// 2. Conflict copies: create and verify local and remote, never touching
	//    the original until both conflict copies are settled.
	if err := s.applyConflicts(plan); err != nil {
		return plan, actions, err
	}
	if stop == StepConflict {
		return plan, actions, nil
	}
	// 3. Recovery copies must succeed before the deletions they guard.
	if err := s.applyRecovery(plan); err != nil {
		return plan, actions, err
	}
	if stop == StepRecovery {
		return plan, actions, nil
	}
	// 4. Local mutations (pull, original handling, apply-tombstone deletes).
	s.applyLocal(plan)
	if stop == StepLocal {
		return plan, actions, nil
	}
	// 5. Remote writes (pushes, tombstones, original remote tombstones).
	if err := s.applyRemote(plan); err != nil {
		return plan, actions, err
	}
	if stop == StepRemote {
		return plan, actions, nil
	}
	// 6. Snapshot baseline commit.
	s.applyBaselines(plan)
	return plan, actions, nil
}

// applyIndex reserves deterministic conflict identities before the original is
// replaced or deleted.
func (s *Sim) applyIndex(plan []Decision) error {
	for _, d := range plan {
		if d.Kind != DecisionCreateConflict {
			continue
		}
		c := d.Conflict
		if _, ok := s.index[c.ConflictSyncID]; ok {
			continue // already reserved
		}
		path := s.pathForEntity(c.ConflictEntity)
		if _, taken := s.indexPathToSyncID(path); taken {
			return fmt.Errorf("conflict path collision: %s", path)
		}
		s.index[c.ConflictSyncID] = IndexEntry{Kind: KindNote, Path: path}
	}
	return nil
}

// applyConflicts creates and VERIFIES the deterministic conflict copies locally
// and remotely before the original is modified. An existing object is
// idempotent success only when its Sync ID and canonical state match; any
// unrelated collision blocks instead of deriving a second conflict copy.
func (s *Sim) applyConflicts(plan []Decision) error {
	for _, d := range plan {
		if d.Kind != DecisionCreateConflict {
			continue
		}
		c := d.Conflict
		if c == nil {
			continue
		}
		// Local conflict copy: create-if-absent, verify identical content.
		path := s.pathForEntity(c.ConflictEntity)
		if md, ok := s.files[path]; ok {
			if md != c.ConflictEntity.Markdown {
				return fmt.Errorf("conflict local path collision: %s", path)
			}
		} else {
			s.writeLocal(path, c.ConflictEntity.Markdown)
		}
		// Remote conflict copy: create-if-absent; verify on existence or an
		// uncertain response.
		data, err := c.ConflictEntity.Serialize()
		if err != nil {
			return err
		}
		if err := s.createRemoteVerified(entityKey(c.ConflictSyncID), data, c.ConflictEntity); err != nil {
			return err
		}
	}
	return nil
}

func (s *Sim) indexPathToSyncID(path string) (string, bool) {
	for id, e := range s.index {
		if e.Path == path {
			return id, true
		}
	}
	return "", false
}

// pathForEntity resolves the vault path of an entity from the indexed folder
// graph.
func (s *Sim) pathForEntity(e *Entity) string {
	dir := ""
	if e.ParentID != "" {
		if p, ok := s.index[e.ParentID]; ok {
			dir = p.Path
		}
	}
	name := e.Name
	if e.Kind == KindNote {
		name += ".md"
	}
	if dir == "" {
		return name
	}
	return dir + "/" + name
}

// createRemoteVerified creates key with data, then re-reads to confirm the
// intended canonical state on any error: a precondition failure means an
// unrelated collision (never idempotent unless the state matches), and a
// lost/uncertain response is idempotent success when the write landed.
func (s *Sim) createRemoteVerified(key string, data []byte, expected *Entity) error {
	ctx := context.Background()
	if _, err := s.remote.Create(ctx, key, data); err == nil {
		return nil
	}
	existing, _, rerr := s.remote.Read(ctx, key)
	if rerr != nil {
		return rerr
	}
	parsed, perr := ParseEntity(existing)
	if perr != nil || parsed.SyncID != expected.SyncID ||
		parsed.ContentHash != expected.ContentHash || parsed.Deleted != expected.Deleted {
		return fmt.Errorf("remote create collision at %s", key)
	}
	return nil // idempotent success: identical canonical state already present
}

// replaceRemoteVerified replaces key with data at version, re-reading on any
// failure. A write that landed with the intended state is idempotent success; a
// stale precondition (or unrelated remote state) is left to the next cycle to
// re-read and re-decide — never an unconditional write.
func (s *Sim) replaceRemoteVerified(key string, data []byte, expected *Entity, version string) error {
	ctx := context.Background()
	if _, err := s.remote.Replace(ctx, key, data, version); err == nil {
		return nil
	}
	existing, _, rerr := s.remote.Read(ctx, key)
	if rerr != nil {
		return rerr
	}
	parsed, perr := ParseEntity(existing)
	if perr == nil && parsed.SyncID == expected.SyncID &&
		parsed.ContentHash == expected.ContentHash && parsed.Deleted == expected.Deleted {
		return nil // uncertain write landed idempotently
	}
	return nil // stale CAS or divergence: the next cycle re-reads and re-decides
}

// applyLocal applies local file mutations from pull and deletion decisions.
// Conflict copies were already created and verified in applyConflicts; here the
// ORIGINAL of a conflict is resolved (accept the remote live entity, or delete
// the local original), and apply-tombstone deletes run after recovery.
func (s *Sim) applyLocal(plan []Decision) {
	for _, d := range plan {
		switch d.Kind {
		case DecisionPullLive:
			path := s.pathForEntity(d.Entity)
			s.writeLocal(path, d.Entity.Markdown)
			if entry, ok := s.index[d.SyncID]; ok {
				entry.Kind = d.Entity.Kind
				entry.Path = path
				s.index[d.SyncID] = entry
			} else {
				s.index[d.SyncID] = IndexEntry{Kind: d.Entity.Kind, Path: path}
			}
		case DecisionCreateConflict:
			c := d.Conflict
			if c == nil {
				continue
			}
			if c.AcceptRemoteOriginal && c.OriginalEntity != nil {
				opath := s.pathForEntity(c.OriginalEntity)
				s.writeLocal(opath, c.OriginalEntity.Markdown)
				if entry, ok := s.index[d.SyncID]; ok {
					entry.Kind = c.OriginalEntity.Kind
					entry.Path = opath
					s.index[d.SyncID] = entry
				}
			} else if c.OriginalTombstone {
				// The original is (or becomes) tombstoned: delete any local
				// original content (recovery already recorded it).
				if entry, ok := s.index[d.SyncID]; ok {
					s.deleteLocal(entry.Path)
				}
			}
		case DecisionApplyTombstone:
			if entry, ok := s.index[d.SyncID]; ok {
				s.deleteLocal(entry.Path)
			}
		}
	}
}

// applyRemote applies remote create/replace/tombstone writes with CAS. Conflict
// copies were created and verified in applyConflicts; here only the original
// remote tombstone of a conflict (when the original is not already a tombstone)
// is written.
func (s *Sim) applyRemote(plan []Decision) error {
	for _, d := range plan {
		switch d.Kind {
		case DecisionPushLive:
			data, err := d.Entity.Serialize()
			if err != nil {
				return err
			}
			if d.Version == "" {
				if err := s.createRemoteVerified(entityKey(d.SyncID), data, d.Entity); err != nil {
					return err
				}
			} else if err := s.replaceRemoteVerified(entityKey(d.SyncID), data, d.Entity, d.Version); err != nil {
				return err
			}
		case DecisionPushTombstone:
			data, err := d.Entity.Serialize()
			if err != nil {
				return err
			}
			if err := s.replaceRemoteVerified(entityKey(d.SyncID), data, d.Entity, d.Version); err != nil {
				return err
			}
		case DecisionCreateConflict:
			c := d.Conflict
			if c == nil {
				continue
			}
			if c.OriginalTombstone && c.OriginalVersion != "" && c.OriginalTombstoneEntity != nil {
				data, err := c.OriginalTombstoneEntity.Serialize()
				if err != nil {
					return err
				}
				if err := s.replaceRemoteVerified(entityKey(d.SyncID), data, c.OriginalTombstoneEntity, c.OriginalVersion); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// applyRecovery writes recovery copies for every entity that will be deleted
// locally (apply-tombstone and conflict originals). It runs BEFORE applyLocal
// and returns an error on failure, so a failed recovery write prevents the
// deletion. Writing the same (stateHash, markdown) again is idempotent.
func (s *Sim) applyRecovery(plan []Decision) error {
	if s.failRecovery {
		return fmt.Errorf("injected recovery failure")
	}
	for _, d := range plan {
		switch d.Kind {
		case DecisionApplyTombstone:
			if err := s.recoverEntity(d.SyncID); err != nil {
				return err
			}
		case DecisionCreateConflict:
			if c := d.Conflict; c != nil && c.OriginalTombstone {
				if err := s.recoverEntity(d.SyncID); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (s *Sim) recoverEntity(syncID string) error {
	entry, ok := s.index[syncID]
	if !ok {
		return nil
	}
	md, ok := s.files[entry.Path]
	if !ok {
		return nil // nothing present to recover
	}
	e := s.buildLocalEntity(syncID, entry.Path, entry.Kind)
	s.recovery[syncID] = ScenarioRecovery{StateHash: StateHash(e.ContentHash, false), Markdown: md}
	return nil
}

// applyBaselines commits the established baselines to the durable snapshot and
// advances the cursor only when every planned change reached a safe terminal
// state (no blocks, retries, or pending repairs).
func (s *Sim) applyBaselines(plan []Decision) {
	advanced := true
	for _, d := range plan {
		switch d.Kind {
		case DecisionBlock, DecisionRetry, DecisionRepairIndex:
			advanced = false
		}
	}
	for _, d := range plan {
		if d.Kind != DecisionEstablishBaseline {
			continue
		}
		s.baselines[d.SyncID] = ScenarioBaseline{
			ContentHash: d.ContentHash, Deleted: d.Deleted, RemoteVersion: d.Version,
		}
	}
	if advanced && s.cursor == "" {
		s.cursor = "c1"
	}
}

func (s *Sim) writeLocal(path, markdown string) {
	s.files[path] = markdown
	s.seq++
	s.revisions[path] = fmt.Sprintf("r%d", s.seq)
}

func (s *Sim) deleteLocal(path string) {
	delete(s.files, path)
	s.seq++
	s.revisions[path] = fmt.Sprintf("r%d", s.seq)
}

// Quiescent reports whether the cycle's planned decisions require no further
// side-effecting work (only noop/block/retry/repair-index remain). A repair
// index decision is terminal: it surfaces a requirement for an explicit user
// decision (an ambiguous absence), not a sync action.
func Quiescent(plan []Decision) bool {
	for _, d := range plan {
		switch d.Kind {
		case DecisionNoop, DecisionBlock, DecisionRetry, DecisionRepairIndex:
			continue
		default:
			return false
		}
	}
	return true
}

// RunUntilQuiescent runs full cycles until no side-effecting work remains. It
// returns the number of cycles run.
func (s *Sim) RunUntilQuiescent(ctx context.Context) (int, error) {
	for i := 1; i <= 20; i++ {
		plan, _, err := s.RunCycle(ctx, StepDone)
		if err != nil {
			return i, err
		}
		if Quiescent(plan) {
			return i, nil
		}
	}
	return 20, fmt.Errorf("did not converge within 20 cycles")
}

// normalizeDecisions converts planned decisions into the normalized trace form
// shared with the TypeScript suite.
func normalizeDecisions(plan []Decision) []ScenarioDecision {
	out := make([]ScenarioDecision, 0, len(plan))
	for _, d := range plan {
		del := d.Deleted
		sd := ScenarioDecision{
			SyncID:        d.SyncID,
			Kind:          d.Kind.String(),
			Reason:        d.Reason,
			ParentID:      d.ParentID,
			ContentHash:   d.ContentHash,
			Deleted:       &del,
			Version:       d.Version,
			LocalRevision: d.LocalRevision,
		}
		if d.Conflict != nil {
			c := d.Conflict
			sd.Conflict = &ScenarioConflict{
				SourceSyncID:            c.SourceSyncID,
				ConflictSyncID:          c.ConflictSyncID,
				ConflictEntity:          c.ConflictEntity,
				OriginalTombstone:       c.OriginalTombstone,
				OriginalVersion:         c.OriginalVersion,
				AcceptRemoteOriginal:    c.AcceptRemoteOriginal,
				OriginalEntity:          c.OriginalEntity,
				OriginalTombstoneEntity: c.OriginalTombstoneEntity,
				LocalStateHash:          c.LocalStateHash,
				RemoteStateHash:         c.RemoteStateHash,
			}
		}
		out = append(out, sd)
	}
	return out
}

func cloneMap(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func cloneIndex(m map[string]IndexEntry) map[string]IndexEntry {
	out := make(map[string]IndexEntry, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func cloneBaselines(m map[string]ScenarioBaseline) map[string]ScenarioBaseline {
	out := make(map[string]ScenarioBaseline, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func cloneRecovery(m map[string]ScenarioRecovery) map[string]ScenarioRecovery {
	out := make(map[string]ScenarioRecovery, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

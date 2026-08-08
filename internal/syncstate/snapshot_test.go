package syncstate

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func validSnapshot() *Snapshot {
	return &Snapshot{
		SchemaVersion:   SnapshotSchemaVersion,
		VaultID:         "11111111-1111-4111-8111-111111111111",
		ReplicaID:       "22222222-2222-4222-8222-222222222222",
		RepositoryID:    "33333333-3333-4333-8333-333333333333",
		ProviderProfile: strings.Repeat("a", 64),
		Entities: map[string]SnapshotEntity{
			"44444444-4444-4444-8444-444444444444": {
				ContentHash:   strings.Repeat("b", 64),
				RemoteVersion: "v1",
			},
			"04b2cbe6-19cf-584f-bad4-55fa03d9c05a": { // a deterministic v5 conflict ID
				ContentHash:   strings.Repeat("c", 64),
				RemoteVersion: "v2",
				Deleted:       true,
			},
		},
		Cursor: "opaque-token",
	}
}

func identityFor(s *Snapshot) ExpectedIdentity {
	return ExpectedIdentity{
		VaultID: s.VaultID, ReplicaID: s.ReplicaID,
		ProviderProfile: s.ProviderProfile, RepositoryID: s.RepositoryID,
	}
}

func newStore(dir string, io fsIO) *SnapshotStore {
	return &SnapshotStore{dir: dir, io: io}
}

func TestSnapshotRoundTripDeterministic(t *testing.T) {
	snap := validSnapshot()
	ser, err := snap.Serialize()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseSnapshot(ser)
	if err != nil {
		t.Fatalf("parse canonical snapshot: %v", err)
	}
	ser2, _ := parsed.Serialize()
	if string(ser) != string(ser2) {
		t.Fatalf("round trip not deterministic\n got %q\nwant %q", ser2, ser)
	}
	if parsed.Cursor != snap.Cursor || len(parsed.Entities) != len(snap.Entities) {
		t.Fatalf("round trip lost fields: %+v", parsed)
	}
}

func TestSnapshotValidateRejectsBadInvariants(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Snapshot)
	}{
		{"schema", func(s *Snapshot) { s.SchemaVersion = 2 }},
		{"vault id", func(s *Snapshot) { s.VaultID = "not-a-uuid" }},
		{"replica id", func(s *Snapshot) { s.ReplicaID = "nope" }},
		{"repository id", func(s *Snapshot) { s.RepositoryID = "04b2cbe6-19cf-584f-bad4-55fa03d9c05a" }}, // v5 not allowed
		{"provider profile", func(s *Snapshot) { s.ProviderProfile = "ABC" }},
		{"nil entities", func(s *Snapshot) { s.Entities = nil }},
		{"bad sync id", func(s *Snapshot) { s.Entities["nope"] = s.Entities["44444444-4444-4444-8444-444444444444"] }},
		{"bad content hash", func(s *Snapshot) {
			s.Entities["44444444-4444-4444-8444-444444444444"] = SnapshotEntity{ContentHash: "abc", RemoteVersion: "v1"}
		}},
		{"empty remote version", func(s *Snapshot) {
			s.Entities["44444444-4444-4444-8444-444444444444"] = SnapshotEntity{ContentHash: strings.Repeat("b", 64), RemoteVersion: ""}
		}},
	}
	for _, tc := range cases {
		s := validSnapshot()
		tc.mutate(s)
		if err := s.Validate(); err == nil {
			t.Errorf("%s: accepted", tc.name)
		}
	}
}

func TestParseSnapshotRejectsStrictJSON(t *testing.T) {
	snap := validSnapshot()
	ser, _ := snap.Serialize()
	doc := strings.TrimSuffix(string(ser), "\n")
	bad := []struct {
		name string
		json string
	}{
		{"truncated", doc[:len(doc)/2]},
		{"not an object", `[]`},
		{"unknown field", `{"schemaVersion":1,"vaultId":"11111111-1111-4111-8111-111111111111","replicaId":"22222222-2222-4222-8222-222222222222","repositoryId":"33333333-3333-4333-8333-333333333333","providerProfile":"` + strings.Repeat("a", 64) + `","entities":{},"evil":true}`},
		{"missing field", `{"schemaVersion":1,"vaultId":"11111111-1111-4111-8111-111111111111","replicaId":"22222222-2222-4222-8222-222222222222","repositoryId":"33333333-3333-4333-8333-333333333333","providerProfile":"` + strings.Repeat("a", 64) + `"}`},
		{"trailing content", doc + `{}`},
		{"wrong type", `{"schemaVersion":"one","vaultId":"11111111-1111-4111-8111-111111111111","replicaId":"22222222-2222-4222-8222-222222222222","repositoryId":"33333333-3333-4333-8333-333333333333","providerProfile":"` + strings.Repeat("a", 64) + `","entities":{}}`},
	}
	dup := strings.Replace(doc, `"schemaVersion":1`, `"schemaVersion":1,"schemaVersion":1`, 1)
	bad = append(bad, struct{ name, json string }{"duplicate field", dup})
	for _, tc := range bad {
		if _, err := ParseSnapshot([]byte(tc.json)); err == nil {
			t.Errorf("%s: accepted", tc.name)
		}
	}
}

func TestSnapshotStoreReplaceLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	st := newStore(dir, osFsIO{})
	snap := validSnapshot()
	if err := st.Replace(snap); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(st.Path()); err != nil {
		t.Fatalf("state.json not written: %v", err)
	}
	loaded, reason, err := st.Load(identityFor(snap))
	if err != nil || reason != NoDiscard || loaded == nil {
		t.Fatalf("load = %v, %v, %v", loaded, reason, err)
	}
	if loaded.RepositoryID != snap.RepositoryID || len(loaded.Entities) != len(snap.Entities) {
		t.Fatalf("loaded snapshot differs: %+v", loaded)
	}
}

func TestSnapshotStoreLoadClassifications(t *testing.T) {
	dir := t.TempDir()
	st := newStore(dir, osFsIO{})
	snap := validSnapshot()
	exp := identityFor(snap)

	write := func(content string) {
		t.Helper()
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(st.Path(), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}

	// Missing file: not an error, not usable.
	if _, reason, err := st.Load(exp); reason != DiscardMissing || err != nil {
		t.Fatalf("missing = %v, %v (want DiscardMissing, nil)", reason, err)
	}

	// Malformed / truncated: corrupt.
	write(`{"schemaVersion":`)
	if _, reason, err := st.Load(exp); reason != DiscardCorrupt || err != nil {
		t.Fatalf("truncated = %v, %v (want corrupt)", reason, err)
	}
	write(`not json`)
	if _, reason, err := st.Load(exp); reason != DiscardCorrupt || err != nil {
		t.Fatalf("malformed = %v, %v (want corrupt)", reason, err)
	}

	// Unknown schema: corrupt.
	ser, _ := snap.Serialize()
	write(strings.Replace(string(ser), `"schemaVersion":1`, `"schemaVersion":9`, 1))
	if _, reason, err := st.Load(exp); reason != DiscardCorrupt || err != nil {
		t.Fatalf("unknown schema = %v, %v (want corrupt)", reason, err)
	}

	// Wrong Vault ID: corrupt (never usable).
	other := validSnapshot()
	other.VaultID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	oser, _ := other.Serialize()
	write(string(oser))
	if _, reason, err := st.Load(exp); reason != DiscardCorrupt || err != nil {
		t.Fatalf("wrong vault = %v, %v (want corrupt)", reason, err)
	}

	// Provider profile mismatch: reconnect, not corrupt.
	prof := validSnapshot()
	prof.ProviderProfile = strings.Repeat("d", 64)
	pser, _ := prof.Serialize()
	write(string(pser))
	if _, reason, err := st.Load(exp); reason != DiscardProfileMismatch || err != nil {
		t.Fatalf("profile mismatch = %v, %v (want profile-mismatch)", reason, err)
	}

	// Repository ID mismatch: always stops.
	repo := validSnapshot()
	repo.RepositoryID = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	rser, _ := repo.Serialize()
	write(string(rser))
	if _, reason, err := st.Load(exp); reason != DiscardRepositoryMismatch || err != nil {
		t.Fatalf("repository mismatch = %v, %v (want repository-mismatch)", reason, err)
	}

	// Real read failure: an error, never "missing".
	write(string(ser))
	f := newFaultFsIO(osFsIO{})
	f.armNext("read", errors.New("permission denied"))
	stFail := newStore(dir, f)
	if _, reason, err := stFail.Load(exp); reason != NoDiscard || err == nil {
		t.Fatalf("read failure = %v, %v (want error)", reason, err)
	}
}

func TestSnapshotStoreReplaceFaultInjection(t *testing.T) {
	snap1 := validSnapshot()
	snap2 := validSnapshot()
	snap2.Cursor = "second"

	// A failed create leaves no snapshot at all.
	f := newFaultFsIO(osFsIO{})
	f.armNext("create", errors.New("boom"))
	st := newStore(t.TempDir(), f)
	if err := st.Replace(snap1); err == nil {
		t.Fatal("create failure accepted")
	}
	if _, reason, _ := st.Load(identityFor(snap1)); reason != DiscardMissing {
		t.Fatalf("after failed create, load = %v (want missing)", reason)
	}

	// Failures before the rename must leave the prior snapshot loadable.
	for _, tc := range []struct {
		name string
		op   string
		fn   func(*faultFsIO)
	}{
		{"write", "write", func(f *faultFsIO) { f.armNextShortWrite("write") }},
		{"write error", "write", func(f *faultFsIO) { f.armNext("write", errors.New("boom")) }},
		{"sync", "sync", func(f *faultFsIO) { f.armNext("sync", errors.New("boom")) }},
		{"rename", "rename", func(f *faultFsIO) { f.armNext("rename", errors.New("boom")) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			f := newFaultFsIO(osFsIO{})
			st := newStore(dir, f)
			if err := st.Replace(snap1); err != nil {
				t.Fatalf("initial replace: %v", err)
			}
			tc.fn(f)
			if err := st.Replace(snap2); err == nil {
				t.Fatal("faulted replace succeeded")
			}
			loaded, reason, err := st.Load(identityFor(snap1))
			if err != nil || reason != NoDiscard {
				t.Fatalf("prior snapshot lost: %v, %v", reason, err)
			}
			if loaded.Cursor != snap1.Cursor {
				t.Fatalf("prior snapshot not the old one: %+v", loaded)
			}
			// No temp files linger.
			entries, _ := os.ReadDir(dir)
			for _, e := range entries {
				if strings.Contains(e.Name(), ".tmp") {
					t.Fatalf("temp file left behind: %s", e.Name())
				}
			}
		})
	}

	// A directory-sync failure happens AFTER the rename: the new snapshot is
	// installed, and Load still returns a usable snapshot (never an error).
	t.Run("sync-dir", func(t *testing.T) {
		dir := t.TempDir()
		f := newFaultFsIO(osFsIO{})
		st := newStore(dir, f)
		if err := st.Replace(snap1); err != nil {
			t.Fatal(err)
		}
		f.armNext("sync-dir", errors.New("boom"))
		if err := st.Replace(snap2); err == nil {
			t.Fatal("dir-sync failure accepted")
		}
		loaded, reason, err := st.Load(identityFor(snap1))
		if err != nil || reason != NoDiscard {
			t.Fatalf("load after dir-sync failure = %v, %v (want usable)", reason, err)
		}
		_ = loaded
	})
}

func TestSnapshotStoreReplacePerformsOneRewrite(t *testing.T) {
	dir := t.TempDir()
	c := &countingFsIO{fsIO: osFsIO{}}
	st := newStore(dir, c)
	snap := validSnapshot()
	if err := st.Replace(snap); err != nil {
		t.Fatal(err)
	}
	if c.writes != 1 || c.syncs != 1 {
		t.Fatalf("one replace performed %d writes / %d syncs", c.writes, c.syncs)
	}
	// A cycle-equivalent second replace rewrites the file exactly once more,
	// with no backup, append, or partial update.
	if err := st.Replace(snap); err != nil {
		t.Fatal(err)
	}
	if c.writes != 2 || c.syncs != 2 {
		t.Fatalf("two replaces performed %d writes / %d syncs", c.writes, c.syncs)
	}
}

func TestLegacySnapshotAndWALIgnored(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	// Legacy files from the old WAL design must never be read or treated as a
	// baseline.
	for _, name := range []string{"state.snapshot.json", "state.wal.ndjson", "state.wal.1.frozen.ndjson"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(`{"schemaVersion":1,"data":{"x":1},"lastAppliedSeq":5,"checksum":"deadbeef"}`), 0600); err != nil {
			t.Fatal(err)
		}
	}
	st := newStore(dir, osFsIO{})
	snap := validSnapshot()
	if _, reason, err := st.Load(identityFor(snap)); reason != DiscardMissing || err != nil {
		t.Fatalf("legacy files treated as a baseline: %v, %v", reason, err)
	}
	// After a real Replace, the legacy files are still ignored.
	if err := st.Replace(snap); err != nil {
		t.Fatal(err)
	}
	loaded, reason, err := st.Load(identityFor(snap))
	if err != nil || reason != NoDiscard || loaded == nil {
		t.Fatalf("load after replace = %v, %v, %v", loaded, reason, err)
	}
	for _, name := range []string{"state.snapshot.json", "state.wal.ndjson", "state.wal.1.frozen.ndjson"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("legacy file %s removed: %v", name, err)
		}
	}
}

type countingFsIO struct {
	fsIO
	writes int
	syncs  int
}

func (c *countingFsIO) WriteAll(f *os.File, b []byte) error {
	c.writes++
	return c.fsIO.WriteAll(f, b)
}

func (c *countingFsIO) Sync(f *os.File) error {
	c.syncs++
	return c.fsIO.Sync(f)
}

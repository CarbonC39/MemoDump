package syncstate

import (
	"errors"
	"os"
	"strings"
	"testing"
)

const (
	testVaultID   = "11111111-1111-4111-8111-111111111111"
	testReplicaID = "22222222-2222-4222-8222-222222222222"
	testRepoID    = "33333333-3333-4333-8333-333333333333"
)

func validSnapshotV2() *SnapshotV2 {
	return &SnapshotV2{
		SchemaVersion:   SnapshotV2SchemaVersion,
		VaultID:         testVaultID,
		ReplicaID:       testReplicaID,
		RepositoryID:    testRepoID,
		ProviderProfile: strings.Repeat("a", 64),
		Notes: map[string]SnapshotEntity{
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
	}
}

func identityForV2(s *SnapshotV2) ExpectedIdentity {
	return ExpectedIdentity{
		VaultID: s.VaultID, ReplicaID: s.ReplicaID,
		ProviderProfile: s.ProviderProfile, RepositoryID: s.RepositoryID,
	}
}

func newStoreV2(dir string, io fsIO) *SnapshotStoreV2 {
	return &SnapshotStoreV2{dir: dir, vaultID: testVaultID, replicaID: testReplicaID, io: io}
}

func TestSnapshotV2RoundTripDeterministic(t *testing.T) {
	snap := validSnapshotV2()
	ser, err := snap.Serialize()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(ser), "cursor") {
		t.Fatalf("v2 snapshot carries a cursor: %s", ser)
	}
	parsed, err := ParseSnapshotV2(ser)
	if err != nil {
		t.Fatalf("parse canonical snapshot: %v", err)
	}
	ser2, _ := parsed.Serialize()
	if string(ser) != string(ser2) {
		t.Fatalf("round trip not deterministic\n got %q\nwant %q", ser2, ser)
	}
	if len(parsed.Notes) != len(snap.Notes) {
		t.Fatalf("round trip lost notes: %+v", parsed)
	}
}

func TestSnapshotV2ValidateRejectsBadInvariants(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*SnapshotV2)
	}{
		{"schema", func(s *SnapshotV2) { s.SchemaVersion = 1 }},
		{"vault id", func(s *SnapshotV2) { s.VaultID = "not-a-uuid" }},
		{"replica id", func(s *SnapshotV2) { s.ReplicaID = "nope" }},
		{"repository id", func(s *SnapshotV2) { s.RepositoryID = "04b2cbe6-19cf-584f-bad4-55fa03d9c05a" }}, // v5 not allowed
		{"provider profile", func(s *SnapshotV2) { s.ProviderProfile = "ABC" }},
		{"nil notes", func(s *SnapshotV2) { s.Notes = nil }},
		{"bad sync id", func(s *SnapshotV2) { s.Notes["nope"] = s.Notes["44444444-4444-4444-8444-444444444444"] }},
		{"bad content hash", func(s *SnapshotV2) {
			s.Notes["44444444-4444-4444-8444-444444444444"] = SnapshotEntity{ContentHash: "abc", RemoteVersion: "v1"}
		}},
		{"empty remote version", func(s *SnapshotV2) {
			s.Notes["44444444-4444-4444-8444-444444444444"] = SnapshotEntity{ContentHash: strings.Repeat("b", 64), RemoteVersion: ""}
		}},
	}
	for _, tc := range cases {
		s := validSnapshotV2()
		tc.mutate(s)
		if err := s.Validate(); err == nil {
			t.Errorf("%s: accepted", tc.name)
		}
	}
}

func TestParseSnapshotV2RejectsStrictJSON(t *testing.T) {
	snap := validSnapshotV2()
	ser, _ := snap.Serialize()
	doc := strings.TrimSuffix(string(ser), "\n")
	profile := strings.Repeat("a", 64)
	head := `{"schemaVersion":2,"vaultId":"` + testVaultID + `","replicaId":"` + testReplicaID +
		`","repositoryId":"` + testRepoID + `","providerProfile":"` + profile + `"`
	bad := []struct {
		name string
		json string
	}{
		{"truncated", doc[:len(doc)/2]},
		{"not an object", `[]`},
		{"unknown field", head + `,"notes":{},"evil":true}`},
		// The prototype schema-v1 keys are not part of v2: entities and cursor
		// are unknown fields, never a baseline.
		{"prototype entities key", head + `,"entities":{}}`},
		{"prototype cursor key", head + `,"notes":{},"cursor":"x"}`},
		{"missing field", head + `}`},
		{"trailing content", doc + `{}`},
		{"wrong type", `{"schemaVersion":"two","vaultId":"` + testVaultID + `","replicaId":"` + testReplicaID +
			`","repositoryId":"` + testRepoID + `","providerProfile":"` + profile + `","notes":{}}`},
		{"unknown note field", head + `,"notes":{"44444444-4444-4444-8444-444444444444":{"contentHash":"` +
			strings.Repeat("b", 64) + `","remoteVersion":"v1","deleted":false,"evil":1}}}`},
	}
	dup := strings.Replace(doc, `"schemaVersion":2`, `"schemaVersion":2,"schemaVersion":2`, 1)
	bad = append(bad, struct{ name, json string }{"duplicate field", dup})
	for _, tc := range bad {
		if _, err := ParseSnapshotV2([]byte(tc.json)); err == nil {
			t.Errorf("%s: accepted", tc.name)
		}
	}
}

// TestParseSnapshotV2ClassifiesPrototype is the migration-classification test:
// a schema-v1 prototype snapshot is reported as ErrUnsupportedPrototype (not
// corrupt, not a usable baseline).
func TestParseSnapshotV2ClassifiesPrototype(t *testing.T) {
	v1 := `{"schemaVersion":1,"vaultId":"` + testVaultID + `","replicaId":"` + testReplicaID +
		`","repositoryId":"` + testRepoID + `","providerProfile":"` + strings.Repeat("a", 64) +
		`","entities":{"44444444-4444-4444-8444-444444444444":{"contentHash":"` + strings.Repeat("b", 64) +
		`","remoteVersion":"v1","deleted":false}},"cursor":"opaque"}`
	if _, err := ParseSnapshotV2([]byte(v1)); !errors.Is(err, ErrUnsupportedPrototype) {
		t.Fatalf("v1 snapshot error = %v, want ErrUnsupportedPrototype", err)
	}
	// A future schema is ordinary unsupported-schema corruption, not prototype.
	if _, err := ParseSnapshotV2([]byte(`{"schemaVersion":9}`)); err == nil || errors.Is(err, ErrUnsupportedPrototype) {
		t.Fatalf("future schema error = %v, want a corrupt error", err)
	}
}

// TestParseSnapshotV2RejectsNullScalars: a note baseline with null for a scalar
// field must be rejected — null deleted would otherwise silently become false.
func TestParseSnapshotV2RejectsNullScalars(t *testing.T) {
	head := `{"schemaVersion":2,"vaultId":"` + testVaultID + `","replicaId":"` + testReplicaID +
		`","repositoryId":"` + testRepoID + `","providerProfile":"` + strings.Repeat("a", 64) + `"`
	base := head + `,"notes":{"44444444-4444-4444-8444-444444444444":{"contentHash":"` +
		strings.Repeat("b", 64) + `","remoteVersion":"v1","deleted":false}}}`
	cases := []struct {
		name string
		json string
	}{
		{"null deleted", strings.Replace(base, `"deleted":false`, `"deleted":null`, 1)},
		{"null contentHash", strings.Replace(base, `"contentHash":"`+strings.Repeat("b", 64)+`"`, `"contentHash":null`, 1)},
		{"null remoteVersion", strings.Replace(base, `"remoteVersion":"v1"`, `"remoteVersion":null`, 1)},
	}
	for _, tc := range cases {
		if _, err := ParseSnapshotV2([]byte(tc.json)); err == nil {
			t.Errorf("%s: accepted", tc.name)
		}
	}
}

func TestSnapshotStoreV2NewRejectsInvalidIdentity(t *testing.T) {
	root := t.TempDir()
	for _, tc := range []struct {
		name      string
		vaultID   string
		replicaID string
	}{
		{"bad vault", "not-a-uuid", testReplicaID},
		{"bad replica", testVaultID, "../escape"},
		{"v5 vault", "04b2cbe6-19cf-584f-bad4-55fa03d9c05a", testReplicaID},
		{"empty replica", testVaultID, ""},
	} {
		if _, err := NewSnapshotStoreV2(root, tc.vaultID, tc.replicaID); err == nil {
			t.Errorf("%s: accepted", tc.name)
		}
	}
	if _, err := NewSnapshotStoreV2(root, testVaultID, testReplicaID); err != nil {
		t.Fatalf("valid identity rejected: %v", err)
	}
}

func TestSnapshotStoreV2ReplaceRejectsCrossIdentity(t *testing.T) {
	root := t.TempDir()
	st := newStoreV2(root, osFsIO{})
	other := validSnapshotV2()
	other.ReplicaID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	if err := st.Replace(other); err == nil {
		t.Fatal("cross-identity snapshot accepted")
	}
	if _, err := os.Stat(st.Path()); !os.IsNotExist(err) {
		t.Fatal("cross-identity snapshot was written to disk")
	}
	if err := st.Replace(validSnapshotV2()); err != nil {
		t.Fatalf("valid snapshot rejected: %v", err)
	}
}

func TestSnapshotStoreV2ReplaceLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	st := newStoreV2(dir, osFsIO{})
	snap := validSnapshotV2()
	if err := st.Replace(snap); err != nil {
		t.Fatal(err)
	}
	loaded, reason, err := st.Load(identityForV2(snap))
	if err != nil || reason != NoDiscard || loaded == nil {
		t.Fatalf("load = %v, %v, %v", loaded, reason, err)
	}
	if loaded.RepositoryID != snap.RepositoryID || len(loaded.Notes) != len(snap.Notes) {
		t.Fatalf("loaded snapshot differs: %+v", loaded)
	}
}

func TestSnapshotStoreV2LoadClassifications(t *testing.T) {
	dir := t.TempDir()
	st := newStoreV2(dir, osFsIO{})
	snap := validSnapshotV2()
	exp := identityForV2(snap)

	write := func(content string) {
		t.Helper()
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(st.Path(), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}

	if _, reason, err := st.Load(exp); reason != DiscardMissing || err != nil {
		t.Fatalf("missing = %v, %v (want DiscardMissing, nil)", reason, err)
	}
	write(`{"schemaVersion":`)
	if _, reason, err := st.Load(exp); reason != DiscardCorrupt || err != nil {
		t.Fatalf("truncated = %v, %v (want corrupt)", reason, err)
	}
	write(`not json`)
	if _, reason, err := st.Load(exp); reason != DiscardCorrupt || err != nil {
		t.Fatalf("malformed = %v, %v (want corrupt)", reason, err)
	}

	// Schema-v1 prototype: unsupported, never corrupt and never a baseline.
	write(`{"schemaVersion":1,"entities":{},"cursor":"x"}`)
	if _, reason, err := st.Load(exp); reason != DiscardUnsupportedPrototype || err != nil {
		t.Fatalf("prototype = %v, %v (want unsupported-prototype)", reason, err)
	}

	// Unknown future schema: corrupt.
	ser, _ := snap.Serialize()
	write(strings.Replace(string(ser), `"schemaVersion":2`, `"schemaVersion":9`, 1))
	if _, reason, err := st.Load(exp); reason != DiscardCorrupt || err != nil {
		t.Fatalf("unknown schema = %v, %v (want corrupt)", reason, err)
	}

	// Wrong Vault ID: corrupt (never usable).
	other := validSnapshotV2()
	other.VaultID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	oser, _ := other.Serialize()
	write(string(oser))
	if _, reason, err := st.Load(exp); reason != DiscardCorrupt || err != nil {
		t.Fatalf("wrong vault = %v, %v (want corrupt)", reason, err)
	}

	// Provider profile mismatch: reconnect, not corrupt.
	prof := validSnapshotV2()
	prof.ProviderProfile = strings.Repeat("d", 64)
	pser, _ := prof.Serialize()
	write(string(pser))
	if _, reason, err := st.Load(exp); reason != DiscardProfileMismatch || err != nil {
		t.Fatalf("profile mismatch = %v, %v (want profile-mismatch)", reason, err)
	}

	// Repository ID mismatch: always stops.
	repo := validSnapshotV2()
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
	stFail := newStoreV2(dir, f)
	if _, reason, err := stFail.Load(exp); reason != NoDiscard || err == nil {
		t.Fatalf("read failure = %v, %v (want error)", reason, err)
	}
}

// TestSnapshotStoreV2ReplaceFaultInjection is the durability exit gate: failures
// before the rename leave the prior snapshot loadable; failures after the rename
// still expose a usable snapshot; no temp files linger.
func TestSnapshotStoreV2ReplaceFaultInjection(t *testing.T) {
	snap1 := validSnapshotV2()
	snap2 := validSnapshotV2()
	snap2.Notes["04b2cbe6-19cf-584f-bad4-55fa03d9c05a"] = SnapshotEntity{
		ContentHash: strings.Repeat("c", 64), RemoteVersion: "v2b", Deleted: true,
	}

	f := newFaultFsIO(osFsIO{})
	f.armNext("create", errors.New("boom"))
	st := newStoreV2(t.TempDir(), f)
	if err := st.Replace(snap1); err == nil {
		t.Fatal("create failure accepted")
	}
	if _, reason, _ := st.Load(identityForV2(snap1)); reason != DiscardMissing {
		t.Fatalf("after failed create, load = %v (want missing)", reason)
	}

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
			st := newStoreV2(dir, f)
			if err := st.Replace(snap1); err != nil {
				t.Fatalf("initial replace: %v", err)
			}
			tc.fn(f)
			if err := st.Replace(snap2); err == nil {
				t.Fatal("faulted replace succeeded")
			}
			loaded, reason, err := st.Load(identityForV2(snap1))
			if err != nil || reason != NoDiscard {
				t.Fatalf("prior snapshot lost: %v, %v", reason, err)
			}
			if v := loaded.Notes["04b2cbe6-19cf-584f-bad4-55fa03d9c05a"].RemoteVersion; v != "v2" {
				t.Fatalf("prior snapshot not the old one: %q", v)
			}
			entries, _ := os.ReadDir(dir)
			for _, e := range entries {
				if strings.Contains(e.Name(), ".tmp") {
					t.Fatalf("temp file left behind: %s", e.Name())
				}
			}
		})
	}

	t.Run("sync-dir", func(t *testing.T) {
		dir := t.TempDir()
		f := newFaultFsIO(osFsIO{})
		st := newStoreV2(dir, f)
		if err := st.Replace(snap1); err != nil {
			t.Fatal(err)
		}
		f.armNext("sync-dir", errors.New("boom"))
		if err := st.Replace(snap2); err == nil {
			t.Fatal("dir-sync failure accepted")
		}
		loaded, reason, err := st.Load(identityForV2(snap1))
		if err != nil || reason != NoDiscard {
			t.Fatalf("load after dir-sync failure = %v, %v (want usable)", reason, err)
		}
		_ = loaded
	})
}

func TestSnapshotStoreV2ReplacePerformsOneRewrite(t *testing.T) {
	dir := t.TempDir()
	c := &countingFsIO{fsIO: osFsIO{}}
	st := newStoreV2(dir, c)
	snap := validSnapshotV2()
	if err := st.Replace(snap); err != nil {
		t.Fatal(err)
	}
	if c.writes != 1 || c.syncs != 1 {
		t.Fatalf("one replace performed %d writes / %d syncs", c.writes, c.syncs)
	}
	if err := st.Replace(snap); err != nil {
		t.Fatal(err)
	}
	if c.writes != 2 || c.syncs != 2 {
		t.Fatalf("two replaces performed %d writes / %d syncs", c.writes, c.syncs)
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

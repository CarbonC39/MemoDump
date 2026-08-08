package cloudsync

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"
)

func loadFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("../../testdata/sync/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// TestCanonicalEntityMatchesFixture is the shared-contract test: both Go and
// TypeScript must produce the exact canonical bytes and content hashes the
// fixture pins.
func TestCanonicalEntityMatchesFixture(t *testing.T) {
	var fixture struct {
		Entities []struct {
			Entity        Entity `json:"entity"`
			ContentHash   string `json:"contentHash"`
			CanonicalJSON string `json:"canonicalJson"`
		} `json:"entities"`
	}
	if err := json.Unmarshal(loadFixture(t, "entities.json"), &fixture); err != nil {
		t.Fatal(err)
	}
	for _, tc := range fixture.Entities {
		e := tc.Entity
		if got := e.ComputeContentHash(); got != tc.ContentHash {
			t.Errorf("%s: hash = %s, want %s", e.Name, got, tc.ContentHash)
		}
		ser, err := e.Serialize()
		if err != nil {
			t.Fatal(err)
		}
		if string(ser) != tc.CanonicalJSON {
			t.Errorf("%s: serialization mismatch\n got %q\nwant %q", e.Name, ser, tc.CanonicalJSON)
		}
		// Round-trip: parsing the canonical bytes must reproduce them exactly.
		parsed, err := ParseEntity([]byte(tc.CanonicalJSON))
		if err != nil {
			t.Fatalf("%s: parse error: %v", e.Name, err)
		}
		ser2, _ := parsed.Serialize()
		if string(ser2) != tc.CanonicalJSON {
			t.Errorf("%s: round-trip mismatch", e.Name)
		}
	}
}

func TestRepositoryDescriptorMatchesFixture(t *testing.T) {
	var fixture struct {
		Valid []struct {
			Descriptor    RepositoryDescriptor `json:"descriptor"`
			CanonicalJSON string               `json:"canonicalJson"`
		} `json:"valid"`
		Invalid []struct {
			JSON string `json:"json"`
		} `json:"invalid"`
	}
	if err := json.Unmarshal(loadFixture(t, "repo-descriptors.json"), &fixture); err != nil {
		t.Fatal(err)
	}
	for _, tc := range fixture.Valid {
		ser, err := tc.Descriptor.Serialize()
		if err != nil {
			t.Fatal(err)
		}
		if string(ser) != tc.CanonicalJSON {
			t.Errorf("descriptor serialization mismatch\n got %q\nwant %q", ser, tc.CanonicalJSON)
		}
		if _, err := ParseRepositoryDescriptor([]byte(tc.CanonicalJSON)); err != nil {
			t.Errorf("valid descriptor rejected: %v", err)
		}
	}
	for _, tc := range fixture.Invalid {
		if _, err := ParseRepositoryDescriptor([]byte(tc.JSON)); err == nil {
			t.Errorf("invalid descriptor accepted: %s", tc.JSON)
		}
	}
}

func TestMalformedEntitiesRejected(t *testing.T) {
	var fixture struct {
		EntityCases []struct {
			Name   string         `json:"name"`
			Entity map[string]any `json:"entity"`
		} `json:"entityCases"`
		RawCases []struct {
			Name   string `json:"name"`
			Base64 string `json:"base64,omitempty"`
			JSON   string `json:"json,omitempty"`
		} `json:"rawCases"`
	}
	if err := json.Unmarshal(loadFixture(t, "malformed-input.json"), &fixture); err != nil {
		t.Fatal(err)
	}
	for _, tc := range fixture.EntityCases {
		raw, err := json.Marshal(tc.Entity)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ParseEntity(raw); err == nil {
			t.Errorf("%s: malformed entity accepted", tc.Name)
		}
	}
	for _, tc := range fixture.RawCases {
		var raw []byte
		if tc.Base64 != "" {
			raw, _ = base64.StdEncoding.DecodeString(tc.Base64)
		} else {
			raw = []byte(tc.JSON)
		}
		if _, err := ParseEntity(raw); err == nil {
			t.Errorf("%s: malformed raw input accepted", tc.Name)
		}
	}
}

func TestOversizedEntityRejected(t *testing.T) {
	e := Entity{
		SchemaVersion: 1, SyncID: "5d5d8b2c-94f7-4a38-8318-8cd4cb53dfa8",
		Kind: KindNote, Name: "big", Markdown: strings.Repeat("x", MaxEntityBytes),
		UpdatedBy: "1a2b3c4d-1111-4222-8333-444455556666", UpdatedAt: 1,
	}
	ser, _ := e.Serialize()
	if _, err := ParseEntity(ser); err != ErrOversized {
		t.Fatalf("err = %v, want ErrOversized", err)
	}
}

func TestParentCyclesAndMissingParentsRejected(t *testing.T) {
	a := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	b := "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	n := "nnnnnnnn-nnnn-4nnn-8nnn-nnnnnnnnnnnn"
	by := "1a2b3c4d-1111-4222-8333-444455556666"
	folder := func(id, parent string) *Entity {
		e := &Entity{
			SchemaVersion: 1, SyncID: id, Kind: KindFolder, ParentID: parent, Name: id[:4],
			UpdatedBy: by, UpdatedAt: 1,
		}
		e.ContentHash = e.ComputeContentHash()
		return e
	}
	// Missing parent: b is referenced but not in the map (keyed by syncId).
	missing := map[string]*Entity{a: folder(a, b)}
	if err := ValidateEntities(missing); err == nil {
		t.Fatal("missing parent accepted")
	}
	// Cycle a -> b -> a. The map is keyed by syncId, so the parent lookups
	// resolve and the DFS reaches the cycle branch.
	cycle := map[string]*Entity{
		a: folder(a, b),
		b: folder(b, a),
	}
	if err := ValidateEntities(cycle); err == nil {
		t.Fatal("parent cycle accepted")
	}
	// A note parented to a note is rejected.
	note := func(id, parent string) *Entity {
		e := &Entity{SchemaVersion: 1, SyncID: id, Kind: KindNote, ParentID: parent, Name: id[:1],
			UpdatedBy: by, UpdatedAt: 1}
		e.ContentHash = e.ComputeContentHash()
		return e
	}
	bad := map[string]*Entity{
		a: note(a, ""),
		n: note(n, a),
	}
	if err := ValidateEntities(bad); err == nil {
		t.Fatal("note parented to a note accepted")
	}
}

func TestNormalizeMarkdownFixture(t *testing.T) {
	var fixture struct {
		Cases []struct {
			Name       string `json:"name"`
			Input      string `json:"input"`
			Normalized string `json:"normalized"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(loadFixture(t, "canonical-markdown.json"), &fixture); err != nil {
		t.Fatal(err)
	}
	for _, tc := range fixture.Cases {
		if got := NormalizeMarkdown(tc.Input); got != tc.Normalized {
			t.Errorf("%s: got %q, want %q", tc.Name, got, tc.Normalized)
		}
	}
}

func TestPortablePathKeysFixture(t *testing.T) {
	var fixture struct {
		Cases []struct {
			Path string `json:"path"`
			Key  string `json:"key"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(loadFixture(t, "portable-path-keys.json"), &fixture); err != nil {
		t.Fatal(err)
	}
	for _, tc := range fixture.Cases {
		if got := PortablePathKey(tc.Path); got != tc.Key {
			t.Errorf("PortablePathKey(%q) = %q, want %q", tc.Path, got, tc.Key)
		}
	}
}

func TestConflictNamesFixture(t *testing.T) {
	var fixture struct {
		Cases []struct {
			Name           string `json:"name"`
			Stem           string `json:"stem"`
			ConflictSyncID string `json:"conflictSyncId"`
			Expected       string `json:"expected"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(loadFixture(t, "conflict-names.json"), &fixture); err != nil {
		t.Fatal(err)
	}
	for _, tc := range fixture.Cases {
		if got := ConflictFilename(tc.Stem, tc.ConflictSyncID); got != tc.Expected {
			t.Errorf("ConflictFilename(%q, %q) = %q, want %q", tc.Stem, tc.ConflictSyncID, got, tc.Expected)
		}
		// The derived name is deterministic: repeating the call is identical.
		if again := ConflictFilename(tc.Stem, tc.ConflictSyncID); again != tc.Expected {
			t.Errorf("conflict name not deterministic: %q", again)
		}
	}
}

func TestStateHashesFixture(t *testing.T) {
	var fixture struct {
		Namespace   string `json:"namespace"`
		StateHashes []struct {
			Name        string `json:"name"`
			ContentHash string `json:"contentHash"`
			Deleted     bool   `json:"deleted"`
			Expected    string `json:"expected"`
		} `json:"stateHashes"`
	}
	if err := json.Unmarshal(loadFixture(t, "state-hashes.json"), &fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.Namespace != ConflictNamespace {
		t.Fatalf("namespace = %q, want %q", fixture.Namespace, ConflictNamespace)
	}
	for _, tc := range fixture.StateHashes {
		if got := StateHash(tc.ContentHash, tc.Deleted); got != tc.Expected {
			t.Errorf("%s: StateHash = %q, want %q", tc.Name, got, tc.Expected)
		}
	}
}

func TestConflictIDsFixture(t *testing.T) {
	var fixture struct {
		Namespace   string `json:"namespace"`
		ConflictIDs []struct {
			Name         string `json:"name"`
			SourceSyncID string `json:"sourceSyncId"`
			LocalState   string `json:"localStateHash"`
			RemoteState  string `json:"remoteStateHash"`
			Expected     string `json:"expected"`
		} `json:"conflictIds"`
	}
	if err := json.Unmarshal(loadFixture(t, "state-hashes.json"), &fixture); err != nil {
		t.Fatal(err)
	}
	for _, tc := range fixture.ConflictIDs {
		got, err := DeriveConflictSyncID(tc.SourceSyncID, tc.LocalState, tc.RemoteState)
		if err != nil {
			t.Fatal(err)
		}
		if got != tc.Expected {
			t.Errorf("%s: conflictId = %q, want %q", tc.Name, got, tc.Expected)
		}
		// Repeating a derivation produces the same result.
		if again, _ := DeriveConflictSyncID(tc.SourceSyncID, tc.LocalState, tc.RemoteState); again != got {
			t.Errorf("%s: conflict derivation not deterministic", tc.Name)
		}
	}
}

func TestDeriveConflictSyncIDValidatesInputs(t *testing.T) {
	goodHash := StateHash("a", false)
	if goodHash == "" || !contentHashRe.MatchString(goodHash) {
		t.Fatalf("test setup: StateHash produced %q", goodHash)
	}
	validID := "5d5d8b2c-94f7-4a38-8318-8cd4cb53dfa8"
	badSource := []string{"", "not-a-uuid", "c8f28d1c-85c6-11e6-9d9d-0242ac130002", "5d5d8b2c94f74a3883188cd4cb53dfa8"}
	for _, s := range badSource {
		if _, err := DeriveConflictSyncID(s, goodHash, goodHash); err == nil {
			t.Errorf("DeriveConflictSyncID(%q, ...) accepted", s)
		}
	}
	badHash := []string{"", "ABC", strings.Repeat("0", 63), strings.ToUpper(goodHash)}
	for _, h := range badHash {
		if _, err := DeriveConflictSyncID(validID, h, goodHash); err == nil {
			t.Errorf("DeriveConflictSyncID(_, %q, _) accepted", h)
		}
		if _, err := DeriveConflictSyncID(validID, goodHash, h); err == nil {
			t.Errorf("DeriveConflictSyncID(_, _, %q) accepted", h)
		}
	}
}

func TestSyncIDValidationFixture(t *testing.T) {
	var fixture struct {
		Namespace string `json:"namespace"`
		SyncIDs   struct {
			ValidV4                 []string `json:"validV4"`
			ValidV5                 []string `json:"validV5"`
			InvalidV5AsRepoOrDevice []string `json:"invalidV5AsRepositoryOrDevice"`
			Invalid                 []string `json:"invalid"`
		} `json:"syncIds"`
	}
	if err := json.Unmarshal(loadFixture(t, "state-hashes.json"), &fixture); err != nil {
		t.Fatal(err)
	}
	for _, s := range fixture.SyncIDs.ValidV4 {
		if !IsSyncID(s) || !IsUUIDv4(s) {
			t.Errorf("validV4 %q rejected by IsSyncID/IsUUIDv4", s)
		}
	}
	for _, s := range fixture.SyncIDs.ValidV5 {
		if !IsSyncID(s) {
			t.Errorf("validV5 %q rejected by IsSyncID", s)
		}
		if IsUUIDv4(s) {
			t.Errorf("validV5 %q accepted as UUID v4", s)
		}
	}
	// v5 Sync IDs must never pass the v4-only validators used for Vault,
	// Replica, Device, and Repository IDs.
	for _, s := range fixture.SyncIDs.InvalidV5AsRepoOrDevice {
		if IsUUIDv4(s) {
			t.Errorf("v5 %q accepted where only v4 is allowed", s)
		}
	}
	for _, s := range fixture.SyncIDs.Invalid {
		if IsSyncID(s) {
			t.Errorf("invalid %q accepted as a Sync ID", s)
		}
	}
}

func TestV5EntityAcceptedAndV5UpdatedByRejected(t *testing.T) {
	by := "1a2b3c4d-1111-4222-8333-444455556666"
	mk := func(syncID, parentID string) *Entity {
		e := &Entity{
			SchemaVersion: 1, SyncID: syncID, Kind: KindNote, ParentID: parentID, Name: "conflict",
			Markdown: "# Local version\n", UpdatedBy: by, UpdatedAt: 1,
		}
		e.ContentHash = e.ComputeContentHash()
		return e
	}
	// A conflict record may use a v5 Sync ID and a v5 folder parent.
	conflictID, err := DeriveConflictSyncID("5d5d8b2c-94f7-4a38-8318-8cd4cb53dfa8",
		StateHash("a", false), StateHash("b", false))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseEntity(mustSerialize(t, mk(conflictID, ConflictNamespace))); err != nil {
		t.Errorf("v5 entity rejected: %v", err)
	}
	// updatedBy must stay version-4 only, even on a conflict record.
	bad := mk(conflictID, "")
	bad.UpdatedBy = conflictID
	if err := bad.Validate(); err == nil {
		t.Errorf("v5 updatedBy accepted")
	}
	// Repository IDs stay version-4 only.
	desc := RepositoryDescriptor{FormatVersion: 1, RepositoryID: conflictID, CreatedAt: 1, MinimumClientVersion: "2.0.0"}
	data, err := desc.Serialize()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseRepositoryDescriptor(data); err == nil {
		t.Errorf("v5 repositoryId accepted")
	}
}

func mustSerialize(t *testing.T, e *Entity) []byte {
	t.Helper()
	data, err := e.Serialize()
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestRetryClassesFixture(t *testing.T) {
	var fixture struct {
		Cases []struct {
			Name              string `json:"name"`
			Kind              string `json:"kind"`
			RetryAfterSeconds int    `json:"retryAfterSeconds"`
			Retryable         bool   `json:"retryable"`
			BackoffSeconds    int    `json:"backoffSeconds"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(loadFixture(t, "retry-classes.json"), &fixture); err != nil {
		t.Fatal(err)
	}
	for _, tc := range fixture.Cases {
		kind := storeErrorKindFromString(tc.Kind)
		se := &StoreError{Kind: kind, Message: "test"}
		if tc.RetryAfterSeconds > 0 {
			se.RetryAfter = time.Duration(tc.RetryAfterSeconds) * time.Second
		}
		got := ClassifyRetry(se)
		if got.Retryable != tc.Retryable {
			t.Errorf("%s: retryable = %v, want %v", tc.Name, got.Retryable, tc.Retryable)
		}
		if want := time.Duration(tc.BackoffSeconds) * time.Second; got.Backoff != want {
			t.Errorf("%s: backoff = %v, want %v", tc.Name, got.Backoff, want)
		}
	}
}

func storeErrorKindFromString(s string) StoreErrorKind {
	switch s {
	case "not-found":
		return ErrNotFound
	case "precondition-failed":
		return ErrPreconditionFailed
	case "auth":
		return ErrAuth
	case "permission":
		return ErrPermission
	case "rate-limit":
		return ErrRateLimit
	case "quota":
		return ErrQuota
	case "invalid-response":
		return ErrInvalidResponse
	case "unsupported-capability":
		return ErrUnsupportedCapability
	case "retryable-transport":
		return ErrRetryableTransport
	default:
		return StoreErrorKind(-1)
	}
}

func TestPortablePathKeyIsIdempotent(t *testing.T) {
	for r := rune(0); r <= unicode.MaxRune; r++ {
		if !utf8.ValidRune(r) {
			continue
		}
		ch := string(r)
		once := PortablePathKey(ch)
		if twice := PortablePathKey(once); twice != once {
			t.Errorf("portable path key not idempotent: %U -> %q -> %q", r, once, twice)
		}
	}
}

func TestCherokeeCaseVariantsCollide(t *testing.T) {
	a := PortablePathKey("\U000013A0.md") // Ꭰ (uppercase)
	b := PortablePathKey("ꭰ.md")          // ꭰ (small letter)
	if a != b {
		t.Fatalf("Cherokee case variants produced different keys: %q vs %q", a, b)
	}
}

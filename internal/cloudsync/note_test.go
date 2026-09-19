package cloudsync

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

// TestNoteRecordMatchesFixture is the schema-v2 note contract test: the Go
// implementation must produce exactly the canonical bytes and content hashes
// the committed fixture pins, and parsing the canonical bytes must reproduce
// them exactly.
func TestNoteRecordMatchesFixture(t *testing.T) {
	var fixture struct {
		Valid []struct {
			Name          string     `json:"name"`
			Record        NoteRecord `json:"record"`
			ContentHash   string     `json:"contentHash"`
			CanonicalJSON string     `json:"canonicalJson"`
		} `json:"valid"`
	}
	if err := json.Unmarshal(loadFixture(t, "note-records.json"), &fixture); err != nil {
		t.Fatal(err)
	}
	if len(fixture.Valid) == 0 {
		t.Fatal("note-records.json has no valid cases")
	}
	for _, tc := range fixture.Valid {
		n := tc.Record
		if got := n.ComputeContentHash(); got != tc.ContentHash {
			t.Errorf("%s: hash = %s, want %s", tc.Name, got, tc.ContentHash)
		}
		ser, err := n.Serialize()
		if err != nil {
			t.Fatal(err)
		}
		if string(ser) != tc.CanonicalJSON {
			t.Errorf("%s: serialization mismatch\n got %q\nwant %q", tc.Name, ser, tc.CanonicalJSON)
		}
		parsed, err := ParseNoteRecord([]byte(tc.CanonicalJSON))
		if err != nil {
			t.Fatalf("%s: parse error: %v", tc.Name, err)
		}
		if got := *parsed; got != n {
			t.Errorf("%s: round-trip mismatch\n got %+v\nwant %+v", tc.Name, got, n)
		}
		ser2, _ := parsed.Serialize()
		if string(ser2) != tc.CanonicalJSON {
			t.Errorf("%s: round-trip serialization mismatch", tc.Name)
		}
	}
}

// TestNoteRecordInvalidRejected asserts every malformed record in the fixture is
// rejected, covering newer schemas, bad UUIDs, traversal/absolute/backslash/
// empty-segment/reserved paths, wrong extensions, tombstone markdown, missing
// markdown, CRLF markdown, bad hashes, and invalid media keys.
func TestNoteRecordInvalidRejected(t *testing.T) {
	var fixture struct {
		Invalid []struct {
			Name   string         `json:"name"`
			Record map[string]any `json:"record"`
		} `json:"invalid"`
	}
	if err := json.Unmarshal(loadFixture(t, "note-records.json"), &fixture); err != nil {
		t.Fatal(err)
	}
	for _, tc := range fixture.Invalid {
		raw, err := json.Marshal(tc.Record)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ParseNoteRecord(raw); err == nil {
			t.Errorf("%s: malformed note record accepted", tc.Name)
		}
	}
}

// TestNoteRecordInvalidRawRejected covers malformed raw input: unknown fields,
// missing required fields, trailing JSON values, and invalid UTF-8.
func TestNoteRecordInvalidRawRejected(t *testing.T) {
	var fixture struct {
		InvalidRaw []struct {
			Name   string `json:"name"`
			Base64 string `json:"base64,omitempty"`
			JSON   string `json:"json,omitempty"`
		} `json:"invalidRaw"`
	}
	if err := json.Unmarshal(loadFixture(t, "note-records.json"), &fixture); err != nil {
		t.Fatal(err)
	}
	// A raw CRLF case is exercised here too, using the same byte-level path a
	// provider would deliver: invalid UTF-8 is encoded as base64.
	raw := append([]byte(nil), 0xff)
	rawCases := append(fixture.InvalidRaw, struct {
		Name   string `json:"name"`
		Base64 string `json:"base64,omitempty"`
		JSON   string `json:"json,omitempty"`
	}{Name: "invalid utf-8", Base64: base64.StdEncoding.EncodeToString(raw)})
	for _, tc := range rawCases {
		var data []byte
		if tc.Base64 != "" {
			data, _ = base64.StdEncoding.DecodeString(tc.Base64)
		} else {
			data = []byte(tc.JSON)
		}
		if _, err := ParseNoteRecord(data); err == nil {
			t.Errorf("%s: malformed raw note accepted", tc.Name)
		}
	}
}

// TestNoteRecordPortableCollisions pins the cycle-level collision fixture: each
// record in a group is individually valid, but its portable path key collides
// with the other records in the group and differs from the next group's key.
func TestNoteRecordPortableCollisions(t *testing.T) {
	var fixture struct {
		PortableCollisions []struct {
			Name            string       `json:"name"`
			PortablePathKey string       `json:"portablePathKey"`
			Records         []NoteRecord `json:"records"`
		} `json:"portableCollisions"`
	}
	if err := json.Unmarshal(loadFixture(t, "note-records.json"), &fixture); err != nil {
		t.Fatal(err)
	}
	if len(fixture.PortableCollisions) < 2 {
		t.Fatal("need at least two collision groups")
	}
	seenKeys := map[string]bool{}
	for _, group := range fixture.PortableCollisions {
		if len(group.Records) < 2 {
			t.Fatalf("%s: collision group needs at least two records", group.Name)
		}
		for i := range group.Records {
			r := group.Records[i]
			if err := r.Validate(); err != nil {
				t.Errorf("%s: record %d fails Validate: %v", group.Name, i, err)
			}
			if got := PortablePathKey(r.Path); got != group.PortablePathKey {
				t.Errorf("%s: record %d key = %q, want %q", group.Name, i, got, group.PortablePathKey)
			}
		}
		if seenKeys[group.PortablePathKey] {
			t.Errorf("%s: duplicate portable path key %q", group.Name, group.PortablePathKey)
		}
		seenKeys[group.PortablePathKey] = true
	}
}

// TestNoteDuplicateFieldsRejected is the strict-JSON contract the reviewer
// flagged: a remote record carrying the same field twice (e.g. two syncId or
// two deleted keys) is ambiguous and must be rejected, never resolved by
// silently keeping the last value.
func TestNoteDuplicateFieldsRejected(t *testing.T) {
	base := `{"schemaVersion":2,"syncId":"5d5d8b2c-94f7-4a38-8318-8cd4cb53dfa8","path":"idea.md","markdown":"x","deleted":false}`
	dup := []struct {
		name string
		json string
	}{
		{"duplicate syncId", strings.Replace(base, `"syncId":"5d5d8b2c-94f7-4a38-8318-8cd4cb53dfa8"`, `"syncId":"5d5d8b2c-94f7-4a38-8318-8cd4cb53dfa8","syncId":"6e6e9c3d-a5b8-4c49-9409-9de566677770"`, 1)},
		{"duplicate path", strings.Replace(base, `"path":"idea.md"`, `"path":"idea.md","path":"other.md"`, 1)},
		{"duplicate deleted", strings.Replace(base, `"deleted":false`, `"deleted":false,"deleted":true`, 1)},
		{"duplicate schemaVersion", strings.Replace(base, `"schemaVersion":2`, `"schemaVersion":2,"schemaVersion":2`, 1)},
	}
	for _, tc := range dup {
		if _, err := ParseNoteRecord([]byte(tc.json)); err == nil {
			t.Errorf("%s: accepted", tc.name)
		}
	}
}

// TestNoteNullScalarsRejected: a remote record carrying null for a scalar field
// is ambiguous and must be rejected — Go's JSON decoder would otherwise accept
// null and keep the zero value (null markdown becoming "", null deleted
// becoming false).
func TestNoteNullScalarsRejected(t *testing.T) {
	base := `{"schemaVersion":2,"syncId":"5d5d8b2c-94f7-4a38-8318-8cd4cb53dfa8","path":"idea.md","markdown":"x","deleted":false}`
	cases := []struct {
		name string
		json string
	}{
		{"null markdown", strings.Replace(base, `"markdown":"x"`, `"markdown":null`, 1)},
		{"null deleted", strings.Replace(base, `"deleted":false`, `"deleted":null`, 1)},
		{"null syncId", strings.Replace(base, `"syncId":"5d5d8b2c-94f7-4a38-8318-8cd4cb53dfa8"`, `"syncId":null`, 1)},
		{"null path", strings.Replace(base, `"path":"idea.md"`, `"path":null`, 1)},
		{"null schemaVersion", strings.Replace(base, `"schemaVersion":2`, `"schemaVersion":null`, 1)},
	}
	for _, tc := range cases {
		if _, err := ParseNoteRecord([]byte(tc.json)); err == nil {
			t.Errorf("%s: accepted", tc.name)
		}
	}
}

// TestNoteSerializeSizeBoundary pins the trailing-LF size rule: a record whose
// serialized bytes are exactly MaxEntityBytes is both locally serializable and
// remotely parseable; one byte larger is rejected on Serialize.
func TestNoteSerializeSizeBoundary(t *testing.T) {
	n := &NoteRecord{
		SchemaVersion: NoteSchemaVersion, SyncID: "5d5d8b2c-94f7-4a38-8318-8cd4cb53dfa8",
		Path: "big.md", Markdown: "",
	}
	empty, err := n.Serialize()
	if err != nil {
		t.Fatal(err)
	}
	// For an all-'x' markdown of length m the serialized length is len(empty)+m
	// (the empty markdown contributes only its quotes).
	m := MaxEntityBytes - len(empty)
	n.Markdown = strings.Repeat("x", m)
	ser, err := n.Serialize()
	if err != nil {
		t.Fatalf("boundary record rejected: %v", err)
	}
	if len(ser) != MaxEntityBytes {
		t.Fatalf("serialized length = %d, want %d", len(ser), MaxEntityBytes)
	}
	if _, err := ParseNoteRecord(ser); err != nil {
		t.Fatalf("locally-serialized boundary record rejected remotely: %v", err)
	}
	n.Markdown = strings.Repeat("x", m+1)
	if _, err := n.Serialize(); err != ErrOversized {
		t.Fatalf("Serialize = %v, want ErrOversized", err)
	}
}

// TestNoteSerializeRejectsInvalidRecords covers the reviewer's finding that a
// locally-constructed NoteRecord could serialize a record the remote parser
// rejects (bad path, CRLF markdown, invalid UTF-8). Serialize now performs the
// same validation a parse would.
func TestNoteSerializeRejectsInvalidRecords(t *testing.T) {
	base := func(mut func(*NoteRecord)) *NoteRecord {
		n := &NoteRecord{
			SchemaVersion: NoteSchemaVersion, SyncID: "5d5d8b2c-94f7-4a38-8318-8cd4cb53dfa8",
			Path: "idea.md", Markdown: "# x\n",
		}
		mut(n)
		return n
	}
	cases := []struct {
		name string
		rec  *NoteRecord
	}{
		{"traversal path", base(func(n *NoteRecord) { n.Path = "../evil.md" })},
		{"reserved path", base(func(n *NoteRecord) { n.Path = ".memodump/x.md" })},
		{"non-md path", base(func(n *NoteRecord) { n.Path = "note.txt" })},
		{"crlf markdown", base(func(n *NoteRecord) { n.Markdown = "a\r\nb\n" })},
		{"invalid utf-8 markdown", base(func(n *NoteRecord) { n.Markdown = string([]byte{0xff, 0xfe}) })},
		{"invalid utf-8 path", base(func(n *NoteRecord) { n.Path = string([]byte{0xff}) + ".md" })},
		{"tombstone with markdown", base(func(n *NoteRecord) { n.Deleted = true })},
		{"wrong schema", base(func(n *NoteRecord) { n.SchemaVersion = 1 })},
	}
	for _, tc := range cases {
		if _, err := tc.rec.Serialize(); err == nil {
			t.Errorf("%s: Serialize accepted an invalid record", tc.name)
		}
	}
}

// TestNoteTombstoneOmitsMarkdown verifies the tombstone wire rule: a tombstone
// serializes without the markdown key, while a live note always carries it even
// when the body is empty.
func TestNoteTombstoneOmitsMarkdown(t *testing.T) {
	tomb := &NoteRecord{
		SchemaVersion: NoteSchemaVersion, SyncID: "8a8a1e5f-c7da-4e6b-b62b-1f0788899992",
		Path: "gone.md", Deleted: true,
	}
	ser, err := tomb.Serialize()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(ser), "markdown") {
		t.Fatalf("tombstone serialization carries markdown: %s", ser)
	}
	// A live note with an empty body must still emit "markdown":"" so a parse
	// does not mistake it for a tombstone.
	blank := &NoteRecord{
		SchemaVersion: NoteSchemaVersion, SyncID: "acac3051-e9fc-408d-884d-3119aaaa4bb4",
		Path: "blank.md",
	}
	ser2, err := blank.Serialize()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(ser2), `"markdown":""`) {
		t.Fatalf("live empty note must carry empty markdown: %s", ser2)
	}
	if _, err := ParseNoteRecord(ser2); err != nil {
		t.Fatalf("live empty note rejected: %v", err)
	}
}

// TestNoteOversizedRejected caps remote note records at MaxEntityBytes, both on
// serialize (an oversized record must never be uploaded) and on parse.
func TestNoteOversizedRejected(t *testing.T) {
	n := &NoteRecord{
		SchemaVersion: NoteSchemaVersion, SyncID: "5d5d8b2c-94f7-4a38-8318-8cd4cb53dfa8",
		Path: "big.md", Markdown: strings.Repeat("x", MaxEntityBytes),
	}
	if _, err := n.Serialize(); err != ErrOversized {
		t.Fatalf("Serialize = %v, want ErrOversized", err)
	}
	// Raw input over the limit is rejected before any decoding.
	raw := append([]byte(`{"schemaVersion":2,"syncId":"5d5d8b2c-94f7-4a38-8318-8cd4cb53dfa8","path":"big.md","markdown":"`),
		[]byte(strings.Repeat("x", MaxEntityBytes))...)
	if _, err := ParseNoteRecord(raw); err != ErrOversized {
		t.Fatalf("ParseNoteRecord = %v, want ErrOversized", err)
	}
}

// TestValidNotePath unit-covers the path predicate independently of the
// fixtures, including the reserved-segment rule and the extension rule.
func TestValidNotePath(t *testing.T) {
	valid := []string{
		"idea.md",
		"Projects/idea.md",
		"Projects/Sub/deep.md",
		"你好/笔记.md",
		"a b.md",
		".hidden.md",
	}
	for _, p := range valid {
		if !ValidNotePath(p) {
			t.Errorf("ValidNotePath(%q) = false, want true", p)
		}
	}
	invalid := []string{
		"",
		"/abs.md",
		`\abs.md`,
		"a\\b.md",
		"../evil.md",
		"a/../b.md",
		"a//b.md",
		"./a.md",
		"a/./b.md",
		"note.txt",
		"note.md.txt",
		".memodump/x.md",
		".MEMODUMP/x.md",
		"x/.images/y.md",
	}
	for _, p := range invalid {
		if ValidNotePath(p) {
			t.Errorf("ValidNotePath(%q) = true, want false", p)
		}
	}
}

// TestV5ConflictNoteAccepted verifies a note record may carry the deterministic
// UUID v5 conflict identity, and that such an ID never satisfies the v4-only
// validators used for repository/device/vault identities.
func TestV5ConflictNoteAccepted(t *testing.T) {
	conflictID, err := DeriveConflictSyncID("5d5d8b2c-94f7-4a38-8318-8cd4cb53dfa8",
		StateHash("a", false), StateHash("b", false))
	if err != nil {
		t.Fatal(err)
	}
	n := &NoteRecord{
		SchemaVersion: NoteSchemaVersion, SyncID: conflictID,
		Path: "idea (conflict).md", Markdown: "# Local version\n",
	}
	if err := n.Validate(); err != nil {
		t.Fatalf("v5 conflict note rejected: %v", err)
	}
	ser, err := n.Serialize()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseNoteRecord(ser); err != nil {
		t.Fatalf("v5 conflict note rejected on parse: %v", err)
	}
	if IsUUIDv4(n.SyncID) {
		t.Fatal("v5 conflict ID accepted as UUID v4")
	}
}

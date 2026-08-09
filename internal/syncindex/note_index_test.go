package syncindex

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// validNoteIndex returns a v2 index with the vault and note IDs pinned so the
// canonical serialization is deterministic across runs.
func validNoteIndex() *NoteIndex {
	idx := NewNoteIndex("dc56ad15-62c6-4fa7-bf7a-5c6337d574be")
	idx.Notes["5d5d8b2c-94f7-4a38-8318-8cd4cb53dfa8"] = NoteEntry{Path: "Projects/idea.md"}
	idx.Notes["6e6e8b2c-94f7-4a38-8318-8cd4cb53dfa8"] = NoteEntry{Path: "a.md"}
	return idx
}

func TestNoteIndexSerializationIsCanonicalAndRoundTrips(t *testing.T) {
	idx := validNoteIndex()
	want := `{"notes":{"5d5d8b2c-94f7-4a38-8318-8cd4cb53dfa8":{"path":"Projects/idea.md"},` +
		`"6e6e8b2c-94f7-4a38-8318-8cd4cb53dfa8":{"path":"a.md"}},` +
		`"schemaVersion":2,"vaultId":"dc56ad15-62c6-4fa7-bf7a-5c6337d574be"}`
	got, err := idx.Serialize()
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("serialization mismatch\n got %s\nwant %s", got, want)
	}
	parsed, err := ParseNoteIndex(got)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	got2, _ := parsed.Serialize()
	if !bytes.Equal(got, got2) {
		t.Fatalf("round-trip mismatch\n got %s\nwant %s", got2, got)
	}
	// A content-only save is byte-identical.
	if err := parsed.validate(); err != nil {
		t.Fatal(err)
	}
}

func TestNoteIndexSerializationEscapesJSONSpecials(t *testing.T) {
	// A double quote is valid in a portable note path but must be JSON-escaped
	// so the canonical bytes stay one unambiguous document.
	idx := NewNoteIndex(uuid.NewString())
	idx.Notes[uuid.NewString()] = NoteEntry{Path: "quote\"日本語.md"}
	data, err := idx.Serialize()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `\"`) {
		t.Fatalf("quote not escaped: %s", data)
	}
	parsed, err := ParseNoteIndex(data)
	if err != nil {
		t.Fatalf("escaped path failed to parse: %v", err)
	}
	got, _ := parsed.Serialize()
	if !strings.Contains(string(got), "quote\\\"日本語.md") {
		t.Fatalf("round-trip lost the escaped path: %s", got)
	}
	// Control characters are not portable note paths (the wire contract rejects
	// them too) and must never enter the index.
	bad := NewNoteIndex(uuid.NewString())
	bad.Notes[uuid.NewString()] = NoteEntry{Path: "quote\"tab\t日本語.md"}
	if err := bad.validate(); err == nil {
		t.Fatal("control-character path accepted")
	}
}

func TestNoteIndexValidationRejectsCorruptDocuments(t *testing.T) {
	id := "5d5d8b2c-94f7-4a38-8318-8cd4cb53dfa8"
	ok := func(mut func(*NoteIndex)) *NoteIndex {
		idx := NewNoteIndex("dc56ad15-62c6-4fa7-bf7a-5c6337d574be")
		idx.Notes[id] = NoteEntry{Path: "a.md"}
		mut(idx)
		return idx
	}

	cases := []struct {
		name string
		idx  *NoteIndex
	}{
		{"wrong schema version", ok(func(i *NoteIndex) { i.SchemaVersion = 99 })},
		{"empty vault id", ok(func(i *NoteIndex) { i.VaultID = "" })},
		{"non-uuid vault id", ok(func(i *NoteIndex) { i.VaultID = "not-a-uuid" })},
		{"non-uuid sync id", ok(func(i *NoteIndex) { i.Notes["junk"] = NoteEntry{Path: "b.md"} })},
		{"absolute path", ok(func(i *NoteIndex) { i.Notes[id] = NoteEntry{Path: "/etc/a.md"} })},
		{"windows drive path", ok(func(i *NoteIndex) { i.Notes[id] = NoteEntry{Path: `C:\a.md`} })},
		{"traversal", ok(func(i *NoteIndex) { i.Notes[id] = NoteEntry{Path: "../a.md"} })},
		{"traversal mid path", ok(func(i *NoteIndex) { i.Notes[id] = NoteEntry{Path: "x/../../a.md"} })},
		{"backslash separator", ok(func(i *NoteIndex) { i.Notes[id] = NoteEntry{Path: "x\\a.md"} })},
		{"empty path", ok(func(i *NoteIndex) { i.Notes[id] = NoteEntry{Path: ""} })},
		{"dot segment", ok(func(i *NoteIndex) { i.Notes[id] = NoteEntry{Path: "./a.md"} })},
		{"reserved segment", ok(func(i *NoteIndex) { i.Notes[id] = NoteEntry{Path: ".memodump/x.md"} })},
		{"non-md extension", ok(func(i *NoteIndex) { i.Notes[id] = NoteEntry{Path: "note.txt"} })},
		{"duplicate path", ok(func(i *NoteIndex) { i.Notes[uuid.NewString()] = NoteEntry{Path: "a.md"} })},
	}
	for _, tc := range cases {
		if err := tc.idx.validate(); err == nil {
			t.Errorf("%s: accepted", tc.name)
		}
	}
}

func TestParseNoteIndexRejectsStrictJSON(t *testing.T) {
	ser, _ := validNoteIndex().Serialize()
	doc := strings.TrimSuffix(string(ser), "\n")
	bad := []struct {
		name string
		json string
	}{
		{"truncated", doc[:len(doc)/2]},
		{"not an object", `[]`},
		{"unknown field", strings.Replace(doc, `"notes":`, `"evil":1,"notes":`, 1)},
		{"missing vaultId", `{"schemaVersion":2,"notes":{}}`},
		{"missing notes", `{"schemaVersion":2,"vaultId":"dc56ad15-62c6-4fa7-bf7a-5c6337d574be"}`},
		{"trailing content", doc + `{}`},
		{"wrong type", `{"schemaVersion":"two","vaultId":"dc56ad15-62c6-4fa7-bf7a-5c6337d574be","notes":{}}`},
		{"unknown note entry field", `{"schemaVersion":2,"vaultId":"dc56ad15-62c6-4fa7-bf7a-5c6337d574be","notes":{"5d5d8b2c-94f7-4a38-8318-8cd4cb53dfa8":{"path":"a.md","evil":1}}}`},
	}
	dup := strings.Replace(doc, `"schemaVersion":2`, `"schemaVersion":2,"schemaVersion":2`, 1)
	bad = append(bad, struct{ name, json string }{"duplicate field", dup})
	for _, tc := range bad {
		if _, err := ParseNoteIndex([]byte(tc.json)); err == nil {
			t.Errorf("%s: accepted", tc.name)
		}
	}
}

// TestParseNoteIndexClassifiesPrototypeSchema is the migration-classification
// test: a schema-v1 prototype index (kind/folder entities) is classified as
// unsupported, never loaded as a baseline, and never mistaken for corruption or
// an empty vault.
func TestParseNoteIndexClassifiesPrototypeSchema(t *testing.T) {
	v1 := `{"schemaVersion":1,"vaultId":"dc56ad15-62c6-4fa7-bf7a-5c6337d574be",` +
		`"entities":{"5d5d8b2c-94f7-4a38-8318-8cd4cb53dfa8":{"kind":"note","path":"idea.md"}}}`
	_, err := ParseNoteIndex([]byte(v1))
	if !errors.Is(err, ErrUnsupportedSchema) {
		t.Fatalf("v1 index error = %v, want ErrUnsupportedSchema", err)
	}
	// A future schema is equally unsupported, never loadable.
	_, err = ParseNoteIndex([]byte(`{"schemaVersion":3,"vaultId":"dc56ad15-62c6-4fa7-bf7a-5c6337d574be","notes":{}}`))
	if !errors.Is(err, ErrUnsupportedSchema) {
		t.Fatalf("future schema error = %v, want ErrUnsupportedSchema", err)
	}
}

// TestParseNoteIndexRejectsNullEntryPath: a note entry with a null path is
// ambiguous and must be rejected explicitly rather than silently becoming "".
func TestParseNoteIndexRejectsNullEntryPath(t *testing.T) {
	doc := `{"schemaVersion":2,"vaultId":"dc56ad15-62c6-4fa7-bf7a-5c6337d574be",` +
		`"notes":{"5d5d8b2c-94f7-4a38-8318-8cd4cb53dfa8":{"path":null}}}`
	if _, err := ParseNoteIndex([]byte(doc)); err == nil {
		t.Fatal("null note path accepted")
	}
}

func TestNoteIndexAllowsDistinctCaseCollidingPaths(t *testing.T) {
	// The v2 index does not decide portable collisions; it accepts them as
	// distinct paths and stays non-destructive (collision handling is the
	// coordinator's decision).
	idx := NewNoteIndex("dc56ad15-62c6-4fa7-bf7a-5c6337d574be")
	idx.Notes[uuid.NewString()] = NoteEntry{Path: "Note.md"}
	idx.Notes[uuid.NewString()] = NoteEntry{Path: "note.md"}
	if err := idx.validate(); err != nil {
		t.Fatalf("distinct case-colliding paths must be accepted: %v", err)
	}
}

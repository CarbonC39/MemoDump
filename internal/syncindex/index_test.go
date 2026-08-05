package syncindex

import (
	"bytes"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func mustVaultID(t *testing.T) string {
	t.Helper()
	return uuid.NewString()
}

func TestIndexSerializationIsCanonicalAndRoundTrips(t *testing.T) {
	vaultID := "dc56ad15-62c6-4fa7-bf7a-5c6337d574be"
	idx := New(vaultID)
	idx.Entities["5d5d8b2c-94f7-4a38-8318-8cd4cb53dfa8"] = Entity{Kind: "note", Path: "Projects/idea.md"}
	idx.Entities["6e6e8b2c-94f7-4a38-8318-8cd4cb53dfa8"] = Entity{Kind: "folder", Path: "Projects"}
	idx.Entities["7f7f8b2c-94f7-4a38-8318-8cd4cb53dfa8"] = Entity{Kind: "note", Path: "a.md"}

	// Key order is sorted, field order is fixed, and the bytes are stable.
	want := `{"schemaVersion":1,"vaultId":"dc56ad15-62c6-4fa7-bf7a-5c6337d574be",` +
		`"entities":{"5d5d8b2c-94f7-4a38-8318-8cd4cb53dfa8":{"kind":"note","path":"Projects/idea.md"},` +
		`"6e6e8b2c-94f7-4a38-8318-8cd4cb53dfa8":{"kind":"folder","path":"Projects"},` +
		`"7f7f8b2c-94f7-4a38-8318-8cd4cb53dfa8":{"kind":"note","path":"a.md"}}}`
	got, err := idx.Serialize()
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("serialization mismatch\n got %s\nwant %s", got, want)
	}

	// Parsing the canonical bytes must reproduce them exactly.
	parsed, err := parseIndex(got)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	got2, _ := parsed.Serialize()
	if !bytes.Equal(got, got2) {
		t.Fatalf("round-trip mismatch\n got %s\nwant %s", got2, got)
	}
}

func TestIndexSerializationEscapesJSONSpecials(t *testing.T) {
	// A double quote and a tab are valid in a Linux/portable path but must be
	// JSON-escaped so the canonical bytes stay one unambiguous document.
	idx := New(mustVaultID(t))
	idx.Entities[uuid.NewString()] = Entity{Kind: "note", Path: "quote\"tab\t日本語.md"}
	data, err := idx.Serialize()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "\t") {
		t.Fatalf("tab serialized raw: %s", data)
	}
	if !strings.Contains(string(data), `\"`) {
		t.Fatalf("quote not escaped: %s", data)
	}
	parsed, err := parseIndex(data)
	if err != nil {
		t.Fatalf("escaped path failed to parse: %v", err)
	}
	got, _ := parsed.Serialize()
	if !strings.Contains(string(got), "quote\\\"tab\\t日本語.md") {
		t.Fatalf("round-trip lost the escaped path: %s", got)
	}
}

func TestIndexValidationRejectsCorruptDocuments(t *testing.T) {
	id := uuid.NewString()
	ok := func(mut func(*Index)) *Index {
		idx := New(mustVaultID(t))
		idx.Entities[id] = Entity{Kind: "note", Path: "a.md"}
		mut(idx)
		return idx
	}

	cases := []struct {
		name string
		idx  *Index
	}{
		{"wrong schema version", ok(func(i *Index) { i.SchemaVersion = 99 })},
		{"empty vault id", ok(func(i *Index) { i.VaultID = "" })},
		{"non-uuid vault id", ok(func(i *Index) { i.VaultID = "not-a-uuid" })},
		{"non-uuid sync id", ok(func(i *Index) { i.Entities["junk"] = Entity{Kind: "note", Path: "b.md"} })},
		{"bad kind", ok(func(i *Index) { i.Entities[id] = Entity{Kind: "image", Path: "a.md"} })},
		{"empty kind", ok(func(i *Index) { i.Entities[id] = Entity{Kind: "", Path: "a.md"} })},
		{"absolute path", ok(func(i *Index) { i.Entities[id] = Entity{Kind: "note", Path: "/etc/a.md"} })},
		{"windows drive path", ok(func(i *Index) { i.Entities[id] = Entity{Kind: "note", Path: `C:\a.md`} })},
		{"traversal", ok(func(i *Index) { i.Entities[id] = Entity{Kind: "note", Path: "../a.md"} })},
		{"traversal mid path", ok(func(i *Index) { i.Entities[id] = Entity{Kind: "note", Path: "x/../../a.md"} })},
		{"backslash separator", ok(func(i *Index) { i.Entities[id] = Entity{Kind: "note", Path: "x\\a.md"} })},
		{"empty path", ok(func(i *Index) { i.Entities[id] = Entity{Kind: "note", Path: ""} })},
		{"dot segment", ok(func(i *Index) { i.Entities[id] = Entity{Kind: "note", Path: "./a.md"} })},
		{"duplicate path", ok(func(i *Index) { i.Entities[uuid.NewString()] = Entity{Kind: "note", Path: "a.md"} })},
	}
	for _, tc := range cases {
		if err := tc.idx.validate(); err == nil {
			t.Errorf("%s: accepted", tc.name)
		}
	}
}

func TestIndexAllowsDistinctCaseCollidingPaths(t *testing.T) {
	// On a case-insensitive platform these map to one file; the index does not
	// decide that — portable collision handling is a later phase. The index must
	// accept them as distinct paths and stay non-destructive.
	idx := New(mustVaultID(t))
	idx.Entities[uuid.NewString()] = Entity{Kind: "note", Path: "Note.md"}
	idx.Entities[uuid.NewString()] = Entity{Kind: "note", Path: "note.md"}
	if err := idx.validate(); err != nil {
		t.Fatalf("distinct case-colliding paths must be accepted: %v", err)
	}
}

func TestValidPathTable(t *testing.T) {
	valid := []string{"a.md", "a/b/c.md", ".hidden.md", "sub/.hidden/n.md", "日本語.md"}
	for _, p := range valid {
		if !validPath(p) {
			t.Errorf("validPath(%q) = false, want true", p)
		}
	}
	invalid := []string{"", "/abs.md", `\abs.md`, "a\\b.md", "../x.md", "a/../b.md",
		"a//b.md", "a/./b.md", "x/.."}
	for _, p := range invalid {
		if validPath(p) {
			t.Errorf("validPath(%q) = true, want false", p)
		}
	}
}

func TestParseIndexRejectsTrailingAndUnknownFields(t *testing.T) {
	idx := New(mustVaultID(t))
	data, _ := idx.Serialize()

	if _, err := parseIndex(append(append([]byte{}, data...), []byte("{}")...)); err == nil {
		t.Fatal("trailing JSON accepted")
	}
	extra := strings.Replace(string(data), `"entities":{`, `"unknown":1,"entities":{`, 1)
	if _, err := parseIndex([]byte(extra)); err == nil {
		t.Fatal("unknown field accepted")
	}
	if _, err := parseIndex([]byte(`{bad json`)); err == nil {
		t.Fatal("malformed JSON accepted")
	}
}

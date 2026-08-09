package cloudsync

// This file generates testdata/sync/*.json from this package's canonical
// implementation. It is a bootstrap: run `go test -run TestGenerateFixtures`,
// then the committed fixtures pin the contract that the Go and TypeScript
// suites both assert against.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/cases"
)

func TestGenerateFixtures(t *testing.T) {
	if os.Getenv("GEN_FIXTURES") == "" {
		t.Skip("set GEN_FIXTURES=1 to regenerate testdata/sync")
	}
	out := filepath.Join("..", "..", "testdata", "sync")
	if err := os.MkdirAll(out, 0755); err != nil {
		t.Fatal(err)
	}

	writeJSON := func(name string, v any) {
		data, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(out, name), append(data, '\n'), 0644); err != nil {
			t.Fatal(err)
		}
	}

	// ---- note records (schema v2, V1 wire contract) ----
	// The V1 record carries a complete portable .md path and has no kind,
	// parentId, graph, or stored contentHash (the hash is derived). Each case
	// pins the DERIVED content hash so later phases can assert the hash
	// derivation is stable. The conflict-ID case reuses the v5 identity derived
	// from the same note-state hashes pinned in state-hashes.json, so both
	// fixtures agree on the same conflict note.
	sourceID := "5d5d8b2c-94f7-4a38-8318-8cd4cb53dfa8"
	ideaRecord := &NoteRecord{
		SchemaVersion: NoteSchemaVersion, SyncID: sourceID,
		Path: "Projects/idea.md", Markdown: "---\ntags: [\"project\"]\n---\n# Idea\n",
	}
	nestedRecord := &NoteRecord{
		SchemaVersion: NoteSchemaVersion, SyncID: "6e6e9c3d-a5b8-4c49-9409-9de566677770",
		Path: "Projects/Sub/deep.md", Markdown: "# Nested\nbody\n",
	}
	tombRecord := &NoteRecord{
		SchemaVersion: NoteSchemaVersion, SyncID: "8a8a1e5f-c7da-4e6b-b62b-1f0788899992",
		Path: "archive/deleted.md", Deleted: true,
	}
	ideaHash := ideaRecord.ComputeContentHash()
	nestedHash := nestedRecord.ComputeContentHash()
	tombHash := tombRecord.ComputeContentHash()
	localDivergent := StateHash(nestedHash, false)
	remoteDivergent := StateHash(ideaHash, false)
	tombstoneDivergent := StateHash(ideaHash, true)
	conflictID, err := DeriveConflictSyncID(sourceID, localDivergent, remoteDivergent)
	if err != nil {
		t.Fatal(err)
	}
	type noteCase struct {
		Name          string     `json:"name"`
		Record        NoteRecord `json:"record"`
		ContentHash   string     `json:"contentHash"`
		CanonicalJSON string     `json:"canonicalJson"`
	}
	noteRecords := []*NoteRecord{
		ideaRecord,
		nestedRecord,
		tombRecord,
		{
			SchemaVersion: NoteSchemaVersion, SyncID: conflictID,
			Path:     "Projects/" + ConflictFilename("idea", conflictID),
			Markdown: "# Local version\n",
		},
		{
			SchemaVersion: NoteSchemaVersion, SyncID: "9b9b2f60-d8eb-4f7c-a73c-2018999aaab3",
			Path:     "你好/笔记.md",
			Markdown: "# 你好\n中文内容\n",
		},
		{
			SchemaVersion: NoteSchemaVersion, SyncID: "acac3051-e9fc-408d-884d-3119aaaa4bb4",
			Path: "blank.md",
		},
	}
	var noteCases []noteCase
	for _, n := range noteRecords {
		h := n.ComputeContentHash()
		ser, err := n.Serialize()
		if err != nil {
			t.Fatal(err)
		}
		noteCases = append(noteCases, noteCase{
			Name: n.Path, Record: *n, ContentHash: h, CanonicalJSON: string(ser),
		})
	}

	// Portable path collisions: individually valid records whose paths collide
	// under PortablePathKey (case variant and NFC decomposition). The keys are
	// pinned here so the cycle-level collision detection can assert against them.
	portableCollisions := []map[string]any{
		{"name": "case variant", "portablePathKey": PortablePathKey("Projects/Hello.md"), "records": []NoteRecord{
			{
				SchemaVersion: NoteSchemaVersion, SyncID: "bdad4162-faf1-418e-895e-4221bbbb5cc5",
				Path: "Projects/Hello.md", Markdown: "upper\n",
			},
			{
				SchemaVersion: NoteSchemaVersion, SyncID: "cebe5263-fbf2-419f-9a6f-5332cccc6dd6",
				Path: "projects/hello.md", Markdown: "lower\n",
			},
		}},
		{"name": "nfc decomposition", "portablePathKey": PortablePathKey("café.md"), "records": []NoteRecord{
			{
				SchemaVersion: NoteSchemaVersion, SyncID: "dfcf6374-aca3-4f01-ab70-6443dddd7ee7",
				Path: "café.md", Markdown: "composed\n",
			},
			{
				SchemaVersion: NoteSchemaVersion, SyncID: "e0d07485-bdb4-4e12-bc81-7554eee8ff08",
				Path: "cafe\u0301.md", Markdown: "decomposed\n",
			},
		}},
	}

	invalidNotes := []map[string]any{
		{"name": "newer schema", "record": map[string]any{
			"schemaVersion": 3, "syncId": "5d5d8b2c-94f7-4a38-8318-8cd4cb53dfa8",
			"path": "idea.md", "markdown": "x", "deleted": false,
		}},
		{"name": "bad sync uuid", "record": map[string]any{
			"schemaVersion": 2, "syncId": "not-a-uuid", "path": "idea.md",
			"markdown": "x", "deleted": false,
		}},
		{"name": "traversal path", "record": map[string]any{
			"schemaVersion": 2, "syncId": "5d5d8b2c-94f7-4a38-8318-8cd4cb53dfa8",
			"path": "../evil.md", "markdown": "x", "deleted": false,
		}},
		{"name": "absolute path", "record": map[string]any{
			"schemaVersion": 2, "syncId": "5d5d8b2c-94f7-4a38-8318-8cd4cb53dfa8",
			"path": "/abs.md", "markdown": "x", "deleted": false,
		}},
		{"name": "backslash path", "record": map[string]any{
			"schemaVersion": 2, "syncId": "5d5d8b2c-94f7-4a38-8318-8cd4cb53dfa8",
			"path": `a\b.md`, "markdown": "x", "deleted": false,
		}},
		{"name": "empty segment", "record": map[string]any{
			"schemaVersion": 2, "syncId": "5d5d8b2c-94f7-4a38-8318-8cd4cb53dfa8",
			"path": "a//b.md", "markdown": "x", "deleted": false,
		}},
		{"name": "reserved memodump segment", "record": map[string]any{
			"schemaVersion": 2, "syncId": "5d5d8b2c-94f7-4a38-8318-8cd4cb53dfa8",
			"path": ".memodump/secret.md", "markdown": "x", "deleted": false,
		}},
		{"name": "reserved images segment", "record": map[string]any{
			"schemaVersion": 2, "syncId": "5d5d8b2c-94f7-4a38-8318-8cd4cb53dfa8",
			"path": ".images/pic.md", "markdown": "x", "deleted": false,
		}},
		{"name": "non-md extension", "record": map[string]any{
			"schemaVersion": 2, "syncId": "5d5d8b2c-94f7-4a38-8318-8cd4cb53dfa8",
			"path": "note.txt", "markdown": "x", "deleted": false,
		}},
		{"name": "tombstone with markdown", "record": map[string]any{
			"schemaVersion": 2, "syncId": "5d5d8b2c-94f7-4a38-8318-8cd4cb53dfa8",
			"path": "gone.md", "markdown": "x", "deleted": true,
		}},
		{"name": "live note missing markdown", "record": map[string]any{
			"schemaVersion": 2, "syncId": "5d5d8b2c-94f7-4a38-8318-8cd4cb53dfa8",
			"path": "idea.md", "deleted": false,
		}},
		{"name": "crlf markdown", "record": map[string]any{
			"schemaVersion": 2, "syncId": "5d5d8b2c-94f7-4a38-8318-8cd4cb53dfa8",
			"path": "idea.md", "markdown": "a\r\nb\n", "deleted": false,
		}},
		{"name": "invalid media key", "record": map[string]any{
			"schemaVersion": 2, "syncId": "5d5d8b2c-94f7-4a38-8318-8cd4cb53dfa8",
			"path": "idea.md", "markdown": "![x](memodump-media:not-a-key)", "deleted": false,
		}},
		{"name": "empty media key", "record": map[string]any{
			"schemaVersion": 2, "syncId": "5d5d8b2c-94f7-4a38-8318-8cd4cb53dfa8",
			"path": "idea.md", "markdown": "![x](memodump-media:)", "deleted": false,
		}},
		{"name": "wrong field type", "record": map[string]any{
			"schemaVersion": 2, "syncId": 42, "path": "idea.md",
			"markdown": "x", "deleted": false,
		}},
		{"name": "null markdown", "record": map[string]any{
			"schemaVersion": 2, "syncId": "5d5d8b2c-94f7-4a38-8318-8cd4cb53dfa8",
			"path": "idea.md", "markdown": nil, "deleted": false,
		}},
		{"name": "null deleted", "record": map[string]any{
			"schemaVersion": 2, "syncId": "5d5d8b2c-94f7-4a38-8318-8cd4cb53dfa8",
			"path": "idea.md", "markdown": "x", "deleted": nil,
		}},
	}
	rawNoteCases := []map[string]any{
		{"name": "unknown field", "json": `{"schemaVersion":2,"syncId":"5d5d8b2c-94f7-4a38-8318-8cd4cb53dfa8","path":"idea.md","markdown":"x","deleted":false,"evil":true}`},
		{"name": "contentHash not a wire field", "json": `{"schemaVersion":2,"syncId":"5d5d8b2c-94f7-4a38-8318-8cd4cb53dfa8","path":"idea.md","markdown":"x","contentHash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","deleted":false}`},
		{"name": "duplicate syncId", "json": `{"schemaVersion":2,"syncId":"5d5d8b2c-94f7-4a38-8318-8cd4cb53dfa8","syncId":"6e6e9c3d-a5b8-4c49-9409-9de566677770","path":"idea.md","markdown":"x","deleted":false}`},
		{"name": "missing deleted", "json": `{"schemaVersion":2,"syncId":"5d5d8b2c-94f7-4a38-8318-8cd4cb53dfa8","path":"idea.md","markdown":"x"}`},
		{"name": "trailing json value", "json": `{"schemaVersion":2,"syncId":"5d5d8b2c-94f7-4a38-8318-8cd4cb53dfa8","path":"idea.md","markdown":"x","deleted":false}{"x":1}`},
		{"name": "malformed json", "json": `{"schemaVersion":2,`},
	}
	writeJSON("note-records.json", map[string]any{
		"valid":              noteCases,
		"portableCollisions": portableCollisions,
		"invalid":            invalidNotes,
		"invalidRaw":         rawNoteCases,
	})

	// ---- repository descriptors ----
	type repoCase struct {
		Name          string               `json:"name"`
		Descriptor    RepositoryDescriptor `json:"descriptor"`
		CanonicalJSON string               `json:"canonicalJson"`
	}
	desc := RepositoryDescriptor{
		FormatVersion: 1, RepositoryID: "bdbd4162-faf1-418e-895e-4221bbbb5cc5",
		CreatedAt: 1785800000000, MinimumClientVersion: "2.0.0",
	}
	ser, serr := desc.Serialize()
	if serr != nil {
		t.Fatal(serr)
	}
	repoCases := []repoCase{{Name: "standard", Descriptor: desc, CanonicalJSON: string(ser)}}
	invalidRepo := []map[string]any{
		{"name": "newer format", "json": `{"formatVersion":2,"repositoryId":"bdbd4162-faf1-418e-895e-4221bbbb5cc5","createdAt":1785800000000,"minimumClientVersion":"2.0.0"}`},
		{"name": "bad repository id", "json": `{"formatVersion":1,"repositoryId":"nope","createdAt":1785800000000,"minimumClientVersion":"2.0.0"}`},
		{"name": "unknown field", "json": `{"formatVersion":1,"repositoryId":"bdbd4162-faf1-418e-895e-4221bbbb5cc5","createdAt":1785800000000,"minimumClientVersion":"2.0.0","evil":true}`},
		{"name": "missing field", "json": `{"formatVersion":1,"repositoryId":"bdbd4162-faf1-418e-895e-4221bbbb5cc5","createdAt":1785800000000}`},
		{"name": "empty minimumClientVersion", "json": `{"formatVersion":1,"repositoryId":"bdbd4162-faf1-418e-895e-4221bbbb5cc5","createdAt":1785800000000,"minimumClientVersion":""}`},
		{"name": "createdAt beyond safe integer", "json": `{"formatVersion":1,"repositoryId":"bdbd4162-faf1-418e-895e-4221bbbb5cc5","createdAt":9007199254740992,"minimumClientVersion":"2.0.0"}`},
		{"name": "trailing content", "json": `{"formatVersion":1,"repositoryId":"bdbd4162-faf1-418e-895e-4221bbbb5cc5","createdAt":1785800000000,"minimumClientVersion":"2.0.0"}extra`},
	}
	writeJSON("repo-descriptors.json", map[string]any{"valid": repoCases, "invalid": invalidRepo})

	// ---- canonical markdown ----
	writeJSON("canonical-markdown.json", map[string]any{"cases": []map[string]string{
		{"name": "crlf to lf", "input": "a\r\nb\r\n", "normalized": "a\nb\n"},
		{"name": "bare cr to lf", "input": "a\rb", "normalized": "a\nb"},
		{"name": "mixed", "input": "a\r\nb\rc\n", "normalized": "a\nb\nc\n"},
		{"name": "already lf", "input": "a\nb", "normalized": "a\nb"},
	}})

	// ---- full Unicode case-folding table (stabilized) ----
	// Maps every original character whose stabilized fold changes it to that
	// fold. The stabilized fold is ToLower(fold(char)): the full case fold
	// followed by a canonical lowercase, so the mapping is idempotent — e.g.
	// fold(Ꭰ)=ꭰ but fold(ꭰ)=Ꭰ (a 2-cycle), while both stabilize to ꭰ, so two
	// case-variant Cherokee names produce the same collision key. The Go
	// implementation applies ToLower(cases.Fold(...)); the TypeScript one
	// embeds this exact table and applies it per character with NO lowercase
	// fallback, so characters outside the table keep their original value and
	// the two engines agree byte-for-byte.
	caseFold := map[string]string{}
	for r := rune(0); r <= unicode.MaxRune; r++ {
		if !utf8.ValidRune(r) {
			continue
		}
		ch := string(r)
		stabilized := strings.ToLower(cases.Fold().String(ch))
		if stabilized != ch {
			caseFold[ch] = stabilized
		}
	}
	writeJSON("case-fold.json", map[string]any{"table": caseFold})

	// ---- portable path keys ----
	writeJSON("portable-path-keys.json", map[string]any{"cases": []map[string]string{
		{"name": "ascii casefold", "path": "Projects/Hello.md", "key": PortablePathKey("Projects/Hello.md")},
		{"name": "nested", "path": "A/B/C.md", "key": PortablePathKey("A/B/C.md")},
		{"name": "nfc composed", "path": "café/note.md", "key": PortablePathKey("café/note.md")},
		{"name": "sharp s folds to ss", "path": "Straße.md", "key": PortablePathKey("Straße.md")},
		{"name": "uppercase sharp s folds to ss", "path": "STRASSE.md", "key": PortablePathKey("STRASSE.md")},
		{"name": "ligature folds to two letters", "path": "ﬁle.md", "key": PortablePathKey("ﬁle.md")},
		{"name": "greek final sigma folds to sigma", "path": "ος.md", "key": PortablePathKey("ος.md")},
		{"name": "greek capitals collide with final sigma", "path": "ΟΣ.md", "key": PortablePathKey("ΟΣ.md")},
		{"name": "cherokee uppercase and small letters collide", "path": "Ꭰ.md", "key": PortablePathKey("Ꭰ.md")},
		{"name": "cherokee small letter matches its uppercase fold", "path": "ꭰ.md", "key": PortablePathKey("ꭰ.md")},
	}})

	// ---- conflict names (deterministic, no clock or device label) ----
	writeJSON("conflict-names.json", map[string]any{"cases": []map[string]string{
		{"name": "basic", "stem": "idea", "conflictSyncId": conflictID, "expected": ConflictFilename("idea", conflictID)},
		{"name": "unicode stem", "stem": "你好", "conflictSyncId": conflictID, "expected": ConflictFilename("你好", conflictID)},
		{"name": "long id", "stem": "note", "conflictSyncId": "aaaaaaaa-aaaa-5aaa-8aaa-aaaaaaaaaaaa", "expected": ConflictFilename("note", "aaaaaaaa-aaaa-5aaa-8aaa-aaaaaaaaaaaa")},
	}})

	// ---- state hashes and deterministic conflict identities ----
	conflictTombstoneID, err := DeriveConflictSyncID(sourceID, localDivergent, tombstoneDivergent)
	if err != nil {
		t.Fatal(err)
	}
	swappedID, err := DeriveConflictSyncID(sourceID, remoteDivergent, localDivergent)
	if err != nil {
		t.Fatal(err)
	}
	zeroHash := strings.Repeat("0", 64)
	stateCases := []map[string]any{
		{"name": "live idea", "contentHash": ideaHash, "deleted": false, "expected": StateHash(ideaHash, false)},
		{"name": "tombstone idea", "contentHash": ideaHash, "deleted": true, "expected": StateHash(ideaHash, true)},
		{"name": "live nested", "contentHash": nestedHash, "deleted": false, "expected": StateHash(nestedHash, false)},
		{"name": "tombstone deleted", "contentHash": tombHash, "deleted": true, "expected": StateHash(tombHash, true)},
		{"name": "zero content", "contentHash": zeroHash, "deleted": false, "expected": StateHash(zeroHash, false)},
	}
	conflictCases := []map[string]string{
		{"name": "divergent note", "sourceSyncId": sourceID, "localStateHash": localDivergent, "remoteStateHash": remoteDivergent, "expected": conflictID},
		{"name": "edit vs tombstone", "sourceSyncId": sourceID, "localStateHash": localDivergent, "remoteStateHash": tombstoneDivergent, "expected": conflictTombstoneID},
		{"name": "swapped roles differ", "sourceSyncId": sourceID, "localStateHash": remoteDivergent, "remoteStateHash": localDivergent, "expected": swappedID},
	}
	writeJSON("state-hashes.json", map[string]any{
		"namespace":   ConflictNamespace,
		"stateHashes": stateCases,
		"conflictIds": conflictCases,
		"syncIds": map[string]any{
			"validV4": []string{
				"5d5d8b2c-94f7-4a38-8318-8cd4cb53dfa8",
				"1a2b3c4d-1111-4222-8333-444455556666",
				"00000000-0000-4000-8000-000000000000",
			},
			"validV5":                       []string{ConflictNamespace, conflictID, conflictTombstoneID},
			"invalidV5AsRepositoryOrDevice": []string{ConflictNamespace, conflictID},
			"invalid": []string{
				"",
				"not-a-uuid",
				"c8f28d1c-85c6-11e6-9d9d-0242ac130002",
				"5d5d8b2c-94f7-4a38-8318-8cd4cb53dfa8-extra",
				"zzzzzzzz-zzzz-4zzz-8zzz-zzzzzzzzzzzz",
			},
		},
	})

	// ---- retry classes ----
	writeJSON("retry-classes.json", map[string]any{"cases": []map[string]any{
		{"name": "rate-limit", "kind": "rate-limit", "retryAfterSeconds": 5, "retryable": true, "backoffSeconds": 5},
		{"name": "retryable-transport", "kind": "retryable-transport", "retryAfterSeconds": 0, "retryable": true, "backoffSeconds": 1},
		{"name": "auth", "kind": "auth", "retryAfterSeconds": 0, "retryable": false, "backoffSeconds": 0},
		{"name": "permission", "kind": "permission", "retryAfterSeconds": 0, "retryable": false, "backoffSeconds": 0},
		{"name": "quota", "kind": "quota", "retryAfterSeconds": 0, "retryable": false, "backoffSeconds": 0},
		{"name": "precondition-failed", "kind": "precondition-failed", "retryAfterSeconds": 0, "retryable": false, "backoffSeconds": 0},
	}})
}

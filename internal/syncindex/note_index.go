package syncindex

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"unicode/utf8"

	"memodump/internal/cloudsync"
)

// NoteSchemaVersion is the schema of the note-only sync index. It replaces the
// prototype schema-v1 index, which mapped kinds and folders as entities and is
// classified as unsupported rather than loaded as data.
const NoteSchemaVersion = 2

// ErrUnsupportedSchema reports a sync-index document whose schema is not the
// note-only schema v2 — including the prototype schema-v1 state that has never
// shipped with a production provider. The caller must require an explicit
// sync re-enable; it must never be reinterpreted as an empty vault or as
// deletion evidence.
var ErrUnsupportedSchema = errors.New("sync index uses an unsupported schema; re-enable sync")

// ErrInvalidIndex reports a sync-index document that is structurally invalid
// (malformed JSON, unknown/duplicate fields, unsafe paths). The store
// distinguishes it from a real filesystem error: only corruption authorizes
// falling back to the backup, never an I/O failure.
var ErrInvalidIndex = errors.New("invalid sync-index document")

// NoteEntry maps a Sync ID to its current local Markdown path. The index
// carries no kinds, folders, content hashes, cursors, credentials, or remote
// state — it only pins identity.
type NoteEntry struct {
	Path string `json:"path"`
}

// NoteIndex is the v2 portable sync-index document: Sync ID -> Markdown path
// only.
type NoteIndex struct {
	SchemaVersion int                  `json:"schemaVersion"`
	VaultID       string               `json:"vaultId"`
	Notes         map[string]NoteEntry `json:"notes"`
}

// NewNoteIndex returns an empty note-only index for a vault.
func NewNoteIndex(vaultID string) *NoteIndex {
	return &NoteIndex{
		SchemaVersion: NoteSchemaVersion,
		VaultID:       vaultID,
		Notes:         make(map[string]NoteEntry),
	}
}

// validate checks the structural invariants of the index before it is trusted:
// the exact schema, the vault ID, the Sync ID keys, note-path safety, and
// duplicate-path rejection.
func (idx *NoteIndex) validate() error {
	if idx.SchemaVersion != NoteSchemaVersion {
		return fmt.Errorf("unsupported sync-index schema %d", idx.SchemaVersion)
	}
	if !cloudsync.IsUUIDv4(idx.VaultID) {
		return fmt.Errorf("invalid vaultId %q", idx.VaultID)
	}
	// A missing or null notes map is damage, not an empty vault: accepting it
	// would silently reassign every Sync ID on the next enable.
	if idx.Notes == nil {
		return fmt.Errorf("missing notes map")
	}
	seenPaths := make(map[string]string, len(idx.Notes))
	for syncID, e := range idx.Notes {
		if !cloudsync.IsSyncID(syncID) {
			return fmt.Errorf("invalid syncId %q", syncID)
		}
		// ValidNotePath enforces the same complete-.md-path rule as the remote
		// note contract (slash-relative, no traversal, no reserved segment), so
		// the local index and the wire record agree on what a note path is.
		if !cloudsync.ValidNotePath(e.Path) {
			return fmt.Errorf("note %s: unsafe path %q", syncID, e.Path)
		}
		if prev, ok := seenPaths[e.Path]; ok {
			return fmt.Errorf("duplicate path %q (syncIds %s and %s)", e.Path, prev, syncID)
		}
		seenPaths[e.Path] = syncID
	}
	return nil
}

// Serialize returns the canonical note-only index bytes (deterministic key
// order, UTF-8). A content-only rewrite produces byte-identical output.
func (idx *NoteIndex) Serialize() ([]byte, error) {
	notes := make(map[string]any, len(idx.Notes))
	for syncID, e := range idx.Notes {
		notes[syncID] = map[string]any{"path": e.Path}
	}
	return cloudsync.CanonicalBytes(map[string]any{
		"schemaVersion": int64(idx.SchemaVersion),
		"vaultId":       idx.VaultID,
		"notes":         notes,
	})
}

// ParseNoteIndex parses and validates a serialized note-only index. Unknown
// fields, duplicate fields, and trailing content are rejected. A document with
// any schema other than v2 — including the prototype schema-v1 index — is
// rejected with ErrUnsupportedSchema so the caller can require an explicit
// re-enable instead of treating it as an empty vault.
func ParseNoteIndex(data []byte) (*NoteIndex, error) {
	if !utf8.Valid(data) {
		return nil, fmt.Errorf("invalid utf-8")
	}
	// Schema classification before strict field checking: a decodable document
	// with any non-v2 schema (the prototype schema-v1 index included) is
	// unsupported, regardless of field order or shape.
	var probe struct {
		SchemaVersion int `json:"schemaVersion"`
	}
	if err := json.Unmarshal(data, &probe); err == nil && probe.SchemaVersion != NoteSchemaVersion {
		return nil, fmt.Errorf("%w: %d", ErrUnsupportedSchema, probe.SchemaVersion)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	tok, err := dec.Token()
	if err != nil {
		return nil, fmt.Errorf("parse: %v", err)
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, fmt.Errorf("not an object")
	}
	var idx NoteIndex
	seen := make(map[string]bool)
	notesSeen := false
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("parse: %v", err)
		}
		key, ok := keyTok.(string)
		if !ok {
			return nil, fmt.Errorf("non-string field name")
		}
		if seen[key] {
			return nil, fmt.Errorf("duplicate field %q", key)
		}
		seen[key] = true
		switch key {
		case "schemaVersion":
			if err := dec.Decode(&idx.SchemaVersion); err != nil {
				return nil, fieldParseError("schemaVersion")
			}
			// A decodable document with the wrong schema is classified as
			// unsupported (the prototype schema-v1 index included), never as
			// structural corruption.
			if idx.SchemaVersion != NoteSchemaVersion {
				return nil, fmt.Errorf("%w: %d", ErrUnsupportedSchema, idx.SchemaVersion)
			}
		case "vaultId":
			if err := dec.Decode(&idx.VaultID); err != nil {
				return nil, fieldParseError("vaultId")
			}
		case "notes":
			notesSeen = true
			idx.Notes, err = decodeNoteEntries(dec)
			if err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("unknown field %q", key)
		}
	}
	if _, err := dec.Token(); err != nil {
		return nil, fmt.Errorf("parse: %v", err) // closing brace
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("trailing content after sync-index")
		}
		return nil, fmt.Errorf("parse trailing content: %v", err)
	}
	for _, req := range []string{"schemaVersion", "vaultId"} {
		if !seen[req] {
			return nil, fmt.Errorf("missing field %q", req)
		}
	}
	if !notesSeen {
		return nil, fmt.Errorf("missing field %q", "notes")
	}
	if err := idx.validate(); err != nil {
		return nil, err
	}
	return &idx, nil
}

func fieldParseError(key string) error {
	return fmt.Errorf("field %q has the wrong type", key)
}

// decodeNoteEntries decodes the notes object one entry at a time, rejecting
// duplicate note keys and unknown fields inside each entry.
func decodeNoteEntries(dec *json.Decoder) (map[string]NoteEntry, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, fmt.Errorf("parse notes: %v", err)
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, fmt.Errorf("notes is not an object")
	}
	out := make(map[string]NoteEntry)
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("parse note key: %v", err)
		}
		key, ok := keyTok.(string)
		if !ok {
			return nil, fmt.Errorf("non-string note key")
		}
		if _, dup := out[key]; dup {
			return nil, fmt.Errorf("duplicate note %q", key)
		}
		var e NoteEntry
		if err := decodeNoteEntry(dec, &e); err != nil {
			return nil, err
		}
		out[key] = e
	}
	if _, err := dec.Token(); err != nil {
		return nil, fmt.Errorf("parse notes: %v", err) // closing brace
	}
	return out, nil
}

// decodeNoteEntry decodes one note entry with duplicate-field and unknown-field
// rejection.
func decodeNoteEntry(dec *json.Decoder, e *NoteEntry) error {
	tok, err := dec.Token()
	if err != nil {
		return fmt.Errorf("parse note entry: %v", err)
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return fmt.Errorf("note entry is not an object")
	}
	seen := make(map[string]bool)
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return fmt.Errorf("parse note entry field: %v", err)
		}
		key, ok := keyTok.(string)
		if !ok {
			return fmt.Errorf("non-string note entry field name")
		}
		if seen[key] {
			return fmt.Errorf("duplicate note entry field %q", key)
		}
		seen[key] = true
		if key != "path" {
			return fmt.Errorf("unknown note entry field %q", key)
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return fmt.Errorf("note entry field %q has the wrong type", key)
		}
		// null is rejected explicitly; Go's Decode would otherwise accept it and
		// keep the zero value, silently reinterpreting an ambiguous entry.
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return fmt.Errorf("note entry field %q must not be null", key)
		}
		if err := json.Unmarshal(raw, &e.Path); err != nil {
			return fmt.Errorf("note entry field %q has the wrong type", key)
		}
	}
	if _, err := dec.Token(); err != nil {
		return fmt.Errorf("parse note entry: %v", err) // closing brace
	}
	if !seen["path"] {
		return fmt.Errorf("note entry missing field %q", "path")
	}
	return nil
}

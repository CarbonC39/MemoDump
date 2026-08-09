package cloudsync

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

// NoteSchemaVersion is the current remote note-record schema. It is distinct
// from the prototype entity schema (SchemaVersion = 1): the V1 wire contract
// carries a complete portable path and has no kind, parentId, or graph.
const NoteSchemaVersion = 2

// ErrInvalidNote reports a remote note record failing structural validation.
var ErrInvalidNote = errors.New("invalid note record")

// NoteRecord is one synchronized Markdown note: notes/<syncId>.json. A
// tombstone keeps schemaVersion/syncId/path/deleted and omits markdown. There
// is no folder kind, parentId, cursor, or graph — folders are not remote
// entities. The record carries no contentHash: the hash is a derived property
// (ComputeContentHash) used by the snapshot and conflict derivation, never a
// serialized field.
type NoteRecord struct {
	SchemaVersion int    `json:"schemaVersion"`
	SyncID        string `json:"syncId"`
	Path          string `json:"path"`
	Markdown      string `json:"markdown,omitempty"`
	Deleted       bool   `json:"deleted"`
}

// ComputeContentHash returns the canonical digest of the note's content: the
// tuple (syncId, portable path, markdown, deleted). It is the content identity
// of a note record, independent of the provider's opaque version.
func (n *NoteRecord) ComputeContentHash() string {
	data, err := canonicalBytes(map[string]any{
		"deleted":  n.Deleted,
		"markdown": n.Markdown,
		"path":     n.Path,
		"syncId":   n.SyncID,
	})
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Serialize returns the canonical note record: deterministic key order, UTF-8,
// and a trailing LF. A tombstone omits the markdown key. It refuses to emit a
// record the remote parser would reject: the record is fully validated first,
// and the canonical bytes must fit MaxEntityBytes. This guarantees an uploaded
// record can never be rejected (or silently mojibake-encoded) on the receiving
// side.
func (n *NoteRecord) Serialize() ([]byte, error) {
	if err := n.Validate(); err != nil {
		return nil, err
	}
	fields := map[string]any{
		"schemaVersion": int64(n.SchemaVersion),
		"syncId":        n.SyncID,
		"path":          n.Path,
		"deleted":       n.Deleted,
	}
	if !n.Deleted {
		fields["markdown"] = n.Markdown
	}
	data, err := canonicalBytes(fields)
	if err != nil {
		return nil, err
	}
	// Check the FINAL length including the trailing LF: a document whose
	// canonical JSON alone fits MaxEntityBytes but whose serialized bytes do
	// not would be rejected by the remote parser. Locally-serializable must
	// equal remotely-parseable.
	data = append(data, '\n')
	if len(data) > MaxEntityBytes {
		return nil, ErrOversized
	}
	return data, nil
}

// Validate checks single-note invariants before any materialization: the exact
// schema version, UTF-8 record fields, a UUID v4/v5 Sync ID, a safe portable
// path, LF-normalized Markdown, and the tombstone rule (a tombstone carries no
// Markdown).
func (n *NoteRecord) Validate() error {
	if n.SchemaVersion != NoteSchemaVersion {
		return fmt.Errorf("%w: schema %d", ErrInvalidSchema, n.SchemaVersion)
	}
	if !utf8.ValidString(n.SyncID) || !utf8.ValidString(n.Path) || !utf8.ValidString(n.Markdown) {
		return fmt.Errorf("%w: invalid utf-8 in record", ErrInvalidNote)
	}
	if !IsSyncID(n.SyncID) {
		return fmt.Errorf("%w: bad syncId %q", ErrInvalidNote, n.SyncID)
	}
	if !ValidNotePath(n.Path) {
		return fmt.Errorf("%w: bad path %q", ErrInvalidNote, n.Path)
	}
	if n.Markdown != NormalizeMarkdown(n.Markdown) {
		return fmt.Errorf("%w: markdown not LF-normalized", ErrInvalidNote)
	}
	if n.Deleted && n.Markdown != "" {
		return fmt.Errorf("%w: tombstone carries markdown", ErrInvalidNote)
	}
	if key, bad := FirstInvalidMediaKey(n.Markdown); bad {
		return fmt.Errorf("%w: invalid media key %q", ErrInvalidNote, key)
	}
	return nil
}

// ValidNotePath reports whether path is a complete, safe, portable Markdown
// note path: slash-relative, no traversal or empty segments, no backslash, no
// reserved repository segment (.memodump/.images), and a lowercase .md
// extension. The reserved-segment rule mirrors vaultfs.ContainsReservedSegment
// so the wire contract and the repository agree on what a vault path is.
func ValidNotePath(path string) bool {
	if path == "" || strings.HasPrefix(path, "/") || strings.HasPrefix(path, `\`) {
		return false
	}
	if strings.Contains(path, "\\") {
		return false
	}
	segs := strings.Split(path, "/")
	for i, seg := range segs {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
		if i == len(segs)-1 && !strings.HasSuffix(seg, ".md") {
			return false
		}
		if reservedNoteSegment(seg) {
			return false
		}
		for _, r := range seg {
			if r < 0x20 || r == 0x7f {
				return false
			}
		}
	}
	return true
}

// reservedNoteSegment mirrors vaultfs's reserved repository directories
// (.images, .memodump) case-insensitively. The two packages must stay in
// agreement so a remote record can never name a directory the repository owns.
func reservedNoteSegment(seg string) bool {
	lower := strings.ToLower(seg)
	return lower == ".memodump" || lower == ".images"
}

// ParseNoteRecord parses and validates a raw remote note record. The field set
// is strict: every required field must appear exactly once with the correct
// type, duplicate and unknown fields and trailing content are rejected, a live
// note must carry markdown, and a tombstone must not. Parsing is token-based so
// a record with two syncId/path/deleted keys is rejected instead of silently
// accepting the last one.
func ParseNoteRecord(data []byte) (*NoteRecord, error) {
	if len(data) > MaxEntityBytes {
		return nil, ErrOversized
	}
	if !utf8.Valid(data) {
		return nil, fmt.Errorf("%w: invalid utf-8", ErrInvalidNote)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	tok, err := dec.Token()
	if err != nil {
		return nil, fmt.Errorf("%w: parse: %v", ErrInvalidNote, err)
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, fmt.Errorf("%w: not an object", ErrInvalidNote)
	}
	var n NoteRecord
	seen := make(map[string]bool)
	markdownSeen := false
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("%w: parse: %v", ErrInvalidNote, err)
		}
		key, ok := keyTok.(string)
		if !ok {
			return nil, fmt.Errorf("%w: non-string field name", ErrInvalidNote)
		}
		if seen[key] {
			return nil, fmt.Errorf("%w: duplicate field %q", ErrInvalidNote, key)
		}
		seen[key] = true
		switch key {
		case "schemaVersion":
			if err := decodeNoteScalar(dec, key, &n.SchemaVersion); err != nil {
				return nil, err
			}
		case "syncId":
			if err := decodeNoteScalar(dec, key, &n.SyncID); err != nil {
				return nil, err
			}
		case "path":
			if err := decodeNoteScalar(dec, key, &n.Path); err != nil {
				return nil, err
			}
		case "markdown":
			markdownSeen = true
			if err := decodeNoteScalar(dec, key, &n.Markdown); err != nil {
				return nil, err
			}
		case "deleted":
			if err := decodeNoteScalar(dec, key, &n.Deleted); err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("%w: unknown field %q", ErrInvalidNote, key)
		}
	}
	if _, err := dec.Token(); err != nil {
		return nil, fmt.Errorf("%w: parse: %v", ErrInvalidNote, err) // closing brace
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("%w: trailing content after record", ErrInvalidNote)
		}
		return nil, fmt.Errorf("%w: parse trailing content: %v", ErrInvalidNote, err)
	}
	for _, req := range []string{"schemaVersion", "syncId", "path", "deleted"} {
		if !seen[req] {
			return nil, fmt.Errorf("%w: missing field %q", ErrInvalidNote, req)
		}
	}
	if n.Deleted {
		if markdownSeen {
			return nil, fmt.Errorf("%w: tombstone must not carry markdown", ErrInvalidNote)
		}
	} else if !markdownSeen {
		return nil, fmt.Errorf("%w: live note missing markdown", ErrInvalidNote)
	}
	if err := n.Validate(); err != nil {
		return nil, err
	}
	return &n, nil
}

// decodeNoteScalar decodes one scalar field value, rejecting JSON null
// explicitly: Go's json.Decode would otherwise accept null and keep the zero
// value (null markdown becoming "", null deleted becoming false), silently
// reinterpreting an ambiguous record.
func decodeNoteScalar(dec *json.Decoder, key string, dst any) error {
	var raw json.RawMessage
	if err := dec.Decode(&raw); err != nil {
		return noteFieldTypeError(key)
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return fmt.Errorf("%w: field %q must not be null", ErrInvalidNote, key)
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		return noteFieldTypeError(key)
	}
	return nil
}

func noteFieldTypeError(key string) error {
	return fmt.Errorf("%w: field %q has the wrong type", ErrInvalidNote, key)
}

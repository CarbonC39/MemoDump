package cloudsync

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	// SchemaVersion is the current entity record schema.
	SchemaVersion = 1
	// KindNote and KindFolder are the two entity kinds.
	KindNote   = "note"
	KindFolder = "folder"
	// MaxEntityBytes caps untrusted remote records before materialization.
	MaxEntityBytes = 1 << 20 // 1 MiB
)

var (
	// ErrInvalidSchema reports a record whose schema is newer or unknown.
	ErrInvalidSchema = errors.New("unsupported schema version")
	// ErrOversized reports a record exceeding MaxEntityBytes.
	ErrOversized = errors.New("entity exceeds size limit")
	// ErrInvalidEntity reports a record failing structural validation.
	ErrInvalidEntity = errors.New("invalid entity")
	// ErrCycle reports a folder parent graph that contains a cycle.
	ErrCycle = errors.New("parent cycle")
)

// Entity is one record of the remote repository:
// entities/<syncId>.json. Folder records carry no markdown.
type Entity struct {
	SchemaVersion int    `json:"schemaVersion"`
	SyncID        string `json:"syncId"`
	Kind          string `json:"kind"`
	ParentID      string `json:"parentId"`
	Name          string `json:"name"`
	Markdown      string `json:"markdown,omitempty"`
	ContentHash   string `json:"contentHash"`
	Deleted       bool   `json:"deleted"`
	UpdatedBy     string `json:"updatedBy"`
	UpdatedAt     int64  `json:"updatedAt"`
}

// ComputeContentHash returns the canonical digest of the content fields.
func (e *Entity) ComputeContentHash() string {
	return ContentHash(e.Kind, e.ParentID, e.Name, e.Markdown)
}

// Serialize returns the canonical entity record: deterministic key order,
// UTF-8, and a trailing LF. Folder records omit the markdown key.
func (e *Entity) Serialize() ([]byte, error) {
	fields := map[string]any{
		"schemaVersion": int64(e.SchemaVersion),
		"syncId":        e.SyncID,
		"kind":          e.Kind,
		"parentId":      e.ParentID,
		"name":          e.Name,
		"contentHash":   e.ContentHash,
		"deleted":       e.Deleted,
		"updatedBy":     e.UpdatedBy,
		"updatedAt":     int64(e.UpdatedAt),
	}
	if e.Kind == KindNote {
		fields["markdown"] = e.Markdown
	}
	data, err := canonicalBytes(fields)
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// Validate checks single-entity invariants before any materialization.
func (e *Entity) Validate() error {
	if e.SchemaVersion != SchemaVersion {
		return fmt.Errorf("%w: %d", ErrInvalidSchema, e.SchemaVersion)
	}
	if e.Kind != KindNote && e.Kind != KindFolder {
		return fmt.Errorf("%w: bad kind %q", ErrInvalidEntity, e.Kind)
	}
	if e.Kind == KindFolder && e.Markdown != "" {
		return fmt.Errorf("%w: folder carries markdown", ErrInvalidEntity)
	}
	if !IsUUIDv4(e.SyncID) {
		return fmt.Errorf("%w: bad syncId %q", ErrInvalidEntity, e.SyncID)
	}
	if e.ParentID != "" && !IsUUIDv4(e.ParentID) {
		return fmt.Errorf("%w: bad parentId %q", ErrInvalidEntity, e.ParentID)
	}
	if !IsUUIDv4(e.UpdatedBy) {
		return fmt.Errorf("%w: bad updatedBy %q", ErrInvalidEntity, e.UpdatedBy)
	}
	if !ValidEntityName(e.Name) {
		return fmt.Errorf("%w: bad name %q", ErrInvalidEntity, e.Name)
	}
	if !contentHashRe.MatchString(e.ContentHash) {
		return fmt.Errorf("%w: bad contentHash %q", ErrInvalidEntity, e.ContentHash)
	}
	if e.ContentHash != e.ComputeContentHash() {
		return fmt.Errorf("%w: content hash mismatch", ErrInvalidEntity)
	}
	if key, bad := FirstInvalidMediaKey(e.Markdown); bad {
		return fmt.Errorf("%w: invalid media key %q", ErrInvalidEntity, key)
	}
	return nil
}

var contentHashRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

// MaxSafeInteger is JavaScript's Number.MAX_SAFE_INTEGER (2^53-1). The TypeScript
// side validates against it, so the Go parser caps the same fields to keep the
// rejection behavior identical.
const MaxSafeInteger int64 = 1<<53 - 1

var allowedEntityFields = map[string]bool{
	"schemaVersion": true, "syncId": true, "kind": true, "parentId": true,
	"name": true, "markdown": true, "contentHash": true, "deleted": true,
	"updatedBy": true, "updatedAt": true,
}

// ParseEntity parses and validates a raw remote entity record. The field set is
// strict: every required field must be present with the correct type, unknown
// fields and trailing content are rejected, and a note must carry markdown
// while a folder must not.
func ParseEntity(data []byte) (*Entity, error) {
	if len(data) > MaxEntityBytes {
		return nil, ErrOversized
	}
	if !utf8.Valid(data) {
		return nil, fmt.Errorf("%w: invalid utf-8", ErrInvalidEntity)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var fields map[string]json.RawMessage
	if err := dec.Decode(&fields); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidEntity, err)
	}
	// Reject anything after the record other than whitespace.
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("%w: trailing content after record", ErrInvalidEntity)
	}
	for k := range fields {
		if !allowedEntityFields[k] {
			return nil, fmt.Errorf("%w: unknown field %q", ErrInvalidEntity, k)
		}
	}
	for _, k := range []string{
		"schemaVersion", "syncId", "kind", "parentId", "name",
		"contentHash", "deleted", "updatedBy", "updatedAt",
	} {
		if _, ok := fields[k]; !ok {
			return nil, fmt.Errorf("%w: missing field %q", ErrInvalidEntity, k)
		}
	}
	kind, err := requireString(fields, "kind")
	if err != nil {
		return nil, err
	}
	if kind == KindNote {
		if _, ok := fields["markdown"]; !ok {
			return nil, fmt.Errorf("%w: note missing markdown", ErrInvalidEntity)
		}
	} else if kind == KindFolder {
		if _, ok := fields["markdown"]; ok {
			return nil, fmt.Errorf("%w: folder must not carry markdown", ErrInvalidEntity)
		}
	}

	var e Entity
	if err := json.Unmarshal(fields["schemaVersion"], &e.SchemaVersion); err != nil {
		return nil, fieldTypeError("schemaVersion")
	}
	if e.SchemaVersion != SchemaVersion {
		return nil, fmt.Errorf("%w: %d", ErrInvalidSchema, e.SchemaVersion)
	}
	if e.SyncID, err = requireString(fields, "syncId"); err != nil {
		return nil, err
	}
	e.Kind = kind
	if e.ParentID, err = requireString(fields, "parentId"); err != nil {
		return nil, err
	}
	if e.Name, err = requireString(fields, "name"); err != nil {
		return nil, err
	}
	if kind == KindNote {
		if e.Markdown, err = requireString(fields, "markdown"); err != nil {
			return nil, err
		}
	}
	if e.ContentHash, err = requireString(fields, "contentHash"); err != nil {
		return nil, err
	}
	if e.UpdatedBy, err = requireString(fields, "updatedBy"); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(fields["deleted"], &e.Deleted); err != nil {
		return nil, fieldTypeError("deleted")
	}
	if err := json.Unmarshal(fields["updatedAt"], &e.UpdatedAt); err != nil {
		return nil, fieldTypeError("updatedAt")
	}
	if e.UpdatedAt <= 0 || e.UpdatedAt > MaxSafeInteger {
		return nil, fmt.Errorf("%w: bad updatedAt %d", ErrInvalidEntity, e.UpdatedAt)
	}
	if err := e.Validate(); err != nil {
		return nil, err
	}
	return &e, nil
}

func requireString(fields map[string]json.RawMessage, key string) (string, error) {
	raw, ok := fields[key]
	if !ok {
		return "", fmt.Errorf("%w: missing field %q", ErrInvalidEntity, key)
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", fieldTypeError(key)
	}
	return s, nil
}

func fieldTypeError(key string) error {
	return fmt.Errorf("%w: field %q has the wrong type", ErrInvalidEntity, key)
}

// ValidateEntities validates a set of entities as a graph: every parentId must
// reference a folder, and the folder parent graph must be acyclic.
func ValidateEntities(entities map[string]*Entity) error {
	for id, e := range entities {
		if err := e.Validate(); err != nil {
			return fmt.Errorf("entity %s: %w", id, err)
		}
	}
	// Parent must exist and be a folder (root parentId "" is implicit).
	for id, e := range entities {
		if e.ParentID == "" {
			continue
		}
		parent, ok := entities[e.ParentID]
		if !ok {
			return fmt.Errorf("%w: %s has missing parent %s", ErrInvalidEntity, id, e.ParentID)
		}
		if parent.Kind != KindFolder {
			return fmt.Errorf("%w: %s parent %s is not a folder", ErrInvalidEntity, id, e.ParentID)
		}
	}
	// Acyclic parent graph via three-color DFS.
	const (
		white = 0
		gray  = 1
		black = 2
	)
	color := make(map[string]int, len(entities))
	var visit func(id string) error
	visit = func(id string) error {
		switch color[id] {
		case gray:
			return fmt.Errorf("%w: %s", ErrCycle, id)
		case black:
			return nil
		}
		color[id] = gray
		if e := entities[id]; e.ParentID != "" {
			if err := visit(e.ParentID); err != nil {
				return err
			}
		}
		color[id] = black
		return nil
	}
	for id := range entities {
		if err := visit(id); err != nil {
			return err
		}
	}
	return nil
}

var uuidV4Re = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// IsUUIDv4 reports whether s is a syntactically valid version-4 UUID.
func IsUUIDv4(s string) bool {
	return uuidV4Re.MatchString(s)
}

// ValidEntityName reports whether name is safe to materialize as a path
// segment: no separators, no traversal, no control characters.
func ValidEntityName(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}
	if strings.ContainsAny(name, `/\`) {
		return false
	}
	if strings.HasPrefix(name, ".") {
		return false
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

// mediaKeyRe matches a valid content-addressed media key at the START of the
// text that follows a "memodump-media:" prefix (the caller slices off the
// prefix before matching).
var mediaKeyRe = regexp.MustCompile(`^([a-f0-9]{64}\.(png|jpg|gif|webp|avif))`)

// FirstInvalidMediaKey returns the first invalid memodump-media: key referenced
// in markdown and true, or ("", false) when every reference is well-formed. An
// empty reference (`memodump-media:` with no key) is invalid — the bool keeps
// that distinct from "no error".
func FirstInvalidMediaKey(markdown string) (string, bool) {
	// Split on the scheme prefix so a malformed reference (missing hash or bad
	// extension) is still seen.
	idx := 0
	for {
		rel := strings.Index(markdown[idx:], "memodump-media:")
		if rel < 0 {
			return "", false
		}
		start := idx + rel + len("memodump-media:")
		rest := markdown[start:]
		if m := mediaKeyRe.FindStringSubmatch(rest); m != nil {
			// A valid key must end at the token boundary.
			key := m[1]
			next := start + len(key)
			if next >= len(markdown) || !isKeyContinuation(markdown[next]) {
				idx = start + len(m[0])
				continue
			}
		}
		// No valid key at this position: report the first token.
		tok := rest
		if i := strings.IndexAny(tok, " \t\n\"')]"); i >= 0 {
			tok = tok[:i]
		}
		if tok == "" {
			tok = "<empty>"
		}
		return tok, true
	}
}

func isKeyContinuation(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' ||
		b == '.' || b == '-' || b == '_' || b == '/' || b == ':'
}

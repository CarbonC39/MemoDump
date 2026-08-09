package syncstate

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"unicode/utf8"

	"memodump/internal/cloudsync"
)

// SnapshotV2SchemaVersion is the current disposable note-snapshot schema. It
// replaces the prototype schema-v1 snapshot: it stores note baselines only
// (the "notes" key), carries no cursor, and reuses SnapshotEntity's
// {contentHash, remoteVersion, deleted} per note.
const SnapshotV2SchemaVersion = 2

// ErrUnsupportedPrototype reports a snapshot that is the schema-v1 prototype
// device state, which never shipped with a production provider. It must be
// reported for explicit re-enable and never loaded as a baseline.
var ErrUnsupportedPrototype = errors.New("unsupported prototype snapshot; re-enable sync")

// SnapshotV2 is one replica's disposable device snapshot: the last state this
// replica knows was equal locally and remotely, per note. It is a cache, not a
// log — atomically replaced at most once per cycle and never appended to. The
// note map is keyed by Sync ID.
type SnapshotV2 struct {
	SchemaVersion   int                       `json:"schemaVersion"`
	VaultID         string                    `json:"vaultId"`
	ReplicaID       string                    `json:"replicaId"`
	RepositoryID    string                    `json:"repositoryId"`
	ProviderProfile string                    `json:"providerProfile"`
	Notes           map[string]SnapshotEntity `json:"notes"`
}

// Validate checks the snapshot's invariants before it is trusted or persisted:
// the exact schema version, UUID-v4 Vault/Replica/Repository IDs, a lowercase
// 64-hex provider fingerprint, a non-null notes map, Sync IDs passing
// IsSyncID, lowercase 64-hex content hashes, and a non-empty remote version
// for every stored baseline.
func (s *SnapshotV2) Validate() error {
	if s.SchemaVersion != SnapshotV2SchemaVersion {
		return fmt.Errorf("%w: schema %d", ErrSnapshotInvalid, s.SchemaVersion)
	}
	if !cloudsync.IsUUIDv4(s.VaultID) {
		return fmt.Errorf("%w: invalid vaultId %q", ErrSnapshotInvalid, s.VaultID)
	}
	if !cloudsync.IsUUIDv4(s.ReplicaID) {
		return fmt.Errorf("%w: invalid replicaId %q", ErrSnapshotInvalid, s.ReplicaID)
	}
	if !cloudsync.IsUUIDv4(s.RepositoryID) {
		return fmt.Errorf("%w: invalid repositoryId %q", ErrSnapshotInvalid, s.RepositoryID)
	}
	if !hex64Re.MatchString(s.ProviderProfile) {
		return fmt.Errorf("%w: invalid providerProfile %q", ErrSnapshotInvalid, s.ProviderProfile)
	}
	// A missing or null notes map is damage, not an empty replica: accepting it
	// would silently forget every baseline.
	if s.Notes == nil {
		return fmt.Errorf("%w: missing notes map", ErrSnapshotInvalid)
	}
	for syncID, e := range s.Notes {
		if !cloudsync.IsSyncID(syncID) {
			return fmt.Errorf("%w: invalid syncId %q", ErrSnapshotInvalid, syncID)
		}
		if !hex64Re.MatchString(e.ContentHash) {
			return fmt.Errorf("%w: note %s: invalid contentHash %q", ErrSnapshotInvalid, syncID, e.ContentHash)
		}
		if e.RemoteVersion == "" {
			return fmt.Errorf("%w: note %s: empty remoteVersion", ErrSnapshotInvalid, syncID)
		}
	}
	return nil
}

// Serialize returns the canonical snapshot bytes (sorted keys, trailing LF).
// The notes map and every note baseline are canonicalized so a content-only
// rewrite produces byte-identical output. There is no cursor.
func (s *SnapshotV2) Serialize() ([]byte, error) {
	notes := make(map[string]any, len(s.Notes))
	for syncID, e := range s.Notes {
		notes[syncID] = map[string]any{
			"contentHash":   e.ContentHash,
			"remoteVersion": e.RemoteVersion,
			"deleted":       e.Deleted,
		}
	}
	fields := map[string]any{
		"schemaVersion":   int64(s.SchemaVersion),
		"vaultId":         s.VaultID,
		"replicaId":       s.ReplicaID,
		"repositoryId":    s.RepositoryID,
		"providerProfile": s.ProviderProfile,
		"notes":           notes,
	}
	data, err := cloudsync.CanonicalBytes(fields)
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// ParseSnapshotV2 parses and validates raw snapshot bytes. The field set is
// strict: every required field must appear exactly once, unknown fields,
// duplicate fields, and trailing content are rejected, and the document must
// pass Validate. A schema-v1 prototype snapshot is reported as
// ErrUnsupportedPrototype before any strict field checking, so it is never
// mistaken for a corrupt v2 file.
func ParseSnapshotV2(data []byte) (*SnapshotV2, error) {
	if !utf8.Valid(data) {
		return nil, fmt.Errorf("%w: invalid utf-8", ErrSnapshotInvalid)
	}
	// Prototype classification: a decodable schema-v1 document is unsupported
	// prototype state (its "entities"/"cursor" shape would otherwise fail the
	// strict v2 field set as corruption).
	var probe struct {
		SchemaVersion int `json:"schemaVersion"`
	}
	if err := json.Unmarshal(data, &probe); err == nil && probe.SchemaVersion == 1 {
		return nil, fmt.Errorf("%w: %d", ErrUnsupportedPrototype, probe.SchemaVersion)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	tok, err := dec.Token()
	if err != nil {
		return nil, fmt.Errorf("%w: parse: %v", ErrSnapshotInvalid, err)
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, fmt.Errorf("%w: not an object", ErrSnapshotInvalid)
	}
	var s SnapshotV2
	seen := make(map[string]bool)
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("%w: parse: %v", ErrSnapshotInvalid, err)
		}
		key, ok := keyTok.(string)
		if !ok {
			return nil, fmt.Errorf("%w: non-string field name", ErrSnapshotInvalid)
		}
		if seen[key] {
			return nil, fmt.Errorf("%w: duplicate field %q", ErrSnapshotInvalid, key)
		}
		seen[key] = true
		switch key {
		case "schemaVersion":
			if err := dec.Decode(&s.SchemaVersion); err != nil {
				return nil, snapshotV2FieldError(key)
			}
		case "vaultId":
			if err := dec.Decode(&s.VaultID); err != nil {
				return nil, snapshotV2FieldError(key)
			}
		case "replicaId":
			if err := dec.Decode(&s.ReplicaID); err != nil {
				return nil, snapshotV2FieldError(key)
			}
		case "repositoryId":
			if err := dec.Decode(&s.RepositoryID); err != nil {
				return nil, snapshotV2FieldError(key)
			}
		case "providerProfile":
			if err := dec.Decode(&s.ProviderProfile); err != nil {
				return nil, snapshotV2FieldError(key)
			}
		case "notes":
			notes, err := decodeSnapshotV2Notes(dec)
			if err != nil {
				return nil, err
			}
			s.Notes = notes
		default:
			return nil, fmt.Errorf("%w: unknown field %q", ErrSnapshotInvalid, key)
		}
	}
	if _, err := dec.Token(); err != nil {
		return nil, fmt.Errorf("%w: parse: %v", ErrSnapshotInvalid, err) // closing brace
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("%w: trailing content after snapshot", ErrSnapshotInvalid)
		}
		return nil, fmt.Errorf("%w: parse trailing content: %v", ErrSnapshotInvalid, err)
	}
	for _, req := range []string{
		"schemaVersion", "vaultId", "replicaId", "repositoryId", "providerProfile", "notes",
	} {
		if !seen[req] {
			return nil, fmt.Errorf("%w: missing field %q", ErrSnapshotInvalid, req)
		}
	}
	if err := s.Validate(); err != nil {
		return nil, err
	}
	return &s, nil
}

func snapshotV2FieldError(key string) error {
	return fmt.Errorf("%w: field %q has the wrong type", ErrSnapshotInvalid, key)
}

// decodeSnapshotV2Notes decodes the notes object one entry at a time, rejecting
// duplicate note keys and unknown fields inside each baseline object.
func decodeSnapshotV2Notes(dec *json.Decoder) (map[string]SnapshotEntity, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, fmt.Errorf("%w: parse notes: %v", ErrSnapshotInvalid, err)
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, fmt.Errorf("%w: notes is not an object", ErrSnapshotInvalid)
	}
	out := make(map[string]SnapshotEntity)
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("%w: parse note key: %v", ErrSnapshotInvalid, err)
		}
		key, ok := keyTok.(string)
		if !ok {
			return nil, fmt.Errorf("%w: non-string note key", ErrSnapshotInvalid)
		}
		if _, dup := out[key]; dup {
			return nil, fmt.Errorf("%w: duplicate note %q", ErrSnapshotInvalid, key)
		}
		var e SnapshotEntity
		if err := decodeSnapshotV2Note(dec, &e); err != nil {
			return nil, err
		}
		out[key] = e
	}
	if _, err := dec.Token(); err != nil {
		return nil, fmt.Errorf("%w: parse notes: %v", ErrSnapshotInvalid, err) // closing brace
	}
	return out, nil
}

// decodeSnapshotV2Scalar decodes one scalar note-baseline field, rejecting JSON
// null explicitly (null deleted would otherwise silently become false).
func decodeSnapshotV2Scalar(dec *json.Decoder, key string, dst any) error {
	var raw json.RawMessage
	if err := dec.Decode(&raw); err != nil {
		return fmt.Errorf("%w: note field %q has the wrong type", ErrSnapshotInvalid, key)
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return fmt.Errorf("%w: note field %q must not be null", ErrSnapshotInvalid, key)
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		return fmt.Errorf("%w: note field %q has the wrong type", ErrSnapshotInvalid, key)
	}
	return nil
}

// decodeSnapshotV2Note decodes one note baseline with duplicate-field and
// unknown-field rejection. It reuses SnapshotEntity's shape, so the field rules
// are identical to the schema-v1 entity value.
func decodeSnapshotV2Note(dec *json.Decoder, e *SnapshotEntity) error {
	tok, err := dec.Token()
	if err != nil {
		return fmt.Errorf("%w: parse note: %v", ErrSnapshotInvalid, err)
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return fmt.Errorf("%w: note is not an object", ErrSnapshotInvalid)
	}
	seen := make(map[string]bool)
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return fmt.Errorf("%w: parse note field: %v", ErrSnapshotInvalid, err)
		}
		key, ok := keyTok.(string)
		if !ok {
			return fmt.Errorf("%w: non-string note field name", ErrSnapshotInvalid)
		}
		if seen[key] {
			return fmt.Errorf("%w: duplicate note field %q", ErrSnapshotInvalid, key)
		}
		seen[key] = true
		switch key {
		case "contentHash":
			if err := decodeSnapshotV2Scalar(dec, key, &e.ContentHash); err != nil {
				return err
			}
		case "remoteVersion":
			if err := decodeSnapshotV2Scalar(dec, key, &e.RemoteVersion); err != nil {
				return err
			}
		case "deleted":
			if err := decodeSnapshotV2Scalar(dec, key, &e.Deleted); err != nil {
				return err
			}
		default:
			return fmt.Errorf("%w: unknown note field %q", ErrSnapshotInvalid, key)
		}
	}
	if _, err := dec.Token(); err != nil {
		return fmt.Errorf("%w: parse note: %v", ErrSnapshotInvalid, err) // closing brace
	}
	for _, req := range []string{"contentHash", "remoteVersion", "deleted"} {
		if !seen[req] {
			return fmt.Errorf("%w: note missing field %q", ErrSnapshotInvalid, req)
		}
	}
	return nil
}

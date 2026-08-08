package syncstate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"unicode/utf8"

	"memodump/internal/cloudsync"
)

// SnapshotSchemaVersion is the current disposable device-snapshot schema.
const SnapshotSchemaVersion = 1

// SnapshotName is the per-replica snapshot file inside its state directory:
// <stateRoot>/<vaultId>/<replicaId>/state.json.
const SnapshotName = "state.json"

// ErrSnapshotInvalid reports a snapshot that fails structural validation.
// SnapshotStore.Load turns it into DiscardCorrupt; it is surfaced separately
// so direct ParseSnapshot callers can tell malformed JSON from bad values.
var ErrSnapshotInvalid = fmt.Errorf("invalid snapshot")

// SnapshotEntity is the complete canonical state of one entity at the last
// known-equal moment: the remote contentHash, the deleted bit, and the
// provider's opaque version/etag. Both the content hash and the deleted bit
// must match for two states to be equal.
type SnapshotEntity struct {
	ContentHash   string `json:"contentHash"`
	RemoteVersion string `json:"remoteVersion"`
	Deleted       bool   `json:"deleted"`
}

// Snapshot is one replica's disposable device snapshot: the last state this
// replica knows was equal locally and remotely. It is a cache, not a log — it
// is atomically replaced at most once per cycle and never appended to. The
// entity map is keyed by Sync ID.
type Snapshot struct {
	SchemaVersion   int                       `json:"schemaVersion"`
	VaultID         string                    `json:"vaultId"`
	ReplicaID       string                    `json:"replicaId"`
	RepositoryID    string                    `json:"repositoryId"`
	ProviderProfile string                    `json:"providerProfile"`
	Entities        map[string]SnapshotEntity `json:"entities"`
	Cursor          string                    `json:"cursor,omitempty"`
}

// hex64Re matches a lowercase 64-hex digest, used for both provider-profile
// fingerprints and content hashes.
var hex64Re = regexp.MustCompile(`^[0-9a-f]{64}$`)

// Validate checks the snapshot's invariants before it is trusted or persisted:
// the exact schema version, UUID-v4 Vault/Replica/Repository IDs, a lowercase
// 64-hex provider fingerprint, a non-null entity map, Sync IDs passing
// IsSyncID, lowercase 64-hex content hashes, and a non-empty remote version for
// every stored baseline. The cursor is opaque and optional.
func (s *Snapshot) Validate() error {
	if s.SchemaVersion != SnapshotSchemaVersion {
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
	// A missing or null entity map is damage, not an empty replica: accepting it
	// would silently forget every baseline.
	if s.Entities == nil {
		return fmt.Errorf("%w: missing entities map", ErrSnapshotInvalid)
	}
	for syncID, e := range s.Entities {
		if !cloudsync.IsSyncID(syncID) {
			return fmt.Errorf("%w: invalid syncId %q", ErrSnapshotInvalid, syncID)
		}
		if !hex64Re.MatchString(e.ContentHash) {
			return fmt.Errorf("%w: entity %s: invalid contentHash %q", ErrSnapshotInvalid, syncID, e.ContentHash)
		}
		if e.RemoteVersion == "" {
			return fmt.Errorf("%w: entity %s: empty remoteVersion", ErrSnapshotInvalid, syncID)
		}
	}
	return nil
}

// Serialize returns the canonical snapshot bytes (sorted keys, trailing LF).
// The entities map and every entity object are canonicalized so a content-only
// rewrite produces byte-identical output.
func (s *Snapshot) Serialize() ([]byte, error) {
	entities := make(map[string]any, len(s.Entities))
	for syncID, e := range s.Entities {
		entities[syncID] = map[string]any{
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
		"entities":        entities,
	}
	if s.Cursor != "" {
		fields["cursor"] = s.Cursor
	}
	data, err := cloudsync.CanonicalBytes(fields)
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// ParseSnapshot parses and validates raw snapshot bytes. The field set is
// strict: every required field must appear exactly once, unknown fields,
// duplicate fields, and trailing content are rejected, and the document must
// pass Validate. A missing field is rejected so a snapshot can never silently
// decode with a zeroed identity.
func ParseSnapshot(data []byte) (*Snapshot, error) {
	if !utf8.Valid(data) {
		return nil, fmt.Errorf("%w: invalid utf-8", ErrSnapshotInvalid)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	tok, err := dec.Token()
	if err != nil {
		return nil, fmt.Errorf("%w: parse: %v", ErrSnapshotInvalid, err)
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, fmt.Errorf("%w: not an object", ErrSnapshotInvalid)
	}
	var s Snapshot
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
				return nil, fieldError(key)
			}
		case "vaultId":
			if err := dec.Decode(&s.VaultID); err != nil {
				return nil, fieldError(key)
			}
		case "replicaId":
			if err := dec.Decode(&s.ReplicaID); err != nil {
				return nil, fieldError(key)
			}
		case "repositoryId":
			if err := dec.Decode(&s.RepositoryID); err != nil {
				return nil, fieldError(key)
			}
		case "providerProfile":
			if err := dec.Decode(&s.ProviderProfile); err != nil {
				return nil, fieldError(key)
			}
		case "cursor":
			if err := dec.Decode(&s.Cursor); err != nil {
				return nil, fieldError(key)
			}
		case "entities":
			ents, err := decodeSnapshotEntities(dec)
			if err != nil {
				return nil, err
			}
			s.Entities = ents
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
		"schemaVersion", "vaultId", "replicaId", "repositoryId", "providerProfile", "entities",
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

func fieldError(key string) error {
	return fmt.Errorf("%w: field %q has the wrong type", ErrSnapshotInvalid, key)
}

// decodeSnapshotEntities decodes the entities object one entry at a time,
// rejecting duplicate entity keys and unknown fields inside each entity object.
func decodeSnapshotEntities(dec *json.Decoder) (map[string]SnapshotEntity, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, fmt.Errorf("%w: parse entities: %v", ErrSnapshotInvalid, err)
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, fmt.Errorf("%w: entities is not an object", ErrSnapshotInvalid)
	}
	out := make(map[string]SnapshotEntity)
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("%w: parse entity key: %v", ErrSnapshotInvalid, err)
		}
		key, ok := keyTok.(string)
		if !ok {
			return nil, fmt.Errorf("%w: non-string entity key", ErrSnapshotInvalid)
		}
		if _, dup := out[key]; dup {
			return nil, fmt.Errorf("%w: duplicate entity %q", ErrSnapshotInvalid, key)
		}
		var e SnapshotEntity
		if err := decodeSnapshotEntity(dec, &e); err != nil {
			return nil, err
		}
		out[key] = e
	}
	if _, err := dec.Token(); err != nil {
		return nil, fmt.Errorf("%w: parse entities: %v", ErrSnapshotInvalid, err) // closing brace
	}
	return out, nil
}

// decodeSnapshotEntity decodes one entity object with duplicate-field and
// unknown-field rejection.
func decodeSnapshotEntity(dec *json.Decoder, e *SnapshotEntity) error {
	tok, err := dec.Token()
	if err != nil {
		return fmt.Errorf("%w: parse entity: %v", ErrSnapshotInvalid, err)
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return fmt.Errorf("%w: entity is not an object", ErrSnapshotInvalid)
	}
	seen := make(map[string]bool)
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return fmt.Errorf("%w: parse entity field: %v", ErrSnapshotInvalid, err)
		}
		key, ok := keyTok.(string)
		if !ok {
			return fmt.Errorf("%w: non-string entity field name", ErrSnapshotInvalid)
		}
		if seen[key] {
			return fmt.Errorf("%w: duplicate entity field %q", ErrSnapshotInvalid, key)
		}
		seen[key] = true
		switch key {
		case "contentHash":
			if err := dec.Decode(&e.ContentHash); err != nil {
				return fmt.Errorf("%w: entity field %q has the wrong type", ErrSnapshotInvalid, key)
			}
		case "remoteVersion":
			if err := dec.Decode(&e.RemoteVersion); err != nil {
				return fmt.Errorf("%w: entity field %q has the wrong type", ErrSnapshotInvalid, key)
			}
		case "deleted":
			if err := dec.Decode(&e.Deleted); err != nil {
				return fmt.Errorf("%w: entity field %q has the wrong type", ErrSnapshotInvalid, key)
			}
		default:
			return fmt.Errorf("%w: unknown entity field %q", ErrSnapshotInvalid, key)
		}
	}
	if _, err := dec.Token(); err != nil {
		return fmt.Errorf("%w: parse entity: %v", ErrSnapshotInvalid, err) // closing brace
	}
	for _, req := range []string{"contentHash", "remoteVersion", "deleted"} {
		if !seen[req] {
			return fmt.Errorf("%w: entity missing field %q", ErrSnapshotInvalid, req)
		}
	}
	return nil
}

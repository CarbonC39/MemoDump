package syncindex

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/google/uuid"

	"memodump/internal/cloudsync"
	"memodump/internal/vaultfs"
)

const (
	// SchemaVersion is the current sync-index schema.
	SchemaVersion = 1
	// DirName is the reserved vault metadata directory. The literal lives in
	// vaultfs so every repository path shares one source of truth.
	DirName = vaultfs.SyncMetadataDir
	// IndexName is the portable sync index file.
	IndexName = "sync-index.json"
	// BackupName is the last-known-good copy.
	BackupName = "sync-index.json.bak"
)

// Entity is one entry in the portable index: a Sync ID mapped to a vault path.
// The index deliberately carries no content hashes, credentials, provider
// URLs, cursors, remote versions, or device state — it only pins identity.
type Entity struct {
	Kind string `json:"kind"` // "note" or "folder"
	Path string `json:"path"`
}

// Index is the portable, atomically-replaced sync-index document.
type Index struct {
	SchemaVersion int               `json:"schemaVersion"`
	VaultID       string            `json:"vaultId"`
	Entities      map[string]Entity `json:"entities"`
}

// New returns an empty index for a vault.
func New(vaultID string) *Index {
	return &Index{
		SchemaVersion: SchemaVersion,
		VaultID:       vaultID,
		Entities:      make(map[string]Entity),
	}
}

// NewVaultID returns a fresh version-4 UUID for a vault that is enabling sync.
func NewVaultID() string { return uuid.NewString() }

// validate checks the structural invariants of the index before it is trusted:
// the schema, the vault ID, the Sync ID keys, entity kinds, and path safety.
func (idx *Index) validate() error {
	if idx.SchemaVersion != SchemaVersion {
		return fmt.Errorf("unsupported sync-index schema %d", idx.SchemaVersion)
	}
	if !cloudsync.IsUUIDv4(idx.VaultID) {
		return fmt.Errorf("invalid vaultId %q", idx.VaultID)
	}
	// A missing or null entities map is damage, not an empty vault: accepting it
	// would silently reassign every Sync ID on the next enable. Load must treat
	// it as corrupt and fall back to the backup.
	if idx.Entities == nil {
		return fmt.Errorf("missing entities map")
	}
	seenPaths := make(map[string]string, len(idx.Entities))
	for syncID, e := range idx.Entities {
		if !cloudsync.IsUUIDv4(syncID) {
			return fmt.Errorf("invalid syncId %q", syncID)
		}
		if e.Kind != cloudsync.KindNote && e.Kind != cloudsync.KindFolder {
			return fmt.Errorf("entity %s: bad kind %q", syncID, e.Kind)
		}
		if !validVaultPath(e.Path) {
			return fmt.Errorf("entity %s: unsafe path %q", syncID, e.Path)
		}
		if prev, ok := seenPaths[e.Path]; ok {
			return fmt.Errorf("duplicate path %q (syncIds %s and %s)", e.Path, prev, syncID)
		}
		seenPaths[e.Path] = syncID
	}
	return nil
}

// validPath reports whether a slash-relative vault path is safe to materialize:
// no absolute path, no traversal segments, no path separators in a leaf name.
func validPath(p string) bool {
	if p == "" || strings.HasPrefix(p, "/") || strings.HasPrefix(p, `\`) {
		return false
	}
	if strings.Contains(p, "\\") {
		return false
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
	}
	return true
}

// validVaultPath is validPath plus the reserved-directory rule: a sync entity
// can never live inside the image vault or the sync metadata directory. It
// shares vaultfs.ContainsReservedSegment so the index and the repository agree
// on what a vault path is.
func validVaultPath(p string) bool {
	return validPath(p) && !vaultfs.ContainsReservedSegment(p)
}

// Serialize returns the canonical portable-index bytes (sorted keys, trailing
// LF) so content-only saves never rewrite it.
func (idx *Index) Serialize() ([]byte, error) {
	entities := make(map[string]Entity, len(idx.Entities))
	ids := make([]string, 0, len(idx.Entities))
	for id, e := range idx.Entities {
		ids = append(ids, id)
		entities[id] = e
	}
	sort.Strings(ids)

	var sb strings.Builder
	sb.WriteString(`{"schemaVersion":`)
	sb.WriteString(fmt.Sprintf("%d", idx.SchemaVersion))
	sb.WriteString(`,"vaultId":`)
	writeJSONString(&sb, idx.VaultID)
	sb.WriteString(`,"entities":{`)
	for i, id := range ids {
		if i > 0 {
			sb.WriteByte(',')
		}
		writeJSONString(&sb, id)
		sb.WriteString(`:{`)
		sb.WriteString(`"kind":`)
		writeJSONString(&sb, entities[id].Kind)
		sb.WriteString(`,"path":`)
		writeJSONString(&sb, entities[id].Path)
		sb.WriteByte('}')
	}
	sb.WriteString("}}")
	return []byte(sb.String()), nil
}

func writeJSONString(sb *strings.Builder, s string) {
	sb.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			sb.WriteString(`\"`)
		case '\\':
			sb.WriteString(`\\`)
		case '\n':
			sb.WriteString(`\n`)
		case '\r':
			sb.WriteString(`\r`)
		case '\t':
			sb.WriteString(`\t`)
		default:
			if r < 0x20 {
				fmt.Fprintf(sb, `\u%04x`, r)
			} else {
				sb.WriteRune(r)
			}
		}
	}
	sb.WriteByte('"')
}

// parseIndex decodes and validates a serialized index. Unknown fields and
// trailing content are rejected so a v1 record stays canonical. A missing or
// null entities map fails validation, so Load treats the document as corrupt
// and falls back to the backup instead of silently losing every Sync ID.
func parseIndex(data []byte) (*Index, error) {
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	var idx Index
	if err := dec.Decode(&idx); err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	// Reject anything after the document other than whitespace.
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("trailing content after sync-index")
	}
	if err := idx.validate(); err != nil {
		return nil, err
	}
	return &idx, nil
}

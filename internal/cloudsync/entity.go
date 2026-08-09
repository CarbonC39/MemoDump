package cloudsync

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// Shared wire-contract helpers for the note record, repository descriptor, and
// path/identity validation. The prototype folder-entity record was removed with
// the folder planner; these constants and validators remain because the v2 note
// contract, the repo descriptor, and the deterministic identity helpers build
// on them.

const (
	// MaxEntityBytes caps untrusted remote records before materialization.
	MaxEntityBytes = 1 << 20 // 1 MiB
	// KindNote and KindFolder classify filesystem observations in the vault
	// scanner. Folders are not synced entities, but the scanner still labels
	// its observations with them.
	KindNote   = "note"
	KindFolder = "folder"
)

var (
	// ErrInvalidSchema reports a record whose schema is newer or unknown.
	ErrInvalidSchema = fmt.Errorf("unsupported schema version")
	// ErrOversized reports a record exceeding MaxEntityBytes.
	ErrOversized = fmt.Errorf("entity exceeds size limit")
	// ErrInvalidEntity reports a record failing structural validation.
	ErrInvalidEntity = fmt.Errorf("invalid entity")
)

var contentHashRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

// MaxSafeInteger is JavaScript's Number.MAX_SAFE_INTEGER (2^53-1). The TypeScript
// side validates against it, so the Go parser caps the same fields to keep the
// rejection behavior identical.
const MaxSafeInteger int64 = 1<<53 - 1

var uuidV4Re = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

var uuidV5Re = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-5[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// IsUUIDv4 reports whether s is a syntactically valid version-4 UUID.
func IsUUIDv4(s string) bool {
	return uuidV4Re.MatchString(s)
}

// IsSyncID reports whether s is a valid note Sync ID: UUID v4 for ordinary
// notes or UUID v5 for deterministic conflict copies. Vault, Replica, Device,
// and Repository IDs must remain version-4 only (IsUUIDv4).
func IsSyncID(s string) bool {
	return uuidV4Re.MatchString(s) || uuidV5Re.MatchString(s)
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

// requireString reads a required string field from a decoded JSON object.
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

// fieldTypeError reports a decoded JSON field with the wrong type.
func fieldTypeError(key string) error {
	return fmt.Errorf("%w: field %q has the wrong type", ErrInvalidEntity, key)
}

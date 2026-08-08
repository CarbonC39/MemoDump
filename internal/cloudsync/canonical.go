package cloudsync

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// canonicalBytes serializes an object with keys in sorted order and a
// deterministic string escaping (quote, backslash, \n \r \t, and control
// characters as \uXXXX; everything else emitted as literal UTF-8). This is the
// shared wire contract: the Go and TypeScript implementations must produce
// byte-identical output, verified against the same fixtures.
func canonicalBytes(v map[string]any) ([]byte, error) {
	var sb strings.Builder
	if err := writeCanonicalObject(&sb, v); err != nil {
		return nil, err
	}
	return []byte(sb.String()), nil
}

// CanonicalBytes is the exported form of canonicalBytes. It is used by the
// disposable device snapshot (internal/syncstate) so one canonical JSON
// implementation serves both the wire contract and durable local state.
func CanonicalBytes(v map[string]any) ([]byte, error) { return canonicalBytes(v) }

func writeCanonicalObject(sb *strings.Builder, v map[string]any) error {
	keys := make([]string, 0, len(v))
	for k := range v {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	sb.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			sb.WriteByte(',')
		}
		writeCanonicalString(sb, k)
		sb.WriteByte(':')
		if err := writeCanonicalValue(sb, v[k]); err != nil {
			return err
		}
	}
	sb.WriteByte('}')
	return nil
}

func writeCanonicalValue(sb *strings.Builder, v any) error {
	switch val := v.(type) {
	case nil:
		sb.WriteString("null")
	case string:
		writeCanonicalString(sb, val)
	case bool:
		if val {
			sb.WriteString("true")
		} else {
			sb.WriteString("false")
		}
	case int64:
		sb.WriteString(strconv.FormatInt(val, 10))
	case int:
		sb.WriteString(strconv.Itoa(val))
	case json.Number:
		// Numbers decoded with a UseNumber decoder arrive here; emit their
		// literal form so integer-valued fields never become float64.
		sb.WriteString(val.String())
	case json.RawMessage:
		// Raw bytes are emitted verbatim; callers pass already-canonical bytes.
		sb.Write(val)
	case []any:
		sb.WriteByte('[')
		for i, item := range val {
			if i > 0 {
				sb.WriteByte(',')
			}
			if err := writeCanonicalValue(sb, item); err != nil {
				return err
			}
		}
		sb.WriteByte(']')
	case map[string]any:
		return writeCanonicalObject(sb, val)
	default:
		return fmt.Errorf("cloudsync: cannot canonicalize %T", v)
	}
	return nil
}

func writeCanonicalString(sb *strings.Builder, s string) {
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

// ContentHash is the canonical digest over kind, parentId, name, and markdown,
// as defined by the shared golden fixtures. It is the content identity of an
// entity, independent of the provider's ETag/version.
func ContentHash(kind, parentId, name, markdown string) string {
	data, err := canonicalBytes(map[string]any{
		"kind":     kind,
		"parentId": parentId,
		"name":     name,
		"markdown": markdown,
	})
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// StateHash is the canonical digest of an entity's complete state: the tuple
// (contentHash, deleted). Two states are equal only when both the content hash
// and the deleted bit match, and this is the digest used for the disposable
// device snapshot and for deterministic conflict derivation. It reuses the same
// canonical JSON writer so Go and TypeScript produce byte-identical output.
func StateHash(contentHash string, deleted bool) string {
	data, err := canonicalBytes(map[string]any{
		"contentHash": contentHash,
		"deleted":     deleted,
	})
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

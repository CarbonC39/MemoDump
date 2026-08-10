package cloudsync

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"unicode/utf8"
)

// RepositoryFormatVersion is the current repo.json format.
const RepositoryFormatVersion = 1

var allowedRepoFields = map[string]bool{
	"formatVersion": true, "repositoryId": true, "createdAt": true, "minimumClientVersion": true,
}

// RepositoryDescriptor is the small repository root object (repo.json). It
// changes only during explicit format upgrades.
type RepositoryDescriptor struct {
	FormatVersion        int    `json:"formatVersion"`
	RepositoryID         string `json:"repositoryId"`
	CreatedAt            int64  `json:"createdAt"`
	MinimumClientVersion string `json:"minimumClientVersion"`
}

// Serialize returns the canonical repo.json bytes (sorted keys, trailing LF).
func (d *RepositoryDescriptor) Serialize() ([]byte, error) {
	data, err := canonicalBytes(map[string]any{
		"formatVersion":        int64(d.FormatVersion),
		"repositoryId":         d.RepositoryID,
		"createdAt":            int64(d.CreatedAt),
		"minimumClientVersion": d.MinimumClientVersion,
	})
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// ParseRepositoryDescriptor parses and validates repo.json. Every field is
// required, unknown fields, duplicate fields, and trailing content are
// rejected, and unknown/newer formats are rejected. Parsing is token-based so a
// descriptor carrying the same key twice is rejected instead of silently
// keeping the last value.
func ParseRepositoryDescriptor(data []byte) (*RepositoryDescriptor, error) {
	if len(data) > MaxEntityBytes {
		return nil, ErrOversized
	}
	if !utf8.Valid(data) {
		return nil, fmt.Errorf("%w: invalid utf-8", ErrInvalidEntity)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	tok, err := dec.Token()
	if err != nil {
		return nil, fmt.Errorf("%w: parse: %v", ErrInvalidEntity, err)
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, fmt.Errorf("%w: not an object", ErrInvalidEntity)
	}
	var d RepositoryDescriptor
	seen := make(map[string]bool)
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("%w: parse: %v", ErrInvalidEntity, err)
		}
		key, ok := keyTok.(string)
		if !ok {
			return nil, fmt.Errorf("%w: non-string field name", ErrInvalidEntity)
		}
		if seen[key] {
			return nil, fmt.Errorf("%w: duplicate field %q", ErrInvalidEntity, key)
		}
		if !allowedRepoFields[key] {
			return nil, fmt.Errorf("%w: unknown field %q", ErrInvalidEntity, key)
		}
		seen[key] = true
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, fmt.Errorf("%w: field %q has the wrong type", ErrInvalidEntity, key)
		}
		switch key {
		case "formatVersion":
			if err := decodeRepoScalar(raw, key, &d.FormatVersion); err != nil {
				return nil, err
			}
		case "repositoryId":
			if err := decodeRepoScalar(raw, key, &d.RepositoryID); err != nil {
				return nil, err
			}
		case "createdAt":
			if err := decodeRepoScalar(raw, key, &d.CreatedAt); err != nil {
				return nil, err
			}
		case "minimumClientVersion":
			if err := decodeRepoScalar(raw, key, &d.MinimumClientVersion); err != nil {
				return nil, err
			}
		}
	}
	if _, err := dec.Token(); err != nil {
		return nil, fmt.Errorf("%w: parse: %v", ErrInvalidEntity, err) // closing brace
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("%w: trailing content after record", ErrInvalidEntity)
	}
	for _, req := range []string{"formatVersion", "repositoryId", "createdAt", "minimumClientVersion"} {
		if !seen[req] {
			return nil, fmt.Errorf("%w: missing field %q", ErrInvalidEntity, req)
		}
	}
	if d.FormatVersion != RepositoryFormatVersion {
		return nil, fmt.Errorf("%w: repository format %d", ErrInvalidSchema, d.FormatVersion)
	}
	if !IsUUIDv4(d.RepositoryID) {
		return nil, fmt.Errorf("%w: bad repositoryId %q", ErrInvalidEntity, d.RepositoryID)
	}
	if d.CreatedAt <= 0 || d.CreatedAt > MaxSafeInteger {
		return nil, fmt.Errorf("%w: bad createdAt %d", ErrInvalidEntity, d.CreatedAt)
	}
	if d.MinimumClientVersion == "" {
		return nil, fmt.Errorf("%w: empty minimumClientVersion", ErrInvalidEntity)
	}
	return &d, nil
}

// decodeRepoScalar decodes one scalar field value, rejecting JSON null
// explicitly: Go's json.Decode would otherwise accept null and keep the zero
// value (null repositoryId becoming "", null createdAt becoming 0), silently
// reinterpreting an ambiguous record.
func decodeRepoScalar(raw json.RawMessage, key string, dst any) error {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return fmt.Errorf("%w: field %q must not be null", ErrInvalidEntity, key)
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		return fmt.Errorf("%w: field %q has the wrong type", ErrInvalidEntity, key)
	}
	return nil
}

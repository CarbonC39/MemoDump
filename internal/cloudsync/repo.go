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
// required, unknown fields and trailing content are rejected, and unknown/newer
// formats are rejected.
func ParseRepositoryDescriptor(data []byte) (*RepositoryDescriptor, error) {
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
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("%w: trailing content after record", ErrInvalidEntity)
	}
	for k := range fields {
		if !allowedRepoFields[k] {
			return nil, fmt.Errorf("%w: unknown field %q", ErrInvalidEntity, k)
		}
	}
	for _, k := range []string{"formatVersion", "repositoryId", "createdAt", "minimumClientVersion"} {
		if _, ok := fields[k]; !ok {
			return nil, fmt.Errorf("%w: missing field %q", ErrInvalidEntity, k)
		}
	}
	var d RepositoryDescriptor
	if err := json.Unmarshal(fields["formatVersion"], &d.FormatVersion); err != nil {
		return nil, fieldTypeError("formatVersion")
	}
	if d.FormatVersion != RepositoryFormatVersion {
		return nil, fmt.Errorf("%w: repository format %d", ErrInvalidSchema, d.FormatVersion)
	}
	repoID, err := requireString(fields, "repositoryId")
	if err != nil {
		return nil, err
	}
	d.RepositoryID = repoID
	if err := json.Unmarshal(fields["createdAt"], &d.CreatedAt); err != nil {
		return nil, fieldTypeError("createdAt")
	}
	minVer, err := requireString(fields, "minimumClientVersion")
	if err != nil {
		return nil, err
	}
	d.MinimumClientVersion = minVer
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

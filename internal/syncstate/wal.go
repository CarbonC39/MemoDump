package syncstate

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"sort"

	"memodump/internal/cloudsync"
)

const (
	// WalSchemaVersion is the current device-state WAL record schema.
	WalSchemaVersion = 1
	// walOpPut and walOpDelete are the two durable state transitions.
	walOpPut    = "put"
	walOpDelete = "delete"
)

// walRecord is one NDJSON line of the device-state WAL. The checksum covers
// everything except itself — schemaVersion, seq, op, and payload — canonicalized
// through cloudsync.CanonicalBytes, so any torn or corrupted record is detected.
type walRecord struct {
	SchemaVersion int             `json:"schemaVersion"`
	Seq           int64           `json:"seq"`
	Op            string          `json:"op"`
	Payload       json.RawMessage `json:"payload"`
	Checksum      string          `json:"checksum"`
}

// Serialize returns the canonical NDJSON line for the record (trailing LF).
func (r *walRecord) Serialize() ([]byte, error) {
	sum, err := r.checksum()
	if err != nil {
		return nil, err
	}
	data, err := cloudsync.CanonicalBytes(map[string]any{
		"schemaVersion": int64(r.SchemaVersion),
		"seq":           r.Seq,
		"op":            r.Op,
		"payload":       r.Payload,
		"checksum":      sum,
	})
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

func (r *walRecord) checksum() (string, error) {
	in, err := cloudsync.CanonicalBytes(map[string]any{
		"schemaVersion": int64(r.SchemaVersion),
		"seq":           r.Seq,
		"op":            r.Op,
		"payload":       r.Payload,
	})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(in)
	return hex.EncodeToString(sum[:]), nil
}

// parseWalRecord decodes and verifies one complete NDJSON line. A schema
// mismatch, unknown op, non-positive seq, unknown field, trailing content, or
// checksum mismatch are all corruption.
func parseWalRecord(line []byte) (*walRecord, error) {
	var r walRecord
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&r); err != nil {
		return nil, fmt.Errorf("parse wal record: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("trailing content after wal record")
	}
	if r.SchemaVersion != WalSchemaVersion {
		return nil, fmt.Errorf("unsupported wal schema %d", r.SchemaVersion)
	}
	if r.Seq <= 0 {
		return nil, fmt.Errorf("non-positive wal seq %d", r.Seq)
	}
	if r.Op != walOpPut && r.Op != walOpDelete {
		return nil, fmt.Errorf("unknown wal op %q", r.Op)
	}
	want, err := r.checksum()
	if err != nil {
		return nil, err
	}
	if r.Checksum != want {
		return nil, fmt.Errorf("wal checksum mismatch")
	}
	return &r, nil
}

// walPayload is the decoded payload of a put/delete record.
type walPayload struct {
	Key   string          `json:"key"`
	Value json.RawMessage `json:"value,omitempty"`
}

// putPayload builds the canonical payload for a put of value under key.
func putPayload(key string, value any) (json.RawMessage, error) {
	data, err := cloudsync.CanonicalBytes(map[string]any{"key": key, "value": value})
	if err != nil {
		return nil, err
	}
	return json.RawMessage(data), nil
}

// deletePayload builds the canonical payload for deleting key.
func deletePayload(key string) (json.RawMessage, error) {
	data, err := cloudsync.CanonicalBytes(map[string]any{"key": key})
	if err != nil {
		return nil, err
	}
	return json.RawMessage(data), nil
}

func decodePayload(raw json.RawMessage) (walPayload, error) {
	var p walPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return p, err
	}
	if p.Key == "" {
		return p, fmt.Errorf("wal payload missing key")
	}
	return p, nil
}

// --- snapshot ----------------------------------------------------------------

// snapshot is the compacted device state: the last applied sequence plus the
// full state map, with a canonical checksum so corruption is detected even when
// the JSON still parses.
type snapshot struct {
	SchemaVersion  int                        `json:"schemaVersion"`
	LastAppliedSeq int64                      `json:"lastAppliedSeq"`
	Data           map[string]json.RawMessage `json:"data"`
	Checksum       string                     `json:"checksum"`
}

// writeSnapshotDoc writes the canonical snapshot document. When sum is empty
// the checksum field is omitted; that form is the checksum input.
func writeSnapshotDoc(w io.Writer, sum string, lastApplied int64, data map[string]json.RawMessage) error {
	if sum != "" {
		if _, err := fmt.Fprintf(w, `{"checksum":"%s","data":{`, sum); err != nil {
			return err
		}
	} else if _, err := io.WriteString(w, `{"data":{`); err != nil {
		return err
	}
	keys := make([]string, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for i, k := range keys {
		if i > 0 {
			if _, err := io.WriteString(w, ","); err != nil {
				return err
			}
		}
		if err := writeJSONString(w, k); err != nil {
			return err
		}
		if _, err := io.WriteString(w, ":"); err != nil {
			return err
		}
		if _, err := w.Write(data[k]); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(w, `},"lastAppliedSeq":%d,"schemaVersion":1}`, lastApplied); err != nil {
		return err
	}
	return nil
}

// snapshotChecksum returns the hex digest over the snapshot document without
// the checksum field.
func snapshotChecksum(lastApplied int64, data map[string]json.RawMessage) (string, error) {
	h := sha256.New()
	if err := writeSnapshotDoc(h, "", lastApplied, data); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// writeSnapshotFile streams the snapshot document to w (data emitted through a
// buffered encoder, never a whole-document string) and returns the checksum.
func writeSnapshotFile(w io.Writer, lastApplied int64, data map[string]json.RawMessage) (string, error) {
	sum, err := snapshotChecksum(lastApplied, data)
	if err != nil {
		return "", err
	}
	bw := bufio.NewWriter(w)
	if err := writeSnapshotDoc(bw, sum, lastApplied, data); err != nil {
		return "", err
	}
	if _, err := bw.WriteString("\n"); err != nil {
		return "", err
	}
	if err := bw.Flush(); err != nil {
		return "", err
	}
	return sum, nil
}

// parseSnapshot decodes and verifies state.snapshot.json. A corrupt or
// checksum-mismatched snapshot is an error; the caller stops recovery.
func parseSnapshot(data []byte) (*snapshot, error) {
	var s snapshot
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		return nil, fmt.Errorf("parse snapshot: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("trailing content after snapshot")
	}
	if s.SchemaVersion != WalSchemaVersion {
		return nil, fmt.Errorf("unsupported snapshot schema %d", s.SchemaVersion)
	}
	if s.LastAppliedSeq < 0 {
		return nil, fmt.Errorf("negative lastAppliedSeq %d", s.LastAppliedSeq)
	}
	want, err := snapshotChecksum(s.LastAppliedSeq, s.Data)
	if err != nil {
		return nil, err
	}
	if s.Checksum != want {
		return nil, fmt.Errorf("snapshot checksum mismatch")
	}
	if s.Data == nil {
		s.Data = make(map[string]json.RawMessage)
	}
	return &s, nil
}

// writeJSONString emits an escaped JSON string (same rules as the canonical
// wire format).
func writeJSONString(w io.Writer, s string) error {
	if _, err := io.WriteString(w, `"`); err != nil {
		return err
	}
	for _, r := range s {
		switch r {
		case '"':
			if _, err := io.WriteString(w, `\"`); err != nil {
				return err
			}
		case '\\':
			if _, err := io.WriteString(w, `\\`); err != nil {
				return err
			}
		case '\n':
			if _, err := io.WriteString(w, `\n`); err != nil {
				return err
			}
		case '\r':
			if _, err := io.WriteString(w, `\r`); err != nil {
				return err
			}
		case '\t':
			if _, err := io.WriteString(w, `\t`); err != nil {
				return err
			}
		default:
			if r < 0x20 {
				if _, err := fmt.Fprintf(w, `\u%04x`, r); err != nil {
					return err
				}
			} else if _, err := io.WriteString(w, string(r)); err != nil {
				return err
			}
		}
	}
	_, err := io.WriteString(w, `"`)
	return err
}

package syncstate

import (
	"bufio"
	"bytes"
	"context"
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

// putPayload builds the canonical payload for a put of value under key. The
// result is validated as JSON so a caller error (for example an invalid
// json.RawMessage value) is rejected here, before any record is serialized or
// written to the WAL — a caller error must never touch durable state.
func putPayload(key string, value any) (json.RawMessage, error) {
	data, err := cloudsync.CanonicalBytes(map[string]any{"key": key, "value": value})
	if err != nil {
		return nil, err
	}
	if !json.Valid(data) {
		return nil, fmt.Errorf("wal payload for key %q is not valid JSON", key)
	}
	return json.RawMessage(data), nil
}

// deletePayload builds the canonical payload for deleting key.
func deletePayload(key string) (json.RawMessage, error) {
	data, err := cloudsync.CanonicalBytes(map[string]any{"key": key})
	if err != nil {
		return nil, err
	}
	if !json.Valid(data) {
		return nil, fmt.Errorf("wal payload for key %q is not valid JSON", key)
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
// full state map. It carries no checksum (spec §5.5 defines it as the compacted
// state plus lastAppliedSeq; the WAL records carry the checksums, and snapshot
// corruption is caught by strict JSON parsing). Keeping the snapshot
// checksum-free is what lets it stream both ways without a whole-document pass.
type snapshot struct {
	SchemaVersion  int                        `json:"schemaVersion"`
	LastAppliedSeq int64                      `json:"lastAppliedSeq"`
	Data           map[string]json.RawMessage `json:"data"`
}

// writeSnapshotDoc streams the canonical snapshot document, checking ctx per
// data key so a cancelled compactor stops during the long encode.
func writeSnapshotDoc(ctx context.Context, w io.Writer, lastApplied int64, data map[string]json.RawMessage) error {
	if _, err := io.WriteString(w, `{"data":{`); err != nil {
		return err
	}
	keys := make([]string, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for i, k := range keys {
		if err := ctx.Err(); err != nil {
			return err
		}
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

// writeSnapshotFile streams the snapshot document to w (data emitted through a
// buffered encoder, never a whole-document string) and returns the byte size.
func writeSnapshotFile(ctx context.Context, w io.Writer, lastApplied int64, data map[string]json.RawMessage) (int64, error) {
	bw := bufio.NewWriter(w)
	cw := &countingWriter{w: bw}
	if err := writeSnapshotDoc(ctx, cw, lastApplied, data); err != nil {
		return 0, err
	}
	if _, err := cw.Write([]byte("\n")); err != nil {
		return 0, err
	}
	if err := bw.Flush(); err != nil {
		return 0, err
	}
	return cw.n, nil
}

// readSnapshot streams state.snapshot.json from a file with a token-based
// decoder, checking ctx between fields, so a large snapshot is decoded
// incrementally and a cancelled compactor stops at a field boundary. Unknown
// fields, duplicate fields, trailing content, a bad schema, and a negative
// watermark are all corruption.
func readSnapshot(ctx context.Context, wio walIO, path string) (*snapshot, error) {
	f, err := wio.OpenRead(path)
	if err != nil {
		return nil, err // propagates os.ErrNotExist
	}
	defer f.Close()
	dec := json.NewDecoder(f)

	tok, err := dec.Token()
	if err != nil {
		return nil, fmt.Errorf("parse snapshot: %w", err)
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, fmt.Errorf("parse snapshot: not an object")
	}
	var s snapshot
	seen := make(map[string]bool)
	for dec.More() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		keyTok, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("parse snapshot: %w", err)
		}
		key, ok := keyTok.(string)
		if !ok {
			return nil, fmt.Errorf("parse snapshot: non-string key")
		}
		if seen[key] {
			return nil, fmt.Errorf("parse snapshot: duplicate field %q", key)
		}
		seen[key] = true
		switch key {
		case "data":
			if err := dec.Decode(&s.Data); err != nil {
				return nil, fmt.Errorf("parse snapshot data: %w", err)
			}
			if s.Data == nil {
				s.Data = make(map[string]json.RawMessage)
			}
		case "lastAppliedSeq":
			if err := dec.Decode(&s.LastAppliedSeq); err != nil {
				return nil, fmt.Errorf("parse snapshot lastAppliedSeq: %w", err)
			}
		case "schemaVersion":
			if err := dec.Decode(&s.SchemaVersion); err != nil {
				return nil, fmt.Errorf("parse snapshot schemaVersion: %w", err)
			}
		default:
			return nil, fmt.Errorf("parse snapshot: unknown field %q", key)
		}
	}
	if _, err := dec.Token(); err != nil {
		return nil, fmt.Errorf("parse snapshot: %w", err) // closing brace
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

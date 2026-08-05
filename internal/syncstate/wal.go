package syncstate

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
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
// key and the result are validated here, before any record is serialized or
// written to the WAL — a caller error must never touch durable state. In
// particular an empty key is rejected because replay (decodePayload) would
// reject it after the record was already fsynced, leaving an unrecoverable WAL.
func putPayload(key string, value any) (json.RawMessage, error) {
	if key == "" {
		return nil, fmt.Errorf("wal payload: empty key")
	}
	data, err := cloudsync.CanonicalBytes(map[string]any{"key": key, "value": value})
	if err != nil {
		return nil, err
	}
	if !json.Valid(data) {
		return nil, fmt.Errorf("wal payload for key %q is not valid JSON", key)
	}
	return json.RawMessage(data), nil
}

// deletePayload builds the canonical payload for deleting key. An empty key is
// rejected for the same reason as putPayload.
func deletePayload(key string) (json.RawMessage, error) {
	if key == "" {
		return nil, fmt.Errorf("wal payload: empty key")
	}
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
// full state map. It carries a canonical checksum over the body (data,
// lastAppliedSeq, schemaVersion), so corruption that still parses as JSON (for
// example a cursor value silently changing) is detected. The document is
// written as {"data":D,"lastAppliedSeq":N,"schemaVersion":1,"checksum":"C"}:
// the checksum field comes last because it cannot be known until the body has
// been streamed.
type snapshot struct {
	SchemaVersion  int                        `json:"schemaVersion"`
	LastAppliedSeq int64                      `json:"lastAppliedSeq"`
	Data           map[string]json.RawMessage `json:"data"`
	Checksum       string                     `json:"checksum"`
}

// writeSnapshotBody streams the canonical snapshot body to w and h, WITHOUT the
// closing brace: the body is {"data":D,"lastAppliedSeq":N,"schemaVersion":1.
// The caller writes the closing brace into the checksum hash (so the checksum
// covers the complete, self-contained body object per spec) and the checksum
// field into the document (closing it). The same bytes are written to w (the
// destination) and h (a hash), and ctx is checked per data key so a cancelled
// compactor stops during the long encode. Verification calls it with
// w = io.Discard to re-encode the decoded body into a hash only.
func writeSnapshotBody(ctx context.Context, w, h io.Writer, lastApplied int64, data map[string]json.RawMessage) error {
	mw := io.MultiWriter(w, h)
	if _, err := io.WriteString(mw, `{"data":{`); err != nil {
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
			if _, err := io.WriteString(mw, ","); err != nil {
				return err
			}
		}
		if err := writeJSONString(mw, k); err != nil {
			return err
		}
		if _, err := io.WriteString(mw, ":"); err != nil {
			return err
		}
		if _, err := mw.Write(data[k]); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(mw, `},"lastAppliedSeq":%d,"schemaVersion":1`, lastApplied); err != nil {
		return err
	}
	return nil
}

// writeSnapshotFile streams the snapshot document to w (data emitted through a
// buffered encoder, never a whole-document string) and returns the byte size.
func writeSnapshotFile(ctx context.Context, w io.Writer, lastApplied int64, data map[string]json.RawMessage) (int64, error) {
	bw := bufio.NewWriter(w)
	cw := &countingWriter{w: bw}
	h := sha256.New()
	if err := writeSnapshotBody(ctx, cw, h, lastApplied, data); err != nil {
		return 0, err
	}
	h.Write([]byte("}")) // close the body object in the hash (checksum input)
	sum := hex.EncodeToString(h.Sum(nil))
	if _, err := io.WriteString(cw, `,"checksum":"`+sum+`"}`); err != nil {
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

// corrupt wraps an error as device-state corruption. I/O errors (a failed open)
// are deliberately NOT wrapped: they are not corruption.
func corrupt(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrStateCorrupt, fmt.Sprintf(format, args...))
}

// classifyDecode reports a decoder error as corruption only for JSON parse and
// type problems (or an unexpected end of input); an underlying I/O error (for
// example EIO mid-read) is returned unchanged so it is not misreported as
// device corruption.
func classifyDecode(what string, err error) error {
	var syn *json.SyntaxError
	var typ *json.UnmarshalTypeError
	if errors.As(err, &syn) || errors.As(err, &typ) || errors.Is(err, io.EOF) {
		return corrupt("%s: %v", what, err)
	}
	return err
}

// readSnapshot streams state.snapshot.json from a file with a token-based
// decoder, checking ctx between fields, so a large snapshot is decoded
// incrementally and a cancelled compactor stops at a field boundary. The data
// map is decoded one entry at a time. All four fields (checksum, data,
// lastAppliedSeq, schemaVersion) must appear exactly once, and the checksum is
// verified by canonically re-encoding the decoded body into a hash. Parse,
// schema, checksum, unknown-field, and missing-field problems are corruption;
// a failed open is returned as-is (I/O, not corruption), and a missing file is
// os.ErrNotExist.
func readSnapshot(ctx context.Context, wio walIO, path string) (*snapshot, error) {
	f, err := wio.OpenRead(path)
	if err != nil {
		return nil, err // propagates os.ErrNotExist and raw I/O errors
	}
	defer f.Close()
	dec := json.NewDecoder(f)

	tok, err := dec.Token()
	if err != nil {
		return nil, classifyDecode("parse snapshot", err)
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, corrupt("parse snapshot: not an object")
	}
	var s snapshot
	seen := make(map[string]bool)
	for dec.More() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		keyTok, err := dec.Token()
		if err != nil {
			return nil, classifyDecode("parse snapshot", err)
		}
		key, ok := keyTok.(string)
		if !ok {
			return nil, corrupt("parse snapshot: non-string key")
		}
		if seen[key] {
			return nil, corrupt("parse snapshot: duplicate field %q", key)
		}
		seen[key] = true
		switch key {
		case "checksum":
			if err := dec.Decode(&s.Checksum); err != nil {
				return nil, classifyDecode("parse snapshot checksum", err)
			}
		case "data":
			data, err := decodeDataMap(ctx, dec)
			if err != nil {
				return nil, err
			}
			s.Data = data
		case "lastAppliedSeq":
			if err := dec.Decode(&s.LastAppliedSeq); err != nil {
				return nil, classifyDecode("parse snapshot lastAppliedSeq", err)
			}
		case "schemaVersion":
			if err := dec.Decode(&s.SchemaVersion); err != nil {
				return nil, classifyDecode("parse snapshot schemaVersion", err)
			}
		default:
			return nil, corrupt("parse snapshot: unknown field %q", key)
		}
	}
	if _, err := dec.Token(); err != nil {
		return nil, classifyDecode("parse snapshot", err) // closing brace
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return nil, corrupt("trailing content after snapshot")
	}
	// Every field must appear exactly once: a snapshot missing lastAppliedSeq
	// must not silently decode as a 0 watermark and reuse durable sequences.
	for _, req := range []string{"checksum", "data", "lastAppliedSeq", "schemaVersion"} {
		if !seen[req] {
			return nil, corrupt("parse snapshot: missing field %q", req)
		}
	}
	if s.SchemaVersion != WalSchemaVersion {
		return nil, corrupt("unsupported snapshot schema %d", s.SchemaVersion)
	}
	if s.LastAppliedSeq < 0 {
		return nil, corrupt("negative lastAppliedSeq %d", s.LastAppliedSeq)
	}
	// Verify the checksum by re-encoding the decoded body into a hash (no
	// whole-document byte slice), closing the body object exactly as the writer
	// did.
	h := sha256.New()
	if err := writeSnapshotBody(ctx, io.Discard, h, s.LastAppliedSeq, s.Data); err != nil {
		return nil, err
	}
	h.Write([]byte("}"))
	if hex.EncodeToString(h.Sum(nil)) != s.Checksum {
		return nil, corrupt("snapshot checksum mismatch")
	}
	return &s, nil
}

// decodeDataMap decodes the snapshot's data object one entry at a time, so a
// large map is built incrementally and ctx is checked per key.
func decodeDataMap(ctx context.Context, dec *json.Decoder) (map[string]json.RawMessage, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, classifyDecode("parse snapshot data", err)
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, corrupt("parse snapshot data: not an object")
	}
	out := make(map[string]json.RawMessage)
	for dec.More() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		keyTok, err := dec.Token()
		if err != nil {
			return nil, classifyDecode("parse snapshot data key", err)
		}
		key, ok := keyTok.(string)
		if !ok {
			return nil, corrupt("parse snapshot data: non-string key")
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, classifyDecode(fmt.Sprintf("parse snapshot data value %q", key), err)
		}
		if _, dup := out[key]; dup {
			return nil, corrupt("parse snapshot data: duplicate key %q", key)
		}
		out[key] = raw
	}
	if _, err := dec.Token(); err != nil {
		return nil, classifyDecode("parse snapshot data", err) // closing brace
	}
	return out, nil
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

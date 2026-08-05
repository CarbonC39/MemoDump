package syncstate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testPutRecord(t *testing.T, seq int64, key string, value any) *walRecord {
	t.Helper()
	payload, err := putPayload(key, value)
	if err != nil {
		t.Fatal(err)
	}
	return &walRecord{SchemaVersion: WalSchemaVersion, Seq: seq, Op: walOpPut, Payload: payload}
}

func TestWalRecordSerializeParseRoundTrip(t *testing.T) {
	rec := testPutRecord(t, 42, "baseline.uuid", map[string]any{"hash": "abc", "n": int64(7)})
	line, err := rec.Serialize()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasSuffix(line, []byte{'\n'}) {
		t.Fatalf("record is not newline-terminated")
	}
	parsed, err := parseWalRecord(line)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if parsed.Seq != 42 || parsed.Op != walOpPut || parsed.Checksum == "" {
		t.Fatalf("parsed record mismatch: %+v", parsed)
	}
	p, err := decodePayload(parsed.Payload)
	if err != nil {
		t.Fatal(err)
	}
	if p.Key != "baseline.uuid" {
		t.Fatalf("key = %q", p.Key)
	}
	if string(p.Value) != `{"hash":"abc","n":7}` {
		t.Fatalf("value = %s", p.Value)
	}
}

func TestWalRecordDetectsCorruption(t *testing.T) {
	line, err := testPutRecord(t, 1, "k", "v").Serialize()
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		mutate func() []byte
	}{
		{"tampered payload", func() []byte {
			return bytes.Replace(line, []byte(`"value":"v"`), []byte(`"value":"x"`), 1)
		}},
		{"tampered seq", func() []byte {
			return bytes.Replace(line, []byte(`"seq":1`), []byte(`"seq":2`), 1)
		}},
		{"bad schema", func() []byte {
			return bytes.Replace(line, []byte(`"schemaVersion":1`), []byte(`"schemaVersion":9`), 1)
		}},
		{"bad op", func() []byte {
			return bytes.Replace(line, []byte(`"op":"put"`), []byte(`"op":"explode"`), 1)
		}},
		{"unknown field", func() []byte {
			s := strings.TrimSuffix(string(line), "\n")
			return []byte(strings.Replace(s, `"checksum":`, `"extra":1,"checksum":`, 1) + "\n")
		}},
		{"trailing content", func() []byte {
			return append(append([]byte{}, line...), []byte("{}")...)
		}},
	}
	for _, tc := range cases {
		if _, err := parseWalRecord(tc.mutate()); err == nil {
			t.Errorf("%s: corrupt record accepted", tc.name)
		}
	}
}

func TestSnapshotWriteParseRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "snap.json")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	data := map[string]json.RawMessage{
		"b": json.RawMessage(`2`),
		"a": json.RawMessage(`{"x":1}`),
	}
	size, err := writeSnapshotFile(context.Background(), f, 99, data)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	snap, err := readSnapshot(context.Background(), osWalIO{}, path)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if snap.LastAppliedSeq != 99 {
		t.Fatalf("lastAppliedSeq = %d", snap.LastAppliedSeq)
	}
	if string(snap.Data["a"]) != `{"x":1}` || string(snap.Data["b"]) != `2` {
		t.Fatalf("data mismatch: %v", snap.Data)
	}
	// Keys are sorted in the canonical document and the size is exact.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(`"data":{"a":{"x":1},"b":2},"lastAppliedSeq":99,"schemaVersion":1`)) {
		t.Fatalf("snapshot not canonical: %s", raw)
	}
	if size != int64(len(raw)) {
		t.Fatalf("snapshot size = %d, want %d", size, len(raw))
	}
}

func TestSnapshotDetectsCorruption(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "snap.json")
	writeValid := func() {
		t.Helper()
		f, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := writeSnapshotFile(context.Background(), f, 1,
			map[string]json.RawMessage{"k": json.RawMessage(`1`)}); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	}
	writeValid()
	if _, err := readSnapshot(context.Background(), osWalIO{}, path); err != nil {
		t.Fatalf("valid snapshot rejected: %v", err)
	}

	// A value that still parses as JSON must be caught by the checksum.
	corrupt := func(replacer func([]byte) []byte) {
		t.Helper()
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		raw = replacer(raw)
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	corrupt(func(b []byte) []byte { return bytes.Replace(b, []byte(`"k":1`), []byte(`"k":2`), 1) })
	if _, err := readSnapshot(context.Background(), osWalIO{}, path); err == nil {
		t.Fatal("value corruption with valid JSON was not caught by the checksum")
	}

	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	// Structural and schema corruption.
	cases := []string{
		`{not json`,
		`{"data":{},"lastAppliedSeq":1,"schemaVersion":1,"extra":1,"checksum":"` + strings.Repeat("0", 64) + `"}`,
		`{"data":{},"lastAppliedSeq":1,"schemaVersion":1,"checksum":"` + strings.Repeat("0", 64) + `"} junk`,
		`{"data":{},"lastAppliedSeq":1,"schemaVersion":9,"checksum":"` + strings.Repeat("0", 64) + `"}`,
		`{"data":{},"lastAppliedSeq":-1,"schemaVersion":1,"checksum":"` + strings.Repeat("0", 64) + `"}`,
		// A missing field must be rejected (a missing lastAppliedSeq could
		// silently decode as a 0 watermark and reuse durable sequences).
		`{"data":{},"schemaVersion":1,"checksum":"` + strings.Repeat("0", 64) + `"}`,
	}
	for _, body := range cases {
		write(body)
		if _, err := readSnapshot(context.Background(), osWalIO{}, path); err == nil {
			t.Errorf("corrupt snapshot accepted: %s", body)
		}
	}
}

func TestSnapshotReadIOErrorIsNotCorruption(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "snap.json")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writeSnapshotFile(context.Background(), f, 1, map[string]json.RawMessage{}); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	// A transient open failure is an I/O error, never corruption.
	fault := newFaultWalIO(osWalIO{})
	fault.armNext("read", errors.New("transient io"))
	if _, err := readSnapshot(context.Background(), fault, path); err == nil {
		t.Fatal("read succeeded despite the injected failure")
	} else if errors.Is(err, ErrStateCorrupt) {
		t.Fatalf("I/O error misclassified as corruption: %v", err)
	}
}

func TestPutPayloadIsCanonical(t *testing.T) {
	p1, err := putPayload("k", map[string]any{"b": int64(2), "a": int64(1)})
	if err != nil {
		t.Fatal(err)
	}
	if string(p1) != `{"key":"k","value":{"a":1,"b":2}}` {
		t.Fatalf("payload not canonical: %s", p1)
	}
	// Nested maps keep sorted keys.
	p2, err := putPayload("k", []any{map[string]any{"z": int64(1), "y": int64(2)}})
	if err != nil {
		t.Fatal(err)
	}
	if string(p2) != `{"key":"k","value":[{"y":2,"z":1}]}` {
		t.Fatalf("nested payload not canonical: %s", p2)
	}
}

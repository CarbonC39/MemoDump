package syncstate

import (
	"bytes"
	"encoding/json"
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
	var buf bytes.Buffer
	data := map[string]json.RawMessage{
		"b": json.RawMessage(`2`),
		"a": json.RawMessage(`{"x":1}`),
	}
	sum, err := writeSnapshotFile(&buf, 99, data)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := parseSnapshot(buf.Bytes())
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if snap.LastAppliedSeq != 99 {
		t.Fatalf("lastAppliedSeq = %d", snap.LastAppliedSeq)
	}
	if snap.Checksum != sum {
		t.Fatalf("checksum mismatch")
	}
	if string(snap.Data["a"]) != `{"x":1}` || string(snap.Data["b"]) != `2` {
		t.Fatalf("data mismatch: %v", snap.Data)
	}
	// Keys are sorted in the canonical document.
	if !bytes.Contains(buf.Bytes(), []byte(`"data":{"a":{"x":1},"b":2}`)) {
		t.Fatalf("snapshot not canonical: %s", buf.String())
	}
}

func TestSnapshotDetectsCorruption(t *testing.T) {
	var buf bytes.Buffer
	if _, err := writeSnapshotFile(&buf, 5, map[string]json.RawMessage{"k": json.RawMessage(`1`)}); err != nil {
		t.Fatal(err)
	}
	// Corrupt a value; the checksum must catch it even though the JSON parses.
	corrupt := bytes.Replace(buf.Bytes(), []byte(`"k":1`), []byte(`"k":2`), 1)
	if _, err := parseSnapshot(corrupt); err == nil {
		t.Fatal("corrupt snapshot accepted")
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

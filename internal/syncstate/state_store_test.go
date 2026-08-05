package syncstate

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func openStore(t *testing.T, dir string, opts Options) *Store {
	t.Helper()
	s, err := Open(dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func putAll(t *testing.T, s *Store, keys ...string) {
	t.Helper()
	for i, k := range keys {
		if _, err := s.Put(k, map[string]any{"i": int64(i)}); err != nil {
			t.Fatal(err)
		}
	}
}

func walLine(seq int64, op, key string, value any) []byte {
	payload, err := putPayload(key, value)
	if err != nil {
		panic(err)
	}
	rec := &walRecord{SchemaVersion: WalSchemaVersion, Seq: seq, Op: op, Payload: payload}
	line, err := rec.Serialize()
	if err != nil {
		panic(err)
	}
	return line
}

func appendBytes(t *testing.T, path string, b []byte) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.Write(b); err != nil {
		t.Fatal(err)
	}
}

func badChecksumLine(seq int64) []byte {
	return []byte(`{"schemaVersion":1,"seq":` + strconv.FormatInt(seq, 10) + `,"op":"put","payload":{"key":"x","value":1},"checksum":"0000000000000000000000000000000000000000000000000000000000000000"}` + "\n")
}

func TestPutGetDeleteDurable(t *testing.T) {
	s := openStore(t, t.TempDir(), Options{})
	seq, err := s.Put("a", map[string]any{"x": int64(1)})
	if err != nil || seq != 1 {
		t.Fatalf("put = %d, %v", seq, err)
	}
	if got, _ := s.Get("a"); string(got) != `{"x":1}` {
		t.Fatalf("get a = %s", got)
	}
	if _, err := s.Delete("a"); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Get("a"); ok {
		t.Fatal("deleted key still present")
	}
}

func TestRecoveryReplaysState(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir, Options{})
	putAll(t, s, "a", "b", "c")
	s.Close()

	s2 := openStore(t, dir, Options{})
	for _, k := range []string{"a", "b", "c"} {
		if _, ok := s2.Get(k); !ok {
			t.Fatalf("key %q lost across restart", k)
		}
	}
}

func TestSequenceMonotonicAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir, Options{})
	putAll(t, s, "a", "b", "c")
	s.Close()

	s2 := openStore(t, dir, Options{})
	seq, err := s2.Put("d", 4)
	if err != nil {
		t.Fatal(err)
	}
	if seq != 4 {
		t.Fatalf("next seq after restart = %d, want 4 (no reuse)", seq)
	}
}

func TestTornTailTruncated(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir, Options{})
	putAll(t, s, "a", "b", "c")
	s.Close()

	// A syntactically partial, unterminated fragment after the last valid line.
	appendBytes(t, filepath.Join(dir, activeWalName), []byte(`{"schemaVersion":1,"seq":99,`))

	s2 := openStore(t, dir, Options{})
	if _, ok := s2.Get("c"); !ok {
		t.Fatal("valid records before the torn tail lost")
	}
	seq, err := s2.Put("d", 4)
	if err != nil {
		t.Fatal(err)
	}
	if seq != 4 {
		t.Fatalf("next seq after torn tail = %d, want 4", seq)
	}
}

func TestCompleteBadChecksumStopsRecovery(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir, Options{})
	putAll(t, s, "a")
	s.Close()

	// A complete newline-terminated record with a bad checksum must never be
	// silently discarded as a presumed torn write.
	appendBytes(t, filepath.Join(dir, activeWalName), badChecksumLine(50))

	if _, err := Open(dir, Options{}); !errors.Is(err, ErrStateCorrupt) {
		t.Fatalf("Open = %v, want ErrStateCorrupt", err)
	}
}

func TestMidWalCorruptionStopsRecovery(t *testing.T) {
	dir := t.TempDir()
	var data []byte
	data = append(data, walLine(1, "put", "a", 1)...)
	data = append(data, badChecksumLine(2)...)
	data = append(data, walLine(3, "put", "c", 3)...)
	if err := os.WriteFile(filepath.Join(dir, activeWalName), data, 0600); err != nil {
		t.Fatal(err)
	}
	// Corruption before the tail stops recovery rather than truncating forward.
	if _, err := Open(dir, Options{}); !errors.Is(err, ErrStateCorrupt) {
		t.Fatalf("Open = %v, want ErrStateCorrupt", err)
	}
}

func TestSequenceGapTolerated(t *testing.T) {
	dir := t.TempDir()
	var data []byte
	data = append(data, walLine(1, "put", "a", 1)...)
	data = append(data, walLine(3, "put", "c", 3)...)
	if err := os.WriteFile(filepath.Join(dir, activeWalName), data, 0600); err != nil {
		t.Fatal(err)
	}
	s := openStore(t, dir, Options{})
	if _, ok := s.Get("a"); !ok {
		t.Fatal("seq 1 not applied")
	}
	if _, ok := s.Get("c"); !ok {
		t.Fatal("seq 3 not applied")
	}
	seq, err := s.Put("d", 4)
	if err != nil {
		t.Fatal(err)
	}
	if seq != 4 {
		t.Fatalf("next seq after gap = %d, want 4", seq)
	}
}

func TestMissingActiveWalRecovers(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, frozenName(1)), walLine(1, "put", "a", 1), 0600); err != nil {
		t.Fatal(err)
	}
	s := openStore(t, dir, Options{})
	if _, ok := s.Get("a"); !ok {
		t.Fatal("frozen record not recovered")
	}
	seq, err := s.Put("b", 2)
	if err != nil {
		t.Fatal(err)
	}
	if seq != 2 {
		t.Fatalf("next seq = %d, want 2", seq)
	}
}

func TestMultipleFrozenGenerationsInOrder(t *testing.T) {
	dir := t.TempDir()
	gen1 := append(walLine(1, "put", "a", 1), walLine(2, "put", "b", 2)...)
	gen2 := append(walLine(3, "put", "c", 3), walLine(4, "put", "d", 4)...)
	if err := os.WriteFile(filepath.Join(dir, frozenName(1)), gen1, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, frozenName(2)), gen2, 0600); err != nil {
		t.Fatal(err)
	}
	s := openStore(t, dir, Options{})
	for _, k := range []string{"a", "b", "c", "d"} {
		if _, ok := s.Get(k); !ok {
			t.Fatalf("key %q not recovered", k)
		}
	}
	seq, _ := s.Put("e", 5)
	if seq != 5 {
		t.Fatalf("next seq = %d, want 5", seq)
	}
}

func TestStaleTempFilesIgnored(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir, Options{})
	putAll(t, s, "a")
	s.Close()
	// Leftovers from crashed snapshot writes must not break recovery.
	if err := os.WriteFile(filepath.Join(dir, ".state-snapshot-1.tmp"), []byte("junk"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".state-2.tmp"), []byte("junk"), 0600); err != nil {
		t.Fatal(err)
	}
	s2 := openStore(t, dir, Options{})
	if _, ok := s2.Get("a"); !ok {
		t.Fatal("stale temp files broke recovery")
	}
}

func TestShortWriteLastAppendRecovers(t *testing.T) {
	dir := t.TempDir()
	fault := newFaultWalIO(osWalIO{})
	s := openStore(t, dir, Options{FS: fault})
	putAll(t, s, "a")

	fault.armNextShortWrite("write")
	if _, err := s.Put("b", 2); err == nil {
		t.Fatal("short write did not fail the append")
	}
	s.Close()

	// The partial line is the torn tail: truncated, the failed append absent.
	s2 := openStore(t, dir, Options{})
	if _, ok := s2.Get("b"); ok {
		t.Fatal("failed append recovered as durable")
	}
	seq, err := s2.Put("b", 2)
	if err != nil || seq != 2 {
		t.Fatalf("retry after short write = %d, %v", seq, err)
	}
}

func TestShortWriteThenMoreAppendsIsCorrupt(t *testing.T) {
	dir := t.TempDir()
	fault := newFaultWalIO(osWalIO{})
	s := openStore(t, dir, Options{FS: fault})
	putAll(t, s, "a")
	fault.armNextShortWrite("write")
	if _, err := s.Put("b", 2); err == nil {
		t.Fatal("short write did not fail")
	}
	// A later append lands after the partial line: mid-WAL corruption.
	if _, err := s.Put("c", 2); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if _, err := Open(dir, Options{}); !errors.Is(err, ErrStateCorrupt) {
		t.Fatalf("Open = %v, want ErrStateCorrupt (partial line is not a tail)", err)
	}
}

func TestFailedFsyncDoesNotAckAndRecoveryIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	fault := newFaultWalIO(osWalIO{})
	s := openStore(t, dir, Options{FS: fault})
	putAll(t, s, "a")

	fault.armNext("sync", errors.New("disk error"))
	if _, err := s.Put("b", 2); err == nil {
		t.Fatal("fsync failure did not fail the append")
	}
	seq, err := s.Put("b", 2) // retry reuses the un-acked sequence
	if err != nil || seq != 2 {
		t.Fatalf("retry = %d, %v", seq, err)
	}
	s.Close()

	s2 := openStore(t, dir, Options{})
	if _, ok := s2.Get("b"); !ok {
		t.Fatal("retried record lost across restart")
	}
	seq2, err := s2.Put("d", 4)
	if err != nil {
		t.Fatal(err)
	}
	if seq2 != 3 {
		t.Fatalf("next seq = %d, want 3 (both seq-2 records replayed idempotently)", seq2)
	}
}

func TestClosePreventsFurtherAppends(t *testing.T) {
	s := openStore(t, t.TempDir(), Options{})
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Put("a", 1); err == nil {
		t.Fatal("append after Close succeeded")
	}
}

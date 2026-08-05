package syncstate

import (
	"errors"
	"testing"
)

func TestBaselineRoundTrip(t *testing.T) {
	s := openStore(t, t.TempDir(), Options{})
	want := Baseline{
		LocalHash:     "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		RemoteHash:    "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		RemoteVersion: "rev-7",
	}
	if err := PutBaseline(s, "5d5d8b2c-94f7-4a38-8318-8cd4cb53dfa8", want); err != nil {
		t.Fatal(err)
	}
	got, ok, err := GetBaseline(s, "5d5d8b2c-94f7-4a38-8318-8cd4cb53dfa8")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("baseline missing after put")
	}
	if got != want {
		t.Fatalf("baseline = %+v, want %+v", got, want)
	}
}

func TestBaselineMissing(t *testing.T) {
	s := openStore(t, t.TempDir(), Options{})
	if _, ok, err := GetBaseline(s, "5d5d8b2c-94f7-4a38-8318-8cd4cb53dfa8"); err != nil || ok {
		t.Fatalf("GetBaseline for absent entity = ok:%v err:%v", ok, err)
	}
}

func TestBaselineDurableAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir, Options{})
	want := Baseline{LocalHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	if err := PutBaseline(s, "5d5d8b2c-94f7-4a38-8318-8cd4cb53dfa8", want); err != nil {
		t.Fatal(err)
	}
	s.Close()

	s2 := openStore(t, dir, Options{})
	got, ok, err := GetBaseline(s2, "5d5d8b2c-94f7-4a38-8318-8cd4cb53dfa8")
	if err != nil || !ok {
		t.Fatalf("baseline lost across reopen: ok:%v err:%v", ok, err)
	}
	if got != want {
		t.Fatalf("baseline = %+v", got)
	}
}

func TestStoreLenTracksBaselineState(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.Len() != 0 {
		t.Fatalf("fresh store Len = %d, want 0", s.Len())
	}
	if err := PutBaseline(s, "5d5d8b2c-94f7-4a38-8318-8cd4cb53dfa8", Baseline{LocalHash: "x"}); err != nil {
		t.Fatal(err)
	}
	if s.Len() != 1 {
		t.Fatalf("store Len after one baseline = %d, want 1", s.Len())
	}
}

func TestHasAnyBaselineIgnoresNonBaselineState(t *testing.T) {
	s := openStore(t, t.TempDir(), Options{})
	if s.HasAnyBaseline() {
		t.Fatal("empty store has baselines")
	}
	// Cursor/config durable state must never count as baseline knowledge: a
	// replica that only ever stored a cursor has no baselines and must probe.
	if _, err := s.Put("cursor", map[string]any{"tok": "x"}); err != nil {
		t.Fatal(err)
	}
	if s.HasAnyBaseline() {
		t.Fatal("a cursor key counted as baseline knowledge")
	}
	if err := PutBaseline(s, "5d5d8b2c-94f7-4a38-8318-8cd4cb53dfa8", Baseline{LocalHash: "x"}); err != nil {
		t.Fatal(err)
	}
	if !s.HasAnyBaseline() {
		t.Fatal("baseline not detected alongside a cursor key")
	}
}

func TestCorruptBaselineIsStateCorruption(t *testing.T) {
	s := openStore(t, t.TempDir(), Options{})
	// The WAL guarantees stored values are valid JSON; a value that parses as
	// JSON but not as a Baseline (wrong type) is device-state corruption, not a
	// silent "no baseline" — the reconciliation must never misclassify a synced
	// entity as baseline-unknown because its baseline failed to decode.
	id := "5d5d8b2c-94f7-4a38-8318-8cd4cb53dfa8"
	if _, err := s.Put(BaselineKey(id), 5); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := GetBaseline(s, id); !errors.Is(err, ErrStateCorrupt) || !ok {
		t.Fatalf("corrupt baseline = ok:%v err:%v, want record-present + ErrStateCorrupt", ok, err)
	}
}

func TestPutBaselineRejectsBadSyncID(t *testing.T) {
	s := openStore(t, t.TempDir(), Options{})
	if err := PutBaseline(s, "not-a-uuid", Baseline{LocalHash: "x"}); err == nil {
		t.Fatal("bad syncId accepted")
	}
	if s.Len() != 0 {
		t.Fatal("bad baseline touched the WAL")
	}
}

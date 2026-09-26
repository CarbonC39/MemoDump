package cloudsync

import (
	"context"
	"fmt"
	"testing"
	"time"
)

var testCtx = context.Background()

func TestMemoryStoreCreateReplaceSemantics(t *testing.T) {
	s := NewMemoryStore()

	// Create on a missing key succeeds and returns an opaque version.
	v1, err := s.Create(testCtx, "a", []byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	if v1 == "" {
		t.Fatal("empty version")
	}

	// Create on an existing key is a precondition failure.
	if _, err := s.Create(testCtx, "a", []byte("y")); !IsStoreError(err, ErrPreconditionFailed) {
		t.Fatalf("create-on-existing err = %v", err)
	}

	// Read returns the stored bytes and the current version.
	got, v, err := s.Read(testCtx, "a")
	if err != nil || string(got) != "x" || v != v1 {
		t.Fatalf("read = %q %q %v", got, v, err)
	}

	// Replace with a stale expected version is a precondition failure and does
	// not modify the object.
	if _, err := s.Replace(testCtx, "a", []byte("z"), "stale"); !IsStoreError(err, ErrPreconditionFailed) {
		t.Fatalf("stale replace err = %v", err)
	}
	if got, _, _ := s.Read(testCtx, "a"); string(got) != "x" {
		t.Fatalf("stale replace modified data: %q", got)
	}

	// Replace with the current version succeeds and bumps the version.
	v2, err := s.Replace(testCtx, "a", []byte("z"), v1)
	if err != nil {
		t.Fatal(err)
	}
	if v2 == v1 {
		t.Fatal("version did not advance")
	}

	// Read/Replace/Remove on a missing key report not-found.
	if _, _, err := s.Read(testCtx, "missing"); !IsStoreError(err, ErrNotFound) {
		t.Fatalf("missing read err = %v", err)
	}
	if _, err := s.Replace(testCtx, "missing", []byte("z"), "anything"); !IsStoreError(err, ErrNotFound) {
		t.Fatalf("missing replace err = %v", err)
	}
	if err := s.Remove(testCtx, "missing"); !IsStoreError(err, ErrNotFound) {
		t.Fatalf("missing remove err = %v", err)
	}
}

func TestMemoryStoreDeltaCursor(t *testing.T) {
	s := NewMemoryStore()
	if _, err := s.Create(testCtx, "a/1", []byte("1")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(testCtx, "b/1", []byte("x")); err != nil {
		t.Fatal(err)
	}

	// Full baseline: current keys under prefix, all created.
	page, err := s.List(testCtx, "a", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Changes) != 1 || page.Changes[0].Key != "a/1" || page.Changes[0].Type != ChangeCreated {
		t.Fatalf("baseline = %+v", page.Changes)
	}
	if page.SyncCursor == "" {
		t.Fatal("baseline produced no sync cursor")
	}

	// Resuming with the sync cursor returns no new changes.
	again, err := s.List(testCtx, "a", page.SyncCursor)
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Changes) != 0 {
		t.Fatalf("resume after baseline returned %+v", again.Changes)
	}

	// A new create and an update appear as a delta; a Remove appears as deleted.
	if _, err := s.Create(testCtx, "a/2", []byte("2")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Replace(testCtx, "a/1", []byte("1b"), "1"); err != nil {
		t.Fatal(err)
	}
	if err := s.Remove(testCtx, "b/1"); err != nil {
		t.Fatal(err)
	}
	delta, err := s.List(testCtx, "a", page.SyncCursor)
	if err != nil {
		t.Fatal(err)
	}
	if len(delta.Changes) != 2 {
		t.Fatalf("delta = %+v", delta.Changes)
	}
	seen := map[string]ChangeType{}
	for _, c := range delta.Changes {
		seen[c.Key] = c.Type
	}
	if seen["a/2"] != ChangeCreated || seen["a/1"] != ChangeUpdated {
		t.Fatalf("delta types = %+v", seen)
	}
	// b/1 was removed but is outside the "a" prefix.
	if _, ok := seen["b/1"]; ok {
		t.Fatalf("b/1 leaked into a-prefix delta")
	}

	// The deleted b/1 is visible under its own prefix.
	deleted, err := s.List(testCtx, "b", page.SyncCursor)
	if err != nil {
		t.Fatal(err)
	}
	if len(deleted.Changes) != 1 || deleted.Changes[0].Type != ChangeDeleted {
		t.Fatalf("deleted delta = %+v", deleted.Changes)
	}
}

func TestMemoryStoreCursorReset(t *testing.T) {
	s := NewMemoryStore()
	if _, err := s.Create(testCtx, "a/1", []byte("1")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(testCtx, "a/2", []byte("2")); err != nil {
		t.Fatal(err)
	}

	// An invalid cursor resets to a full baseline scan.
	reset, err := s.List(testCtx, "a", "garbage-cursor")
	if err != nil {
		t.Fatal(err)
	}
	if len(reset.Changes) != 2 || reset.Changes[0].Key != "a/1" {
		t.Fatalf("reset baseline = %+v", reset.Changes)
	}
	if reset.Changes[0].Type != ChangeCreated {
		t.Fatalf("reset should report created: %+v", reset.Changes[0])
	}
}

func TestMemoryStoreFaultInjection(t *testing.T) {
	s := NewMemoryStore()
	if _, err := s.Create(testCtx, "k", []byte("v")); err != nil {
		t.Fatal(err)
	}

	// Arm a deterministic read fault; the next read fails with the injected
	// error, then the fault is consumed.
	s.ArmFault("read", &StoreError{Kind: ErrRetryableTransport, Message: "flaky"})
	if _, _, err := s.Read(testCtx, "k"); !IsStoreError(err, ErrRetryableTransport) {
		t.Fatalf("faulted read err = %v", err)
	}
	if _, _, err := s.Read(testCtx, "k"); err != nil {
		t.Fatalf("fault was not consumed: %v", err)
	}

	// A create fault.
	s.ArmFault("create", &StoreError{Kind: ErrRateLimit, RetryAfter: time.Second, Message: "slow down"})
	if _, err := s.Create(testCtx, "k2", []byte("v")); !IsStoreError(err, ErrRateLimit) {
		t.Fatalf("faulted create err = %v", err)
	}
	if _, err := s.Create(testCtx, "k2", []byte("v")); err != nil {
		t.Fatalf("create after fault failed: %v", err)
	}
}

func TestMemoryStoreUncertainWriteFault(t *testing.T) {
	s := NewMemoryStore()
	// A create that lands but whose response is lost: the caller must re-read.
	s.ArmUncertainWrite("create", &StoreError{Kind: ErrRetryableTransport, Message: "response lost"})
	if _, err := s.Create(testCtx, "k", []byte("v")); !IsStoreError(err, ErrRetryableTransport) {
		t.Fatalf("uncertain create err = %v", err)
	}
	data, version, err := s.Read(testCtx, "k")
	if err != nil || string(data) != "v" {
		t.Fatalf("uncertain write did not land: %s, %v", data, err)
	}
	_ = version

	// A replace that lands but whose response is lost.
	s.ArmUncertainWrite("replace", &StoreError{Kind: ErrRetryableTransport, Message: "response lost"})
	if _, err := s.Replace(testCtx, "k", []byte("v2"), version); !IsStoreError(err, ErrRetryableTransport) {
		t.Fatalf("uncertain replace err = %v", err)
	}
	data, _, err = s.Read(testCtx, "k")
	if err != nil || string(data) != "v2" {
		t.Fatalf("uncertain replace did not land: %s, %v", data, err)
	}
}

func TestMemoryStoreCursorRejectAndIncompleteListFaults(t *testing.T) {
	s := NewMemoryStore()
	for _, k := range []string{"a/1", "a/2", "a/3"} {
		if _, err := s.Create(testCtx, k, []byte(k)); err != nil {
			t.Fatal(err)
		}
	}
	// A cursor-reject fault makes List ignore a valid delta cursor and return a
	// full baseline, so no later event is ever skipped.
	seq, err := s.List(testCtx, "a", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Replace(testCtx, "a/1", []byte("v1b"), seq.Changes[0].Version); err != nil {
		t.Fatal(err)
	}
	s.ArmCursorReject()
	page, err := s.List(testCtx, "a", seq.SyncCursor)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Changes) != 3 || page.Changes[0].Type != ChangeCreated {
		t.Fatalf("cursor-reject did not fall back to a full baseline: %+v", page.Changes)
	}

	// An incomplete-list fault is a typed error, never a silent partial
	// listing: the engine stops on it.
	s.ArmIncompleteList(1)
	if _, err := s.List(testCtx, "a", ""); !IsStoreError(err, ErrIncompleteList) {
		t.Fatalf("incomplete listing error = %v, want ErrIncompleteList", err)
	}
	// Without the fault the full listing is complete.
	page, err = s.List(testCtx, "a", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Changes) != 3 {
		t.Fatalf("complete listing returned %d changes, want 3", len(page.Changes))
	}
}

func TestMemoryStoreBaselinePaginationWatermark(t *testing.T) {
	s := NewMemoryStore()
	// 105 keys so the baseline paginates (pageSize 100).
	for i := 0; i < 105; i++ {
		key := fmt.Sprintf("a/%03d", i)
		if _, err := s.Create(testCtx, key, []byte(key)); err != nil {
			t.Fatal(err)
		}
	}

	p1, err := s.List(testCtx, "a", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(p1.Changes) != 100 || p1.NextCursor == "" {
		t.Fatalf("page1 = %d changes, next=%q", len(p1.Changes), p1.NextCursor)
	}
	watermark := p1.SyncCursor

	// Modify a key already scanned in page 1 (a/000). This is a change AFTER the
	// watermark.
	origVersion := p1.Changes[0].Version
	if _, err := s.Replace(testCtx, "a/000", []byte("changed"), origVersion); err != nil {
		t.Fatal(err)
	}

	// The continuation must keep the FIRST scan's watermark and never report the
	// modified key with its new version.
	p2, err := s.List(testCtx, "a", p1.NextCursor)
	if err != nil {
		t.Fatal(err)
	}
	if p2.SyncCursor != watermark {
		t.Fatalf("sync cursor moved across baseline pages: %q -> %q", watermark, p2.SyncCursor)
	}
	if len(p2.Changes) != 5 {
		t.Fatalf("page2 = %d changes, want 5", len(p2.Changes))
	}
	for _, c := range p2.Changes {
		if c.Key == "a/000" {
			t.Fatalf("a/000 leaked into the continuation: %+v", c)
		}
	}

	// A delta from the watermark must report the a/000 modification.
	delta, err := s.List(testCtx, "a", watermark)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range delta.Changes {
		if c.Key == "a/000" && c.Type == ChangeUpdated {
			found = true
		}
	}
	if !found {
		t.Fatalf("delta after baseline missed the a/000 modification: %+v", delta.Changes)
	}
}

func TestMemoryStoreCursorBeyondSeqResets(t *testing.T) {
	s := NewMemoryStore()
	if _, err := s.Create(testCtx, "a/1", []byte("1")); err != nil {
		t.Fatal(err)
	}
	// A numeric cursor beyond the current seq must reset to a full baseline, not
	// be silently accepted (which would permanently skip later events).
	page, err := s.List(testCtx, "a", "999999999")
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Changes) != 1 || page.Changes[0].Key != "a/1" || page.Changes[0].Type != ChangeCreated {
		t.Fatalf("reset baseline = %+v", page.Changes)
	}
}

func TestMemoryStoreBaselineCursorValidation(t *testing.T) {
	s := NewMemoryStore()
	for i := 0; i < 105; i++ {
		key := fmt.Sprintf("a/%03d", i)
		if _, err := s.Create(testCtx, key, []byte(key)); err != nil {
			t.Fatal(err)
		}
	}
	p1, err := s.List(testCtx, "a", "")
	if err != nil {
		t.Fatal(err)
	}
	if p1.NextCursor == "" {
		t.Fatal("baseline did not paginate")
	}
	current := p1.SyncCursor

	// An out-of-range watermark must reset to a full baseline at the current
	// position, never emit a future sync cursor.
	reset, err := s.List(testCtx, "a", "base:999999999:a/099")
	if err != nil {
		t.Fatal(err)
	}
	if reset.SyncCursor != current {
		t.Fatalf("out-of-range watermark produced cursor %q, want %q", reset.SyncCursor, current)
	}
	// The reset baseline is paginated: first page is the page size.
	if len(reset.Changes) != 100 || reset.NextCursor == "" {
		t.Fatalf("reset after bad watermark = %d changes, next=%q", len(reset.Changes), reset.NextCursor)
	}

	// A continuation key absent from the snapshot must reset.
	absent, err := s.List(testCtx, "a", "base:"+current+":nonexistent")
	if err != nil {
		t.Fatal(err)
	}
	if absent.SyncCursor != current {
		t.Fatalf("absent key produced cursor %q, want %q", absent.SyncCursor, current)
	}
	if len(absent.Changes) != 100 {
		t.Fatalf("reset after absent key = %d changes, want 100", len(absent.Changes))
	}

	// A malformed watermark must reset.
	garbage, err := s.List(testCtx, "a", "base:garbage:key")
	if err != nil {
		t.Fatal(err)
	}
	if garbage.SyncCursor != current {
		t.Fatalf("malformed watermark produced cursor %q, want %q", garbage.SyncCursor, current)
	}
}

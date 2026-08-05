package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"memodump/internal/vaultfs"
)

type noteSemanticsFixture struct {
	NameCases []struct {
		Input  string `json:"input"`
		Output string `json:"output"`
	} `json:"nameCases"`
	TagCases [][]string `json:"tagCases"`
}

func loadNoteSemanticsFixture(t *testing.T) noteSemanticsFixture {
	t.Helper()
	data, err := os.ReadFile("testdata/contracts/note_semantics.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture noteSemanticsFixture
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func TestV2AscendingCursorPagination(t *testing.T) {
	notes := []noteSummaryV2{
		{ID: "new.md", ModifiedAt: 30},
		{ID: "old.md", ModifiedAt: 10},
		{ID: "middle.md", ModifiedAt: 20},
	}
	sortNotesV2(notes, "modified-asc")
	first := pageNotesV2(notes, nil, 2, "modified-asc")
	if len(first.Items) != 2 || first.Items[0].ID != "old.md" || first.NextCursor == nil {
		t.Fatalf("first ascending page = %#v", first)
	}
	cursor, err := decodeV2Cursor(*first.NextCursor)
	if err != nil {
		t.Fatal(err)
	}
	second := pageNotesV2(notes, cursor, 2, "modified-asc")
	if len(second.Items) != 1 || second.Items[0].ID != "new.md" {
		t.Fatalf("second ascending page = %#v", second)
	}
}

func TestSharedNameSemantics(t *testing.T) {
	fixture := loadNoteSemanticsFixture(t)
	for _, testCase := range fixture.NameCases {
		if got := vaultfs.SanitizeName(testCase.Input); got != testCase.Output {
			t.Errorf("SanitizeName(%q) = %q, want %q", testCase.Input, got, testCase.Output)
		}
	}
}

func TestSharedTagSemantics(t *testing.T) {
	fixture := loadNoteSemanticsFixture(t)
	for _, want := range fixture.TagCases {
		md, err := (&vaultfs.Document{Body: "body"}).WithTags(want)
		if err != nil {
			t.Fatalf("WithTags(%#v) error: %v", want, err)
		}
		got := vaultfs.ParseDocument(md).Tags
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("tags = %#v, want %#v", got, want)
		}
	}
}

func TestFrontMatterTagsRoundTrip(t *testing.T) {
	want := []string{"one,two", `say "hi"`, `a\b`}
	md, err := (&vaultfs.Document{Body: "body"}).WithTags(want)
	if err != nil {
		t.Fatal(err)
	}
	parsed := vaultfs.ParseDocument(md)
	if parsed.Body != "body" {
		t.Fatalf("body = %q, want body", parsed.Body)
	}
	if !reflect.DeepEqual(parsed.Tags, want) {
		t.Fatalf("tags = %#v, want %#v", parsed.Tags, want)
	}
}

func TestUpdateNoteRenameDoesNotOverwrite(t *testing.T) {
	oldDataDir := dataDir
	dataDir = t.TempDir()
	t.Cleanup(func() { dataDir = oldDataDir })
	initRepo()

	if err := os.WriteFile(filepath.Join(dataDir, "source.md"), []byte("source"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "target.md"), []byte("target"), 0644); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPut, "/api/notes/source.md",
		strings.NewReader(`{"content":"changed","tags":[],"rename":"target"}`))
	req.SetPathValue("path", "source.md")
	rec := httptest.NewRecorder()

	handleUpdateNote(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusConflict, rec.Body.String())
	}
	target, err := os.ReadFile(filepath.Join(dataDir, "target.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(target) != "target" {
		t.Fatalf("target content = %q, want target", target)
	}
	source, err := os.ReadFile(filepath.Join(dataDir, "source.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(source) != "source" {
		t.Fatalf("source content = %q, want source", source)
	}
}

func TestV2ListingsAreDirectAndPaginated(t *testing.T) {
	oldDataDir := dataDir
	dataDir = t.TempDir()
	t.Cleanup(func() { dataDir = oldDataDir })
	initRepo()

	for _, dir := range []string{"a", "a/deep", "b"} {
		if err := os.MkdirAll(filepath.Join(dataDir, dir), 0755); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{"a/one.md", "a/two.md", "a/deep/hidden.md"} {
		if err := os.WriteFile(filepath.Join(dataDir, path), []byte(path), 0644); err != nil {
			t.Fatal(err)
		}
	}

	folderReq := httptest.NewRequest(http.MethodGet, "/api/v2/folders?parent=a", nil)
	folderRec := httptest.NewRecorder()
	handleV2ListFolders(folderRec, folderReq)
	if folderRec.Code != http.StatusOK {
		t.Fatalf("folder status = %d; body=%s", folderRec.Code, folderRec.Body.String())
	}
	var folders folderPageV2
	if err := json.Unmarshal(folderRec.Body.Bytes(), &folders); err != nil {
		t.Fatal(err)
	}
	if len(folders.Items) != 1 || folders.Items[0].ID != "a/deep" {
		t.Fatalf("folders = %#v", folders.Items)
	}

	firstReq := httptest.NewRequest(http.MethodGet, "/api/v2/notes?parent=a&limit=1", nil)
	firstRec := httptest.NewRecorder()
	handleV2ListNotes(firstRec, firstReq)
	var first notePageV2
	if err := json.Unmarshal(firstRec.Body.Bytes(), &first); err != nil {
		t.Fatal(err)
	}
	if len(first.Items) != 1 || first.NextCursor == nil {
		t.Fatalf("first page = %#v", first)
	}

	secondReq := httptest.NewRequest(http.MethodGet,
		"/api/v2/notes?parent=a&limit=1&cursor="+*first.NextCursor, nil)
	secondRec := httptest.NewRecorder()
	handleV2ListNotes(secondRec, secondReq)
	var second notePageV2
	if err := json.Unmarshal(secondRec.Body.Bytes(), &second); err != nil {
		t.Fatal(err)
	}
	if len(second.Items) != 1 || second.Items[0].ID == first.Items[0].ID {
		t.Fatalf("second page = %#v, first = %#v", second, first)
	}
}

// --- Phase 0 revision CAS, HTTP level --------------------------------------

func apiNoteRepo(t *testing.T) {
	t.Helper()
	oldDataDir := dataDir
	dataDir = t.TempDir()
	t.Cleanup(func() { dataDir = oldDataDir })
	initRepo()
}

func createNoteViaAPI(t *testing.T, name, content string) vaultfs.Note {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/notes",
		strings.NewReader(fmt.Sprintf(`{"name":%q,"content":%q,"tags":[]}`, name, content)))
	rec := httptest.NewRecorder()
	handleCreateNote(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d; body=%s", rec.Code, rec.Body.String())
	}
	var note vaultfs.Note
	if err := json.Unmarshal(rec.Body.Bytes(), &note); err != nil {
		t.Fatal(err)
	}
	return note
}

func getNoteViaAPI(t *testing.T, path string) vaultfs.Note {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/notes/"+path, nil)
	req.SetPathValue("path", path)
	rec := httptest.NewRecorder()
	handleGetNote(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("get status = %d; body=%s", rec.Code, rec.Body.String())
	}
	var note vaultfs.Note
	if err := json.Unmarshal(rec.Body.Bytes(), &note); err != nil {
		t.Fatal(err)
	}
	return note
}

func updateNoteViaAPI(t *testing.T, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, "/api/notes/"+path, strings.NewReader(body))
	req.SetPathValue("path", path)
	rec := httptest.NewRecorder()
	handleUpdateNote(rec, req)
	return rec
}

func currentRevision(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dataDir, path))
	if err != nil {
		t.Fatal(err)
	}
	return vaultfs.RevisionOfBytes(data)
}

func TestLegacyUpdateStaleRevisionConflicts(t *testing.T) {
	apiNoteRepo(t)
	createNoteViaAPI(t, "a", "v0")
	rev := currentRevision(t, "a.md")

	rec := updateNoteViaAPI(t, "a.md",
		fmt.Sprintf(`{"content":"v1","tags":[],"baseRevision":%q}`, "deadbeef"))
	if rec.Code != http.StatusConflict {
		t.Fatalf("stale update status = %d, want 409; body=%s", rec.Code, rec.Body.String())
	}
	if got := getNoteViaAPI(t, "a.md").Content; got != "v0" {
		t.Fatalf("file was written despite conflict: %q", got)
	}

	rec = updateNoteViaAPI(t, "a.md",
		fmt.Sprintf(`{"content":"v1","tags":[],"baseRevision":%q}`, rev))
	if rec.Code != http.StatusOK {
		t.Fatalf("fresh update status = %d; body=%s", rec.Code, rec.Body.String())
	}
	var updated vaultfs.Note
	if err := json.Unmarshal(rec.Body.Bytes(), &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Revision == rev {
		t.Fatal("revision must change on a content change")
	}
	if got := getNoteViaAPI(t, "a.md").Content; got != "v1" {
		t.Fatalf("content after fresh update = %q", got)
	}
}

func TestTwoStaleClientsCannotOverwrite(t *testing.T) {
	apiNoteRepo(t)
	// Client A and B both read the same baseline.
	createNoteViaAPI(t, "a", "v0")
	base := currentRevision(t, "a.md")
	_ = getNoteViaAPI(t, "a.md")

	// B writes first, from the shared baseline.
	if rec := updateNoteViaAPI(t, "a.md",
		fmt.Sprintf(`{"content":"from-b","tags":[],"baseRevision":%q}`, base)); rec.Code != http.StatusOK {
		t.Fatalf("client B update status = %d; body=%s", rec.Code, rec.Body.String())
	}

	// A's write is now stale and must be rejected without touching the file.
	rec := updateNoteViaAPI(t, "a.md",
		fmt.Sprintf(`{"content":"from-a","tags":[],"baseRevision":%q}`, base))
	if rec.Code != http.StatusConflict {
		t.Fatalf("client A stale update status = %d, want 409; body=%s", rec.Code, rec.Body.String())
	}
	if got := getNoteViaAPI(t, "a.md").Content; got != "from-b" {
		t.Fatalf("content = %q, want from-b (client A overwrote)", got)
	}
}

func TestExternalModificationDetectedBetweenReadAndUpdate(t *testing.T) {
	apiNoteRepo(t)
	createNoteViaAPI(t, "a", "v0")
	base := currentRevision(t, "a.md")

	// An external editor rewrites the file behind the server's back.
	if err := os.WriteFile(filepath.Join(dataDir, "a.md"), []byte("external"), 0644); err != nil {
		t.Fatal(err)
	}

	// A write based on the stale revision is rejected.
	if rec := updateNoteViaAPI(t, "a.md",
		fmt.Sprintf(`{"content":"mine","tags":[],"baseRevision":%q}`, base)); rec.Code != http.StatusConflict {
		t.Fatalf("stale update status = %d, want 409; body=%s", rec.Code, rec.Body.String())
	}
	if got := getNoteViaAPI(t, "a.md").Content; got != "external" {
		t.Fatalf("external content was clobbered: %q", got)
	}

	// Without a base revision (legacy lenient path) the write still goes through.
	if rec := updateNoteViaAPI(t, "a.md", `{"content":"mine","tags":[]}`); rec.Code != http.StatusOK {
		t.Fatalf("lenient update status = %d; body=%s", rec.Code, rec.Body.String())
	}
}

func TestLegacyDeleteStaleRevisionConflicts(t *testing.T) {
	apiNoteRepo(t)
	createNoteViaAPI(t, "a", "v0")
	base := currentRevision(t, "a.md")

	req := httptest.NewRequest(http.MethodDelete, "/api/notes/a.md?baseRevision=deadbeef", nil)
	req.SetPathValue("path", "a.md")
	rec := httptest.NewRecorder()
	handleDeleteNote(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("stale delete status = %d, want 409; body=%s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dataDir, "a.md")); err != nil {
		t.Fatal("note was deleted despite a stale base revision")
	}

	req = httptest.NewRequest(http.MethodDelete, "/api/notes/a.md?baseRevision="+base, nil)
	req.SetPathValue("path", "a.md")
	rec = httptest.NewRecorder()
	handleDeleteNote(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("fresh delete status = %d; body=%s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dataDir, "a.md")); !os.IsNotExist(err) {
		t.Fatal("note still exists after delete")
	}
}

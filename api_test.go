package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
		if got := sanitizeUploadName(testCase.Input); got != testCase.Output {
			t.Errorf("sanitizeUploadName(%q) = %q, want %q", testCase.Input, got, testCase.Output)
		}
	}
}

func TestSharedTagSemantics(t *testing.T) {
	fixture := loadNoteSemanticsFixture(t)
	for _, want := range fixture.TagCases {
		got, _ := parseFrontMatter(buildFrontMatter(want) + "body")
		if len(got) != len(want) {
			t.Fatalf("tags = %#v, want %#v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("tags = %#v, want %#v", got, want)
			}
		}
	}
}

func TestFrontMatterTagsRoundTrip(t *testing.T) {
	want := []string{"one,two", `say "hi"`, `a\b`}
	tags, body := parseFrontMatter(buildFrontMatter(want) + "body")
	if body != "body" {
		t.Fatalf("body = %q, want body", body)
	}
	if len(tags) != len(want) {
		t.Fatalf("tags = %#v, want %#v", tags, want)
	}
	for i := range want {
		if tags[i] != want[i] {
			t.Fatalf("tags = %#v, want %#v", tags, want)
		}
	}
}

func TestUpdateNoteRenameDoesNotOverwrite(t *testing.T) {
	oldDataDir := dataDir
	dataDir = t.TempDir()
	t.Cleanup(func() { dataDir = oldDataDir })

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

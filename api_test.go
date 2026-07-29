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

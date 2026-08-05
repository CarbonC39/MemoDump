package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"memodump/internal/vaultfs"
)

func v2Create(t *testing.T, body string) noteDocumentV2 {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v2/notes", strings.NewReader(body))
	rec := httptest.NewRecorder()
	handleV2CreateNote(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d; body=%s", rec.Code, rec.Body.String())
	}
	var doc noteDocumentV2
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func v2NoteURL(path string) string {
	return "/api/v2/notes/" + url.PathEscape(path)
}

func v2Get(t *testing.T, path string) noteDocumentV2 {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, v2NoteURL(path), nil)
	req.SetPathValue("path", path)
	rec := httptest.NewRecorder()
	handleV2GetNote(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("get status = %d; body=%s", rec.Code, rec.Body.String())
	}
	var doc noteDocumentV2
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func v2Update(t *testing.T, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, v2NoteURL(path), strings.NewReader(body))
	req.SetPathValue("path", path)
	rec := httptest.NewRecorder()
	handleV2UpdateNote(rec, req)
	return rec
}

func TestV2CreateGetUpdateDeleteRoundTrip(t *testing.T) {
	apiNoteRepo(t)
	created := v2Create(t, `{"name":"a","content":"v0","tags":["x"]}`)
	if created.ID != "a.md" {
		t.Fatalf("id = %q", created.ID)
	}
	if created.Revision == "" {
		t.Fatal("create returned no revision")
	}
	if created.Content != "v0" || len(created.Tags) != 1 || created.Tags[0] != "x" {
		t.Fatalf("created doc = %#v", created)
	}

	got := v2Get(t, "a.md")
	if got.Revision != created.Revision {
		t.Fatalf("revision drift: got %q create %q", got.Revision, created.Revision)
	}

	rec := v2Update(t, "a.md",
		fmt.Sprintf(`{"content":"v1","tags":["y"],"baseRevision":%q}`, created.Revision))
	if rec.Code != http.StatusOK {
		t.Fatalf("update status = %d; body=%s", rec.Code, rec.Body.String())
	}
	var updated noteDocumentV2
	if err := json.Unmarshal(rec.Body.Bytes(), &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Revision == created.Revision {
		t.Fatal("revision must change after a content change")
	}
	if updated.Content != "v1" || len(updated.Tags) != 1 || updated.Tags[0] != "y" {
		t.Fatalf("updated doc = %#v", updated)
	}

	req := httptest.NewRequest(http.MethodDelete, v2NoteURL("a.md")+"?baseRevision="+updated.Revision, nil)
	req.SetPathValue("path", "a.md")
	rec = httptest.NewRecorder()
	handleV2DeleteNote(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete status = %d; body=%s", rec.Code, rec.Body.String())
	}
	req = httptest.NewRequest(http.MethodGet, v2NoteURL("a.md"), nil)
	req.SetPathValue("path", "a.md")
	rec = httptest.NewRecorder()
	handleV2GetNote(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("get after delete status = %d", rec.Code)
	}
}

func TestV2UpdateRequiresBaseRevision(t *testing.T) {
	apiNoteRepo(t)
	v2Create(t, `{"name":"a","content":"v0"}`)
	rec := v2Update(t, "a.md", `{"content":"v1"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "base_revision_required") {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

func TestV2UpdateStaleRevisionConflicts(t *testing.T) {
	apiNoteRepo(t)
	created := v2Create(t, `{"name":"a","content":"v0"}`)
	// Another client updates first.
	if rec := v2Update(t, "a.md",
		fmt.Sprintf(`{"content":"v1","baseRevision":%q}`, created.Revision)); rec.Code != http.StatusOK {
		t.Fatalf("first update status = %d; body=%s", rec.Code, rec.Body.String())
	}
	// The stale client is rejected without writing.
	rec := v2Update(t, "a.md",
		fmt.Sprintf(`{"content":"stale","baseRevision":%q}`, created.Revision))
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if code := body["error"].(map[string]any)["code"]; code != "local_revision_conflict" {
		t.Fatalf("error code = %v", code)
	}
	if got := v2Get(t, "a.md").Content; got != "v1" {
		t.Fatalf("content = %q, want v1", got)
	}
}

func TestV2DeleteRequiresBaseRevision(t *testing.T) {
	apiNoteRepo(t)
	v2Create(t, `{"name":"a","content":"v0"}`)
	req := httptest.NewRequest(http.MethodDelete, v2NoteURL("a.md"), nil)
	req.SetPathValue("path", "a.md")
	rec := httptest.NewRecorder()
	handleV2DeleteNote(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

func TestV2RenameAndMoveReturnPreviousID(t *testing.T) {
	apiNoteRepo(t)
	created := v2Create(t, `{"name":"a","content":"v0"}`)

	rec := v2Update(t, "a.md",
		fmt.Sprintf(`{"content":"v0","rename":"b","baseRevision":%q}`, created.Revision))
	if rec.Code != http.StatusOK {
		t.Fatalf("rename status = %d; body=%s", rec.Code, rec.Body.String())
	}
	var renamed noteDocumentV2
	if err := json.Unmarshal(rec.Body.Bytes(), &renamed); err != nil {
		t.Fatal(err)
	}
	if renamed.ID != "b.md" {
		t.Fatalf("renamed id = %q", renamed.ID)
	}
	if renamed.PreviousID != "a.md" {
		t.Fatalf("previousId = %q, want a.md", renamed.PreviousID)
	}

	// Move into a folder.
	req := httptest.NewRequest(http.MethodPut, "/api/v2/move/b.md",
		strings.NewReader(`{"destination":"proj"}`))
	req.SetPathValue("path", "b.md")
	rec = httptest.NewRecorder()
	handleV2MoveNote(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("move status = %d; body=%s", rec.Code, rec.Body.String())
	}
	var moved noteDocumentV2
	if err := json.Unmarshal(rec.Body.Bytes(), &moved); err != nil {
		t.Fatal(err)
	}
	if moved.ID != "proj/b.md" {
		t.Fatalf("moved id = %q", moved.ID)
	}
	if moved.PreviousID != "b.md" {
		t.Fatalf("move previousId = %q", moved.PreviousID)
	}
	if got := v2Get(t, "proj/b.md").Content; got != "v0" {
		t.Fatalf("moved content = %q", got)
	}
}

func TestV2Duplicate(t *testing.T) {
	apiNoteRepo(t)
	v2Create(t, `{"name":"a","content":"v0","tags":["x"]}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v2/duplicate/a.md", nil)
	req.SetPathValue("path", "a.md")
	rec := httptest.NewRecorder()
	handleV2DuplicateNote(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("duplicate status = %d; body=%s", rec.Code, rec.Body.String())
	}
	var dup noteDocumentV2
	if err := json.Unmarshal(rec.Body.Bytes(), &dup); err != nil {
		t.Fatal(err)
	}
	if dup.ID != "a (copy).md" {
		t.Fatalf("duplicate id = %q", dup.ID)
	}
	if dup.Revision == "" {
		t.Fatal("duplicate has no revision")
	}
	if got := v2Get(t, "a (copy).md"); got.Content != "v0" {
		t.Fatalf("duplicate content = %q", got.Content)
	}
}

func TestV2PreservesUnknownFrontMatter(t *testing.T) {
	apiNoteRepo(t)
	// A note with unknown front-matter keys, written behind the server's back.
	if err := os.WriteFile(filepath.Join(dataDir, "n.md"),
		[]byte("---\ncreated: 2024\n# c\ntags: [\"a\"]\n---\nbody"), 0644); err != nil {
		t.Fatal(err)
	}
	got := v2Get(t, "n.md")
	rec := v2Update(t, "n.md",
		fmt.Sprintf(`{"tags":["b"],"baseRevision":%q}`, got.Revision))
	if rec.Code != http.StatusOK {
		t.Fatalf("update status = %d; body=%s", rec.Code, rec.Body.String())
	}
	data, err := os.ReadFile(filepath.Join(dataDir, "n.md"))
	if err != nil {
		t.Fatal(err)
	}
	if want := "---\ncreated: 2024\n# c\ntags: [\"b\"]\n---\nbody"; string(data) != want {
		t.Fatalf("file = %q, want %q", data, want)
	}
}

func TestV2GetRejectsTraversal(t *testing.T) {
	apiNoteRepo(t)
	req := httptest.NewRequest(http.MethodGet, v2NoteURL("../../etc/passwd"), nil)
	req.SetPathValue("path", "../../etc/passwd")
	rec := httptest.NewRecorder()
	handleV2GetNote(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

var _ = vaultfs.ErrInvalidPath // keep the vaultfs import for error mapping tests

func TestV2UpdateWithDestinationMovesNote(t *testing.T) {
	apiNoteRepo(t)
	created := v2Create(t, `{"name":"a","content":"v0"}`)
	if err := repo.CreateFolder("proj"); err != nil {
		t.Fatal(err)
	}
	rec := v2Update(t, "a.md",
		fmt.Sprintf(`{"content":"v1","rename":"b","destination":"proj","baseRevision":%q}`, created.Revision))
	if rec.Code != http.StatusOK {
		t.Fatalf("update status = %d; body=%s", rec.Code, rec.Body.String())
	}
	var doc noteDocumentV2
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.ID != "proj/b.md" {
		t.Fatalf("id = %q, want proj/b.md", doc.ID)
	}
	if doc.PreviousID != "a.md" {
		t.Fatalf("previousId = %q", doc.PreviousID)
	}
	if doc.Content != "v1" {
		t.Fatalf("content = %q", doc.Content)
	}
	// Source must be gone.
	req := httptest.NewRequest(http.MethodGet, "/api/v2/notes/a.md", nil)
	req.SetPathValue("path", "a.md")
	rec = httptest.NewRecorder()
	handleV2GetNote(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("source still present (status %d)", rec.Code)
	}
}

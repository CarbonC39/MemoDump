package vaultfs

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func newTestRepo(t *testing.T) *Repository {
	t.Helper()
	r, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func readFile(t *testing.T, r *Repository, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(r.Root(), rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestCreateReadUpdateRoundTrip(t *testing.T) {
	r := newTestRepo(t)
	n, err := r.Create(CreateOptions{Name: "idea", Tags: []string{"project"}, Content: "# Idea\n"})
	if err != nil {
		t.Fatal(err)
	}
	if n.Path != "idea.md" {
		t.Fatalf("path = %q, want idea.md", n.Path)
	}
	if want := "---\ntags: [\"project\"]\n---\n# Idea\n"; readFile(t, r, "idea.md") != want {
		t.Fatalf("file bytes = %q, want %q", readFile(t, r, "idea.md"), want)
	}

	got, err := r.Get("idea.md", true)
	if err != nil {
		t.Fatal(err)
	}
	if got.Content != "# Idea\n" {
		t.Fatalf("content = %q", got.Content)
	}
	if got.Revision == "" {
		t.Fatal("revision empty")
	}

	// A content change must change the revision.
	upd, err := r.Update("idea.md", UpdateOptions{
		Content:      strPtr("# Idea 2\n"),
		Tags:         &[]string{"x"},
		BaseRevision: got.Revision,
	})
	if err != nil {
		t.Fatal(err)
	}
	if upd.Content != "# Idea 2\n" {
		t.Fatalf("updated content = %q", upd.Content)
	}
	if upd.Revision == got.Revision {
		t.Fatal("revision must change on a content change")
	}

	// A same-content rewrite may retain the same revision.
	same, err := r.Update("idea.md", UpdateOptions{
		Content:      strPtr("# Idea 2\n"),
		BaseRevision: upd.Revision,
	})
	if err != nil {
		t.Fatal(err)
	}
	if same.Revision != upd.Revision {
		t.Fatal("revision changed for an identical rewrite")
	}
}

func TestUpdateStaleRevisionConflictsWithoutWrite(t *testing.T) {
	r := newTestRepo(t)
	if _, err := r.Create(CreateOptions{Name: "a", Content: "v0"}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Update("a.md", UpdateOptions{Content: strPtr("x"), BaseRevision: "deadbeef"}); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("err = %v, want ErrRevisionConflict", err)
	}
	if got := readFile(t, r, "a.md"); got != "v0" {
		t.Fatalf("file was written despite conflict: %q", got)
	}
}

func TestConcurrentStaleUpdatesSerialize(t *testing.T) {
	r := newTestRepo(t)
	n, err := r.Create(CreateOptions{Name: "a", Content: "v0"})
	if err != nil {
		t.Fatal(err)
	}
	base := n.Revision

	var wg sync.WaitGroup
	var mu sync.Mutex
	conflicts := 0
	wins := 0
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := r.Update("a.md", UpdateOptions{
				Content:      strPtr(fmt.Sprintf("v%d", i)),
				BaseRevision: base,
			})
			mu.Lock()
			defer mu.Unlock()
			switch {
			case errors.Is(err, ErrRevisionConflict):
				conflicts++
			case err != nil:
				t.Errorf("update %d failed: %v", i, err)
			default:
				wins++
			}
		}(i)
	}
	wg.Wait()

	if wins != 1 {
		t.Fatalf("wins = %d, want 1", wins)
	}
	if conflicts != 3 {
		t.Fatalf("conflicts = %d, want 3", conflicts)
	}
	final, _ := r.Get("a.md", true)
	if final.Content != "v0" && final.Content != "v1" && final.Content != "v2" && final.Content != "v3" {
		t.Fatalf("final content = %q", final.Content)
	}
}

func TestDeleteRequiresCurrentRevision(t *testing.T) {
	r := newTestRepo(t)
	n, err := r.Create(CreateOptions{Name: "a", Content: "v0"})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Delete("a.md", "wrong"); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("err = %v, want ErrRevisionConflict", err)
	}
	if _, err := os.Stat(filepath.Join(r.Root(), "a.md")); err != nil {
		t.Fatal("note was deleted despite conflict")
	}
	if err := r.Delete("a.md", n.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(r.Root(), "a.md")); !os.IsNotExist(err) {
		t.Fatal("note still exists after delete")
	}
}

func TestUpdatePreservesUnknownFrontMatter(t *testing.T) {
	r := newTestRepo(t)
	markdown := "---\ncreated: 2024\n# c\ntags: [\"a\"]\n---\nbody"
	if err := os.WriteFile(filepath.Join(r.Root(), "n.md"), []byte(markdown), 0644); err != nil {
		t.Fatal(err)
	}
	n, err := r.Get("n.md", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Update("n.md", UpdateOptions{Tags: &[]string{"b"}, BaseRevision: n.Revision}); err != nil {
		t.Fatal(err)
	}
	want := "---\ncreated: 2024\n# c\ntags: [\"b\"]\n---\nbody"
	if got := readFile(t, r, "n.md"); got != want {
		t.Fatalf("file = %q, want %q", got, want)
	}
}

func TestUpdateUnsafeFrontMatterErrorsWithoutOverwrite(t *testing.T) {
	r := newTestRepo(t)
	markdown := "---\ntags:\n  - a\n---\nbody"
	if err := os.WriteFile(filepath.Join(r.Root(), "n.md"), []byte(markdown), 0644); err != nil {
		t.Fatal(err)
	}
	n, err := r.Get("n.md", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Update("n.md", UpdateOptions{Tags: &[]string{"b"}, BaseRevision: n.Revision}); !errors.Is(err, ErrFrontMatterNotEditable) {
		t.Fatalf("err = %v, want ErrFrontMatterNotEditable", err)
	}
	if got := readFile(t, r, "n.md"); got != markdown {
		t.Fatalf("file was overwritten: %q", got)
	}
}

func TestRenameConflictLeavesBothUntouched(t *testing.T) {
	r := newTestRepo(t)
	if _, err := r.Create(CreateOptions{Name: "source", Content: "src"}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Create(CreateOptions{Name: "target", Content: "tgt"}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Update("source.md", UpdateOptions{Rename: strPtr("target")}); !errors.Is(err, ErrNameConflict) {
		t.Fatalf("err = %v, want ErrNameConflict", err)
	}
	if got := readFile(t, r, "source.md"); got != "src" {
		t.Fatalf("source = %q", got)
	}
	if got := readFile(t, r, "target.md"); got != "tgt" {
		t.Fatalf("target = %q", got)
	}
}

func TestMoveConflictLeavesBothUntouched(t *testing.T) {
	r := newTestRepo(t)
	if err := r.CreateFolder("dest"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Create(CreateOptions{Name: "a", Folder: "dest", Content: "existing"}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Create(CreateOptions{Name: "a", Content: "source"}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Move("a.md", "dest"); !errors.Is(err, ErrNameConflict) {
		t.Fatalf("err = %v, want ErrNameConflict", err)
	}
	if got := readFile(t, r, "dest/a.md"); got != "existing" {
		t.Fatalf("dest/a.md = %q", got)
	}
	if got := readFile(t, r, "a.md"); got != "source" {
		t.Fatalf("a.md = %q", got)
	}
}

func TestDuplicateCopiesVerbatim(t *testing.T) {
	r := newTestRepo(t)
	markdown := "---\ntags: [\"a\"]\n---\n# body\n"
	if err := os.WriteFile(filepath.Join(r.Root(), "n.md"), []byte(markdown), 0644); err != nil {
		t.Fatal(err)
	}
	dup, err := r.Duplicate("n.md")
	if err != nil {
		t.Fatal(err)
	}
	if dup.Path != "n (copy).md" {
		t.Fatalf("duplicate path = %q", dup.Path)
	}
	if got := readFile(t, r, "n (copy).md"); got != markdown {
		t.Fatalf("duplicate bytes = %q", got)
	}
}

func TestApply(t *testing.T) {
	r := newTestRepo(t)
	n, err := r.Apply("x.md", "# X\n", "")
	if err != nil {
		t.Fatal(err)
	}
	if n.Content != "# X\n" {
		t.Fatalf("apply create content = %q", n.Content)
	}
	if _, err := r.Apply("x.md", "# Y\n", "wrong"); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("err = %v, want ErrRevisionConflict", err)
	}
	if got := readFile(t, r, "x.md"); got != "# X\n" {
		t.Fatalf("apply mismatch overwrote: %q", got)
	}
	n2, err := r.Apply("x.md", "# Y\n", n.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if n2.Content != "# Y\n" {
		t.Fatalf("apply replace content = %q", n2.Content)
	}
}

func TestPathTraversalRejected(t *testing.T) {
	r := newTestRepo(t)
	if _, err := r.Get("../escape.md", false); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("get err = %v, want ErrInvalidPath", err)
	}
	if _, err := r.Create(CreateOptions{Name: "ok", Folder: "../../x", Content: "x"}); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("create err = %v, want ErrInvalidPath", err)
	}
	if err := r.CreateFolder("nested/.images"); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("folder err = %v, want ErrInvalidPath", err)
	}
}

func TestSharedNameSemantics(t *testing.T) {
	data, err := os.ReadFile("../../testdata/contracts/note_semantics.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		NameCases []struct {
			Input  string `json:"input"`
			Output string `json:"output"`
		} `json:"nameCases"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	for _, tc := range fixture.NameCases {
		if got := SanitizeName(tc.Input); got != tc.Output {
			t.Errorf("SanitizeName(%q) = %q, want %q", tc.Input, got, tc.Output)
		}
	}
}

func TestFolderTreeHidesDotDirs(t *testing.T) {
	r := newTestRepo(t)
	if err := r.CreateFolder("visible"); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(r.Root(), ".images"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.Root(), ".images", "x.md"), []byte("hidden"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Create(CreateOptions{Name: "note", Folder: "visible", Content: "body"}); err != nil {
		t.Fatal(err)
	}
	tree := r.FolderTree()
	if len(tree) != 1 || tree[0].Name != "visible" {
		t.Fatalf("tree = %#v", tree)
	}
	if len(tree[0].Notes) != 1 || tree[0].Notes[0].Name != "note" {
		t.Fatalf("visible notes = %#v", tree[0].Notes)
	}
}

func strPtr(s string) *string { return &s }

func TestUpdateBodyEditPreservesFrontMatterBytes(t *testing.T) {
	r := newTestRepo(t)
	markdown := "---\r\ncreated: 2024\r\ntags: [\"a\"] # c\r\n---\r\nbody"
	if err := os.WriteFile(filepath.Join(r.Root(), "n.md"), []byte(markdown), 0644); err != nil {
		t.Fatal(err)
	}
	n, err := r.Get("n.md", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Update("n.md", UpdateOptions{Content: strPtr("new body"), BaseRevision: n.Revision}); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, r, "n.md"); got != "---\r\ncreated: 2024\r\ntags: [\"a\"] # c\r\n---\r\nnew body" {
		t.Fatalf("file = %q", got)
	}
}

func TestUpdateNoopKeepsBytesAndRevision(t *testing.T) {
	r := newTestRepo(t)
	markdown := "---\r\ncreated: 2024\r\ntags: [\"a\"] # c\r\n---\r\nbody"
	if err := os.WriteFile(filepath.Join(r.Root(), "n.md"), []byte(markdown), 0644); err != nil {
		t.Fatal(err)
	}
	n, err := r.Get("n.md", true)
	if err != nil {
		t.Fatal(err)
	}
	upd, err := r.Update("n.md", UpdateOptions{
		Tags:         &[]string{"a"},
		Content:      strPtr("body"),
		BaseRevision: n.Revision,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, r, "n.md"); got != markdown {
		t.Fatalf("no-op update rewrote bytes: %q", got)
	}
	if upd.Revision != n.Revision {
		t.Fatalf("no-op update changed revision: %q -> %q", n.Revision, upd.Revision)
	}
}

func TestUpdateRenameAndDestination(t *testing.T) {
	r := newTestRepo(t)
	// The destination folder is NOT pre-created: Update must make it.
	n, err := r.Create(CreateOptions{Name: "a", Content: "v0"})
	if err != nil {
		t.Fatal(err)
	}
	upd, err := r.Update("a.md", UpdateOptions{
		Rename:       strPtr("b"),
		Destination:  strPtr("dest"),
		Content:      strPtr("v1"),
		BaseRevision: n.Revision,
	})
	if err != nil {
		t.Fatal(err)
	}
	if upd.Path != "dest/b.md" {
		t.Fatalf("path = %q", upd.Path)
	}
	if _, err := os.Stat(filepath.Join(r.Root(), "a.md")); !os.IsNotExist(err) {
		t.Fatal("source still exists after rename+move")
	}
	if got := readFile(t, r, "dest/b.md"); got != "v1" {
		t.Fatalf("content = %q", got)
	}
}

func TestConcurrentDuplicatesPickDistinctNames(t *testing.T) {
	r := newTestRepo(t)
	if _, err := r.Create(CreateOptions{Name: "a", Content: "v0"}); err != nil {
		t.Fatal(err)
	}
	const n = 8
	var wg sync.WaitGroup
	paths := make([]string, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			dup, err := r.Duplicate("a.md")
			paths[i] = ""
			if err == nil {
				paths[i] = dup.Path
			}
			errs[i] = err
		}(i)
	}
	wg.Wait()
	seen := map[string]bool{}
	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Fatalf("duplicate %d: %v", i, errs[i])
		}
		if seen[paths[i]] {
			t.Fatalf("duplicate path %q returned twice", paths[i])
		}
		seen[paths[i]] = true
	}
}

func TestApplyRejectsOverwritingExistingNote(t *testing.T) {
	r := newTestRepo(t)
	if _, err := r.Apply("x.md", "# X\n", ""); err != nil {
		t.Fatal(err)
	}
	// create-if-absent on an existing note must conflict, never overwrite.
	if _, err := r.Apply("x.md", "# Y\n", ""); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("err = %v, want ErrRevisionConflict", err)
	}
	if got := readFile(t, r, "x.md"); got != "# X\n" {
		t.Fatalf("file was overwritten: %q", got)
	}
}

func TestExternalModificationBetweenReadAndUpdateDetected(t *testing.T) {
	r := newTestRepo(t)
	n, err := r.Create(CreateOptions{Name: "a", Content: "v0"})
	if err != nil {
		t.Fatal(err)
	}
	// An external editor rewrites the file between the client's read and save.
	if err := os.WriteFile(filepath.Join(r.Root(), "a.md"), []byte("external"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Update("a.md", UpdateOptions{Content: strPtr("mine"), BaseRevision: n.Revision}); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("err = %v, want ErrRevisionConflict", err)
	}
	if got := readFile(t, r, "a.md"); got != "external" {
		t.Fatalf("external content was clobbered: %q", got)
	}
}

func TestApplyAndEditorSaveRace(t *testing.T) {
	r := newTestRepo(t)
	n, err := r.Apply("a.md", "v0", "")
	if err != nil {
		t.Fatal(err)
	}
	base := n.Revision

	// A remote-style Apply and an editor save (Update) race from the same
	// baseline: exactly one may win; every other attempt must conflict.
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins := 0
	conflicts := 0
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, err := r.Apply("a.md", "from-apply", base)
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				wins++
			} else if errors.Is(err, ErrRevisionConflict) {
				conflicts++
			} else {
				t.Errorf("apply: %v", err)
			}
		}()
		go func() {
			defer wg.Done()
			_, err := r.Update("a.md", UpdateOptions{Content: strPtr("from-editor"), BaseRevision: base})
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				wins++
			} else if errors.Is(err, ErrRevisionConflict) {
				conflicts++
			} else {
				t.Errorf("update: %v", err)
			}
		}()
	}
	wg.Wait()

	if wins != 1 {
		t.Fatalf("wins = %d, want exactly 1 (Apply and editor save must not both succeed)", wins)
	}
	if conflicts != 15 {
		t.Fatalf("conflicts = %d, want 15", conflicts)
	}
	final, _ := r.Get("a.md", true)
	if final.Content != "from-apply" && final.Content != "from-editor" {
		t.Fatalf("final content = %q", final.Content)
	}
}

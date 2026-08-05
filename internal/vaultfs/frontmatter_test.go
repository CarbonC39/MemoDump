package vaultfs

import (
	"encoding/json"
	"os"
	"testing"
)

// frontmatterFixture is the shared contract consumed by the Go and (later)
// TypeScript implementations. It lives in testdata/contracts so both engines
// assert on the same preservation semantics.
type frontmatterFixture struct {
	TagEditCases []struct {
		Name     string   `json:"name"`
		Markdown string   `json:"markdown"`
		NewTags  []string `json:"newTags"`
		Expected string   `json:"expected"`
	} `json:"tagEditCases"`
	ErrorCases []struct {
		Name     string   `json:"name"`
		Markdown string   `json:"markdown"`
		NewTags  []string `json:"newTags"`
	} `json:"errorCases"`
}

func loadFrontmatterFixture(t *testing.T) frontmatterFixture {
	t.Helper()
	data, err := os.ReadFile("../../testdata/contracts/frontmatter.json")
	if err != nil {
		t.Fatal(err)
	}
	var f frontmatterFixture
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestFrontMatterTagEditsPreserveUnknownContent(t *testing.T) {
	f := loadFrontmatterFixture(t)
	for _, tc := range f.TagEditCases {
		t.Run(tc.Name, func(t *testing.T) {
			doc := ParseDocument(tc.Markdown)
			got, err := doc.WithTags(tc.NewTags)
			if err != nil {
				t.Fatalf("WithTags(%v) error: %v", tc.NewTags, err)
			}
			if got != tc.Expected {
				t.Errorf("WithTags mismatch:\n got %q\nwant %q", got, tc.Expected)
			}
		})
	}
}

func TestFrontMatterUnsafeTagsReturnError(t *testing.T) {
	f := loadFrontmatterFixture(t)
	for _, tc := range f.ErrorCases {
		t.Run(tc.Name, func(t *testing.T) {
			doc := ParseDocument(tc.Markdown)
			if _, err := doc.WithTags(tc.NewTags); err != ErrFrontMatterNotEditable {
				t.Errorf("WithTags(%v) error = %v, want ErrFrontMatterNotEditable", tc.NewTags, err)
			}
		})
	}
}

func TestParseDocumentBasics(t *testing.T) {
	d := ParseDocument("hello")
	if d.FrontMatter != "" || d.Body != "hello" || d.Tags != nil {
		t.Fatalf("no-front-matter doc = %#v", d)
	}

	d = ParseDocument("---\ntags: [\"a\"]\n---\nbody")
	if len(d.Tags) != 1 || d.Tags[0] != "a" || d.Body != "body" || d.FrontMatter != `tags: ["a"]` {
		t.Fatalf("front-matter doc = %#v", d)
	}

	d = ParseDocument("---\r\ntags: [\"a\"]\r\n---\r\nbody")
	if len(d.Tags) != 1 || d.Tags[0] != "a" || d.Body != "body" {
		t.Fatalf("CRLF doc = %#v", d)
	}

	d = ParseDocument("---\ntags: [\"a\"]")
	if d.Body != "---\ntags: [\"a\"]" || d.FrontMatter != "" {
		t.Fatalf("unterminated front matter = %#v", d)
	}
}

func TestRevisionStability(t *testing.T) {
	rev := RevisionOfBytes([]byte("hello"))
	if rev == "" {
		t.Fatal("empty revision")
	}
	if RevisionOfBytes([]byte("hello")) != rev {
		t.Fatal("revision not stable for identical bytes")
	}
	if RevisionOfBytes([]byte("hellp")) == rev {
		t.Fatal("revision stable across a content change")
	}
}

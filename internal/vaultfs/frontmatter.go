package vaultfs

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ErrFrontMatterNotEditable reports a front matter block that cannot be modified
// in place without risking corruption (for example a block-style or multi-line
// tags value). Callers must surface it as a clear error instead of rebuilding
// the document and overwriting unknown content.
var ErrFrontMatterNotEditable = errors.New("front matter cannot be edited safely")

// Document is a parsed Markdown note split into YAML front matter and body.
// FrontMatter is the raw text between the fences (fences excluded, LF line
// endings) and is preserved verbatim — including unknown keys, ordering, and
// comments — when tags are edited.
type Document struct {
	FrontMatter string
	Body        string
	Tags        []string
}

// ParseDocument splits full Markdown into front matter and body. When the
// document has no leading front matter, the whole content is the body and
// FrontMatter is empty. Line endings inside the front matter block are
// normalized to LF; the body is preserved verbatim.
func ParseDocument(markdown string) *Document {
	doc := &Document{Body: markdown}
	if !strings.HasPrefix(markdown, "---\n") && !strings.HasPrefix(markdown, "---\r\n") {
		return doc
	}

	// Skip the opening fence.
	rest := markdown
	if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
		rest = rest[nl+1:]
	}

	// Scan for the closing fence, collecting the raw front matter lines.
	var rawLines []string
	idx := 0
	var nl int
	for {
		nl = strings.IndexByte(rest[idx:], '\n')
		if nl < 0 {
			// Unterminated front matter: the whole content is treated as body,
			// matching the previous parser.
			return doc
		}
		line := rest[idx : idx+nl]
		if strings.TrimSuffix(line, "\r") == "---" {
			break
		}
		rawLines = append(rawLines, strings.TrimSuffix(line, "\r"))
		idx += nl + 1
	}

	doc.FrontMatter = strings.Join(rawLines, "\n")
	doc.Body = rest[idx+nl+1:]
	doc.Tags = parseTags(doc.FrontMatter)
	return doc
}

// parseTags extracts tags from a front matter block, mirroring the historical
// parser: a JSON-compatible array is decoded first so commas, quotes and
// backslashes inside tags round-trip; unquoted `tags: [a, b]` is the fallback.
func parseTags(frontMatter string) []string {
	if frontMatter == "" {
		return nil
	}
	for _, line := range strings.Split(frontMatter, "\n") {
		if !strings.HasPrefix(line, "tags:") {
			continue
		}
		val := strings.TrimSpace(line[len("tags:"):])
		var tags []string
		if err := json.Unmarshal([]byte(val), &tags); err != nil {
			// Backward compatibility with older unquoted `tags: [a, b]` files.
			val = strings.TrimPrefix(val, "[")
			val = strings.TrimSuffix(val, "]")
			for tag := range strings.SplitSeq(val, ",") {
				tag = strings.Trim(strings.TrimSpace(tag), `"'`)
				if tag != "" {
					tags = append(tags, tag)
				}
			}
		}
		return tags
	}
	return nil
}

// serializeTags renders the `tags:` line in the JSON-compatible array form the
// historical buildFrontMatter produced.
func serializeTags(tags []string) string {
	var sb strings.Builder
	sb.WriteString("tags: [")
	for i, tag := range tags {
		if i > 0 {
			sb.WriteString(", ")
		}
		fmt.Fprintf(&sb, "%q", tag)
	}
	sb.WriteString("]")
	return sb.String()
}

// FrontMatterPartWithTags returns the front-matter prefix for the document with
// `tags` applied — either "---\n…\n---\n" or "" when the document should have
// none. Unknown front-matter keys, ordering, and comments are preserved
// byte-for-byte. It returns ErrFrontMatterNotEditable when the tags value
// cannot be replaced in place safely (duplicate tags keys, block-style or
// multi-line tags values).
func (d *Document) FrontMatterPartWithTags(tags []string) (string, error) {
	if d.FrontMatter == "" {
		if len(tags) == 0 {
			return "", nil
		}
		return "---\n" + serializeTags(tags) + "\n---\n", nil
	}

	lines := strings.Split(d.FrontMatter, "\n")
	tagsIdx := -1
	for i, line := range lines {
		if !strings.HasPrefix(line, "tags:") {
			continue
		}
		if tagsIdx != -1 {
			// Duplicate tags keys: editing one leaves an ambiguous shadow key.
			return "", ErrFrontMatterNotEditable
		}
		tagsIdx = i
	}

	if tagsIdx >= 0 {
		// A bare "tags:" followed by further lines is a block-style list whose
		// continuation cannot be replaced line-wise without orphaning it.
		value := strings.TrimSpace(lines[tagsIdx][len("tags:"):])
		if value == "" && tagsIdx+1 < len(lines) {
			return "", ErrFrontMatterNotEditable
		}
		if value != "" && (strings.HasPrefix(value, "|") || strings.HasPrefix(value, ">")) {
			return "", ErrFrontMatterNotEditable
		}
		if len(tags) == 0 {
			lines = append(lines[:tagsIdx], lines[tagsIdx+1:]...)
		} else {
			lines[tagsIdx] = serializeTags(tags)
		}
	} else if len(tags) != 0 {
		// No tags key present: insert one at the top, preserving the order of
		// every existing key.
		lines = append([]string{serializeTags(tags)}, lines...)
	}

	// A front matter block left with nothing in it is dropped entirely, which
	// matches the historical behavior of buildFrontMatter(nil) returning "".
	hasContent := false
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			hasContent = true
			break
		}
	}
	if !hasContent {
		return "", nil
	}
	return "---\n" + strings.Join(lines, "\n") + "\n---\n", nil
}

// WithTags returns the full canonical document with `tags` applied, preserving
// unknown front-matter keys, ordering, and comments.
func (d *Document) WithTags(tags []string) (string, error) {
	prefix, err := d.FrontMatterPartWithTags(tags)
	if err != nil {
		return "", err
	}
	return prefix + d.Body, nil
}

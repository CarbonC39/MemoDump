package vaultfs

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ErrFrontMatterNotEditable reports a front matter block whose tags value
// cannot be replaced in place safely (duplicate tags keys, block-style or
// unterminated multi-line values). Callers must surface it as a clear error
// instead of rebuilding the document and overwriting unknown content.
var ErrFrontMatterNotEditable = errors.New("front matter cannot be edited safely")

// Document is a parsed Markdown note split into YAML front matter and body.
// RawPrefix is the verbatim front-matter block including both fences and their
// original line endings ("" when there is no front matter); FrontMatter is the
// text between the fences, verbatim. Editing a document preserves everything
// outside the replaced tags value byte-for-byte — including unknown keys,
// ordering, comments, and CRLF line endings.
type Document struct {
	RawPrefix   string
	FrontMatter string
	Body        string
	Tags        []string
}

// ParseDocument splits full Markdown into front matter and body. When the
// document has no leading front matter, the whole content is the body.
func ParseDocument(markdown string) *Document {
	if !strings.HasPrefix(markdown, "---\n") && !strings.HasPrefix(markdown, "---\r\n") {
		return &Document{Body: markdown}
	}

	openEnd := len("---\n")
	if strings.HasPrefix(markdown, "---\r\n") {
		openEnd = len("---\r\n")
	}

	// Scan for the closing fence (a line exactly "---"), tracking absolute
	// byte offsets into markdown.
	fmEnd := -1
	pos := openEnd
	var nl int
	for {
		nl = strings.IndexByte(markdown[pos:], '\n')
		if nl < 0 {
			// Unterminated front matter: the whole content is treated as body.
			return &Document{Body: markdown}
		}
		if strings.TrimSuffix(markdown[pos:pos+nl], "\r") == "---" {
			fmEnd = pos
			break
		}
		pos += nl + 1
	}
	if fmEnd < 0 {
		return &Document{Body: markdown}
	}

	closeEnd := fmEnd + nl + 1 // after the closing fence's line ending
	return &Document{
		RawPrefix:   markdown[:closeEnd],
		FrontMatter: markdown[openEnd:fmEnd],
		Body:        markdown[closeEnd:],
		Tags:        parseTags(markdown[openEnd:fmEnd]),
	}
}

// tagsSpan locates a tags key in a verbatim front-matter block and the byte
// span of its value. valueStart..valueEnd is the value to replace (empty when
// the key has no value, e.g. `tags:` or `tags: # comment`); for a flow
// sequence it spans every line through the closing "]". tagsLineStart == -1
// means no tags key is present.
type tagsSpan struct {
	tagsLineStart int
	tagsLineEnd   int
	valueStart    int
	valueEnd      int
}

// findTagsSpan is the single source of truth for both parsing and in-place
// editing: it finds the tags key (rejecting duplicates) and the exact byte span
// of its value, handling trailing comments and multi-line flow sequences. It
// returns ErrFrontMatterNotEditable for blocks that cannot be safely edited.
func findTagsSpan(frontMatter string) (tagsSpan, error) {
	span := tagsSpan{tagsLineStart: -1}
	pos := 0
	for {
		nl := strings.IndexByte(frontMatter[pos:], '\n')
		var lineEnd int
		if nl < 0 {
			lineEnd = len(frontMatter)
		} else {
			lineEnd = pos + nl
		}
		if strings.HasPrefix(strings.TrimSuffix(frontMatter[pos:lineEnd], "\r"), "tags:") {
			if span.tagsLineStart != -1 {
				return span, ErrFrontMatterNotEditable // duplicate tags keys
			}
			span.tagsLineStart, span.tagsLineEnd = pos, lineEnd
		}
		if nl < 0 {
			break
		}
		pos = lineEnd + 1
	}
	if span.tagsLineStart == -1 {
		return span, nil
	}

	// valueStart: the first non-space byte after "tags:".
	span.valueStart = span.tagsLineStart + len("tags:")
	for span.valueStart < span.tagsLineEnd &&
		(frontMatter[span.valueStart] == ' ' || frontMatter[span.valueStart] == '\t') {
		span.valueStart++
	}

	// Empty value (`tags:` alone or `tags: # comment`).
	if span.valueStart >= span.tagsLineEnd || frontMatter[span.valueStart] == '#' {
		if span.valueStart >= span.tagsLineEnd && span.tagsLineEnd < len(frontMatter) {
			// A bare "tags:" followed by more content is a block-style list.
			return span, ErrFrontMatterNotEditable
		}
		span.valueEnd = span.valueStart
		return span, nil
	}

	// Block scalar headers continue on following lines; they cannot be replaced.
	if frontMatter[span.valueStart] == '|' || frontMatter[span.valueStart] == '>' {
		return span, ErrFrontMatterNotEditable
	}

	if frontMatter[span.valueStart] == '[' {
		end, ok := flowSeqEnd(frontMatter, span.valueStart)
		if !ok {
			return span, ErrFrontMatterNotEditable // unterminated flow sequence
		}
		span.valueEnd = end + 1
		return span, nil
	}
	span.valueEnd = scalarCommentStart(frontMatter, span.valueStart, span.tagsLineEnd)
	return span, nil
}

// parseTags extracts tags from a front matter block using the same value span
// the editor edits, so a trailing comment or a multi-line flow sequence parses
// identically to how it is rewritten. A JSON-compatible array is decoded first
// so commas, quotes and backslashes inside tags round-trip; unquoted
// `tags: [a, b]` is the fallback.
func parseTags(frontMatter string) []string {
	span, err := findTagsSpan(frontMatter)
	if err != nil || span.tagsLineStart == -1 {
		return nil
	}
	val := frontMatter[span.valueStart:span.valueEnd]
	var tags []string
	if err := json.Unmarshal([]byte(val), &tags); err == nil {
		return tags
	}
	// Backward compatibility with older unquoted `tags: [a, b]` files.
	val = strings.Trim(val, "[]")
	var out []string
	for tag := range strings.SplitSeq(val, ",") {
		tag = strings.Trim(strings.TrimSpace(tag), `"'`)
		if tag != "" {
			out = append(out, tag)
		}
	}
	return out
}

// serializeTags renders the `tags:` line in the JSON-compatible array form the
// historical buildFrontMatter produced.
func serializeTags(tags []string) string {
	return "tags: " + serializeTagsValue(tags)
}

// serializeTagsValue renders just the JSON-compatible array value (`["a", "b"]`)
// for replacing the tags value span in place.
func serializeTagsValue(tags []string) string {
	var sb strings.Builder
	sb.WriteString("[")
	for i, tag := range tags {
		if i > 0 {
			sb.WriteString(", ")
		}
		fmt.Fprintf(&sb, "%q", tag)
	}
	sb.WriteString("]")
	return sb.String()
}

func tagsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// FrontMatterPartWithTags returns the front-matter prefix ("---\n…\n---\n", or
// "" when the document should have none) with `tags` applied. When tags are
// unchanged, the original prefix is returned verbatim — including CRLF and any
// formatting quirks. Otherwise only the tags value's byte span is replaced.
func (d *Document) FrontMatterPartWithTags(tags []string) (string, error) {
	if d.RawPrefix == "" {
		if len(tags) == 0 {
			return "", nil
		}
		return "---\n" + serializeTags(tags) + "\n---\n", nil
	}
	if tagsEqual(d.Tags, tags) {
		return d.RawPrefix, nil
	}

	edited, err := editTagsInFrontMatter(d.FrontMatter, tags)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(edited) == "" {
		// Clearing tags left nothing in the front matter: drop it entirely.
		return "", nil
	}
	openLen := len("---\n")
	if strings.HasPrefix(d.RawPrefix, "---\r\n") {
		openLen = len("---\r\n")
	}
	// Splice the edited front matter between the original fences, preserving
	// their line endings.
	return d.RawPrefix[:openLen] + edited + d.RawPrefix[openLen+len(d.FrontMatter):], nil
}

// WithTags returns the full canonical document with `tags` applied.
func (d *Document) WithTags(tags []string) (string, error) {
	prefix, err := d.FrontMatterPartWithTags(tags)
	if err != nil {
		return "", err
	}
	return prefix + d.Body, nil
}

// editTagsInFrontMatter replaces the tags value span in a verbatim front-matter
// block. Everything outside that span is preserved byte-for-byte.
func editTagsInFrontMatter(frontMatter string, newTags []string) (string, error) {
	if frontMatter == "" {
		if len(newTags) == 0 {
			return "", nil
		}
		return serializeTags(newTags) + "\n", nil
	}
	eol := "\n"
	if strings.Contains(frontMatter, "\r\n") {
		eol = "\r\n"
	}

	span, err := findTagsSpan(frontMatter)
	if err != nil {
		return "", err
	}
	if span.tagsLineStart == -1 {
		// No tags key present: insert one at the top.
		if len(newTags) == 0 {
			return frontMatter, nil
		}
		return serializeTags(newTags) + eol + frontMatter, nil
	}

	if len(newTags) == 0 {
		// Clearing removes the whole tags construct — the key, every line of a
		// multi-line flow value, any trailing comment, and the line ending.
		removeEnd := span.valueEnd
		if nl := strings.IndexByte(frontMatter[removeEnd:], '\n'); nl >= 0 {
			removeEnd += nl + 1
		} else {
			removeEnd = len(frontMatter)
		}
		return frontMatter[:span.tagsLineStart] + frontMatter[removeEnd:], nil
	}

	// Setting replaces only the value span (single- or multi-line), preserving
	// everything around it including any trailing comment. When the value was
	// empty and a comment follows (`tags: # c`), keep a space before it.
	suffix := frontMatter[span.valueEnd:]
	if span.valueEnd < len(frontMatter) && frontMatter[span.valueEnd] == '#' {
		suffix = " " + suffix
	}
	return frontMatter[:span.valueStart] + serializeTagsValue(newTags) + suffix, nil
}

// flowSeqEnd finds the index of the "]" that closes a flow sequence starting
// at start, respecting quotes. "[" / "]" pairs may nest.
func flowSeqEnd(s string, start int) (int, bool) {
	depth := 0
	inSingle, inDouble := false, false
	for i := start; i < len(s); i++ {
		ch := s[i]
		switch {
		case inDouble:
			if ch == '\\' {
				i++
				continue
			}
			if ch == '"' {
				inDouble = false
			}
		case inSingle:
			if ch == '\\' {
				i++
				continue
			}
			if ch == '\'' {
				inSingle = false
			}
		case ch == '"':
			inDouble = true
		case ch == '\'':
			inSingle = true
		case ch == '#':
			// A comment (only recognized after whitespace) runs to end of line.
			if i > start && (s[i-1] == ' ' || s[i-1] == '\t') {
				if nl := strings.IndexByte(s[i:], '\n'); nl >= 0 {
					i += nl
				} else {
					return 0, false
				}
			}
		case ch == '[':
			depth++
		case ch == ']':
			depth--
			if depth == 0 {
				return i, true
			}
		}
	}
	return 0, false
}

// scalarCommentStart returns the index of the first unquoted " #" comment
// within [start, lineEnd), or lineEnd when there is none.
func scalarCommentStart(s string, start, lineEnd int) int {
	inSingle, inDouble := false, false
	for i := start; i < lineEnd; i++ {
		ch := s[i]
		switch {
		case inDouble:
			if ch == '\\' {
				i++
				continue
			}
			if ch == '"' {
				inDouble = false
			}
		case inSingle:
			if ch == '\\' {
				i++
				continue
			}
			if ch == '\'' {
				inSingle = false
			}
		case ch == '"':
			inDouble = true
		case ch == '\'':
			inSingle = true
		case ch == '#':
			if i > start && (s[i-1] == ' ' || s[i-1] == '\t') {
				return i
			}
		}
	}
	return lineEnd
}

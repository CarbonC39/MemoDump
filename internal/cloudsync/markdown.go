package cloudsync

import "strings"

// NormalizeMarkdown converts CRLF and bare CR line endings to LF, the canonical
// form used at the local repository boundary before hashing or serializing.
func NormalizeMarkdown(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "\n")
}

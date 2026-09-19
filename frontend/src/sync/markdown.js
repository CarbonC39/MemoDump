// Markdown line-ending normalization for the browser sync port (R6.1). Matches
// internal/cloudsync/markdown.go: CRLF and bare CR become LF, the canonical form
// used at the wire boundary before hashing or serializing.
export function normalizeMarkdown(s) {
  s = s.replace(/\r\n/g, '\n')
  return s.replace(/\r/g, '\n')
}

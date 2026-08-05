// Canonical markdown normalization mirroring internal/cloudsync/markdown.go.

/** Converts CRLF and bare CR line endings to LF, the canonical boundary form. */
export function normalizeMarkdown(s: string): string {
  return s.replace(/\r\n/g, '\n').replace(/\r/g, '\n')
}

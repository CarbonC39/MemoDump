/**
 * Strip markdown symbols for plain text preview
 */
export function stripMarkdown(text) {
  if (!text) return ''
  return text
    .replace(/^#{1,6}\s+/gm, '')              // headings
    .replace(/<(https?:\/\/[^>\s]+)>/g, '$1') // autolinks <url> → bare url
    .replace(/<[^>]*>/g, '')                  // HTML tags
    .replace(/```[\s\S]*?```/g, '')           // fenced code blocks
    .replace(/!\[.*?\]\(.*?\)/g, '')          // images
    .replace(/\[([^\]]+)\]\(.*?\)/g, '$1')    // links — keep link text, drop URL
    .replace(/^\s*[-*+]\s+\[[ xX]\]\s*/gm, '') // task list items: - [ ] / - [x]
    // Underscore emphasis: only strip when delimiters are at word boundaries,
    // so URLs/identifiers like foo_bar or example.com/a_b keep their underscores.
    .replace(/(^|[^\w])__(\S(?:[^_\n]*?\S)?)__(?=[^\w]|$)/g, '$1$2')
    .replace(/(^|[^\w])_(\S(?:[^_\n]*?\S)?)_(?=[^\w]|$)/g, '$1$2')
    .replace(/[*~`]/g, '')                    // bold/italic asterisks, strikethrough, inline code
    .replace(/^\s*>\s*/gm, '')                // blockquote markers
    .replace(/^\s*[-+*]\s+/gm, '')            // unordered list bullets
    .replace(/^\s*\d+\.\s+/gm, '')            // ordered list
    .replace(/^---+$/gm, '')                  // hr
    .replace(/[ \t\r]+/g, ' ')                // collapse horizontal whitespace
    .replace(/\n+/g, '\n')                    // collapse multiple blank lines
    .trim()
}

/**
 * Check if a note name is a timestamp (e.g., "2025-01-15_123456")
 */
export function isTimestampName(name) {
  return /^\d{4}-\d{2}-\d{2}_\d{6}/.test(name)
}

import { unified } from 'unified'
import remarkParse from 'remark-parse'
import remarkGfm from 'remark-gfm'

const previewParser = unified().use(remarkParse).use(remarkGfm)
const previewBlockNodes = new Set([
  'paragraph', 'heading', 'blockquote', 'list', 'listItem', 'table', 'tableRow',
  'code', 'html', 'thematicBreak',
])
const ignoredPreviewNodes = new Set([
  'code', 'html', 'image', 'imageReference', 'definition', 'thematicBreak',
])

function collectPreviewText(node, output) {
  if (node.type === 'text' || node.type === 'inlineCode') {
    output.push(node.value || '')
    return
  }
  if (node.type === 'break') {
    output.push('\n')
    return
  }
  if (ignoredPreviewNodes.has(node.type)) {
    if (previewBlockNodes.has(node.type)) output.push('\n')
    return
  }

  for (const child of node.children || []) collectPreviewText(child, output)
  if (previewBlockNodes.has(node.type)) output.push('\n')
}

/**
 * Convert Markdown into the visible plain text used by note previews.
 * Parsing first means plain angle-bracket text is not confused with HTML.
 */
export function stripMarkdown(text) {
  if (!text) return ''
  const output = []
  collectPreviewText(previewParser.parse(text), output)
  return output.join('')
    .replace(/[ \t\r]+/g, ' ')                // collapse horizontal whitespace
    .replace(/ *\n */g, '\n')                 // trim around line boundaries
    .replace(/\n+/g, '\n')                    // collapse multiple blank lines
    .trim()
}

/**
 * Check if a note name is a timestamp (e.g., "2025-01-15_123456")
 */
export function isTimestampName(name) {
  return /^\d{4}-\d{2}-\d{2}_\d{6}/.test(name)
}

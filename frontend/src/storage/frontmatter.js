// Browser mirror of the Go front-matter editor in internal/vaultfs.
//
// A parsed document keeps the verbatim front-matter block (both fences and
// their original line endings) in `rawPrefix`. Editing replaces only the tags
// value's byte span: when tags are unchanged the whole prefix is reused
// byte-for-byte, and a tag edit preserves unknown keys, ordering, comments,
// and CRLF line endings. This is the same contract the Go server enforces and
// both engines are tested against testdata/contracts/frontmatter.json.

/** Thrown when a front matter block cannot be modified in place safely. */
export class FrontMatterNotEditable extends Error {
  constructor(message) {
    super(message)
    this.name = 'FrontMatterNotEditable'
  }
}

/**
 * Parses a full Markdown document into a verbatim front-matter prefix (fences
 * included), the front matter between the fences, the body, and tags.
 */
export function parseDocument(markdown) {
  if (!markdown.startsWith('---\n') && !markdown.startsWith('---\r\n')) {
    return { rawPrefix: '', frontMatter: '', body: markdown, tags: [] }
  }
  const openEnd = markdown.startsWith('---\r\n') ? 5 : 4
  let fmEnd = -1
  let pos = openEnd
  let nl = 0
  while (true) {
    nl = markdown.indexOf('\n', pos)
    if (nl < 0) {
      // Unterminated front matter: the whole content is the body.
      return { rawPrefix: '', frontMatter: '', body: markdown, tags: [] }
    }
    if (markdown.slice(pos, nl).replace(/\r$/, '') === '---') {
      fmEnd = pos
      break
    }
    pos = nl + 1
  }
  // nl is the absolute index of the closing fence's newline (indexOf returns
  // absolute positions), so the body starts just after it.
  const closeEnd = nl + 1
  const frontMatter = markdown.slice(openEnd, fmEnd)
  return {
    rawPrefix: markdown.slice(0, closeEnd),
    frontMatter,
    body: markdown.slice(closeEnd),
    tags: parseTags(frontMatter),
  }
}

// findTagsSpan is the single source of truth for both parsing and in-place
// editing: it locates the tags key (rejecting duplicates) and the exact span of
// its value, handling trailing comments and multi-line flow sequences. It
// throws FrontMatterNotEditable for blocks that cannot be safely edited.
function findTagsSpan(frontMatter) {
  const span = { tagsLineStart: -1, tagsLineEnd: -1, valueStart: -1, valueEnd: -1 }
  if (!frontMatter) return span
  let pos = 0
  while (true) {
    const nl = frontMatter.indexOf('\n', pos)
    const lineEnd = nl < 0 ? frontMatter.length : nl
    if (frontMatter.slice(pos, lineEnd).replace(/\r$/, '').startsWith('tags:')) {
      if (span.tagsLineStart !== -1) throw new FrontMatterNotEditable('duplicate tags keys')
      span.tagsLineStart = pos
      span.tagsLineEnd = lineEnd
    }
    if (nl < 0) break
    pos = lineEnd + 1
  }
  if (span.tagsLineStart === -1) return span

  span.valueStart = span.tagsLineStart + 'tags:'.length
  while (span.valueStart < span.tagsLineEnd && (frontMatter[span.valueStart] === ' ' || frontMatter[span.valueStart] === '\t')) {
    span.valueStart++
  }

  // Empty value (`tags:` alone or `tags: # comment`).
  if (span.valueStart >= span.tagsLineEnd || frontMatter[span.valueStart] === '#') {
    if (span.valueStart >= span.tagsLineEnd && span.tagsLineEnd < frontMatter.length) {
      throw new FrontMatterNotEditable('block-style tags')
    }
    span.valueEnd = span.valueStart
    return span
  }
  if (frontMatter[span.valueStart] === '|' || frontMatter[span.valueStart] === '>') {
    throw new FrontMatterNotEditable('block scalar tags')
  }
  if (frontMatter[span.valueStart] === '[') {
    const end = flowSeqEnd(frontMatter, span.valueStart)
    if (end < 0) throw new FrontMatterNotEditable('unterminated flow sequence')
    span.valueEnd = end + 1
    return span
  }
  span.valueEnd = scalarCommentStart(frontMatter, span.valueStart, span.tagsLineEnd)
  return span
}

// parseTags extracts tags using the same value span the editor edits, so a
// trailing comment or a multi-line flow sequence parses identically to how it
// is rewritten. A JSON-compatible array is decoded first; unquoted
// `tags: [a, b]` is the fallback.
function parseTags(frontMatter) {
  let span
  try { span = findTagsSpan(frontMatter) } catch (_) { return [] }
  if (span.tagsLineStart === -1) return []
  const val = frontMatter.slice(span.valueStart, span.valueEnd)
  try {
    const parsed = JSON.parse(val)
    if (Array.isArray(parsed)) return parsed.map(String)
  } catch (_) { /* fall through to the unquoted form */ }
  const tags = []
  for (const raw of val.replace(/^\[/, '').replace(/\]$/, '').split(',')) {
    const tag = raw.trim().replace(/^['"]|['"]$/g, '')
    if (tag) tags.push(tag)
  }
  return tags
}

/** Renders the full `tags: […]` line. */
export function serializeTags(tags) {
  return `tags: ${serializeTagsValue(tags)}`
}

/** Renders just the JSON-compatible array value (`["a", "b"]`). */
export function serializeTagsValue(tags) {
  return `[${tags.map(t => JSON.stringify(t)).join(', ')}]`
}

function tagsEqual(a, b) {
  if (a.length !== b.length) return false
  for (let i = 0; i < a.length; i++) if (a[i] !== b[i]) return false
  return true
}

/**
 * Returns the front-matter prefix ("---\n…\n---\n", or "" when the document
 * should have none) for the given document with `tags` applied. `doc` is the
 * output of parseDocument. When tags are unchanged the original prefix is
 * returned verbatim; otherwise only the tags value's byte span is replaced.
 * Throws FrontMatterNotEditable for unsafe blocks.
 */
export function frontMatterPartWithTags(doc, tags) {
  if (!doc.rawPrefix) {
    if (!tags.length) return ''
    return `---\n${serializeTags(tags)}\n---\n`
  }
  if (tagsEqual(doc.tags, tags)) return doc.rawPrefix
  const edited = editTagsInFrontMatter(doc.frontMatter, tags)
  if (!edited.trim()) return ''
  const openLen = doc.rawPrefix.startsWith('---\r\n') ? 5 : 4
  return doc.rawPrefix.slice(0, openLen) + edited + doc.rawPrefix.slice(openLen + doc.frontMatter.length)
}

/** Returns the full canonical document with `tags` applied. */
export function withTags(doc, tags) {
  return frontMatterPartWithTags(doc, tags) + doc.body
}

/** Rebuilds the document with new tags AND a new body, preserving the prefix. */
export function applyTagsAndBody(doc, tags, body) {
  return frontMatterPartWithTags(doc, tags) + body
}

/** Compatibility projection: {tags, body} as the previous API surface used. */
export function parseFrontMatter(content) {
  const doc = parseDocument(content)
  return { tags: doc.tags, body: doc.body }
}

// ---- byte-span editing ------------------------------------------------------

function editTagsInFrontMatter(frontMatter, newTags) {
  if (!frontMatter) {
    if (!newTags.length) return ''
    return serializeTags(newTags) + '\n'
  }
  const eol = frontMatter.includes('\r\n') ? '\r\n' : '\n'

  const span = findTagsSpan(frontMatter)
  if (span.tagsLineStart === -1) {
    if (!newTags.length) return frontMatter
    return serializeTags(newTags) + eol + frontMatter
  }

  if (newTags.length === 0) {
    // Clearing removes the whole tags construct — the key, every line of a
    // multi-line flow value, any trailing comment, and the line ending.
    const nl = frontMatter.indexOf('\n', span.valueEnd)
    const removeEnd = nl < 0 ? frontMatter.length : nl + 1
    return frontMatter.slice(0, span.tagsLineStart) + frontMatter.slice(removeEnd)
  }

  // Setting replaces only the value span, preserving everything around it.
  // When the value was empty and a comment follows (`tags: # c`), keep a space.
  let suffix = frontMatter.slice(span.valueEnd)
  if (span.valueEnd < frontMatter.length && frontMatter[span.valueEnd] === '#') {
    suffix = ' ' + suffix
  }
  return frontMatter.slice(0, span.valueStart) + serializeTagsValue(newTags) + suffix
}

// Returns the index of the "]" closing the flow sequence starting at start, or
// -1 when unterminated. Respects quotes and nested brackets; comments run to
// end of line.
function flowSeqEnd(s, start) {
  let depth = 0
  let inSingle = false
  let inDouble = false
  for (let i = start; i < s.length; i++) {
    const ch = s[i]
    if (inDouble) {
      if (ch === '\\') { i++; continue }
      if (ch === '"') inDouble = false
      continue
    }
    if (inSingle) {
      if (ch === '\\') { i++; continue }
      if (ch === "'") inSingle = false
      continue
    }
    if (ch === '"') { inDouble = true; continue }
    if (ch === "'") { inSingle = true; continue }
    if (ch === '#') {
      if (i > start && (s[i - 1] === ' ' || s[i - 1] === '\t')) {
        const nl = s.indexOf('\n', i)
        if (nl < 0) return -1
        i = nl // loop i++ skips the newline
      }
      continue
    }
    if (ch === '[') { depth++; continue }
    if (ch === ']') {
      depth--
      if (depth === 0) return i
    }
  }
  return -1
}

// Returns the index of the first unquoted " #" comment within [start, lineEnd),
// or lineEnd when there is none.
function scalarCommentStart(s, start, lineEnd) {
  let inSingle = false
  let inDouble = false
  for (let i = start; i < lineEnd; i++) {
    const ch = s[i]
    if (inDouble) {
      if (ch === '\\') { i++; continue }
      if (ch === '"') inDouble = false
      continue
    }
    if (inSingle) {
      if (ch === '\\') { i++; continue }
      if (ch === "'") inSingle = false
      continue
    }
    if (ch === '"') { inDouble = true; continue }
    if (ch === "'") { inSingle = true; continue }
    if (ch === '#') {
      if (i > start && (s[i - 1] === ' ' || s[i - 1] === '\t')) return i
    }
  }
  return lineEnd
}

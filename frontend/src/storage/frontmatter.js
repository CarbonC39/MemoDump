// Browser mirror of the Go front-matter editor in internal/vaultfs.
//
// The pure-frontend build stores the canonical full Markdown per note so that
// unknown front-matter keys, ordering and comments survive tag edits — the
// same contract the Go server enforces. This module implements the identical
// rules and is tested against the same shared fixture
// (testdata/contracts/frontmatter.json).

/** Thrown when a front matter block cannot be modified in place safely. */
export class FrontMatterNotEditable extends Error {
  constructor(message) {
    super(message)
    this.name = 'FrontMatterNotEditable'
  }
}

/**
 * Parses a full Markdown document into front matter (fences excluded, LF line
 * endings), body, and tags.
 */
export function parseDocument(markdown) {
  const doc = { frontMatter: '', body: markdown, tags: [] }
  if (!markdown.startsWith('---\n') && !markdown.startsWith('---\r\n')) return doc

  let rest = markdown
  const firstNl = rest.indexOf('\n')
  if (firstNl < 0) return doc
  rest = rest.slice(firstNl + 1)

  const rawLines = []
  let idx = 0
  let nl = 0
  while (true) {
    nl = rest.indexOf('\n', idx)
    if (nl < 0) return doc // unterminated: whole content is the body
    const line = rest.slice(idx, nl)
    if (line.replace(/\r$/, '') === '---') break
    rawLines.push(line.replace(/\r$/, ''))
    idx = nl + 1
  }

  doc.frontMatter = rawLines.join('\n')
  doc.body = rest.slice(nl + 1)
  doc.tags = parseTags(doc.frontMatter)
  return doc
}

function parseTags(frontMatter) {
  if (!frontMatter) return []
  for (const line of frontMatter.split('\n')) {
    if (!line.startsWith('tags:')) continue
    const val = line.slice('tags:'.length).trim()
    try {
      const parsed = JSON.parse(val)
      if (Array.isArray(parsed)) return parsed.map(String)
    } catch (_) { /* fall through to the unquoted form */ }
    const tags = []
    const clean = val.replace(/^\[/, '').replace(/\]$/, '')
    for (const raw of clean.split(',')) {
      const tag = raw.trim().replace(/^['"]|['"]$/g, '')
      if (tag) tags.push(tag)
    }
    return tags
  }
  return []
}

/** Renders the `tags:` line in the JSON-compatible array form. */
export function serializeTags(tags) {
  return `tags: [${tags.map(t => JSON.stringify(t)).join(', ')}]`
}

/**
 * Returns the front-matter prefix ("---\n…\n---\n", or "" when the document
 * should have none) for the given front-matter block with `tags` applied,
 * preserving unknown keys, ordering and comments byte-for-byte. Throws
 * FrontMatterNotEditable when the tags value cannot be replaced in place.
 */
export function frontMatterPartWithTags(frontMatter, tags) {
  if (!frontMatter) {
    if (!tags.length) return ''
    return `---\n${serializeTags(tags)}\n---\n`
  }

  const lines = frontMatter.split('\n')
  let tagsIdx = -1
  for (let i = 0; i < lines.length; i++) {
    if (!lines[i].startsWith('tags:')) continue
    if (tagsIdx !== -1) throw new FrontMatterNotEditable('duplicate tags keys')
    tagsIdx = i
  }

  if (tagsIdx >= 0) {
    const value = lines[tagsIdx].slice('tags:'.length).trim()
    if (value === '' && tagsIdx + 1 < lines.length) {
      throw new FrontMatterNotEditable('block-style tags')
    }
    if (value !== '' && (value.startsWith('|') || value.startsWith('>'))) {
      throw new FrontMatterNotEditable('block scalar tags')
    }
    if (tags.length === 0) lines.splice(tagsIdx, 1)
    else lines[tagsIdx] = serializeTags(tags)
  } else if (tags.length !== 0) {
    lines.unshift(serializeTags(tags))
  }

  if (!lines.some(l => l.trim() !== '')) return ''
  return `---\n${lines.join('\n')}\n---\n`
}

/**
 * Returns the full canonical document with `tags` applied. `document` may be a
 * string (parsed on the fly) or an object from parseDocument.
 */
export function withTags(document, tags) {
  const doc = typeof document === 'string' ? parseDocument(document) : document
  return frontMatterPartWithTags(doc.frontMatter, tags) + doc.body
}

/**
 * Rebuilds the full document with new tags AND a new body, preserving the
 * rest of the front matter. Mirrors the Go repository's Update.
 */
export function applyTagsAndBody(document, tags, body) {
  const doc = typeof document === 'string' ? parseDocument(document) : document
  return frontMatterPartWithTags(doc.frontMatter, tags) + body
}

/** Compatibility projection: {tags, body} as the previous API surface used. */
export function parseFrontMatter(content) {
  const doc = parseDocument(content)
  return { tags: doc.tags, body: doc.body }
}

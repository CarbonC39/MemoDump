// The schema-v2 note wire record for the browser sync port (R6.1). Matches
// internal/cloudsync/note.go: a note record is a flat object with
// schemaVersion/syncId/path/deleted (markdown omitted on a tombstone), parsed
// strictly (duplicate/unknown fields, null scalars, wrong types, trailing
// content, invalid UTF-8 and oversized input all rejected) and serialized to
// canonical bytes with a trailing LF that a remote parser would accept.
import { canonicalBytes, canonicalSha256 } from './canonical.js'
import { utf8Bytes } from './hash.js'
import { isSyncID } from './uuid.js'
import { normalizeMarkdown } from './markdown.js'
import { parseJSONObject, decodeWireInput, SyncParseError } from './jsonscan.js'

export const NOTE_SCHEMA_VERSION = 2
export const NOTE_KEY_PREFIX = 'notes/'
export const NOTE_MAX_ENTITY_BYTES = 1 << 20 // 1 MiB

// noteKey returns the remote key for a note record.
export function noteKey(syncId) {
  return NOTE_KEY_PREFIX + syncId + '.json'
}

// parseNoteKey extracts the Sync ID from a note key, reporting whether the key
// is a well-formed note key.
export function parseNoteKey(key) {
  if (!key.startsWith(NOTE_KEY_PREFIX) || !key.endsWith('.json')) return null
  const id = key.slice(NOTE_KEY_PREFIX.length, -'.json'.length)
  return isSyncID(id) ? id : null
}

// validNotePath reports whether path is a complete, safe, portable Markdown
// note path: slash-relative, no traversal or empty segments, no backslash, no
// reserved repository segment (.memodump/.images), and a lowercase .md
// extension.
export function validNotePath(path) {
  if (path === '' || path.startsWith('/') || path.startsWith('\\')) return false
  if (path.includes('\\')) return false
  const segs = path.split('/')
  for (let i = 0; i < segs.length; i++) {
    const seg = segs[i]
    if (seg === '' || seg === '.' || seg === '..') return false
    if (i === segs.length - 1 && !seg.endsWith('.md')) return false
    if (isReservedNoteSegment(seg)) return false
    for (const ch of seg) {
      const c = ch.codePointAt(0)
      if (c < 0x20 || c === 0x7f) return false
    }
  }
  return true
}

function isReservedNoteSegment(seg) {
  const lower = seg.toLowerCase()
  return lower === '.memodump' || lower === '.images'
}

const MEDIA_KEY_RE = /^([a-f0-9]{64}\.(png|jpg|gif|webp|avif))/

// firstInvalidMediaKey returns the first invalid memodump-media: key referenced
// in markdown, or null when every reference is well-formed.
export function firstInvalidMediaKey(markdown) {
  const prefix = 'memodump-media:'
  let idx = 0
  while (true) {
    const rel = markdown.indexOf(prefix, idx)
    if (rel < 0) return null
    const start = rel + prefix.length
    const rest = markdown.slice(start)
    const m = MEDIA_KEY_RE.exec(rest)
    if (m !== null) {
      const key = m[1]
      const next = start + key.length
      if (next >= markdown.length || !isKeyContinuation(markdown.charCodeAt(next))) {
        idx = start + m[0].length
        continue
      }
    }
    let tok = rest
    const cut = tok.search(/[ \t\n"')\]]/)
    if (cut >= 0) tok = tok.slice(0, cut)
    return tok === '' ? '<empty>' : tok
  }
}

function isKeyContinuation(code) {
  return (code >= 0x61 && code <= 0x7a) || (code >= 0x41 && code <= 0x5a) ||
    (code >= 0x30 && code <= 0x39) || code === 0x2e || code === 0x2d ||
    code === 0x5f || code === 0x2f || code === 0x3a
}

function hasLoneSurrogates(s) {
  for (let i = 0; i < s.length; i++) {
    const c = s.charCodeAt(i)
    if (c >= 0xd800 && c <= 0xdbff) {
      if (i + 1 >= s.length) return true
      const next = s.charCodeAt(i + 1)
      if (next < 0xdc00 || next > 0xdfff) return true
      i++
    } else if (c >= 0xdc00 && c <= 0xdfff) {
      return true
    }
  }
  return false
}

export class InvalidNoteError extends Error {
  constructor(message) { super(message) }
}

// withDefaults fills the Go zero-value default for markdown (""): a tombstone
// or blank record in the wire fixtures omits the key, and the content hash and
// serialization must treat it as the empty string, never as null.
function withDefaults(n) {
  return { ...n, markdown: n.markdown === undefined ? '' : n.markdown }
}

// validateNoteRecord checks single-note invariants: the exact schema version,
// UTF-8 fields, a UUID v4/v5 Sync ID, a safe portable path, LF-normalized
// Markdown, and the tombstone rule.
export function validateNoteRecord(n) {
  n = withDefaults(n)
  if (n.schemaVersion !== NOTE_SCHEMA_VERSION) throw new InvalidNoteError(`invalid note record: schema ${n.schemaVersion}`)
  if (hasLoneSurrogates(n.syncId) || hasLoneSurrogates(n.path) || hasLoneSurrogates(n.markdown)) {
    throw new InvalidNoteError('invalid note record: invalid utf-8 in record')
  }
  if (!isSyncID(n.syncId)) throw new InvalidNoteError(`invalid note record: bad syncId "${n.syncId}"`)
  if (!validNotePath(n.path)) throw new InvalidNoteError(`invalid note record: bad path "${n.path}"`)
  if (n.markdown !== normalizeMarkdown(n.markdown)) throw new InvalidNoteError('invalid note record: markdown not LF-normalized')
  if (n.deleted && n.markdown !== '') throw new InvalidNoteError('invalid note record: tombstone carries markdown')
  const bad = firstInvalidMediaKey(n.markdown)
  if (bad !== null) throw new InvalidNoteError(`invalid note record: invalid media key "${bad}"`)
}

// computeContentHash returns the canonical digest of the note's content: the
// tuple (syncId, portable path, markdown, deleted).
export async function computeContentHash(n) {
  n = withDefaults(n)
  return canonicalSha256({
    deleted: n.deleted,
    markdown: n.markdown,
    path: n.path,
    syncId: n.syncId,
  })
}

// serializeNoteRecord returns the canonical record string (deterministic key
// order, UTF-8, trailing LF). A tombstone omits the markdown key. It refuses to
// emit a record the remote parser would reject.
export function serializeNoteRecord(n) {
  n = withDefaults(n)
  validateNoteRecord(n)
  const fields = {
    schemaVersion: n.schemaVersion,
    syncId: n.syncId,
    path: n.path,
    deleted: n.deleted,
  }
  if (!n.deleted) fields.markdown = n.markdown
  const data = canonicalBytes(fields) + '\n'
  if (utf8Bytes(data).length > NOTE_MAX_ENTITY_BYTES) throw new InvalidNoteError('entity exceeds size limit')
  return data
}

// parseNoteRecord parses and validates a raw remote note record with the same
// strict field rules as Go. The input may be raw UTF-8 bytes (Uint8Array or
// ArrayBuffer, exactly as a provider would deliver them) or a JS string; bytes
// are size-checked and strictly UTF-8 decoded before any parsing.
export function parseNoteRecord(input) {
  let text
  try {
    text = decodeWireInput(input, NOTE_MAX_ENTITY_BYTES)
  } catch (e) {
    throw new InvalidNoteError(e.message)
  }
  let fields
  try {
    fields = parseJSONObject(text).fields
  } catch (e) {
    if (e instanceof SyncParseError) throw new InvalidNoteError(e.message)
    throw e
  }
  const n = { schemaVersion: null, syncId: '', path: '', markdown: '', deleted: null }
  let markdownSeen = false
  for (const f of fields) {
    switch (f.key) {
      case 'schemaVersion':
        if (f.wasNull || f.raw === undefined || !/^-?(0|[1-9][0-9]*)$/.test(f.raw)) {
          throw new InvalidNoteError(`invalid note record: field "schemaVersion" has the wrong type`)
        }
        n.schemaVersion = f.value
        break
      case 'syncId':
        if (f.wasNull || typeof f.value !== 'string') throw new InvalidNoteError('invalid note record: field "syncId" has the wrong type')
        n.syncId = f.value
        break
      case 'path':
        if (f.wasNull || typeof f.value !== 'string') throw new InvalidNoteError('invalid note record: field "path" has the wrong type')
        n.path = f.value
        break
      case 'markdown':
        markdownSeen = true
        if (f.wasNull || typeof f.value !== 'string') throw new InvalidNoteError('invalid note record: field "markdown" has the wrong type')
        n.markdown = f.value
        break
      case 'deleted':
        if (f.wasNull || typeof f.value !== 'boolean') throw new InvalidNoteError('invalid note record: field "deleted" has the wrong type')
        n.deleted = f.value
        break
      default:
        throw new InvalidNoteError(`invalid note record: unknown field "${f.key}"`)
    }
  }
  for (const req of ['schemaVersion', 'syncId', 'path', 'deleted']) {
    if (fields.findIndex((f) => f.key === req) < 0) {
      throw new InvalidNoteError(`invalid note record: missing field "${req}"`)
    }
  }
  if (n.deleted) {
    if (markdownSeen) throw new InvalidNoteError('invalid note record: tombstone must not carry markdown')
  } else if (!markdownSeen) {
    throw new InvalidNoteError('invalid note record: live note missing markdown')
  }
  validateNoteRecord(n)
  return n
}

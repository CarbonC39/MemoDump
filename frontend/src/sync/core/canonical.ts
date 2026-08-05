// Canonical JSON writer mirroring internal/cloudsync/canonical.go.
//
// Objects are serialized with sorted keys and a deterministic string escaping
// (quote, backslash, \n \r \t, control characters as \uXXXX; everything else
// literal UTF-8). The Go and TypeScript implementations must produce
// byte-identical output, verified against the same testdata/sync fixtures.

import { sha256Hex } from '../../storage/sha256'

export type CanonicalValue =
  | null
  | string
  | boolean
  | number
  | CanonicalValue[]
  | { [key: string]: CanonicalValue }

export function canonicalString(s: string): string {
  let out = '"'
  for (const ch of s) {
    const cp = ch.codePointAt(0)!
    switch (ch) {
      case '"': out += '\\"'; break
      case '\\': out += '\\\\'; break
      case '\n': out += '\\n'; break
      case '\r': out += '\\r'; break
      case '\t': out += '\\t'; break
      default:
        if (cp < 0x20) out += '\\u' + cp.toString(16).padStart(4, '0')
        else out += ch
    }
  }
  return out + '"'
}

function writeValue(v: CanonicalValue): string {
  if (v === null) return 'null'
  if (typeof v === 'string') return canonicalString(v)
  if (typeof v === 'boolean') return v ? 'true' : 'false'
  if (typeof v === 'number') return String(v)
  if (Array.isArray(v)) return '[' + v.map(writeValue).join(',') + ']'
  const keys = Object.keys(v).sort()
  return '{' + keys.map(k => canonicalString(k) + ':' + writeValue(v[k])).join(',') + '}'
}

/** Returns the canonical JSON bytes of an object (as a UTF-8 JS string). */
export function canonicalBytes(v: { [key: string]: CanonicalValue }): string {
  return writeValue(v)
}

/**
 * ContentHash is the canonical digest over kind, parentId, name, and markdown,
 * as pinned by the shared golden fixtures. It is the content identity of an
 * entity, independent of the provider's ETag/version.
 */
export function contentHash(kind: string, parentId: string, name: string, markdown: string): string {
  return sha256Hex(canonicalBytes({ kind, parentId, name, markdown }))
}

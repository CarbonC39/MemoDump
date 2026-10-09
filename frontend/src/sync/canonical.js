// The canonical JSON writer shared by the wire contract and the deterministic
// hashes (R6.1). It must produce byte-identical output to the Go engine
// (internal/cloudsync/canonical.go): keys in sorted order, deterministic string
// escaping (quote, backslash, \n \r \t, control characters as \uXXXX, everything
// else literal), and the same treatment of the flat scalar objects this contract
// uses. The committed fixtures pin the exact bytes.
import { sha256Hex } from './hash.js'

// canonicalBytes serializes a flat object of scalars (the wire records and
// hash tuples) with sorted keys and the canonical escaping. Nested objects and
// arrays are supported for completeness but the note/repo contract is flat.
export function canonicalBytes(v) {
  const out = []
  writeCanonicalValue(out, v)
  return out.join('')
}

function writeCanonicalObject(out, v) {
  const keys = Object.keys(v).sort()
  out.push('{')
  for (let i = 0; i < keys.length; i++) {
    if (i > 0) out.push(',')
    writeCanonicalString(out, keys[i])
    out.push(':')
    writeCanonicalValue(out, v[keys[i]])
  }
  out.push('}')
}

function writeCanonicalValue(out, v) {
  if (v === null || v === undefined) {
    out.push('null')
    return
  }
  switch (typeof v) {
    case 'string':
      writeCanonicalString(out, v)
      return
    case 'boolean':
      out.push(v ? 'true' : 'false')
      return
    case 'number':
      if (!Number.isInteger(v)) throw new Error('cloudsync: cannot canonicalize a non-integer number')
      out.push(String(v))
      return
    case 'object':
      if (Array.isArray(v)) {
        out.push('[')
        for (let i = 0; i < v.length; i++) {
          if (i > 0) out.push(',')
          writeCanonicalValue(out, v[i])
        }
        out.push(']')
        return
      }
      writeCanonicalObject(out, v)
      return
    default:
      throw new Error(`cloudsync: cannot canonicalize ${typeof v}`)
  }
}

function writeCanonicalString(out, s) {
  out.push('"')
  for (let i = 0; i < s.length; i++) {
    const c = s.charCodeAt(i)
    switch (c) {
      case 0x22: out.push('\\"'); break
      case 0x5c: out.push('\\\\'); break
      case 0x0a: out.push('\\n'); break
      case 0x0d: out.push('\\r'); break
      case 0x09: out.push('\\t'); break
      default:
        if (c < 0x20) out.push('\\u' + c.toString(16).padStart(4, '0'))
        else out.push(s[i])
    }
  }
  out.push('"')
}

// canonicalSha256 returns the lowercase hex SHA-256 of the canonical bytes of
// a flat object (e.g. a NoteRecord's content tuple). Used by
// computeContentHash and stateHash.
export async function canonicalSha256(v) {
  return sha256Hex(canonicalBytes(v))
}

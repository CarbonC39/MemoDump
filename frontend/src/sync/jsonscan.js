// A strict, minimal JSON scanner for the browser sync port (R6.1). Go's wire
// parsers reject duplicate keys, null scalars where a value is required, wrong
// types, unknown fields, trailing content, and invalid UTF-8. JSON.parse cannot
// detect duplicate keys, so this small tokenizer feeds the note/repo parsers
// with the same information Go's token-based decoder exposes.
//
// parseJSONObject(text) returns { fields: [{key, value, raw, wasNull}] } for the
// top-level object and the index just past its closing brace, or throws
// SyncParseError. Callers enforce their own field sets and type rules.

export class SyncParseError extends Error {}

import { utf8Bytes } from './hash.js'

const WS = /[ \t\n\r]/

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

function skipWS(text, i) {
  while (i < text.length && WS.test(text[i])) i++
  return i
}

function parseString(text, i) {
  if (text[i] !== '"') throw new SyncParseError('cloudsync: expected a string')
  i++
  let out = ''
  while (i < text.length) {
    const c = text[i]
    if (c === '"') return { value: out, end: i + 1 }
    if (c === '\\') {
      i++
      const e = text[i]
      switch (e) {
        case '"': out += '"'; i++; break
        case '\\': out += '\\'; i++; break
        case '/': out += '/'; i++; break
        case 'b': out += '\b'; i++; break
        case 'f': out += '\f'; i++; break
        case 'n': out += '\n'; i++; break
        case 'r': out += '\r'; i++; break
        case 't': out += '\t'; i++; break
        case 'u': {
          const hex = text.slice(i + 1, i + 5)
          if (!/^[0-9a-fA-F]{4}$/.test(hex)) throw new SyncParseError('cloudsync: bad unicode escape')
          out += String.fromCharCode(parseInt(hex, 16))
          i += 5
          break
        }
        default:
          throw new SyncParseError('cloudsync: bad escape')
      }
      continue
    }
    const code = text.charCodeAt(i)
    if (code < 0x20) throw new SyncParseError('cloudsync: control character in string')
    out += c
    i++
  }
  throw new SyncParseError('cloudsync: unterminated string')
}

function parseValue(text, i) {
  i = skipWS(text, i)
  if (i >= text.length) throw new SyncParseError('cloudsync: unexpected end of input')
  const c = text[i]
  if (c === '"') {
    const { value, end } = parseString(text, i)
    return { value, end, isNull: false, isString: true }
  }
  if (c === '{' || c === '[') {
    // Nested structures are not used by the flat wire records; parse and skip
    // them so a wrong-typed field is still rejected as a whole.
    throw new SyncParseError('cloudsync: nested structures are not allowed in wire records')
  }
  if (c === 't' && text.startsWith('true', i)) return { value: true, end: i + 4, isNull: false, isString: false }
  if (c === 'f' && text.startsWith('false', i)) return { value: false, end: i + 5, isNull: false, isString: false }
  if (c === 'n' && text.startsWith('null', i)) return { value: null, end: i + 4, isNull: true, isString: false }
  if (c === '-' || (c >= '0' && c <= '9')) {
    let j = i + 1
    while (j < text.length && /[0-9eE+\-.]/.test(text[j])) j++
    const numText = text.slice(i, j)
    if (!/^-?(0|[1-9][0-9]*)(\.[0-9]+)?([eE][+-]?[0-9]+)?$/.test(numText)) {
      throw new SyncParseError('cloudsync: malformed number')
    }
    return { value: Number(numText), raw: numText, end: j, isNull: false, isString: false }
  }
  throw new SyncParseError('cloudsync: unexpected token')
}

// decodeWireInput converts raw wire input to a decoded string, enforcing the
// byte-size limit and strict UTF-8. Raw bytes (Uint8Array/ArrayBuffer) are
// decoded with a fatal TextDecoder so invalid sequences throw exactly like Go's
// utf8.Valid — a plain response.text() would silently replace them with U+FFFD
// and let a malformed record be pulled as valid content. A leading UTF-8 BOM is
// rejected explicitly (TextDecoder strips it by default, but Go's JSON parser
// rejects the raw bytes) and ignoreBOM:true keeps any BOM character in the
// decoded string as defense in depth. A string input is encoded first and cannot
// carry invalid UTF-8 or a raw BOM.
export function decodeWireInput(input, maxBytes) {
  let bytes
  if (input instanceof Uint8Array) {
    bytes = input
  } else if (input instanceof ArrayBuffer) {
    bytes = new Uint8Array(input)
  } else if (typeof input === 'string') {
    bytes = utf8Bytes(input)
  } else {
    throw new SyncParseError('cloudsync: unsupported input type')
  }
  if (bytes.length > maxBytes) throw new SyncParseError('entity exceeds size limit')
  if (bytes.length >= 3 && bytes[0] === 0xef && bytes[1] === 0xbb && bytes[2] === 0xbf) {
    throw new SyncParseError('cloudsync: leading BOM is not allowed')
  }
  try {
    return new TextDecoder('utf-8', { fatal: true, ignoreBOM: true }).decode(bytes)
  } catch (_) {
    throw new SyncParseError('cloudsync: invalid utf-8')
  }
}

// parseJSONObject parses a JSON object whose values are scalars (no nested
// objects/arrays). It returns the field list in document order and the index
// just past the closing brace. Duplicate keys and trailing content are errors.
export function parseJSONObject(text) {
  if (hasLoneSurrogates(text)) throw new SyncParseError('cloudsync: invalid utf-8')
  let i = skipWS(text, 0)
  if (text[i] !== '{') throw new SyncParseError('cloudsync: not an object')
  i++
  const fields = []
  const seen = new Set()
  while (true) {
    i = skipWS(text, i)
    if (text[i] === '}') {
      i++
      break
    }
    if (fields.length > 0) {
      if (text[i] !== ',') throw new SyncParseError('cloudsync: missing comma')
      i++
      i = skipWS(text, i)
    }
    const key = parseString(text, i)
    if (key.value === '') throw new SyncParseError('cloudsync: empty field name')
    if (seen.has(key.value)) throw new SyncParseError(`cloudsync: duplicate field "${key.value}"`)
    seen.add(key.value)
    i = key.end
    i = skipWS(text, i)
    if (text[i] !== ':') throw new SyncParseError('cloudsync: missing colon')
    i++
    const val = parseValue(text, i)
    i = val.end
    fields.push({
      key: key.value,
      value: val.value,
      raw: typeof val.value === 'string' ? undefined : val.raw,
      wasNull: val.isNull,
    })
  }
  i = skipWS(text, i)
  if (i !== text.length) throw new SyncParseError('cloudsync: trailing content after record')
  return { fields, end: i }
}

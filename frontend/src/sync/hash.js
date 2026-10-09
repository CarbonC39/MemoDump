// Web Crypto digest helpers for the browser sync port (R6.1). SHA-256 drives the
// canonical content/state hashes and SHA-1 drives the deterministic UUID v5
// conflict identity. crypto.subtle is async, so every hash is awaited; the pure
// decision core stays free of IndexedDB, Vue, timers, and network I/O.
const textEncoder = new TextEncoder()

// utf8Bytes encodes a JS string to UTF-8 without a hand-rolled encoder.
export function utf8Bytes(text) {
  return textEncoder.encode(text)
}

// sha256Hex returns the lowercase hex digest of the UTF-8 encoding of text.
export async function sha256Hex(text) {
  const digest = await crypto.subtle.digest('SHA-256', utf8Bytes(text))
  return toHex(new Uint8Array(digest))
}

// sha1 returns the raw SHA-1 digest bytes of an ArrayBufferView.
export async function sha1(bytes) {
  const digest = await crypto.subtle.digest('SHA-1', bytes)
  return new Uint8Array(digest)
}

export function toHex(bytes) {
  let out = ''
  for (const b of bytes) out += b.toString(16).padStart(2, '0')
  return out
}

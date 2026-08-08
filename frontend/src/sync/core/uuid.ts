// Deterministic UUID v5 (RFC 4122, name-based, SHA-1), mirroring
// github.com/google/uuid#NewSHA1 on the Go side. Both languages must derive
// byte-identical identifiers for the same namespace and name bytes — conflict
// Sync IDs are part of the shared wire contract.

import { sha1Bytes } from './sha1'

const HEX = '0123456789abcdef'

/** Parses a canonical hyphenated UUID string into its 16 bytes. */
export function parseUuid(s: string): Uint8Array {
  if (!/^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/.test(s)) {
    throw new Error(`invalid uuid: ${s}`)
  }
  const hex = s.replace(/-/g, '')
  const bytes = new Uint8Array(16)
  for (let i = 0; i < 16; i++) {
    bytes[i] = parseInt(hex.slice(i * 2, i * 2 + 2), 16)
  }
  return bytes
}

function formatBytes(bytes: Uint8Array): string {
  let s = ''
  for (let i = 0; i < 16; i++) {
    s += HEX[bytes[i] >> 4] + HEX[bytes[i] & 0x0f]
    if (i === 3 || i === 5 || i === 7 || i === 9) s += '-'
  }
  return s
}

/**
 * Returns the UUID v5 for name within the given namespace. Only the first 16
 * bytes of the SHA-1 digest are used, then the version (5) and RFC 4122 variant
 * bits are set — exactly what the Go uuid.NewSHA1 helper does.
 */
export function uuidv5(namespace: string, name: string): string {
  const nsBytes = parseUuid(namespace)
  const nameBytes = new TextEncoder().encode(name)
  const data = new Uint8Array(16 + nameBytes.length)
  data.set(nsBytes)
  data.set(nameBytes, 16)
  const hash = sha1Bytes(data)
  hash[6] = (hash[6] & 0x0f) | 0x50
  hash[8] = (hash[8] & 0x3f) | 0x80
  return formatBytes(hash.subarray(0, 16))
}

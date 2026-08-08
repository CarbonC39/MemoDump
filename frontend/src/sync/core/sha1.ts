// Dependency-free synchronous SHA-1 (RFC 3174), used only for deterministic
// UUID v5 conflict identities.
//
// crypto.subtle is unavailable in non-secure contexts (plain-http LAN builds),
// so this mirrors the Go standard library's sha1 to guarantee byte-identical
// digests on both sides — the derived conflict UUIDs are part of the wire
// contract. Only the UUID-v5 helper in uuid.ts consumes it.

const K1 = 0x5a827999
const K2 = 0x6ed9eba1
const K3 = 0x8f1bbcdc
const K4 = 0xca62c1d6

const H_INIT = new Uint32Array([
  0x67452301, 0xefcdab89, 0x98badcfe, 0x10325476, 0xc3d2e1f0,
])

function rol(x: number, n: number): number {
  return ((x << n) | (x >>> (32 - n))) >>> 0
}

/** Returns the 20-byte SHA-1 digest of the input bytes. */
export function sha1Bytes(bytes: Uint8Array): Uint8Array {
  const len = bytes.length
  const bitLen = len * 8
  const padded = new Uint8Array(((len + 9 + 63) >> 6) << 6)
  padded.set(bytes)
  padded[len] = 0x80
  const dv = new DataView(padded.buffer)
  dv.setUint32(padded.length - 8, Math.floor(bitLen / 0x100000000))
  dv.setUint32(padded.length - 4, bitLen >>> 0)

  const h = new Uint32Array(H_INIT)
  const w = new Uint32Array(80)

  for (let i = 0; i < padded.length; i += 64) {
    for (let j = 0; j < 16; j++) w[j] = dv.getUint32(i + j * 4)
    for (let j = 16; j < 80; j++) {
      w[j] = rol(w[j - 3] ^ w[j - 8] ^ w[j - 14] ^ w[j - 16], 1)
    }
    let a = h[0], b = h[1], c = h[2], d = h[3], e = h[4]
    for (let j = 0; j < 80; j++) {
      let f: number
      let k: number
      if (j < 20) {
        f = (b & c) | (~b & d)
        k = K1
      } else if (j < 40) {
        f = b ^ c ^ d
        k = K2
      } else if (j < 60) {
        f = (b & c) | (b & d) | (c & d)
        k = K3
      } else {
        f = b ^ c ^ d
        k = K4
      }
      const temp = (rol(a, 5) + f + e + k + w[j]) >>> 0
      e = d
      d = c
      c = rol(b, 30)
      b = a
      a = temp
    }
    h[0] = (h[0] + a) >>> 0
    h[1] = (h[1] + b) >>> 0
    h[2] = (h[2] + c) >>> 0
    h[3] = (h[3] + d) >>> 0
    h[4] = (h[4] + e) >>> 0
  }

  const out = new Uint8Array(20)
  const dvOut = new DataView(out.buffer)
  for (let i = 0; i < 5; i++) dvOut.setUint32(i * 4, h[i])
  return out
}

/** Returns the lowercase hex SHA-1 of a UTF-8 string. */
export function sha1Hex(input: string): string {
  const bytes = sha1Bytes(new TextEncoder().encode(input))
  let out = ''
  for (let i = 0; i < bytes.length; i++) out += bytes[i].toString(16).padStart(2, '0')
  return out
}

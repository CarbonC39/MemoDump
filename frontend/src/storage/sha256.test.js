import { describe, it, expect } from 'vitest'
import { sha256Hex } from './sha256'

describe('sha256Hex', () => {
  it('matches NIST vectors', () => {
    expect(sha256Hex('')).toBe('e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855')
    expect(sha256Hex('abc')).toBe('ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad')
    expect(sha256Hex('hello')).toBe('2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824')
    expect(sha256Hex('The quick brown fox jumps over the lazy dog'))
      .toBe('d7a8fbb307d7809469ca9abcb0082e4f8d5651e46d3cdb762d02d0bf37c9e592')
  })

  it('hashes across a block boundary', () => {
    // 55 bytes fits a single 64-byte block; 56 bytes needs a second block. The
    // NIST vectors above pin the algorithm; these guard the padding path.
    expect(sha256Hex('a'.repeat(55))).toBe('9f4390f8d30c2dd92ec9f095b65e2b9ae9b0a925a5258e241c9f1e910f734318')
    expect(sha256Hex('a'.repeat(56))).toBe('b35439a4ac6f0948b6d6f9e3c6af0f5f590ce20f1bde7090ef7970686ec6738a')
  })

  it('is deterministic and order-sensitive', () => {
    expect(sha256Hex('one')).toBe(sha256Hex('one'))
    expect(sha256Hex('one')).not.toBe(sha256Hex('one!'))
  })

  it('encodes UTF-8 so non-ASCII content hashes consistently', () => {
    expect(sha256Hex('你好')).toBe(sha256Hex(new TextEncoder().encode('你好')))
  })
})

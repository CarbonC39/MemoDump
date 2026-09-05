import { describe, expect, it } from 'vitest'
import { fixtures } from './fixtures.js'
import {
  parseNoteRecord,
  serializeNoteRecord,
  validateNoteRecord,
  computeContentHash,
  noteKey,
  parseNoteKey,
  validNotePath,
  NOTE_SCHEMA_VERSION,
  NOTE_MAX_ENTITY_BYTES,
} from './note.js'
import { normalizeMarkdown } from './markdown.js'
import { portablePathKey } from './paths.js'

// TestNoteRecordMatchesFixture: the schema-v2 note contract. The browser must
// produce exactly the canonical bytes and content hashes the fixture pins, and
// parsing the canonical bytes must reproduce them exactly.
describe('note records match the shared fixture', () => {
  for (const tc of fixtures.noteRecords.valid) {
    it(`serializes and hashes ${tc.name}`, async () => {
      const n = tc.record
      const hash = await computeContentHash(n)
      expect(hash).toBe(tc.contentHash)
      const ser = serializeNoteRecord(n)
      expect(ser).toBe(tc.canonicalJson)
      const parsed = parseNoteRecord(tc.canonicalJson)
      // A tombstone/blank fixture record omits markdown; Go's zero value is "".
      expect(parsed).toEqual({ ...n, markdown: n.markdown ?? '' })
      expect(serializeNoteRecord(parsed)).toBe(tc.canonicalJson)
    })
  }

  for (const tc of fixtures.noteRecords.invalid) {
    it(`rejects invalid record: ${tc.name}`, () => {
      expect(() => parseNoteRecord(JSON.stringify(tc.record))).toThrow()
    })
  }

  for (const tc of fixtures.noteRecords.invalidRaw) {
    it(`rejects invalid raw record: ${tc.name}`, () => {
      expect(() => parseNoteRecord(tc.json)).toThrow()
    })
  }

  it('rejects raw bytes that are not valid UTF-8 (a response.text() would mangle them)', () => {
    // A lone 0xFF byte and a truncated 2-byte sequence are invalid UTF-8.
    expect(() => parseNoteRecord(new Uint8Array([0xff]))).toThrow()
    expect(() => parseNoteRecord(new Uint8Array([0xc3, 0x28]))).toThrow()
    // A valid record delivered as raw UTF-8 bytes parses identically.
    const valid = fixtures.noteRecords.valid[0]
    const bytes = new TextEncoder().encode(valid.canonicalJson)
    expect(parseNoteRecord(bytes)).toEqual({ ...valid.record, markdown: valid.record.markdown ?? '' })
    expect(() => parseNoteRecord(new ArrayBuffer(0))).toThrow()
  })

  it('rejects a leading UTF-8 BOM that TextDecoder would otherwise strip', () => {
    const valid = fixtures.noteRecords.valid[0]
    const bytes = new TextEncoder().encode(valid.canonicalJson)
    const withBom = new Uint8Array(3 + bytes.length)
    withBom.set([0xef, 0xbb, 0xbf], 0)
    withBom.set(bytes, 3)
    // Go's JSON parser rejects the raw BOM bytes; the browser must too.
    expect(() => parseNoteRecord(withBom)).toThrow()
    expect(() => parseNoteRecord(new Uint8Array([0xef, 0xbb, 0xbf]))).toThrow()
  })

  it('accepts a record at exactly 1 MiB and rejects one byte over', () => {
    const n = {
      schemaVersion: 2,
      syncId: '5d5d8b2c-94f7-4a38-8318-8cd4cb53dfa8',
      path: 'big.md',
      markdown: '',
      deleted: false,
    }
    const empty = serializeNoteRecord(n)
    const m = NOTE_MAX_ENTITY_BYTES - new TextEncoder().encode(empty).length
    const boundary = { ...n, markdown: 'x'.repeat(m) }
    const ser = serializeNoteRecord(boundary)
    expect(new TextEncoder().encode(ser).length).toBe(NOTE_MAX_ENTITY_BYTES)
    // The locally-serialized boundary record is remotely parseable.
    expect(parseNoteRecord(new TextEncoder().encode(ser))).toEqual(boundary)
    // One byte larger is rejected both on serialize and on parse (oversize
    // check runs before any decoding, so the raw length alone is enough).
    expect(() => serializeNoteRecord({ ...n, markdown: 'x'.repeat(m + 1) })).toThrow()
    expect(() => parseNoteRecord(new Uint8Array(NOTE_MAX_ENTITY_BYTES + 1))).toThrow()
  })

  for (const group of fixtures.noteRecords.portableCollisions) {
    it(`collision group ${group.name}: every record is valid and keys collide`, () => {
      expect(group.records.length).toBeGreaterThanOrEqual(2)
      const seen = new Set()
      for (const r of group.records) {
        expect(() => validateNoteRecord(r)).not.toThrow()
        expect(portablePathKey(r.path)).toBe(group.portablePathKey)
      }
      expect(seen.has(group.portablePathKey)).toBe(false)
      seen.add(group.portablePathKey)
    })
  }
})

describe('note key helpers', () => {
  it('round-trips note keys', () => {
    const id = '5d5d8b2c-94f7-4a38-8318-8cd4cb53dfa8'
    expect(noteKey(id)).toBe(`notes/${id}.json`)
    expect(parseNoteKey(`notes/${id}.json`)).toBe(id)
  })

  it('rejects malformed keys', () => {
    expect(parseNoteKey('notes/not-a-uuid.json')).toBe(null)
    expect(parseNoteKey('other/x.json')).toBe(null)
    expect(parseNoteKey('notes/abc.json.extra')).toBe(null)
    expect(parseNoteKey('')).toBe(null)
  })
})

describe('validNotePath', () => {
  const valid = [
    'idea.md',
    'Projects/idea.md',
    'Projects/Sub/deep.md',
    '你好/笔记.md',
    'a b.md',
    '.hidden.md',
  ]
  const invalid = [
    '',
    '/abs.md',
    '\\abs.md',
    'a\\b.md',
    '../evil.md',
    'a/../b.md',
    'a//b.md',
    './a.md',
    'a/./b.md',
    'note.txt',
    'note.md.txt',
    '.memodump/x.md',
    '.MEMODUMP/x.md',
    'x/.images/y.md',
  ]
  for (const p of valid) {
    it(`accepts ${JSON.stringify(p)}`, () => {
      expect(validNotePath(p)).toBe(true)
    })
  }
  for (const p of invalid) {
    it(`rejects ${JSON.stringify(p)}`, () => {
      expect(validNotePath(p)).toBe(false)
    })
  }
})

describe('tombstone wire rules', () => {
  it('tombstone omits markdown; live empty note carries it', () => {
    const tomb = serializeNoteRecord({
      schemaVersion: NOTE_SCHEMA_VERSION,
      syncId: '8a8a1e5f-c7da-4e6b-b62b-1f0788899992',
      path: 'gone.md',
      deleted: true,
    })
    expect(tomb).not.toContain('markdown')
    const blank = serializeNoteRecord({
      schemaVersion: NOTE_SCHEMA_VERSION,
      syncId: 'acac3051-e9fc-408d-884d-3119aaaa4bb4',
      path: 'blank.md',
      markdown: '',
      deleted: false,
    })
    expect(blank).toContain('"markdown":""')
    expect(() => parseNoteRecord(blank)).not.toThrow()
  })

  it('rejects CRLF markdown and non-normalized records on serialize', () => {
    expect(() => serializeNoteRecord({
      schemaVersion: 2,
      syncId: '5d5d8b2c-94f7-4a38-8318-8cd4cb53dfa8',
      path: 'idea.md',
      markdown: 'a\r\nb\n',
      deleted: false,
    })).toThrow()
  })
})

describe('markdown normalization', () => {
  for (const tc of fixtures.canonicalMarkdown.cases) {
    it(tc.name, () => {
      expect(normalizeMarkdown(tc.input)).toBe(tc.normalized)
    })
  }
})

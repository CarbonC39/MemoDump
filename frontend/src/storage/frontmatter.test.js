import { describe, it, expect } from 'vitest'
import {
  parseDocument,
  withTags,
  frontMatterPartWithTags,
  FrontMatterNotEditable,
} from './frontmatter'
import semantics from '../../../testdata/contracts/frontmatter.json'

describe('shared front-matter contract (Go + TS)', () => {
  for (const tc of semantics.tagEditCases) {
    it(`tag edit: ${tc.name}`, () => {
      const doc = parseDocument(tc.markdown)
      const got = withTags(doc, tc.newTags)
      expect(got).toBe(tc.expected)
    })
  }

  for (const tc of semantics.errorCases) {
    it(`unsafe: ${tc.name}`, () => {
      const doc = parseDocument(tc.markdown)
      expect(() => withTags(doc, tc.newTags)).toThrow(FrontMatterNotEditable)
    })
  }

  for (const tc of semantics.parseCases) {
    it(`parse: ${tc.name}`, () => {
      const doc = parseDocument(tc.markdown)
      expect(doc.tags).toEqual(tc.tags)
    })
  }
})

describe('parseDocument', () => {
  it('treats a body without front matter as all-body', () => {
    const doc = parseDocument('hello')
    expect(doc).toEqual({ rawPrefix: '', frontMatter: '', body: 'hello', tags: [] })
  })

  it('splits tags and body, CRLF preserved verbatim', () => {
    const doc = parseDocument('---\r\ntags: ["a"]\r\n---\r\nbody')
    expect(doc.tags).toEqual(['a'])
    expect(doc.body).toBe('body')
    expect(doc.rawPrefix).toBe('---\r\ntags: ["a"]\r\n---\r\n')
    expect(doc.frontMatter).toBe('tags: ["a"]\r\n')
  })

  it('treats unterminated front matter as all-body', () => {
    const doc = parseDocument('---\ntags: ["a"]')
    expect(doc.frontMatter).toBe('')
    expect(doc.body).toBe('---\ntags: ["a"]')
  })
})

describe('frontMatterPartWithTags', () => {
  const emptyDoc = () => ({ rawPrefix: '', frontMatter: '', body: '', tags: [] })
  it('adds front matter when absent', () => {
    expect(frontMatterPartWithTags(emptyDoc(), ['a'])).toBe('---\ntags: ["a"]\n---\n')
  })
  it('returns empty when clearing tags on an empty block', () => {
    expect(frontMatterPartWithTags(emptyDoc(), [])).toBe('')
  })
})

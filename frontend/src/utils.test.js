import { describe, expect, it } from 'vitest'
import { stripMarkdown } from './utils'

describe('stripMarkdown', () => {
  it('keeps plain angle-bracket text instead of treating it as HTML', () => {
    expect(stripMarkdown('<! 这里是一段文本 >')).toBe('<! 这里是一段文本 >')
    expect(stripMarkdown('1 < 2 / 3 > 0')).toBe('1 < 2 / 3 > 0')
  })

  it('preserves slashes, identifiers, bare URLs and autolinks', () => {
    expect(stripMarkdown('a/b foo_bar https://example.com/a/b')).toBe('a/b foo_bar https://example.com/a/b')
    expect(stripMarkdown('<https://example.com/a/b>')).toBe('https://example.com/a/b')
  })

  it('extracts visible text from common Markdown constructs', () => {
    const markdown = '# Heading\n\n- [x] **done**\n- [ ] [nested](https://example.com/a_(b))\n\n> `quote`'
    expect(stripMarkdown(markdown)).toBe('Heading\ndone\nnested\nquote')
  })

  it('omits valid HTML, images and fenced code from previews', () => {
    const markdown = '<div>hidden</div>\n\n![alt](image.png)\n\n```js\nconst x = 1\n```\n\nvisible'
    expect(stripMarkdown(markdown)).toBe('visible')
  })

  it('keeps incomplete markup as literal text', () => {
    expect(stripMarkdown('before [unfinished](a/b')).toBe('before [unfinished](a/b')
  })
})

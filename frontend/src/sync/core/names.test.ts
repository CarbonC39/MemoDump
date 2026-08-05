import { describe, it, expect } from 'vitest'
import pathKeys from '../../../../testdata/sync/portable-path-keys.json'
import conflictNames from '../../../../testdata/sync/conflict-names.json'
import retryClasses from '../../../../testdata/sync/retry-classes.json'
import markdownCases from '../../../../testdata/sync/canonical-markdown.json'
import caseFoldFixture from '../../../../testdata/sync/case-fold.json'
import { portablePathKey, conflictName, CASE_FOLD } from './names'
import { normalizeMarkdown } from './markdown'
import { classifyRetry } from './retry'
import { StoreError, type StoreErrorKind } from './remoteStore'

describe('portable path keys', () => {
  for (const tc of pathKeys.cases) {
    it(tc.name, () => {
      expect(portablePathKey(tc.path)).toBe(tc.key)
    })
  }
})

describe('conflict names', () => {
  for (const tc of conflictNames.cases) {
    it(tc.name, () => {
      expect(conflictName(tc.stem, tc.device, new Date(tc.timestamp))).toBe(tc.expected)
    })
  }
})

describe('retry classes', () => {
  for (const tc of retryClasses.cases) {
    it(tc.name, () => {
      const err = new StoreError(
        tc.kind as StoreErrorKind,
        'test',
        tc.retryAfterSeconds ? tc.retryAfterSeconds * 1000 : undefined,
      )
      const d = classifyRetry(err)
      expect(d.retryable).toBe(tc.retryable)
      expect(d.backoffMs).toBe(tc.backoffSeconds * 1000)
    })
  }
})

describe('canonical markdown', () => {
  for (const tc of markdownCases.cases) {
    it(tc.name, () => {
      expect(normalizeMarkdown(tc.input)).toBe(tc.normalized)
    })
  }
})

describe('case-fold table', () => {
  it('the TypeScript table exactly equals the shared fixture', () => {
    expect(CASE_FOLD).toEqual(caseFoldFixture.table)
  })
})

describe('case-fold idempotence', () => {
  it('applying the key twice equals applying it once for every folded character', () => {
    // Chars outside the table keep their original value, so the table's domain
    // (every key and every fold target) is a complete idempotence check.
    const sources = new Set<string>([...Object.keys(CASE_FOLD), ...Object.values(CASE_FOLD)])
    for (const ch of sources) {
      const once = portablePathKey(ch)
      expect(portablePathKey(once)).toBe(once)
    }
  })

  it('Cherokee uppercase and small letters collide', () => {
    expect(portablePathKey('Ꭰ.md')).toBe(portablePathKey('ꭰ.md'))
  })
})

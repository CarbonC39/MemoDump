import { describe, it, expect } from 'vitest'
import pathKeys from '../../../../testdata/sync/portable-path-keys.json'
import conflictNames from '../../../../testdata/sync/conflict-names.json'
import stateHashes from '../../../../testdata/sync/state-hashes.json'
import retryClasses from '../../../../testdata/sync/retry-classes.json'
import markdownCases from '../../../../testdata/sync/canonical-markdown.json'
import caseFoldFixture from '../../../../testdata/sync/case-fold.json'
import {
  portablePathKey,
  conflictFilename,
  conflictNamespace,
  deriveConflictSyncId,
  CASE_FOLD,
} from './names'
import { isSyncId, isUuidV4 } from './entity'
import { stateHash } from './canonical'
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

describe('conflict filenames', () => {
  for (const tc of conflictNames.cases) {
    it(tc.name, () => {
      expect(conflictFilename(tc.stem, tc.conflictSyncId)).toBe(tc.expected)
      // The derived name is deterministic: repeating the call is identical.
      expect(conflictFilename(tc.stem, tc.conflictSyncId)).toBe(tc.expected)
    })
  }
})

describe('state hashes', () => {
  it('the namespace matches the shared fixture', () => {
    expect(conflictNamespace).toBe(stateHashes.namespace)
  })
  for (const tc of stateHashes.stateHashes) {
    it(tc.name, () => {
      expect(stateHash(tc.contentHash, tc.deleted)).toBe(tc.expected)
    })
  }
})

describe('derived conflict identities', () => {
  for (const tc of stateHashes.conflictIds) {
    it(tc.name, () => {
      expect(deriveConflictSyncId(tc.sourceSyncId, tc.localStateHash, tc.remoteStateHash)).toBe(tc.expected)
      // Repeating a derivation produces the same result.
      expect(deriveConflictSyncId(tc.sourceSyncId, tc.localStateHash, tc.remoteStateHash)).toBe(tc.expected)
    })
  }

  it('rejects a malformed source Sync ID or state hash', () => {
    const good = stateHashes.stateHashes[0].expected
    const source = stateHashes.syncIds.validV4[0]
    expect(() => deriveConflictSyncId('not-a-uuid', good, good)).toThrow()
    expect(() => deriveConflictSyncId(source, '', good)).toThrow()
    expect(() => deriveConflictSyncId(source, 'ABC', good)).toThrow()
    expect(() => deriveConflictSyncId(source, good, '0'.repeat(63))).toThrow()
    expect(() => deriveConflictSyncId(source, good, good.toUpperCase())).toThrow()
  })
})

describe('sync id validation', () => {
  const ids = stateHashes.syncIds
  it('accepts v4 and v5 as Sync IDs', () => {
    for (const s of ids.validV4) expect(isSyncId(s)).toBe(true)
    for (const s of ids.validV5) expect(isSyncId(s)).toBe(true)
  })
  it('v5 Sync IDs never pass the v4-only validators', () => {
    for (const s of ids.validV5) expect(isUuidV4(s)).toBe(false)
    for (const s of ids.invalidV5AsRepositoryOrDevice) expect(isUuidV4(s)).toBe(false)
  })
  it('rejects invalid IDs', () => {
    for (const s of ids.invalid) expect(isSyncId(s)).toBe(false)
  })
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

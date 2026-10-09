import { describe, expect, it } from 'vitest'
import { fixtures } from './fixtures.js'
import {
  serializeRepositoryDescriptor,
  parseRepositoryDescriptor,
} from './repo.js'
import { ConflictNamespace, isUUIDv4, isSyncID, deriveConflictSyncID } from './uuid.js'
import { portablePathKey, conflictFilename, conflictPath } from './paths.js'
import { stateHash } from './decision.js'
import { classifyRetry, classifyErrorLabel } from './retry.js'
import { CASEFOLD_TABLE } from './casefold.js'

describe('the embedded case-fold table matches its fixture', () => {
  it('has exactly the pinned entries', () => {
    const fixtureTable = fixtures.caseFold.table
    expect(Object.keys(CASEFOLD_TABLE).length).toBe(Object.keys(fixtureTable).length)
    for (const [key, value] of Object.entries(fixtureTable)) {
      expect(CASEFOLD_TABLE[key]).toBe(value)
    }
  })
})

describe('repository descriptors match the shared fixture', () => {
  for (const tc of fixtures.repoDescriptors.valid) {
    it(`serializes and parses ${tc.name}`, () => {
      const d = tc.descriptor
      const ser = serializeRepositoryDescriptor(d)
      expect(ser).toBe(tc.canonicalJson)
      expect(parseRepositoryDescriptor(tc.canonicalJson)).toEqual(d)
    })
  }
  for (const tc of fixtures.repoDescriptors.invalid) {
    it(`rejects ${tc.name}`, () => {
      expect(() => parseRepositoryDescriptor(tc.json)).toThrow()
    })
  }
  it('rejects a leading UTF-8 BOM that TextDecoder would otherwise strip', () => {
    const canonical = fixtures.repoDescriptors.valid[0].canonicalJson
    const bytes = new TextEncoder().encode(canonical)
    const withBom = new Uint8Array(3 + bytes.length)
    withBom.set([0xef, 0xbb, 0xbf], 0)
    withBom.set(bytes, 3)
    expect(() => parseRepositoryDescriptor(withBom)).toThrow()
  })
})

describe('portable path keys match the shared fixture', () => {
  for (const tc of fixtures.portablePathKeys.cases) {
    it(tc.name, () => {
      expect(portablePathKey(tc.path)).toBe(tc.key)
      expect(portablePathKey(portablePathKey(tc.path))).toBe(tc.key)
    })
  }
})

describe('conflict names match the shared fixture', () => {
  for (const tc of fixtures.conflictNames.cases) {
    it(tc.name, () => {
      expect(conflictFilename(tc.stem, tc.conflictSyncId)).toBe(tc.expected)
      expect(conflictFilename(tc.stem, tc.conflictSyncId)).toBe(tc.expected)
    })
  }
  it('derives conflict paths without clock or numeric suffix', () => {
    const id = '04b2cbe6-19cf-584f-bad4-55fa03d9c05a'
    expect(conflictPath('idea.md', id)).toBe('idea (conflict 04b2cbe619cf).md')
    expect(conflictPath('Projects/idea.md', id)).toBe('Projects/idea (conflict 04b2cbe619cf).md')
    expect(conflictPath('你好/笔记.md', id)).toBe('你好/笔记 (conflict 04b2cbe619cf).md')
  })
})

describe('state hashes match the shared fixture', () => {
  it('namespace matches', () => {
    expect(fixtures.stateHashes.namespace).toBe(ConflictNamespace)
  })
  for (const tc of fixtures.stateHashes.stateHashes) {
    it(tc.name, async () => {
      expect(await stateHash(tc.contentHash, tc.deleted)).toBe(tc.expected)
    })
  }
})

describe('deterministic conflict IDs match the shared fixture', () => {
  for (const tc of fixtures.stateHashes.conflictIds) {
    it(tc.name, async () => {
      const got = await deriveConflictSyncID(tc.sourceSyncId, tc.localStateHash, tc.remoteStateHash)
      expect(got).toBe(tc.expected)
      expect(await deriveConflictSyncID(tc.sourceSyncId, tc.localStateHash, tc.remoteStateHash)).toBe(got)
    })
  }
  it('rejects malformed inputs', async () => {
    const good = '0bb58f3c8e8e5094238204e7951d1e97ecd8790064ea12bce10bcec4a5740b6a'
    await expect(deriveConflictSyncID('not-a-uuid', good, good)).rejects.toThrow()
    await expect(deriveConflictSyncID('5d5d8b2c-94f7-4a38-8318-8cd4cb53dfa8', 'ABC', good)).rejects.toThrow()
    await expect(deriveConflictSyncID('5d5d8b2c-94f7-4a38-8318-8cd4cb53dfa8', good, good.toUpperCase())).rejects.toThrow()
  })
})

describe('sync ID validation matches the shared fixture', () => {
  for (const s of fixtures.stateHashes.syncIds.validV4) {
    it(`accepts v4 ${s}`, () => {
      expect(isSyncID(s)).toBe(true)
      expect(isUUIDv4(s)).toBe(true)
    })
  }
  for (const s of fixtures.stateHashes.syncIds.validV5) {
    it(`accepts v5 ${s} as a sync ID but not as a v4-only identity`, () => {
      expect(isSyncID(s)).toBe(true)
      expect(isUUIDv4(s)).toBe(false)
    })
  }
  for (const s of fixtures.stateHashes.syncIds.invalidV5AsRepositoryOrDevice) {
    it(`rejects v5 ${s} as repository/device`, () => {
      expect(isUUIDv4(s)).toBe(false)
    })
  }
  for (const s of fixtures.stateHashes.syncIds.invalid) {
    it(`rejects invalid ${JSON.stringify(s)}`, () => {
      expect(isSyncID(s)).toBe(false)
    })
  }
})

describe('retry classes match the shared fixture', () => {
  for (const tc of fixtures.retryClasses.cases) {
    it(tc.name, () => {
      const got = classifyRetry({ kind: tc.kind, retryAfterSeconds: tc.retryAfterSeconds })
      expect(got.retryable).toBe(tc.retryable)
      expect(got.backoffSeconds).toBe(tc.backoffSeconds)
    })
  }
})

describe('redacted error labels', () => {
  const cases = {
    auth: 'permission',
    permission: 'permission',
    quota: 'quota',
    'rate-limit': 'rate-limit',
    'invalid-response': 'invalid-response',
    'unsupported-capability': 'unsupported',
    'incomplete-list': 'incomplete-list',
    'not-found': 'provider-error',
    'precondition-failed': 'provider-error',
    'retryable-transport': 'provider-error',
  }
  for (const [kind, expected] of Object.entries(cases)) {
    it(`${kind} -> ${expected}`, () => {
      expect(classifyErrorLabel({ kind })).toBe(expected)
    })
  }
  it('cancelled wins over the kind', () => {
    expect(classifyErrorLabel({ kind: 'quota', cancelled: true })).toBe('cancelled')
  })
})

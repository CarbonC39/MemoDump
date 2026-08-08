import { describe, it, expect } from 'vitest'
import entitiesFixture from '../../../../testdata/sync/entities.json'
import repoFixture from '../../../../testdata/sync/repo-descriptors.json'
import malformedFixture from '../../../../testdata/sync/malformed-input.json'
import {
  computeContentHash,
  serializeEntity,
  parseEntity,
  validateEntities,
  isSyncId,
  isUuidV4,
  type Entity,
  OversizedError,
  InvalidEntityError,
  CycleError,
  KIND_NOTE,
  KIND_FOLDER,
} from './entity'
import { parseRepositoryDescriptor, serializeRepositoryDescriptor } from './repo'
import stateHashes from '../../../../testdata/sync/state-hashes.json'

describe('shared entity contract (Go + TS)', () => {
  for (const tc of entitiesFixture.entities) {
    it(`canonical ${tc.name}`, () => {
      const e = tc.entity as unknown as Entity
      expect(computeContentHash(e)).toBe(tc.contentHash)
      expect(serializeEntity(e)).toBe(tc.canonicalJson)
      // Round-trip: parsing the canonical bytes reproduces them exactly.
      const parsed = parseEntity(tc.canonicalJson)
      expect(serializeEntity(parsed)).toBe(tc.canonicalJson)
    })
  }
})

describe('repository descriptors', () => {
  it('serializes and parses valid descriptors', () => {
    for (const tc of repoFixture.valid) {
      expect(serializeRepositoryDescriptor(tc.descriptor as never)).toBe(tc.canonicalJson)
      expect(parseRepositoryDescriptor(tc.canonicalJson)).toEqual(tc.descriptor)
    }
  })
  it('rejects invalid descriptors', () => {
    for (const tc of repoFixture.invalid) {
      expect(() => parseRepositoryDescriptor(tc.json)).toThrow()
    }
  })
})

describe('malformed entities rejected', () => {
  it('rejects entity-shaped cases', () => {
    for (const tc of malformedFixture.entityCases) {
      expect(() => parseEntity(JSON.stringify(tc.entity))).toThrow()
    }
  })
  it('rejects raw byte cases', () => {
    for (const tc of malformedFixture.rawCases) {
      if (tc.base64) {
        const bytes = Uint8Array.from(atob(tc.base64), c => c.charCodeAt(0))
        expect(() => parseEntity(bytes)).toThrow()
      } else if (tc.json != null) {
        expect(() => parseEntity(tc.json)).toThrow()
      }
    }
  })
})

describe('oversized', () => {
  it('rejects a record over 1 MiB', () => {
    const e: Entity = {
      schemaVersion: 1,
      syncId: '5d5d8b2c-94f7-4a38-8318-8cd4cb53dfa8',
      kind: KIND_NOTE,
      parentId: '',
      name: 'big',
      markdown: 'x'.repeat(1 << 20),
      contentHash: '',
      deleted: false,
      updatedBy: '1a2b3c4d-1111-4222-8333-444455556666',
      updatedAt: 1,
    }
    expect(() => parseEntity(serializeEntity(e))).toThrow(OversizedError)
  })
})

describe('parent graph validation', () => {
  it('rejects a missing parent, a cycle, and a note-as-parent', () => {
    const a = 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa'
    const b = 'bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb'
    const n = 'nnnnnnnn-nnnn-4nnn-8nnn-nnnnnnnnnnnn'
    const entity = (id: string, kind: string, parent: string): Entity => {
      const e: Entity = {
        schemaVersion: 1,
        syncId: id,
        kind,
        parentId: parent,
        name: id.slice(0, 4),
        contentHash: '',
        deleted: false,
        updatedBy: '1a2b3c4d-1111-4222-8333-444455556666',
        updatedAt: 1,
      }
      e.contentHash = computeContentHash(e)
      return e
    }
    expect(() => validateEntities({ [a]: entity(a, KIND_FOLDER, b) })).toThrow(InvalidEntityError)
    expect(() => validateEntities({
      [a]: entity(a, KIND_FOLDER, b),
      [b]: entity(b, KIND_FOLDER, a),
    })).toThrow(CycleError)
    expect(() => validateEntities({
      [a]: entity(a, KIND_NOTE, ''),
      [n]: entity(n, KIND_FOLDER, a),
    })).toThrow(InvalidEntityError)
  })
})

describe('sync id validation', () => {
  it('a conflict record with a v5 Sync ID parses', () => {
    const v5 = stateHashes.conflictIds[0].expected
    expect(isSyncId(v5)).toBe(true)
    expect(isUuidV4(v5)).toBe(false)
    const e: Entity = {
      schemaVersion: 1,
      syncId: v5,
      kind: KIND_NOTE,
      parentId: '',
      name: 'conflict copy',
      markdown: '# Local version\n',
      contentHash: '',
      deleted: false,
      updatedBy: '1a2b3c4d-1111-4222-8333-444455556666',
      updatedAt: 1,
    }
    e.contentHash = computeContentHash(e)
    expect(parseEntity(serializeEntity(e)).syncId).toBe(v5)
  })

  it('a v5 parentId is accepted for conflict entities', () => {
    const v5 = stateHashes.conflictIds[0].expected
    const e: Entity = {
      schemaVersion: 1,
      syncId: '5d5d8b2c-94f7-4a38-8318-8cd4cb53dfa8',
      kind: KIND_FOLDER,
      parentId: v5,
      name: 'folder',
      contentHash: '',
      deleted: false,
      updatedBy: '1a2b3c4d-1111-4222-8333-444455556666',
      updatedAt: 1,
    }
    e.contentHash = computeContentHash(e)
    expect(parseEntity(serializeEntity(e)).parentId).toBe(v5)
  })

  it('v5 is rejected for updatedBy and repositoryId', () => {
    const v5 = stateHashes.conflictIds[0].expected
    const e: Entity = {
      schemaVersion: 1,
      syncId: '5d5d8b2c-94f7-4a38-8318-8cd4cb53dfa8',
      kind: KIND_NOTE,
      parentId: '',
      name: 'idea',
      markdown: 'x',
      contentHash: '',
      deleted: false,
      updatedBy: v5,
      updatedAt: 1,
    }
    e.contentHash = computeContentHash(e)
    expect(() => parseEntity(serializeEntity(e))).toThrow(InvalidEntityError)

    const repo = {
      formatVersion: 1,
      repositoryId: v5,
      createdAt: 1,
      minimumClientVersion: '2.0.0',
    }
    expect(() => parseRepositoryDescriptor(JSON.stringify(repo))).toThrow(InvalidEntityError)
  })
})

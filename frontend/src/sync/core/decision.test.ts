// The TypeScript suite must emit the exact normalized decisions the Go engine
// committed for every shared scenario trace in testdata/sync/scenarios. It
// drives decideEntity/decideRepository from the trace's stored observations —
// the same pure inputs the Go simulator derives from the high-level state.

import { describe, it, expect } from 'vitest'
import blockedPathConflict from '../../../../testdata/sync/scenarios/blocked-path-conflict.json'
import convergedDelete from '../../../../testdata/sync/scenarios/converged-delete.json'
import divergentEdits from '../../../../testdata/sync/scenarios/divergent-edits.json'
import firstLocalUpload from '../../../../testdata/sync/scenarios/first-local-upload.json'
import firstRemoteDownload from '../../../../testdata/sync/scenarios/first-remote-download.json'
import folderStructuralConflict from '../../../../testdata/sync/scenarios/folder-structural-conflict.json'
import identicalEditBoth from '../../../../testdata/sync/scenarios/identical-edit-both.json'
import identicalOnboarding from '../../../../testdata/sync/scenarios/identical-onboarding.json'
import invalidRemote from '../../../../testdata/sync/scenarios/invalid-remote.json'
import localDelete from '../../../../testdata/sync/scenarios/local-delete.json'
import localEditVsTombstone from '../../../../testdata/sync/scenarios/local-edit-vs-tombstone.json'
import oneSidedEdit from '../../../../testdata/sync/scenarios/one-sided-edit.json'
import oneSidedRename from '../../../../testdata/sync/scenarios/one-sided-rename.json'
import parentCycle from '../../../../testdata/sync/scenarios/parent-cycle.json'
import physicalMissingAbsent from '../../../../testdata/sync/scenarios/physical-missing-absent.json'
import physicalMissingLive from '../../../../testdata/sync/scenarios/physical-missing-live.json'
import recreateDivergentFromDeletedBaseline from '../../../../testdata/sync/scenarios/recreate-divergent-from-deleted-baseline.json'
import recreateFromDeletedBaseline from '../../../../testdata/sync/scenarios/recreate-from-deleted-baseline.json'
import remoteEdit from '../../../../testdata/sync/scenarios/remote-edit.json'
import remoteEditVsLocalDelete from '../../../../testdata/sync/scenarios/remote-edit-vs-local-delete.json'
import remoteTombstone from '../../../../testdata/sync/scenarios/remote-tombstone.json'
import {
  decideEntity,
  decideRepository,
  type Decision,
  type LocalObservation,
  type RemoteObservation,
  type Baseline,
  type Annotations,
} from './decision'
import type { Entity } from './entity'
import { contentHash } from './canonical'

interface Scenario {
  observations: Observation[]
  expected: ScenarioDecision[]
}

interface Observation {
  syncId: string
  local: { state: string; entity?: Entity; revision?: string }
  remote: { state: string; entity?: Entity; version?: string }
  baseline?: { contentHash: string; remoteVersion: string; deleted: boolean }
  blocked?: boolean
}

interface ScenarioConflict {
  sourceSyncId: string
  conflictSyncId: string
  conflictEntity: Entity
  originalTombstone: boolean
  originalVersion?: string
  acceptRemoteOriginal: boolean
  originalEntity?: Entity
  originalTombstoneEntity?: Entity
  localStateHash: string
  remoteStateHash: string
}

interface ScenarioDecision {
  syncId: string
  kind: string
  reason?: string
  parentId?: string
  contentHash?: string
  deleted: boolean
  version?: string
  localRevision?: string
  conflict?: ScenarioConflict
}

const SCENARIOS: { name: string; data: Scenario }[] = [
  { name: 'blocked-path-conflict', data: blockedPathConflict as unknown as Scenario },
  { name: 'converged-delete', data: convergedDelete as unknown as Scenario },
  { name: 'divergent-edits', data: divergentEdits as unknown as Scenario },
  { name: 'first-local-upload', data: firstLocalUpload as unknown as Scenario },
  { name: 'first-remote-download', data: firstRemoteDownload as unknown as Scenario },
  { name: 'folder-structural-conflict', data: folderStructuralConflict as unknown as Scenario },
  { name: 'identical-edit-both', data: identicalEditBoth as unknown as Scenario },
  { name: 'identical-onboarding', data: identicalOnboarding as unknown as Scenario },
  { name: 'invalid-remote', data: invalidRemote as unknown as Scenario },
  { name: 'local-delete', data: localDelete as unknown as Scenario },
  { name: 'local-edit-vs-tombstone', data: localEditVsTombstone as unknown as Scenario },
  { name: 'one-sided-edit', data: oneSidedEdit as unknown as Scenario },
  { name: 'one-sided-rename', data: oneSidedRename as unknown as Scenario },
  { name: 'parent-cycle', data: parentCycle as unknown as Scenario },
  { name: 'physical-missing-absent', data: physicalMissingAbsent as unknown as Scenario },
  { name: 'physical-missing-live', data: physicalMissingLive as unknown as Scenario },
  { name: 'recreate-divergent-from-deleted-baseline', data: recreateDivergentFromDeletedBaseline as unknown as Scenario },
  { name: 'recreate-from-deleted-baseline', data: recreateFromDeletedBaseline as unknown as Scenario },
  { name: 'remote-edit', data: remoteEdit as unknown as Scenario },
  { name: 'remote-edit-vs-local-delete', data: remoteEditVsLocalDelete as unknown as Scenario },
  { name: 'remote-tombstone', data: remoteTombstone as unknown as Scenario },
]

/** Converts a Decision into the normalized trace form the Go engine commits. */
function normalizeDecision(d: Decision): ScenarioDecision {
  const out: ScenarioDecision = {
    syncId: d.syncId,
    kind: d.kind,
    deleted: d.deleted ?? false,
  }
  if (d.reason) out.reason = d.reason
  if (d.parentId) out.parentId = d.parentId
  if (d.contentHash) out.contentHash = d.contentHash
  if (d.version) out.version = d.version
  if (d.localRevision) out.localRevision = d.localRevision
  if (d.conflict) {
    const c = d.conflict
    const conflict: ScenarioConflict = {
      sourceSyncId: c.sourceSyncId,
      conflictSyncId: c.conflictSyncId,
      conflictEntity: c.conflictEntity,
      originalTombstone: c.originalTombstone,
      acceptRemoteOriginal: c.acceptRemoteOriginal,
      localStateHash: c.localStateHash,
      remoteStateHash: c.remoteStateHash,
    }
    if (c.originalVersion) conflict.originalVersion = c.originalVersion
    if (c.originalEntity) conflict.originalEntity = c.originalEntity
    if (c.originalTombstoneEntity) conflict.originalTombstoneEntity = c.originalTombstoneEntity
    out.conflict = conflict
  }
  return out
}

describe('shared scenario decisions (Go + TS identical)', () => {
  for (const { name, data } of SCENARIOS) {
    it(name, () => {
      const decisions: Decision[] = []
      for (const obs of data.observations) {
        const local: LocalObservation = {
          syncId: obs.syncId,
          state: obs.local.state as LocalObservation['state'],
          entity: obs.local.entity,
          revision: obs.local.revision,
        }
        const remote: RemoteObservation = {
          syncId: obs.syncId,
          state: obs.remote.state as RemoteObservation['state'],
          entity: obs.remote.entity,
          version: obs.remote.version,
        }
        let baseline: Baseline | undefined
        if (obs.baseline) {
          baseline = {
            contentHash: obs.baseline.contentHash,
            deleted: obs.baseline.deleted,
            remoteVersion: obs.baseline.remoteVersion,
          }
        }
        const annotations: Annotations = { pathConflict: obs.blocked ?? false }
        decisions.push(decideEntity(local, remote, baseline, annotations))
      }
      const normalized = decideRepository(decisions).map(normalizeDecision)
      expect(normalized).toEqual(data.expected)
    })
  }
})

describe('planning order and deleted-baseline rebuilds', () => {
  const parentId = '7f7f0d4e-b6c9-4d5a-a51a-0ef677788881'
  const childId = '6e6e9c3d-a5b8-4c49-9409-9de566677770'

  it('parents precede children even when the child comes first in the input', () => {
    const out = decideRepository([
      { syncId: childId, kind: 'pull-live', parentId },
      { syncId: parentId, kind: 'pull-live' },
    ])
    expect(out.map(d => d.syncId)).toEqual([parentId, childId])
  })

  it('tombstones are applied child-first', () => {
    const out = decideRepository([
      { syncId: parentId, kind: 'push-tombstone' },
      { syncId: childId, kind: 'push-tombstone', parentId },
    ])
    expect(out.map(d => d.syncId)).toEqual([childId, parentId])
  })

  it('a parent cycle never hangs planning', () => {
    const out = decideRepository([
      { syncId: parentId, kind: 'pull-live', parentId: childId },
      { syncId: childId, kind: 'pull-live', parentId },
    ])
    expect(out).toHaveLength(2)
  })

  it('an identical rebuild from a deleted baseline establishes a live baseline', () => {
    const e: Entity = {
      schemaVersion: 1, syncId: childId, kind: 'note', parentId: '', name: 'idea',
      markdown: '# x\n', contentHash: '', deleted: false,
      updatedBy: '1a2b3c4d-1111-4222-8333-444455556666', updatedAt: 1,
    }
    e.contentHash = contentHash(e.kind, e.parentId, e.name, e.markdown ?? '')
    const baseline: Baseline = { contentHash: e.contentHash, deleted: true, remoteVersion: 'v2' }
    const d = decideEntity(
      { syncId: childId, state: 'live', entity: e, revision: 'r1' },
      { syncId: childId, state: 'live', entity: e, version: 'v3' },
      baseline,
    )
    expect(d.kind).toBe('establish-baseline')
  })

  it('a divergent rebuild from a deleted baseline keeps the remote original live', () => {
    const local: Entity = { ...note('idea', '# a\n'), syncId: childId }
    const remote: Entity = { ...note('idea', '# b\n'), syncId: childId }
    local.contentHash = contentHash(local.kind, local.parentId, local.name, local.markdown ?? '')
    remote.contentHash = contentHash(remote.kind, remote.parentId, remote.name, remote.markdown ?? '')
    const baseline: Baseline = {
      contentHash: contentHash('note', '', 'idea', '# x\n'),
      deleted: true, remoteVersion: 'v2',
    }
    const d = decideEntity(
      { syncId: childId, state: 'live', entity: local, revision: 'r1' },
      { syncId: childId, state: 'live', entity: remote, version: 'v3' },
      baseline,
    )
    expect(d.kind).toBe('create-conflict')
    expect(d.conflict?.originalTombstone).toBe(false)
    expect(d.conflict?.acceptRemoteOriginal).toBe(true)
  })
})

function note(name: string, markdown: string): Entity {
  return {
    schemaVersion: 1, syncId: '', kind: 'note', parentId: '', name, markdown,
    contentHash: '', deleted: false,
    updatedBy: '1a2b3c4d-1111-4222-8333-444455556666', updatedAt: 1,
  }
}

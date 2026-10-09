import { describe, expect, it } from 'vitest'
import { fixtures } from './fixtures.js'
import {
  decideNote,
  isConvergedDeletion,
  LocalState,
  NoteDecisionKind,
  RemoteState,
} from './decision.js'

const SYNC_ID = '5d5d8b2c-94f7-4a38-8318-8cd4cb53dfa8'

function toLocal(o) {
  return {
    syncId: SYNC_ID,
    state: o.state,
    path: o.path || '',
    markdown: o.markdown || '',
    contentHash: o.contentHash || '',
    revision: o.revision || '',
  }
}

function toRemote(o) {
  return {
    syncId: SYNC_ID,
    state: o.state,
    path: o.path || '',
    markdown: o.markdown || '',
    contentHash: o.contentHash || '',
    version: o.version || '',
    retryable: o.retryable || false,
  }
}

// TestDecideNoteMatchesSharedFixture: the same decisions.json the Go engine
// asserts against, so both runtimes must agree on every row of the fixed R1
// decision table. The fixture pins the COMPLETE normalized decision (every
// executed field plus the full conflict plan), compared as a whole.
describe('decideNote matches the shared decision fixture', () => {
  for (const tc of fixtures.decisions.cases) {
    it(tc.name, async () => {
      const d = await decideNote({
        local: toLocal(tc.local),
        remote: toRemote(tc.remote),
        baseline: tc.baseline || null,
        pathConflict: tc.pathConflict,
      })
      expect(d).toEqual(tc.expected)
    })
  }
})

// TestDecideNoteUsesCurrentRemoteVersion: every remote conditional write uses
// the CURRENT cycle's remote version, never the baseline's stale one, and equal
// content with a new version refreshes the baseline.
describe('decideNote uses the current remote version', () => {
  const h0 = 'a'.repeat(64)
  const h1 = 'b'.repeat(64)
  const th = 'd'.repeat(64)
  const path = 'idea.md'
  const live = (hash, version) => ({
    state: RemoteState.LIVE, path, markdown: 'body\n', contentHash: hash, version,
  })
  const stale = { contentHash: h0, deleted: false, remoteVersion: 'v0' }

  it('push_live uploads with the current token v1, not the stale v0', async () => {
    const d = await decideNote({
      local: { syncId: SYNC_ID, state: LocalState.LIVE, path, markdown: 'body\n', contentHash: h1, revision: 'r' },
      remote: live(h0, 'v1'),
      baseline: stale,
      pathConflict: false,
    })
    expect(d.kind).toBe(NoteDecisionKind.PUSH_LIVE)
    expect(d.version).toBe('v1')
  })

  it('equal content with a new version refreshes the baseline', async () => {
    const d = await decideNote({
      local: { syncId: SYNC_ID, state: LocalState.LIVE, path, markdown: 'body\n', contentHash: h0, revision: 'r' },
      remote: live(h0, 'v1'),
      baseline: stale,
      pathConflict: false,
    })
    expect(d.kind).toBe(NoteDecisionKind.ESTABLISH_BASELINE)
    expect(d.version).toBe('v1')
  })

  it('a re-read after a failed CAS advances to the current token', async () => {
    const first = await decideNote({
      local: { syncId: SYNC_ID, state: LocalState.LIVE, path, markdown: 'body\n', contentHash: h1, revision: 'r' },
      remote: live(h0, 'v0'),
      baseline: stale,
      pathConflict: false,
    })
    expect(first.version).toBe('v0')
    const reRead = await decideNote({
      local: { syncId: SYNC_ID, state: LocalState.LIVE, path, markdown: 'body\n', contentHash: h1, revision: 'r' },
      remote: live(h0, 'v1'),
      baseline: stale,
      pathConflict: false,
    })
    expect(reRead.kind).toBe(NoteDecisionKind.PUSH_LIVE)
    expect(reRead.version).toBe('v1')
  })

  it('converged deletion with a stale version refreshes the baseline', async () => {
    const delStale = { contentHash: th, deleted: true, remoteVersion: 'v0' }
    const d = await decideNote({
      local: { syncId: SYNC_ID, state: LocalState.ABSENT, path: '', markdown: '', contentHash: '', revision: '' },
      remote: { state: RemoteState.TOMBSTONE, path, contentHash: th, version: 'v1' },
      baseline: delStale,
      pathConflict: false,
    })
    expect(d.kind).toBe(NoteDecisionKind.ESTABLISH_BASELINE)
    expect(d.version).toBe('v1')
  })
})

// TestDecideNoteIsDeterministic: repeating identical inputs repeats the exact
// decision, including the derived conflict identity and path.
describe('decideNote is deterministic', () => {
  it('repeats the same conflict identity and path', async () => {
    const input = {
      local: { syncId: SYNC_ID, state: LocalState.LIVE, path: 'a.md', markdown: 'body\n', contentHash: 'b'.repeat(64), revision: 'r' },
      remote: { state: RemoteState.LIVE, path: 'a.md', markdown: 'body\n', contentHash: 'a'.repeat(64), version: 'v2' },
      baseline: { contentHash: 'a'.repeat(64), deleted: false, remoteVersion: 'v0' },
      pathConflict: false,
    }
    const first = await decideNote(input)
    for (let i = 0; i < 5; i++) {
      const again = await decideNote(input)
      expect(again.kind).toBe(first.kind)
      if (again.conflict) {
        expect(again.conflict.conflictSyncId).toBe(first.conflict.conflictSyncId)
        expect(again.conflict.conflictPath).toBe(first.conflict.conflictPath)
      }
    }
  })
})

// TestDecideNoteUnknownOrInvalidNeverDeletes: unknown local state, invalid
// remote input, remote damage, and path conflicts always produce block/retry
// and never authorize a deletion.
describe('decideNote never deletes on unknown or invalid input', () => {
  const cases = [
    {
      name: 'unknown local, remote tombstone',
      local: { syncId: SYNC_ID, state: LocalState.UNKNOWN },
      remote: { state: RemoteState.TOMBSTONE, path: 'a.md', contentHash: 'b'.repeat(64), version: 'v1' },
      baseline: { contentHash: 'b'.repeat(64), deleted: false, remoteVersion: 'v0' },
    },
    {
      name: 'invalid remote, local absent',
      local: { syncId: SYNC_ID, state: LocalState.ABSENT },
      remote: { state: RemoteState.INVALID, path: 'a.md', retryable: false },
      baseline: { contentHash: 'b'.repeat(64), deleted: false, remoteVersion: 'v0' },
    },
    {
      name: 'remote missing with baseline, local absent',
      local: { syncId: SYNC_ID, state: LocalState.ABSENT },
      remote: { state: RemoteState.MISSING, path: 'a.md' },
      baseline: { contentHash: 'b'.repeat(64), deleted: true, remoteVersion: 'v1' },
    },
    {
      name: 'path conflict, local absent remote live',
      local: { syncId: SYNC_ID, state: LocalState.ABSENT },
      remote: { state: RemoteState.LIVE, path: 'a.md', contentHash: 'b'.repeat(64), version: 'v1' },
      baseline: { contentHash: 'b'.repeat(64), deleted: false, remoteVersion: 'v0' },
      pathConflict: true,
    },
  ]
  for (const c of cases) {
    it(c.name, async () => {
      const d = await decideNote({ ...c, baseline: c.baseline || null, pathConflict: c.pathConflict || false })
      expect([NoteDecisionKind.BLOCK, NoteDecisionKind.RETRY]).toContain(d.kind)
      expect(d.deleted).toBe(false)
    })
  }
})

describe('isConvergedDeletion', () => {
  it('recognizes the converged-deletion noop only', async () => {
    const converged = await decideNote({
      local: { syncId: SYNC_ID, state: LocalState.ABSENT },
      remote: { state: RemoteState.TOMBSTONE, path: 'a.md', contentHash: 'b'.repeat(64), version: 'v1' },
      baseline: { contentHash: 'b'.repeat(64), deleted: true, remoteVersion: 'v1' },
      pathConflict: false,
    })
    expect(isConvergedDeletion(converged)).toBe(true)
    const other = await decideNote({
      local: { syncId: SYNC_ID, state: LocalState.ABSENT },
      remote: { state: RemoteState.TOMBSTONE, path: 'a.md', contentHash: 'b'.repeat(64), version: 'v2' },
      baseline: { contentHash: 'b'.repeat(64), deleted: true, remoteVersion: 'v1' },
      pathConflict: false,
    })
    expect(isConvergedDeletion(other)).toBe(false)
  })
})

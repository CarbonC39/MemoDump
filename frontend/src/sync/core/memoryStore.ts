// In-memory RemoteStore mirroring internal/cloudsync/memory_store.go. It is
// backed by an append-only change log so the sync cursor behaves like a real
// provider's delta cursor: a valid cursor resumes exactly the changes after it
// (including physical deletions), and an invalid or empty cursor falls back to
// a full baseline scan. Keys modified while a scan is paginated simply appear
// in a later page.

import type { Capabilities, Change, ChangePage, RemoteStore } from './remoteStore'
import { StoreError } from './remoteStore'

interface MemoryObject {
  data: Uint8Array
  version: string
}

interface LogEntry {
  seq: number
  key: string
  type: Change['type']
  version: string
}

/**
 * Deterministic fault: fails the next matching operation. Op is one of
 * "create", "replace", "remove", "read", "list". Faults are consumed in order.
 * The mode flags shape the failure:
 *  - uncertainWrite: the create/replace SUCCEEDS but throws error, so the
 *    caller cannot know whether the write landed and must re-read;
 *  - cursorReject: list ignores even a valid cursor (full baseline);
 *  - incompleteSkip: list omits the last N keys (an incomplete scan).
 */
export interface Fault {
  op: string
  error: StoreError
  uncertainWrite?: boolean
  cursorReject?: boolean
  incompleteSkip?: number
}

export class MemoryStore implements RemoteStore {
  private objects = new Map<string, MemoryObject>()
  private log: LogEntry[] = []
  private seq = 0
  private faults: Fault[] = []

  async test(): Promise<Capabilities> {
    return { conditionalWrites: true, pagedListing: true, deltaCursor: true }
  }

  /** Queues a deterministic fault to fail the next matching operation. */
  armFault(op: string, error: StoreError): void {
    this.faults.push({ op, error })
  }

  /** Queues a fault where the next create/replace performs the write but
   * throws error, so the caller must re-read to learn the outcome. */
  armUncertainWrite(op: string, error: StoreError): void {
    this.faults.push({ op, error, uncertainWrite: true })
  }

  /** Queues a list fault that ignores a valid cursor and returns a full
   * baseline. */
  armCursorReject(): void {
    this.faults.push({ op: 'list', error: new StoreError('retryable-transport', 'cursor rejected'), cursorReject: true })
  }

  /** Queues a list fault that omits the last skip keys. */
  armIncompleteList(skip: number): void {
    this.faults.push({ op: 'list', error: new StoreError('retryable-transport', 'incomplete listing'), incompleteSkip: skip })
  }

  private takeFault(op: string): Fault | null {
    const idx = this.faults.findIndex(f => f.op === op)
    if (idx < 0) return null
    return this.faults.splice(idx, 1)[0]
  }

  private nextSeq(): number {
    this.seq++
    return this.seq
  }

  private versionOf(seq: number): string {
    return String(seq)
  }

  async create(key: string, bytes: Uint8Array): Promise<{ version: string }> {
    const fault = this.takeFault('create')
    if (fault && !fault.uncertainWrite) throw fault.error
    if (this.objects.has(key)) {
      throw new StoreError('precondition-failed', `key ${key} exists`)
    }
    const seq = this.nextSeq()
    this.objects.set(key, { data: new Uint8Array(bytes), version: this.versionOf(seq) })
    this.log.push({ seq, key, type: 'created', version: this.versionOf(seq) })
    if (fault) throw fault.error // write landed but the response was lost
    return { version: this.versionOf(seq) }
  }

  async replace(key: string, bytes: Uint8Array, expectedVersion: string): Promise<{ version: string }> {
    const fault = this.takeFault('replace')
    if (fault && !fault.uncertainWrite) throw fault.error
    const obj = this.objects.get(key)
    if (!obj) throw new StoreError('not-found', `key ${key} missing`)
    if (obj.version !== expectedVersion) {
      throw new StoreError('precondition-failed', 'stale expected version')
    }
    const seq = this.nextSeq()
    obj.data = new Uint8Array(bytes)
    obj.version = this.versionOf(seq)
    this.log.push({ seq, key, type: 'updated', version: this.versionOf(seq) })
    if (fault) throw fault.error // write landed but the response was lost
    return { version: this.versionOf(seq) }
  }

  /** Physically deletes a key, recording a deleted change. Not part of the
   * RemoteStore contract (V1 propagates deletions as entity tombstones): a key
   * reported physically removed after a known baseline is repository damage,
   * never a deletion. */
  async remove(key: string): Promise<void> {
    if (!this.objects.has(key)) throw new StoreError('not-found', `key ${key} missing`)
    this.objects.delete(key)
    this.log.push({ seq: this.nextSeq(), key, type: 'deleted', version: '' })
  }

  async read(key: string): Promise<{ bytes: Uint8Array; version: string }> {
    const fault = this.takeFault('read')
    if (fault) throw fault.error
    const obj = this.objects.get(key)
    if (!obj) throw new StoreError('not-found', `key ${key} missing`)
    return { bytes: new Uint8Array(obj.data), version: obj.version }
  }

  async list(prefix: string, syncCursor?: string): Promise<ChangePage> {
    const fault = this.takeFault('list')
    if (fault && fault.error && !fault.cursorReject && !fault.incompleteSkip) throw fault.error
    if (fault?.cursorReject) {
      // The provider rejects even a valid cursor: fall back to a full baseline
      // scan so no event is ever skipped.
      syncCursor = ''
    }

    // Reconstruct the set of keys and versions present at a given log position.
    // The log is append-only, so this is deterministic across baseline pages.
    const snapshotAt = (watermark: number): Map<string, string> => {
      const state = new Map<string, string>()
      for (const e of this.log) {
        if (e.seq > watermark) break
        if (e.type === 'deleted') state.delete(e.key)
        else state.set(e.key, e.version)
      }
      return state
    }
    const sortedKeys = (state: Map<string, string>): string[] =>
      [...state.keys()].filter(k => k.startsWith(prefix)).sort()

    interface Pending { seq: number; change: Change }
    let pending: Pending[] = []
    let lastSync = 0
    let delta = false

    const buildBaseline = (watermark: number, after: string): { changes: Pending[]; ok: boolean } => {
      const snapshot = snapshotAt(watermark)
      const keys = sortedKeys(snapshot)
      let start = 0
      if (after) {
        const idx = keys.indexOf(after)
        if (idx < 0) return { changes: [], ok: false }
        start = idx + 1
      }
      const changes = keys.slice(start).map(k => ({
        seq: 0,
        change: { key: k, type: 'created' as const, version: snapshot.get(k)! },
      }))
      return { changes, ok: true }
    }
    const resetBaseline = (): { changes: Pending[]; syncCursor: number } => ({
      changes: buildBaseline(this.seq, '').changes,
      syncCursor: this.seq,
    })

    if (syncCursor?.startsWith('base:')) {
      // Continue a paginated baseline scan; the token carries the watermark
      // captured by the first page. The watermark and continuation key are
      // validated: malformed, out-of-range, or stale tokens reset instead of
      // emitting a future cursor.
      const parts = syncCursor.split(':')
      const watermark = Number(parts[1])
      const after = parts.slice(2).join(':')
      const valid = parts.length === 3 &&
        Number.isSafeInteger(watermark) && watermark >= 0 && watermark <= this.seq
      if (!valid) {
        const reset = resetBaseline()
        pending.push(...reset.changes)
        lastSync = reset.syncCursor
      } else {
        const baseline = buildBaseline(watermark, after)
        if (!baseline.ok) {
          const reset = resetBaseline()
          pending.push(...reset.changes)
          lastSync = reset.syncCursor
        } else {
          pending.push(...baseline.changes)
          lastSync = watermark
        }
      }
    } else if (syncCursor) {
      const seq = Number(syncCursor)
      if (!Number.isSafeInteger(seq) || seq < 0 || seq > this.seq) {
        // Invalid or out-of-range delta cursor: reset to a full baseline so no
        // later event is ever skipped.
        const reset = resetBaseline()
        pending.push(...reset.changes)
        lastSync = reset.syncCursor
      } else {
        // Resume the delta stream after the given position.
        delta = true
        for (const e of this.log) {
          if (e.seq <= seq || !e.key.startsWith(prefix)) continue
          pending.push({ seq: e.seq, change: { key: e.key, type: e.type, version: e.version } })
          lastSync = e.seq
        }
        if (lastSync === 0) lastSync = seq // nothing new: resume from the same position
      }
    } else {
      // Empty cursor: full baseline scan at the current position.
      const reset = resetBaseline()
      pending.push(...reset.changes)
      lastSync = reset.syncCursor
    }

    if (fault?.incompleteSkip && !delta) {
      // Damage: the listing silently omits the last keys.
      pending = pending.length > fault.incompleteSkip ? pending.slice(0, -fault.incompleteSkip) : []
    }

    const pageSize = 100
    let page = pending
    let nextCursor = ''
    if (pending.length > pageSize) {
      page = pending.slice(0, pageSize)
      const lastInPage = page[page.length - 1]
      nextCursor = delta ? String(lastInPage.seq) : `base:${lastSync}:${lastInPage.change.key}`
    }
    return {
      changes: page.map(p => p.change),
      nextCursor,
      syncCursor: String(lastSync),
    }
  }
}

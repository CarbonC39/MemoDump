// Versioned remote entity model mirroring internal/cloudsync/entity.go.
//
// The wire contract: deterministic canonical serialization, a content hash over
// kind/parentId/name/markdown, and validation that rejects unknown/newer
// schemas, oversized records, invalid UTF-8/UUIDs, traversal names, and invalid
// media keys before anything is materialized.

import { canonicalBytes, contentHash, type CanonicalValue } from './canonical'

export const SCHEMA_VERSION = 1
export const KIND_NOTE = 'note'
export const KIND_FOLDER = 'folder'
export const MAX_ENTITY_BYTES = 1 << 20 // 1 MiB

/** Thrown for an unknown or newer schema version. */
export class SchemaError extends Error {
  constructor(message = 'unsupported schema version') {
    super(message)
    this.name = 'SchemaError'
  }
}

/** Thrown when a record exceeds MAX_ENTITY_BYTES. */
export class OversizedError extends Error {
  constructor(message = 'entity exceeds size limit') {
    super(message)
    this.name = 'OversizedError'
  }
}

/** Thrown when a record fails structural validation. */
export class InvalidEntityError extends Error {
  constructor(message = 'invalid entity') {
    super(message)
    this.name = 'InvalidEntityError'
  }
}

/** Thrown when a folder parent graph contains a cycle. */
export class CycleError extends Error {
  constructor(message = 'parent cycle') {
    super(message)
    this.name = 'CycleError'
  }
}

/** One record of entities/<syncId>.json. Folder records carry no markdown. */
export interface Entity {
  schemaVersion: number
  syncId: string
  kind: string
  parentId: string
  name: string
  markdown?: string
  contentHash: string
  deleted: boolean
  updatedBy: string
  updatedAt: number
}

/** Computes the canonical digest of the entity's content fields. */
export function computeContentHash(e: Entity): string {
  return contentHash(e.kind, e.parentId, e.name, e.markdown ?? '')
}

/** Serializes the entity canonically (sorted keys, trailing LF). */
export function serializeEntity(e: Entity): string {
  const fields: { [key: string]: CanonicalValue } = {
    schemaVersion: e.schemaVersion,
    syncId: e.syncId,
    kind: e.kind,
    parentId: e.parentId,
    name: e.name,
    contentHash: e.contentHash,
    deleted: e.deleted,
    updatedBy: e.updatedBy,
    updatedAt: e.updatedAt,
  }
  if (e.kind === KIND_NOTE) fields.markdown = e.markdown ?? ''
  return canonicalBytes(fields) + '\n'
}

const uuidV4Re = /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/

/** Reports whether s is a syntactically valid version-4 UUID. */
export function isUuidV4(s: string): boolean {
  return uuidV4Re.test(s)
}

/** Reports whether name is safe to materialize as a path segment. */
export function validEntityName(name: string): boolean {
  if (name === '' || name === '.' || name === '..') return false
  if (/[/\\]/.test(name)) return false
  if (name.startsWith('.')) return false
  for (const ch of name) {
    const cp = ch.codePointAt(0)!
    if (cp < 0x20 || cp === 0x7f) return false
  }
  return true
}

// Matches a valid content-addressed media key at the START of the text that
// follows a "memodump-media:" prefix (the caller slices off the prefix).
const mediaKeyRe = /^([a-f0-9]{64}\.(png|jpg|gif|webp|avif))/

/**
 * Returns the first invalid memodump-media: key referenced in markdown. An empty
 * reference (`memodump-media:` with no key) is invalid — the `invalid` flag
 * keeps that distinct from "no error".
 */
export function firstInvalidMediaKey(markdown: string): { key: string; invalid: boolean } {
  let idx = 0
  while (true) {
    const rel = markdown.indexOf('memodump-media:', idx)
    if (rel < 0) return { key: '', invalid: false }
    const start = idx + rel + 'memodump-media:'.length
    const rest = markdown.slice(start)
    const m = mediaKeyRe.exec(rest)
    if (m) {
      const next = start + m[1].length
      if (next >= markdown.length || !isKeyContinuation(markdown[next])) {
        idx = start + m[0].length
        continue
      }
    }
    const token = rest.split(/[ \t\n"')]\b/)[0]
    return { key: token === '' ? '<empty>' : token, invalid: true }
  }
}

function isKeyContinuation(b: string): boolean {
  return /[A-Za-z0-9.\-_/:]/.test(b)
}

const ALLOWED_KEYS = new Set([
  'schemaVersion', 'syncId', 'kind', 'parentId', 'name',
  'markdown', 'contentHash', 'deleted', 'updatedBy', 'updatedAt',
])

const contentHashRe = /^[0-9a-f]{64}$/

/** Validates a single entity's invariants before materialization. */
export function validateEntity(e: Entity): void {
  if (e.schemaVersion !== SCHEMA_VERSION) {
    throw new SchemaError(`unsupported schema version: ${e.schemaVersion}`)
  }
  if (e.kind !== KIND_NOTE && e.kind !== KIND_FOLDER) {
    throw new InvalidEntityError(`bad kind: ${e.kind}`)
  }
  if (e.kind === KIND_FOLDER && e.markdown) {
    throw new InvalidEntityError('folder carries markdown')
  }
  if (!isUuidV4(e.syncId)) throw new InvalidEntityError(`bad syncId: ${e.syncId}`)
  if (e.parentId && !isUuidV4(e.parentId)) throw new InvalidEntityError(`bad parentId: ${e.parentId}`)
  if (!isUuidV4(e.updatedBy)) throw new InvalidEntityError(`bad updatedBy: ${e.updatedBy}`)
  if (!validEntityName(e.name)) throw new InvalidEntityError(`bad name: ${e.name}`)
  if (!contentHashRe.test(e.contentHash)) {
    throw new InvalidEntityError(`bad contentHash: ${e.contentHash}`)
  }
  if (e.contentHash !== computeContentHash(e)) {
    throw new InvalidEntityError('content hash mismatch')
  }
  if (firstInvalidMediaKey(e.markdown ?? '').invalid) {
    throw new InvalidEntityError('invalid media key')
  }
}

/**
 * Parses and validates a raw remote entity record (a string or UTF-8 bytes).
 * The field set is strict: every required field must be present with the
 * correct type, unknown fields are rejected, and a note must carry markdown
 * while a folder must not. The 1 MiB cap is measured in UTF-8 bytes.
 */
export function parseEntity(data: string | Uint8Array): Entity {
  const byteLen = typeof data === 'string' ? new TextEncoder().encode(data).length : data.byteLength
  if (byteLen > MAX_ENTITY_BYTES) throw new OversizedError()
  let text: string
  if (typeof data === 'string') {
    text = data
  } else {
    try {
      text = new TextDecoder('utf-8', { fatal: true }).decode(data)
    } catch {
      throw new InvalidEntityError('invalid utf-8')
    }
  }
  let obj: unknown
  try {
    obj = JSON.parse(text)
  } catch {
    throw new InvalidEntityError('malformed json')
  }
  if (typeof obj !== 'object' || obj === null || Array.isArray(obj)) {
    throw new InvalidEntityError('not an object')
  }
  const raw = obj as Record<string, unknown>
  for (const key of Object.keys(raw)) {
    if (!ALLOWED_KEYS.has(key)) throw new InvalidEntityError(`unknown field: ${key}`)
  }
  for (const key of ['schemaVersion', 'syncId', 'kind', 'parentId', 'name', 'contentHash', 'deleted', 'updatedBy', 'updatedAt']) {
    if (!(key in raw)) throw new InvalidEntityError(`missing field: ${key}`)
  }
  const kind = requireString(raw, 'kind')
  if (kind === KIND_NOTE) {
    if (!('markdown' in raw)) throw new InvalidEntityError('note missing markdown')
  } else if (kind === KIND_FOLDER) {
    if ('markdown' in raw) throw new InvalidEntityError('folder must not carry markdown')
  } else {
    throw new InvalidEntityError(`bad kind: ${kind}`)
  }

  const entity: Entity = {
    schemaVersion: requireInt(raw, 'schemaVersion'),
    syncId: requireString(raw, 'syncId'),
    kind,
    parentId: requireString(raw, 'parentId'),
    name: requireString(raw, 'name'),
    contentHash: requireString(raw, 'contentHash'),
    deleted: requireBool(raw, 'deleted'),
    updatedBy: requireString(raw, 'updatedBy'),
    updatedAt: requireInt(raw, 'updatedAt'),
  }
  if (kind === KIND_NOTE) entity.markdown = requireString(raw, 'markdown')
  if (!Number.isSafeInteger(entity.updatedAt) || entity.updatedAt <= 0) {
    throw new InvalidEntityError('bad updatedAt')
  }
  validateEntity(entity)
  return entity
}

function requireString(raw: Record<string, unknown>, key: string): string {
  const v = raw[key]
  if (typeof v !== 'string') throw new InvalidEntityError(`field ${key} has the wrong type`)
  return v
}

function requireBool(raw: Record<string, unknown>, key: string): boolean {
  const v = raw[key]
  if (typeof v !== 'boolean') throw new InvalidEntityError(`field ${key} has the wrong type`)
  return v
}

function requireInt(raw: Record<string, unknown>, key: string): number {
  const v = raw[key]
  if (typeof v !== 'number' || !Number.isSafeInteger(v)) {
    throw new InvalidEntityError(`field ${key} has the wrong type`)
  }
  return v
}

/**
 * Validates a set of entities as a graph: every parentId must reference a
 * folder and the folder parent graph must be acyclic.
 */
export function validateEntities(entities: Record<string, Entity>): void {
  for (const id of Object.keys(entities)) {
    validateEntity(entities[id])
  }
  for (const id of Object.keys(entities)) {
    const e = entities[id]
    if (!e.parentId) continue
    const parent = entities[e.parentId]
    if (!parent) throw new InvalidEntityError(`missing parent ${e.parentId}`)
    if (parent.kind !== KIND_FOLDER) throw new InvalidEntityError('parent is not a folder')
  }
  const color = new Map<string, number>() // 0 white, 1 gray, 2 black
  const visit = (id: string): void => {
    const c = color.get(id) ?? 0
    if (c === 1) throw new CycleError(id)
    if (c === 2) return
    color.set(id, 1)
    const e = entities[id]
    if (e.parentId) visit(e.parentId)
    color.set(id, 2)
  }
  for (const id of Object.keys(entities)) visit(id)
}

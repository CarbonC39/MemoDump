// Repository descriptor (repo.json) model mirroring internal/cloudsync/repo.go.

import { canonicalBytes } from './canonical'
import { isUuidV4, InvalidEntityError, OversizedError, SchemaError, MAX_ENTITY_BYTES } from './entity'

export const REPOSITORY_FORMAT_VERSION = 1

export interface RepositoryDescriptor {
  formatVersion: number
  repositoryId: string
  createdAt: number
  minimumClientVersion: string
}

const ALLOWED_KEYS = new Set(['formatVersion', 'repositoryId', 'createdAt', 'minimumClientVersion'])

/** Serializes the descriptor canonically (sorted keys, trailing LF). */
export function serializeRepositoryDescriptor(d: RepositoryDescriptor): string {
  return canonicalBytes({
    formatVersion: d.formatVersion,
    repositoryId: d.repositoryId,
    createdAt: d.createdAt,
    minimumClientVersion: d.minimumClientVersion,
  }) + '\n'
}

/** Parses and validates repo.json. Every field is required, unknown fields and
 * trailing content are rejected, and newer formats are rejected. The 1 MiB cap
 * is measured in UTF-8 bytes. */
export function parseRepositoryDescriptor(data: string): RepositoryDescriptor {
  const byteLen = new TextEncoder().encode(data).length
  if (byteLen > MAX_ENTITY_BYTES) throw new OversizedError()
  let obj: unknown
  try {
    obj = JSON.parse(data)
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
  for (const key of ['formatVersion', 'repositoryId', 'createdAt', 'minimumClientVersion']) {
    if (!(key in raw)) throw new InvalidEntityError(`missing field: ${key}`)
  }
  const formatVersion = raw.formatVersion
  if (typeof formatVersion !== 'number' || !Number.isSafeInteger(formatVersion)) {
    throw new InvalidEntityError('field formatVersion has the wrong type')
  }
  if (formatVersion !== REPOSITORY_FORMAT_VERSION) {
    throw new SchemaError(`repository format ${formatVersion}`)
  }
  const repositoryId = raw.repositoryId
  if (typeof repositoryId !== 'string') throw new InvalidEntityError('field repositoryId has the wrong type')
  const createdAt = raw.createdAt
  if (typeof createdAt !== 'number' || !Number.isSafeInteger(createdAt) || createdAt <= 0) {
    throw new InvalidEntityError('bad createdAt')
  }
  const minimumClientVersion = raw.minimumClientVersion
  if (typeof minimumClientVersion !== 'string' || !minimumClientVersion) {
    throw new InvalidEntityError('empty minimumClientVersion')
  }
  if (!isUuidV4(repositoryId)) throw new InvalidEntityError(`bad repositoryId: ${repositoryId}`)
  return { formatVersion, repositoryId, createdAt, minimumClientVersion }
}

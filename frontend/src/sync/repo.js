// The schema-v1 repo.json for the browser sync port (R6.1). Matches
// internal/cloudsync/repo.go: every field is required, unknown fields and
// trailing content are rejected, newer formats are rejected, and the repository
// ID stays version-4 only.
import { canonicalBytes } from './canonical.js'
import { isUUIDv4 } from './uuid.js'
import { parseJSONObject, decodeWireInput, SyncParseError } from './jsonscan.js'

export const REPOSITORY_FORMAT_VERSION = 1
export const REPO_MAX_ENTITY_BYTES = 1 << 20 // 1 MiB
export const MAX_SAFE_INTEGER = 9007199254740991 // 2^53 - 1

const ALLOWED_REPO_FIELDS = new Set(['formatVersion', 'repositoryId', 'createdAt', 'minimumClientVersion'])

const INT_RE = /^-?(0|[1-9][0-9]*)$/

export class InvalidRepoError extends Error {
  constructor(message) { super(message) }
}

// serializeRepositoryDescriptor returns the canonical repo.json string (sorted
// keys, trailing LF).
export function serializeRepositoryDescriptor(d) {
  return canonicalBytes({
    formatVersion: d.formatVersion,
    repositoryId: d.repositoryId,
    createdAt: d.createdAt,
    minimumClientVersion: d.minimumClientVersion,
  }) + '\n'
}

// parseRepositoryDescriptor parses and validates repo.json. The input may be
// raw UTF-8 bytes or a JS string; bytes are size-checked and strictly UTF-8
// decoded before parsing.
export function parseRepositoryDescriptor(input) {
  let text
  try {
    text = decodeWireInput(input, REPO_MAX_ENTITY_BYTES)
  } catch (e) {
    throw new InvalidRepoError(e.message)
  }
  let fields
  try {
    fields = parseJSONObject(text).fields
  } catch (e) {
    if (e instanceof SyncParseError) throw new InvalidRepoError(e.message)
    throw e
  }
  const seen = new Set()
  for (const f of fields) {
    if (!ALLOWED_REPO_FIELDS.has(f.key)) throw new InvalidRepoError(`invalid entity: unknown field "${f.key}"`)
    seen.add(f.key)
  }
  for (const req of ['formatVersion', 'repositoryId', 'createdAt', 'minimumClientVersion']) {
    if (!seen.has(req)) throw new InvalidRepoError(`invalid entity: missing field "${req}"`)
  }
  const d = { formatVersion: null, repositoryId: '', createdAt: null, minimumClientVersion: '' }
  for (const f of fields) {
    switch (f.key) {
      case 'formatVersion':
        if (f.wasNull || f.raw === undefined || !INT_RE.test(f.raw)) throw new InvalidRepoError('invalid entity: field "formatVersion" has the wrong type')
        d.formatVersion = f.value
        break
      case 'repositoryId':
        if (f.wasNull || typeof f.value !== 'string') throw new InvalidRepoError('invalid entity: field "repositoryId" has the wrong type')
        d.repositoryId = f.value
        break
      case 'createdAt':
        if (f.wasNull || f.raw === undefined || !INT_RE.test(f.raw)) throw new InvalidRepoError('invalid entity: field "createdAt" has the wrong type')
        d.createdAt = f.value
        break
      case 'minimumClientVersion':
        if (f.wasNull || typeof f.value !== 'string') throw new InvalidRepoError('invalid entity: field "minimumClientVersion" has the wrong type')
        d.minimumClientVersion = f.value
        break
    }
  }
  if (d.formatVersion !== REPOSITORY_FORMAT_VERSION) {
    throw new InvalidRepoError(`invalid entity: repository format ${d.formatVersion}`)
  }
  if (!isUUIDv4(d.repositoryId)) throw new InvalidRepoError(`invalid entity: bad repositoryId "${d.repositoryId}"`)
  if (!Number.isInteger(d.createdAt) || d.createdAt <= 0 || d.createdAt > MAX_SAFE_INTEGER) {
    throw new InvalidRepoError(`invalid entity: bad createdAt ${d.createdAt}`)
  }
  if (d.minimumClientVersion === '') throw new InvalidRepoError('invalid entity: empty minimumClientVersion')
  return d
}

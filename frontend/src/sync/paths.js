// Portable path keys and deterministic conflict-copy naming (R6.1). Matches
// internal/cloudsync/names.go: paths collide when their NFC-normalized,
// stabilized-full-fold keys are equal, and conflict copies are named from the
// derived UUID v5 identity alone — no clock, no device label, no numeric suffix.
import { CASEFOLD_TABLE } from './casefold.js'

// portablePathKey returns the collision key for a local path: Unicode NFC
// normalization plus the stabilized full case fold, applied per code point with
// NO lowercase fallback (characters outside the table keep their value). This is
// byte-identical to the Go engine's ToLower(cases.Fold(NFC(path))).
export function portablePathKey(path) {
  let out = ''
  for (const cp of path.normalize('NFC')) {
    const folded = CASEFOLD_TABLE[cp]
    out += folded === undefined ? cp : folded
  }
  return out
}

// splitNotePath splits a slash-relative path into its directory and basename.
export function splitNotePath(path) {
  const i = path.lastIndexOf('/')
  return i < 0 ? ['', path] : [path.slice(0, i), path.slice(i + 1)]
}

// conflictFilename returns the deterministic conflict-copy filename for a
// derived conflict Sync ID: "<stem> (conflict <first 12 hex digits without
// hyphens>).md".
export function conflictFilename(stem, conflictSyncID) {
  let digits = conflictSyncID.replace(/-/g, '')
  if (digits.length > 12) digits = digits.slice(0, 12)
  return `${stem} (conflict ${digits}).md`
}

// conflictPath returns the deterministic conflict-note path: the original
// note's directory plus the conflict filename.
export function conflictPath(originalPath, conflictSyncID) {
  const [dir, base] = splitNotePath(originalPath)
  const name = conflictFilename(base.endsWith('.md') ? base.slice(0, -3) : base, conflictSyncID)
  return dir ? `${dir}/${name}` : name
}

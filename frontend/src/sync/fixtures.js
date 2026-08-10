// Shared cross-runtime fixtures for the browser sync port (R6.1). These are the
// SAME committed files the Go engine asserts against (testdata/sync), so the
// browser implementation must reproduce the exact bytes, hashes, and decisions.
// Imported only by tests and R6.2+ coordinator modules that need fixture data;
// the pure wire modules themselves perform no I/O.
import repoDescriptors from '../../../testdata/sync/repo-descriptors.json'
import canonicalMarkdown from '../../../testdata/sync/canonical-markdown.json'
import portablePathKeys from '../../../testdata/sync/portable-path-keys.json'
import conflictNames from '../../../testdata/sync/conflict-names.json'
import stateHashes from '../../../testdata/sync/state-hashes.json'
import noteRecords from '../../../testdata/sync/note-records.json'
import retryClasses from '../../../testdata/sync/retry-classes.json'
import decisions from '../../../testdata/sync/decisions.json'
import caseFold from '../../../testdata/sync/case-fold.json'

export const fixtures = {
  repoDescriptors,
  canonicalMarkdown,
  portablePathKeys,
  conflictNames,
  stateHashes,
  noteRecords,
  retryClasses,
  decisions,
  caseFold,
}

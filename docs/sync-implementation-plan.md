# MemoDump Cloud Sync Implementation Plan

Status: proposed execution plan
Date: 2026-08-04
Architecture contract: [`sync-spec.md`](sync-spec.md)

## 1. Purpose and authority

This document turns the cloud-sync specification into reviewable implementation
slices for the current repository. It defines dependency order, code ownership,
test gates, and intended file locations. It does not redefine wire formats or
conflict semantics; `sync-spec.md` remains authoritative when the two documents
differ.

The plan is deliberately incremental. Every phase must leave sync disabled by
default, preserve existing notes and image behavior, pass all existing tests,
and be safe to ship behind an experimental flag.

## 2. Current repository baseline

The implementation starts from these facts, not from an imagined greenfield
architecture:

- Go code is currently one flat `package main`, with shared global `dataDir` and
  HTTP handlers that directly read and mutate the filesystem.
- `/api/v2` currently implements paginated note/folder listing and search only.
  Mutations and the common frontend adapter still use legacy `/api/*` routes.
- `NoteDocument` has no optimistic `revision` contract yet.
- Wails serves the same HTTP mux through its asset handler. Except for native
  folder selection, it does not need a second synchronization API surface.
- The pure frontend stores notes and folders in version 1 of the `memodump`
  IndexedDB database through `frontend/src/localApi.js`.
- Browser-server offline note writes use the separate `memodump-outbox`
  database. It is not the cloud-sync journal and must remain separate.
- Feature 1 media staging uses the separate `memodump-media` database and has a
  working S3 client. External URL images remain independent from managed sync
  media.
- Go and Vitest already consume shared JSON fixtures from `testdata/`; sync must
  extend that pattern.
- The repository has Go unit/HTTP tests and Vitest tests, but no general service
  container, migration framework, filesystem watcher, or cross-process lock.

The critical dependency path is:

```text
Phase 0 local CAS/repository boundary
  ├─▶ Phase 1 formats + fixtures ───────────────┐
  └─▶ Phase 2 identity ─▶ Phase 3 WAL ─▶ Phase 4 scan
                                                └─▶ Phase 5 engine
                                                     ├─▶ Phase 6 Go runtime
                                                     └─▶ Phase 7 browser runtime
                                                          └─▶ Phase 8 providers
                                                               └─▶ Phase 9 media
                                                                    └─▶ Phase 10 release
```

Phase 1 and Phase 2 may overlap only after Phase 0 freezes the local repository
contract. Everything that performs real network I/O waits for the in-memory
engine gate in Phase 5.

## 3. Target module boundaries

Do not put the sync engine into `api.go`, `localApi.js`, or provider adapters.
The intended layout is:

```text
internal/
  vaultfs/                 filesystem note/folder repository + revisions
  cloudsync/               pure models, canonicalization, reconciliation engine
  syncindex/               .memodump/sync-index.json
  syncstate/               Replica registry, lock, snapshot, WAL, compaction
  syncprovider/
    dropbox/
    webdav/
    s3/

sync_service.go            Go worker lifecycle and dependency assembly
api_sync.go                authenticated /api/v2/sync/* handlers only

frontend/src/
  storage/localVaultDb.js  shared IndexedDB open/migrate/transaction boundary
  sync/
    core/                  TypeScript models, canonicalization, state machine
    storage/               IndexedDB sync index/state/conflicts
    providers/             Dropbox, WebDAV, S3 RemoteStore adapters
    coordinator/           leader election, scheduling, retries
  composables/useSync.js   UI-facing status and commands

testdata/sync/             fixtures consumed unchanged by Go and TypeScript
```

`internal/cloudsync` must not import `net/http`, provider SDKs, `package main`,
or filesystem globals. Its inputs are local/remote observations and durable
baselines; its outputs are explicit actions. Provider packages implement the
small `RemoteStore` interface and contain no conflict decisions.

`internal/vaultfs` becomes the only Go component allowed to materialize sync
changes in the vault. Existing HTTP handlers migrate to it incrementally so UI
writes, external scans, and cloud applies share collision and revision rules.
This is an extraction, not authorization for a wholesale API rewrite.

For the pure frontend, notes and sync state that must commit atomically live in
different object stores of the same `memodump` IndexedDB database. Separate
databases cannot provide the required transaction boundary. The existing
server-offline and media databases remain separate because their lifecycles are
independent. Sync stores are added by a lazy database-version upgrade only when
the user first enables sync; ordinary application upgrades do not create them.

## 4. Global implementation rules

These rules apply to every phase:

1. No cloud network call occurs until the local mutation that justifies it is
   durable.
2. Provider cursors become durable only after all covered entity baselines are
   durable.
3. Tests use an injected clock, UUID source, filesystem operations, and
   `RemoteStore`; correctness tests must not depend on sleeps or live services.
4. Secrets never enter fixtures, logs, errors, vault metadata, remote records,
   or snapshots/WAL payloads.
5. Sync-disabled filesystem vaults do not gain `.memodump`; a pure frontend
   vault that never enables sync does not gain sync object stores.
6. Existing `/api/*`, image URLs, note paths, and `VITE_LOCAL=1` behavior remain
   compatible while migrations are staged.
7. Go and TypeScript canonical bytes are compared through golden fixtures, not
   by assuming their JSON serializers happen to match.
8. Remote objects are untrusted and are validated before they enter the engine.
9. Every background worker has explicit `Start`, `Stop`, and cancellation
   behavior. Tests must prove shutdown at atomic boundaries.
10. A phase is incomplete until its failure paths and recovery behavior are
    tested, even if its happy-path UI works.

## 5. Phase 0 — Local revision and repository boundary

This phase applies even when cloud sync is disabled and is the only intentional
Feature 2 prerequisite exposed to all users.

### Deliverables

- Update `docs/api-contract.md` so `NoteDocument` includes `revision`, and note
  update/delete requests require `baseRevision` after the frontend migration.
- Define revision as an opaque, versioned digest of the adapter's durable local
  representation: raw Markdown bytes for filesystem notes and the canonical
  browser note record for IndexedDB. It is local CAS state, never a remote
  content hash, and is not compared across replicas. A same-content rewrite may
  retain the same revision; a content change cannot.
- Add v2 get/create/update/delete/move/duplicate endpoints. Return structured
  `409 local_revision_conflict` without touching the destination.
- Extract atomic note reads and mutations from handlers into `internal/vaultfs`.
  Route both legacy and v2 handlers through it before deleting duplicate logic.
- Make tag edits preserve unknown front-matter keys and formatting semantics
  needed by the sync spec; do not rebuild a document from only `tags` and body.
- Add a filesystem apply operation for the future sync worker with explicit
  expected revision and atomic sibling-temp replacement.
- Move pure-frontend DB opening and transactions from `localApi.js` into
  `frontend/src/storage/localVaultDb.js`; migrate note records to retain their
  canonical full Markdown representation and add revisions in one IndexedDB
  transaction. Existing v1 `{content, tags}` records migrate losslessly.
- Migrate frontend editor persistence to the v2 mutation contract. Keep a
  legacy adapter only for compatibility tests during the phase.

### Required tests

- Two stale server clients cannot overwrite one another.
- A remote-style apply racing an editor save produces a revision conflict.
- External file modification between read and update is detected.
- Rename/move collision leaves source and destination untouched.
- Each adapter returns stable revisions for unchanged data and a new revision
  for every durable semantic change; clients treat the value as opaque.
- Existing note, folder, import, search, offline outbox, and image tests pass.

### Exit gate

The frontend uses v2 CRUD in server/Wails builds, and all note writes in Go pass
through `vaultfs`. No sync metadata or worker exists yet.

## 6. Phase 1 — Wire contract and deterministic core fixtures

Implement formats before persistence or providers so incompatibilities appear
as small fixture diffs.

### Deliverables

- Add `testdata/sync/` fixtures for repository descriptors, entity records,
  canonical Markdown, content hashes, portable path keys, tombstones, malformed
  input, retry classes, and conflict names.
- Implement versioned model validation and canonical serialization in
  `internal/cloudsync` and `frontend/src/sync/core`.
- Add TypeScript support only for the new sync modules: add `typescript`, a
  no-emit config, and an `npm run typecheck` gate. Existing Vue/JS files need not
  migrate wholesale.
- Define the Go and TypeScript `RemoteStore` contracts and normalized errors:
  not-found, precondition-failed, auth, permission, rate-limit, quota, invalid
  response, unsupported capability, and retryable transport failure.
- Implement an in-memory RemoteStore in each language with CAS versions, cursor
  reset, and deterministic fault injection.

### Required tests

- Go and TypeScript produce byte-identical canonical entity records and hashes.
- Unknown/newer schemas, oversized objects, invalid UTF-8/UUIDs, parent cycles,
  traversal names, and invalid media keys are rejected before materialization.
- In-memory create/replace semantics match the provider contract.

### Exit gate

Both test suites consume the same fixtures without language-specific expected
values. No production provider code has started.

## 7. Phase 2 — Portable identity and Replica state foundation

### Deliverables

- Implement opt-in `.memodump/sync-index.json` creation, validation, backup,
  atomic replacement, directory sync where supported, and conservative rebuild.
- Ensure every filesystem listing, search, import, watcher, and image GC path
  excludes `.memodump` without following symlinks.
- Implement Vault ID, Device ID, and path-scoped Replica ID generation.
- Add an AppData registry mapping canonical vault locations to Replica IDs.
  A copied path receives a new Replica ID; an unambiguous move can be
  re-associated.
- Add configurable filesystem state root. Wails defaults to the OS application
  data directory. CLI/server supports a flag/environment override and documents
  the path that containers must persist.
- Implement a Replica-level process lock with build-tagged `x/sys/unix` and
  `x/sys/windows` files. A lock loser disables sync but can still edit notes.

### Required tests

- Never-enabled vaults remain byte-for-byte free of sync metadata.
- Initial enable assigns stable UUIDs without changing Markdown.
- Structural batches rewrite the index once; content-only saves never do.
- Primary-index corruption uses `.bak`; dual corruption stops safely.
- Copy, move, missing AppData, duplicate Vault ID, symlink, traversal, and
  case-collision fixtures produce the specified non-destructive outcomes.
- Two processes cannot own one Replica state directory.

### Exit gate

Identity survives rename, restart, copy, and missing device state, but nothing
contacts a provider yet.

## 8. Phase 3 — Go snapshot/WAL persistence

This phase implements Section 5.5 of the spec independently of the sync engine.

### Deliverables

- Add versioned snapshot and WAL record types with canonical checksums and
  monotonic sequence allocation.
- Implement the single writer actor: full-record append, short-write handling,
  `file.Sync()`, and acknowledgment only after success.
- Implement startup replay across snapshot, ordered frozen generations, and
  active WAL. Truncate only a syntactically partial unterminated tail.
- Implement rotation while holding the writer actor, unique frozen generation
  names, durable snapshot replacement, watermark-based deletion, and one
  compactor per Replica.
- Add build-tagged durable-replace and directory-sync helpers. Unsupported
  directory sync is an explicit platform capability, not silently simulated;
  failed file sync or atomic replacement stops the commit. Tests document the
  strongest guarantee actually available on each supported platform.
- Add compaction thresholds, streamed JSON output, buffer reuse, cancellation,
  metrics, and fault-injectable filesystem operations.

### Required tests

- Appenders run continuously during repeated compaction with no missing or
  duplicate durable sequence.
- Inject termination/failure after every numbered rotation step and recover.
- Cover short writes, failed fsync, rename failure, missing active WAL, multiple
  frozen generations, stale temp files, sequence gaps, torn tail, and complete
  bad-checksum records.
- `go test -race` passes the writer/compactor stress suite.
- Benchmarks report peak heap, allocations, writer-lock hold time, compaction
  duration, and fsync distributions for small and large state sets.

### Exit gate

The persistence package can durably store arbitrary baseline/cursor transitions
under crash and concurrency tests. It is not yet wired to network code.

## 9. Phase 4 — Filesystem scan and reconciliation inputs

### Deliverables

- Build a `vaultfs` scanner that returns stable note/folder observations after
  size/mtime settle, ignores reserved paths, and never follows unsafe symlinks.
- Compare scans with the portable index and durable baselines. Ordinary note
  changes are inferred from Markdown bytes; they do not append dirty WAL rows.
- Support in-app rename identity directly. Add conservative offline rename
  inference and explicit delete-plus-create fallback for ambiguous cases.
- Start with periodic scans as the correctness mechanism; add a filesystem
  watcher only as a latency optimization. Watcher overflow/error forces a scan.
- Represent baseline-unknown state and remote Probe requirements without
  scheduling destructive actions.

### Required tests

- External create/edit/delete/rename, editor race, offline ambiguous rename,
  empty folders, watcher overflow, hidden directories, case-only names, and
  unstable writes all produce deterministic observations.
- A missing AppData scan preserves indexed local entities and schedules Probe,
  never upload/delete/tombstone.

### Exit gate

Given a vault plus durable state, the scanner produces complete deterministic
inputs for the pure engine without performing cloud I/O.

## 10. Phase 5 — Reconciliation engine with in-memory remotes

### Deliverables

- Implement pull-first cycles, baseline comparison, CAS actions, desired-state
  coalescing, retry scheduling, and cursor commit ordering in `cloudsync`.
- Implement first-use onboarding for local-only, remote-only, both-non-empty,
  known repository, and repository mismatch.
- Implement note conflicts, edit/delete conflicts, folder-subtree pauses,
  portable path conflicts, tombstones, and conflict resolution transitions.
- Implement the same transition tables in TypeScript. Share fixtures and
  scenario traces; do not share generated source code.
- Add a deterministic multi-replica simulation harness with crash points before
  and after every local, remote, baseline, and cursor boundary.

### Required tests

- Run release scenarios 1–9 and all conflict cases from `sync-spec.md` entirely
  against the in-memory RemoteStore.
- Property/scenario tests assert convergence, idempotence, no silent overwrite,
  and no operation against a different provider profile.
- Go and TypeScript produce the same normalized action trace for shared inputs.

### Exit gate

Two simulated replicas converge through create/edit/move/delete/conflict and
crash recovery without a real cloud account.

## 11. Phase 6 — Go worker, APIs, and runtime lifecycle

### Deliverables

- Assemble `vaultfs`, index, state, engine, credentials, and RemoteStore behind
  `sync_service.go`; do not expose internal packages directly to handlers.
- Define a `SecretStore` boundary. Wails uses an OS credential-store adapter
  with an explicit warned fallback; server/Docker reads environment or a
  permission-restricted file outside the vault. Only opaque secret references
  enter worker configuration.
- Add the authenticated `/api/v2/sync/*` operations from the spec in
  `api_sync.go`, structured errors, redacted config, and a lightweight status
  event stream with polling fallback.
- Start one worker after server/Wails data-directory initialization and stop it
  through context cancellation. Switching the Wails data directory remains a
  restart boundary in V1.
- Add CLI/environment state-root and provider-secret configuration hooks.
- Gate everything behind an experimental setting; enabling requires explicit
  onboarding confirmation.

### Required tests

- Handler auth/ownership, secret redaction, start/stop, manual run, pause,
  reconnect, provider switch, second-process lock failure, and shutdown during
  each atomic boundary.
- Server worker continues without browser clients; Wails stops with its process.

### Exit gate

Go builds can complete all in-memory-provider scenarios through public APIs and
survive restart using real index/WAL files.

## 12. Phase 7 — Pure frontend storage and coordinator

### Deliverables

- On first sync enable, lazily upgrade the `memodump` IndexedDB schema with sync
  metadata, entity mapping, baselines, conflicts, provider profile, and
  coordinator stores. Normal note access opens the current database version so
  a previously enabled database does not fail with `VersionError`. Migration
  failure leaves pre-sync data readable in recovery mode.
- Make remote note/folder apply plus baseline advancement one IndexedDB
  transaction. Provider cursor advancement commits last.
- Implement Web Locks leadership, `BroadcastChannel` status/invalidation, and a
  lease fallback with expiry and fencing tokens.
- Add storage-persistence request, quota/eviction handling, export/rejoin
  recovery, online/startup/manual scheduling, and page-lifecycle cancellation.
- Add browser credential handling: session-only by default, explicit opt-in
  remembered WebDAV/S3 secrets, and no refresh token in page-readable storage
  for Dropbox V1.
- Expose the same commands/status shape through `useSync.js` that server/Wails
  receive from `/api/v2/sync/*`.

### Required tests

- Fake IndexedDB migration, atomic rollback, quota failure, eviction/rejoin,
  service-worker update recovery, leader failover, stale lease fencing, and two
  tabs editing during sync.
- Pure frontend runs the Phase 5 simulation suite through its real IndexedDB
  adapter.

### Exit gate

The local PWA can sync two browser replicas against the in-memory RemoteStore
while only one tab owns network work.

## 13. Phase 8 — Production providers

Providers are added one at a time and cannot change engine semantics.

### Dropbox

- Implement App Folder OAuth/PKCE, token refresh/reauthorization boundaries,
  conditional revision writes, paged listing, cursor reset, rate limits, and
  unknown-result reconciliation.
- Ship first because its revision and delta model exercises the complete
  RemoteStore contract directly.

### WebDAV

- Probe ETag/`If-Match` correctness before enabling.
- Implement RFC 6578 when supported and `PROPFIND Depth: 1` fallback.
- Reject servers that ignore conditional writes. Document browser CORS and
  redirect/header restrictions separately from Go behavior.

### S3-compatible

- Use a sync-specific private bucket/prefix and configuration, never the Feature
  1 public image destination implicitly.
- Implement conditional create/replace and paged `ListObjectsV2`; reject targets
  that do not honor the required preconditions.
- Reuse low-level S3 configuration/normalization only where semantics match;
  do not couple `imageS3Config` to sync repository state.

### Provider gate

Each adapter must pass the same contract suite in Go and browser form where
applicable. Live tests are opt-in, use isolated random prefixes, redact all
output, and clean only their own prefix. Network uncertainty tests cover write
accepted/response lost, auth expiry, quota, retry-after, stale CAS, invalid
cursor, partial listing, and repository reset detection.

## 14. Phase 9 — Managed media and image migration

### Deliverables

- Keep existing external S3 URLs and `/api/images/` behavior unchanged.
- Add immutable managed media upload/download/cache operations to RemoteStore
  coordination. Upload bytes before publishing the first referencing entity.
- Add `memodump-media:<key>` parsing and rendering in WYSIWYG, raw Markdown,
  search, import/export, Go filesystem builds, and pure frontend.
- Add verified magic-byte/hash download, evictable cache, explicit pinning, and
  missing-media status.
- Provide explicit migration from legacy `/api/images/<key>` references. Never
  silently rewrite notes or delete the only verified image bytes.

### Exit gate

Release scenarios 17–20 pass across all three builds, including offline reopen
and provider switching.

## 15. Phase 10 — Product UI and release hardening

### Deliverables

- Add onboarding, provider setup/test, status, pending work, pause/resume,
  reconnect, conflict center, path-conflict resolution, and destructive-action
  confirmations.
- Keep local-save status independent from cloud status.
- Add English and Chinese strings together; no raw provider error or secret is
  rendered.
- Document browser CORS/OAuth limitations, server container persistence,
  provider permissions, backups, recovery, and the lack of E2EE.
- Run cross-platform filename, Windows durable-replace/lock, browser storage,
  large-vault, race, crash, and live-provider matrices.

### Release gate

All 23 required scenarios in `sync-spec.md` pass. The experimental flag remains
until telemetry-free local diagnostics, recovery UI, and manual tests have been
reviewed on Windows, macOS, Linux, and at least two browser engines.

## 16. Commit and review strategy

Each numbered phase should normally be one review series, not one giant commit.
Within a phase, prefer this order:

1. contract/fixtures;
2. pure implementation;
3. persistence or adapter integration;
4. API/UI integration;
5. failure tests and documentation.

Do not mix provider adapters into WAL/index changes, or UI redesign into the
reconciliation engine. A reviewer must be able to run a phase's tests without
real provider credentials. Schema/wire changes require fixtures and migration
code in the same review.

## 17. Standard verification commands

Run the narrow package/test during development, then the full gates before each
phase is merged:

```sh
go test ./...
go test -race ./...

cd frontend
npm test
npm run typecheck
npm run build
npm run build:local
```

Provider live tests and long crash/benchmark suites remain explicit opt-in jobs;
their commands must be added when the corresponding harness exists. Normal CI
must always run shared fixtures, in-memory provider scenarios, WAL recovery, and
IndexedDB migration tests without secrets or network access.

## 18. First implementation checkpoint

The first coding assignment is Phase 0 only. It should not create
`.memodump`, implement WAL, add provider dependencies, or add sync settings UI.
Its review question is narrow: **can every local writer detect a stale revision
and use one repository boundary without regressing existing behavior?**

Only after that checkpoint merges should Phases 1 and 2 begin. Phase 1's pure
fixtures and Phase 2's filesystem identity work may then proceed in parallel if
their model types have already been frozen by the shared contract.

# MemoDump Cloud Sync — Simplified Implementation Plan

Status: proposed handoff plan
Date: 2026-08-08
Architecture contract: [`sync-spec-lite.md`](sync-spec-lite.md)

## 1. How to use this plan

Implement one phase at a time. Do not give an implementation agent the whole
file as one coding assignment. Each phase has a narrow exit gate and must leave
the repository compiling with sync disabled by default.

When this plan and historical documents differ, `sync-spec-lite.md` wins. In
particular, do not restore WAL, compaction, a persisted operation queue, durable
conflict records, or parallel workers. Do not start a real provider until the
shared in-memory crash scenarios pass.

For every phase:

1. read `CLAUDE.md`, the lite spec, this phase, and the files named by it;
2. inspect current code before editing—several prerequisites already exist;
3. add/adjust tests with the implementation, using injected IDs/faults rather
   than sleeps or live services;
4. run the narrow tests, then the standard verification commands;
5. stop at the exit gate and report remaining failures instead of continuing
   into the next phase.

## 2. Current baseline: keep, refactor, remove

### Keep

- `internal/vaultfs`: repository boundary, revision CAS, front-matter
  preservation, reserved paths, stable scanner, and symlink handling.
- `internal/syncindex`: opt-in portable identity, validation, backup, atomic
  replace, and tests.
- `internal/cloudsync`: canonical entity/repository formats, normalized store
  errors, `RemoteStore`, memory store, and shared fixtures.
- `frontend/src/storage`: canonical Markdown/revision storage.
- `frontend/src/sync/core`: TypeScript wire contract and memory store.
- `testdata/sync`: cross-language contract fixtures.
- From `internal/syncstate`: Device/Replica identity, path registry, replica
  process lock, state-root selection, and the small durable-replace helpers.

### Refactor

- `internal/syncscan/reconcile.go` currently depends on WAL-backed baselines and
  decides states too early. Make it produce local observations only; the new
  pure engine compares them with remote and snapshot state and makes identity/
  repair decisions.
- Existing conflict names use clocks and device labels. Replace them with the
  deterministic UUID/path contract from the lite spec.
- Sync-ID validation currently accepts only UUID v4. Add a separate `IsSyncID`
  accepting v4/v5; do not weaken Vault/Replica/Device/Repository validation.

### Remove after replacement is wired

- WAL records and replay;
- compaction, generations, checksums, sequence/watermark code and benchmarks;
- `syncstate.Store.Put/Delete`, `PutBaseline/GetBaseline`, and any scan API that
  reads them.

Do not delete the retained identity/registry/lock files merely because they
share the `syncstate` package. Do not delete legacy state files from a user's
disk; new code simply ignores them.

## 3. Target module boundaries

Use the current layout rather than a broad repository reorganization:

```text
internal/cloudsync/       wire models, pure decisions, action/result types,
                          memory remote, shared scenario runner
internal/syncindex/       portable identity only
internal/syncstate/       identity/registry/lock + one snapshot store
internal/syncscan/        stable local observations only
internal/syncrun/         serialized coordinator executing planned actions
internal/syncprovider/
  s3/
  webdav/
  dropbox/
internal/vaultfs/         only filesystem materialization boundary

sync_service.go           Go lifecycle and dependency assembly
api_sync.go               authenticated /api/v2/sync/* handlers

frontend/src/sync/
  core/                   wire models and pure decisions
  storage/                opt-in memodump-sync IndexedDB
  coordinator/            serialized browser cycle + Web Lock
  providers/              adapters only; no conflict decisions
frontend/src/composables/useSync.js
```

`cloudsync` must not import filesystem, HTTP clients, provider SDKs, IndexedDB,
Vue, or `package main`. Provider adapters must not decide conflicts. `syncrun`
owns ordering and side effects but asks the pure core for decisions.

## 4. Phase 0 — Freeze the amended shared contract

Purpose: remove ambiguities before persistence or engine work.

### Tasks

1. Add `IsSyncID` in Go and TypeScript. Accept UUID v4/v5 for entity `syncId`
   and `parentId`; retain UUID-v4-only checks for all other IDs.
2. Keep the existing entity `contentHash` algorithm unchanged. Add a helper for
   the complete state hash over canonical `{contentHash, deleted}`.
3. Define the fixed MemoDump conflict namespace UUID, deterministic UUID v5,
   and deterministic conflict filename in both languages.
4. Remove clock-based conflict naming from the core API. It may remain only as
   an unrelated UI formatting helper if something already uses it.
5. Add shared fixtures for state hashes, derived conflict IDs/names, v5-valid
   Sync IDs, invalid v5 use as Repository/Device IDs, and collision behavior.
6. Clarify `RemoteStore.List` tests: full listing is complete; delta listing can
   report physical removal but that is damage, not a tombstone.

### Likely files

- `internal/cloudsync/entity.go`, `canonical.go`, `names.go`
- `frontend/src/sync/core/entity.ts`, `canonical.ts`, `names.ts`
- `internal/syncindex/index.go`
- `testdata/sync/entities.json`, `conflict-names.json`, plus a new
  `state-hashes.json`

### Required tests

- Go and TypeScript produce identical state hashes, UUIDs, and filenames.
- Repeating a conflict derivation produces the same result.
- Swapping local/remote *content values* while retaining role labels changes
  the derivation when the conflict semantics differ.
- Existing entity/repository/memory-store fixtures still pass.

### Exit gate

The shared wire and deterministic-ID contract is frozen. No local state format
or production behavior has changed.

## 5. Phase 1 — Replace WAL state with one snapshot

Purpose: complete the architectural cutover before an engine depends on state.

### Tasks

1. Add versioned `Snapshot`, `SnapshotEntity`, validation, and canonical JSON
   serialization in `internal/syncstate/snapshot.go`.
2. Implement a small `SnapshotStore`:
   - `Load(expectedIdentity) -> snapshot or discard reason`;
   - distinguish not-exist/corrupt/identity-mismatch from real I/O error;
   - `Replace(snapshot)` using one temp file, file sync, atomic replace, and
     directory sync where supported;
   - no backup, append, partial update, compactor, or background goroutine.
3. Preserve state root, registry, and replica lock behavior. Use
   `<root>/<vaultId>/<replicaId>/state.json`.
4. Refactor `syncscan` so it no longer accepts `*syncstate.Store`. Its output
   contains indexed present/missing/blocked/unstable observations and unindexed
   observations only. It performs no remote or baseline decision, and rename/
   repair inference is deferred to the engine/coordinator (which derives a
   temporary local digest from remote Markdown only when the remote equals the
   snapshot baseline). Until then an offline rename degrades to lossless
   delete-plus-create.
5. Delete the now-unreachable WAL/baseline/compaction implementation and its
   benchmarks. Keep durability helpers used by the snapshot.
6. Add a regression test that legacy `state.snapshot.json` and WAL files are
   ignored and never treated as a baseline.
7. Update `CLAUDE.md` only after the old code is actually gone.

### Snapshot validation details

- exact schema version and non-null entity map;
- UUID-v4 Vault/Replica/Repository IDs;
- provider fingerprint and content hashes are lowercase 64-hex;
- Sync IDs pass `IsSyncID`;
- remote version is non-empty for every stored baseline;
- cursor is opaque and optional;
- duplicate JSON fields and trailing content are rejected if the parser can
  detect them without a large custom framework.

### Required tests

- round trip and deterministic serialization;
- missing, truncated, malformed, unknown schema, wrong identity/profile, and
  permission/read failure classifications;
- failure injection at create/write/sync/rename/directory-sync boundaries;
- failed replace leaves the prior valid snapshot loadable where the platform
  guarantee allows it;
- one successful cycle-equivalent replace performs one state-file rewrite;
- scanner classifications never turn blocked/unstable/read failure into absent.

### Exit gate

`rg "WAL|compaction|PutBaseline|GetBaseline" internal` finds no live sync-state
implementation or call site (comments about migration may remain). All tests
pass without `state.wal.ndjson`.

## 6. Phase 2 — Pure reconciliation engine and shared scenarios

Purpose: prove decisions without filesystem, IndexedDB, HTTP, or providers.

### Tasks

1. Define small immutable inputs in Go and TypeScript:
   - local observation: present/absent/unknown plus canonical entity and local
     revision when present;
   - remote observation: live/tombstone/missing/invalid plus version;
   - optional snapshot baseline;
   - path/graph conflict annotations.
2. Define normalized decisions such as `noop`, `establish-baseline`,
   `pull-live`, `push-live`, `push-tombstone`, `apply-tombstone`,
   `create-conflict`, `repair-index`, `block`, and `retry`.
3. Implement the known-baseline and no-baseline tables exactly once per
   language as pure functions. Do not put I/O or retry loops in them.
4. Add repository-wide planning order: parents before live children,
   conflict preservation before destructive actions, and tombstones
   child-first.
5. Extend both memory stores with deterministic "write accepted, response
   lost", stale precondition, cursor rejection, incomplete listing, and
   physical-removal faults if not already available.
6. Create shared JSON scenario traces in `testdata/sync/scenarios/`. Each trace
   contains initial local/index/snapshot/remote state, one event, normalized
   expected decisions, and expected final canonical state.
7. Build a small simulator that can stop before/after each abstract local,
   index, remote, recovery, and snapshot boundary, restart from durable state,
   and run until quiescent.

### Minimum shared scenarios

- first local upload, first remote download, identical onboarding;
- one-sided edit and rename;
- simultaneous identical edit;
- divergent note edits;
- local delete, remote tombstone, and both directions of edit/delete;
- deterministic conflict create replay and create-response loss;
- stale replace CAS followed by remote read;
- snapshot missing/corrupt and snapshot-write failure;
- remote object physically missing versus valid tombstone;
- repository/profile mismatch;
- path collision, invalid record, parent cycle, folder structural conflict;
- cursor rejection and a blocked change preventing cursor advancement.

### Exit gate

Go and TypeScript emit the same normalized decisions for every shared scenario.
The simulator converges without duplicate conflict IDs under every injected
restart. There are still no real providers or UI worker.

## 7. Phase 3 — Go filesystem coordinator with the memory remote

Purpose: connect the proven decisions to real local atomic boundaries.

### Tasks

1. Add only the missing `vaultfs` operations:
   - stable full-Markdown read with revision;
   - exact-path note create-if-absent (no timestamp de-collision);
   - CAS replace/move needed by pull;
   - CAS/revalidated note delete;
   - exact folder create/move/delete with collision checks;
   - no direct filesystem work in `syncrun`.
2. Implement a filesystem recovery store under the replica directory. Write
   `<recovery>/<syncId>/<stateHash>.md` atomically and idempotently before a
   pulled tombstone deletes a note.
3. Implement `internal/syncrun.Coordinator` with injected index, snapshot,
   scanner, repository, recovery store, clock/ID attribution, and
   `RemoteStore`. Serialize `Run` and require the existing replica lock.
4. Build canonical local entities from the indexed folder graph. Read Markdown
   only for notes that the cycle needs; keep raw local revision separate.
5. Implement exact action ordering from spec Section 6. Reserve conflict IDs in
   the index before replacing/deleting originals.
6. After CAS/transport uncertainty, re-read instead of guessing. Recompute final
   baselines from known-equal states and replace the snapshot once.
7. Remove settled tombstone path mappings safely. Ensure the union of index,
   snapshot, and remote IDs still observes old tombstones.
8. Add an in-memory or temp-directory repository harness with two replicas.

### Required tests

- all Phase 2 scenarios through real `vaultfs`, `syncindex`, snapshot files,
  and memory remote;
- external edit races every pull replace/delete and survives CAS failure;
- crash points around conflict index reservation, conflict file creation,
  original replacement/delete, index cleanup, and snapshot replace;
- exact-path collision never invokes existing timestamp de-collision helpers;
- recovery write failure prevents delete; repeated recovery is idempotent;
- empty folders and child-first folder deletion;
- index write failure prevents snapshot advancement and restart converges.

### Exit gate

Two temporary filesystem replicas converge through the full in-memory scenario
suite. A killed/restarted coordinator needs no WAL or pending queue.

## 8. Phase 4 — Pure-frontend storage and coordinator with memory remote

Purpose: match the Go behavior using actual IndexedDB boundaries.

### Tasks

1. Add `frontend/src/sync/storage/syncDb.ts` (or `.js` if the surrounding call
   site requires it). Do not change the normal `memodump` DB version merely to
   pre-create sync stores.
2. Lazily create `memodump-sync` on enable with:
   - metadata store keys `index` and `snapshot`;
   - recovery store keyed by `syncId/stateHash`.
3. Implement strict index/snapshot validation and one-transaction replacement
   of each logical record. Never store bodies in the snapshot.
4. Add a browser local adapter over the existing note/folder stores with
   transaction-scoped local revision CAS and exact-path create.
5. Implement the same coordinator ordering and uncertainty re-reads as Go.
   Cross-database atomicity is intentionally not simulated; tests restart
   between notes-DB and sync-DB commits.
6. Acquire a Web Lock scoped to Vault ID + provider fingerprint before a cycle.
   If unavailable or already held, return a visible status and perform no sync
   mutation. Do not add a lease/fencing subsystem in V1.
7. Prove no sync database is opened by normal local app startup, note CRUD, or
   image upload when sync was never enabled.

### Required tests

- the shared Phase 2 scenarios through fake IndexedDB and memory remote;
- transaction abort/quota failure for index, snapshot, recovery, and note apply;
- restart between every cross-database boundary;
- two tabs: only the Web-Lock owner runs, while both retain working local CAS;
- unavailable Web Locks disables only sync;
- never-enabled browser storage has no `memodump-sync` database.

### Exit gate

The browser and filesystem adapters pass equivalent scenarios using their real
local persistence. No production provider exists yet.

## 9. Phase 5 — Manual service/API and minimal status UI

Purpose: expose one manually triggered cycle without adding scheduling
complexity.

### Tasks

1. Add `sync_service.go` to assemble identities, lock, index, snapshot,
   repository/local adapter, provider configuration, and coordinator. It owns
   cancellation and exposes no internal store directly.
2. Add authenticated endpoints in `api_sync.go` and register them in
   `buildAPIMux()` for both CLI and Wails:
   - `GET /api/v2/sync/status`;
   - `POST /api/v2/sync/setup/test`;
   - `POST /api/v2/sync/enable`;
   - `POST /api/v2/sync/run`;
   - `POST /api/v2/sync/disable` (disconnect only; never delete notes/remote);
   - `GET /api/v2/sync/recovery` and a recover action.
3. Define one redacted provider-config boundary. The returned API model contains
   provider kind, configured/fingerprint state, repository ID, and warnings,
   never secret values.
4. Add the equivalent pure-frontend commands behind `useSync.js`.
5. Add minimal settings/status UI: enable/test, manual Sync, phase, last
   completed time, blocked/conflict/error counts, actionable errors, and the
   no-E2EE warning. Add English and Chinese strings together.
6. Keep the feature hidden behind an experimental flag. Do not add timers,
   online listeners, or a retry queue.

### Required tests

- auth and secret redaction;
- double manual run returns already-running without a second coordinator;
- disable during a cycle cancels only at an atomic boundary and keeps index;
- status never reports synced after a failed snapshot commit;
- Wails and CLI share routes/lifecycle;
- pure frontend and server expose equivalent normalized status.

### Exit gate

A user can configure and manually run the complete memory-provider flow through
public UI/API surfaces. Sync remains opt-in and experimental.

## 10. Phase 6 — S3-compatible provider

S3 is first because the repository already depends on MinIO in Go and
`aws4fetch` in the browser. Reuse low-level endpoint/signing normalization only;
do not reuse the public image-host profile or prefix.

### Tasks

- private bucket/prefix configuration and a distinct fingerprint;
- `repo.json`/entity read and paged full listing;
- conditional create (`If-None-Match: *`) and replace (`If-Match`);
- map native errors to `StoreError` without leaking response bodies/secrets;
- reject endpoints that ignore either precondition during setup probe;
- support path-style addressing as an explicit profile field;
- opt-in live contract tests under a random isolated prefix.

### Exit gate

Go and browser adapters pass the same provider contract, including accepted
write/response-loss, stale ETag, pagination, auth, quota, and cleanup limited to
the test prefix. Manual end-to-end sync works before scheduling is added.

## 11. Phase 7 — WebDAV provider

### Tasks

- HTTPS URL normalization with credentials removed from fingerprints/errors;
- `GET`/`PUT`, ETag capture, `If-None-Match`, and `If-Match`;
- `PROPFIND Depth: 1` complete listing; optional `sync-collection` only after the
  fallback is correct;
- setup probe proving the server does not ignore conditional headers;
- redirect policy that never forwards auth across origins;
- browser CORS errors mapped to actionable status;
- opt-in live contract tests against an isolated collection.

### Exit gate

Both adapters pass the common contract on a server without RFC 6578. Servers
with missing/weak/ignored CAS are rejected, never downgraded to LWW.

## 12. Phase 8 — Dropbox provider

### Tasks

- App Folder OAuth with PKCE and least privilege;
- read/write by fixed application-managed key, Dropbox revision CAS, and
  create-if-absent behavior;
- paginated `list_folder`, delta cursor, and cursor-reset-to-full-list path;
- refresh/reauthorization boundary with tokens in the appropriate secret store;
- browser flow that does not put refresh tokens in ordinary page-readable
  storage by default;
- opt-in live contract tests using an isolated application folder.

### Exit gate

Go/browser adapters pass the common contract, cursor reset, auth expiry,
rate-limit, stale revision, and response-loss cases. Provider-specific behavior
has not entered the core engine.

## 13. Phase 9 — Scheduling and release hardening

Only start this phase after at least one real provider passes manual sync.

### Tasks

- startup/open and manual triggers first;
- debounced local-change hint and online-recovery trigger;
- one simple periodic interval while the owner process/tab is active;
- in-memory exponential backoff honoring `Retry-After`; restart forgets it;
- server continues while clients disconnect; Wails/browser stop with owner;
- calm conflict/path/recovery UI and setup merge summary;
- documentation for provider privacy, browser CORS/OAuth, server state-root
  persistence, backups, and unsupported external double-sync layering.

Do not add filesystem watchers for correctness, durable scheduling state,
parallel entity workers, or automatic tombstone/recovery GC.

### Release gate

All acceptance tests in the lite spec pass on Windows, macOS, Linux, and the
supported browser matrix. Live tests pass for each advertised provider. The
experimental flag remains until recovery UI and manual destructive-edge cases
have been reviewed.

## 14. Standard verification commands

Run narrow tests during development, then all gates at every phase exit:

```sh
go test ./...
go test -race ./...
go vet ./...

cd frontend
npm test
npm run typecheck
npm run build
npm run build:local
```

Provider live tests remain opt-in, secret-redacted, and scoped to a random
prefix/folder. They must never clean an account, bucket, or collection root.

## 15. First assignment for a smaller agent

Give the agent **Phase 0 only**. The expected review is small: shared state hash,
deterministic conflict UUID/name, Sync-ID validation, and fixtures. It must not
touch persistence, delete WAL code, implement reconciliation, add UI, or call a
real provider.

After Phase 0 merges, give a fresh agent/context **Phase 1 only**. This ordering
prevents the new engine from inheriting the old WAL baseline shape and keeps
every review independently reversible.

Suggested first handoff prompt:

```text
Read CLAUDE.md, docs/sync-spec-lite.md, and Phase 0 of
docs/sync-lite-implementation-plan.md. Implement Phase 0 only.

Preserve the current entity contentHash wire algorithm. Add the complete-state
hash, deterministic UUID-v5 conflict identity/name, a Sync-ID validator that
accepts v4/v5 without weakening other UUID checks, and shared Go/TypeScript
fixtures/tests. Inspect existing code before editing and keep both languages
byte-identical.

Do not change persistence, syncscan, WAL/snapshot code, providers, API, UI, or
scheduling. Run the Phase 0 narrow tests, then the repository's standard Go and
frontend verification gates. Stop and report if an exit-gate requirement cannot
be met; do not continue into Phase 1.
```

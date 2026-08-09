# MemoDump Versioned-Note Sync — Implementation Plan

Status: proposed handoff plan
Date: 2026-08-08
Architecture contract: [`sync-spec-lite.md`](sync-spec-lite.md)

## 1. Reset point

The previous Phase 0–3 work is a useful prototype, not the architecture to keep
patching. It proved the local CAS, portable index, snapshot, recovery store,
remote conditional-write boundary, and several failure scenarios. It also
showed that folder entities plus a repository-wide planner are too large for
MemoDump V1.

Do not continue the old Phase 3 defect list one item at a time. Replace the
coordinator behind tests with the note-only path below, then delete code that no
longer has callers.

This plan uses `R0`, `R1`, and so on to avoid confusing the reset with the
already implemented phases. Give a small implementation agent exactly one task
inside a reset phase, not the whole document.

## 2. Keep, adapt, retire

Keep:

- `internal/vaultfs` safe paths, stable Markdown reads, local revisions, atomic
  note create/replace/delete, and front-matter preservation;
- `internal/syncstate` identity, registry, replica lock, atomic snapshot helper,
  and recovery store;
- `internal/cloudsync.RemoteStore`, normalized provider errors, conditional
  memory store, canonical JSON helpers, and reusable fault injection;
- `internal/syncscan` stable-read and unsafe/unknown classifications;
- UUID-v5 conflict derivation and state-hash fixtures where their inputs still
  match the new note record.

Adapt:

- `syncindex` becomes schema v2 and maps only Sync ID to Markdown path;
- `Snapshot` becomes schema v2, removes cursor, and stores note baselines only;
- the remote entity becomes a schema-v2 note record with a complete portable
  path and no `kind` or `parentId`;
- the pure decision code becomes one small Go per-note function;
- scanner output covers Markdown notes only.

Retire after replacement tests pass:

- folder remote entities, parent-graph validation, topological action ordering,
  folder recovery/deletion, and remote folder move logic;
- cursor persistence and delta-list decisions;
- the general `Action`/repository planner and simulator executor when the
  note-only coordinator no longer calls them;
- the TypeScript reconciliation/coordinator port. Keep generic wire/store code
  only if it remains used or cheap to maintain;
- any coordinator path that recursively deletes a folder.

Do not delete the historical commits or users' legacy state files. Schema-v1
index/snapshot data has not shipped with a production provider, so V1 may report
it as unsupported prototype state and require sync re-enable; it must never
reinterpret it as deletion evidence.

## 3. Target code shape

```text
internal/cloudsync/
  note.go                 schema-v2 note record + validation/canonical hash
  reconcile_note.go       pure decision for one Sync ID
  remote_store.go         conditional object-store boundary
  memory_store.go         test implementation and faults

internal/syncindex/       Sync ID -> Markdown path only
internal/syncscan/        stable note observations only
internal/syncstate/       identity/lock + snapshot v2 + recovery
internal/syncrun/         one serialized note coordinator
internal/syncprovider/s3/ first real provider

sync_service.go           lifecycle/configuration
api_sync.go               manual setup/run/status endpoints
```

The coordinator may use a small `switch` over a per-note decision. Do not add a
generic action DAG, durable action records, folder graph, cursor abstraction,
worker pool, or retry scheduler.

## 4. R0 — Freeze the note-only contract

Purpose: make later work compile against the product we actually want.

### R0.1 Remote note schema

Implement schema-v2 `NoteRecord` in Go with:

- `syncId`, complete slash-relative `.md` path, `markdown`, and `deleted`;
- UUID v4 for ordinary notes and the existing deterministic v5 allowance for
  conflict notes;
- canonical LF Markdown and deterministic hash/serialization;
- strict size, UTF-8, traversal, reserved-path, extension, and tombstone checks;
- no `kind`, `parentId`, folder record, or graph validation.

Add Go fixtures for live, nested-path, tombstone, conflict-ID, malformed, and
portable-collision records. Do not update TypeScript in this task.

Exit gate: new Go contract tests pass while the old entity contract may coexist
temporarily for compilation.

Estimated review size: 250–450 changed lines.

### R0.2 Index and snapshot v2

Change the index and snapshot models behind new constructors/loaders:

- index maps Sync ID to Markdown path only;
- snapshot uses `notes`, has no cursor, and validates identity/profile/version;
- schema-v1 prototype state is classified as unsupported, never loaded as a
  baseline;
- atomic replace, backup/recovery rules, lock, and recovery files remain.

Do not wire the coordinator yet. Add migration-classification and deterministic
serialization tests.

Exit gate: stores round-trip v2, reject v1 safely, and retain prior valid files
on injected replace failures.

Estimated review size: 250–450 changed lines.

## 5. R1 — One-note decisions

Purpose: replace the general planner with the smallest correctness core.

### R1.1 Pure decision table

Define one immutable input for a single Sync ID:

- local: present, absent, or unknown; live state and raw local revision;
- remote: live, tombstone, missing, or invalid; opaque remote version;
- optional last-known-equal baseline;
- precomputed path-conflict flag.

Return one of a small fixed set:

```text
noop
establish_baseline
push_live
pull_live
push_tombstone
apply_tombstone
preserve_local_then_pull
preserve_local_then_delete
preserve_remote_then_tombstone
block
retry
```

Implement the tables in spec Section 7. The function performs no I/O, contains
no folder branch, and emits no multi-entity action graph. Conflict preservation
is a named compound outcome whose execution order is fixed by its name.

Required tests cover onboarding, one-sided edit, concurrent identical/different
edit, local/remote delete, both edit/delete directions, unknown local state,
physical remote absence, invalid remote input, and path conflict.

Exit gate: every row is table-tested in Go and the package has no filesystem or
provider imports.

Estimated review size: 250–400 changed lines.

### R1.2 Deterministic preservation helpers

Adapt conflict ID/path derivation to schema-v2 note state. Test that retries and
swapped role inputs behave intentionally. Define exact handling when the desired
conflict path collides: block; never append a timestamp or numeric suffix.

Exit gate: repeating a compound conflict decision derives exactly one identity
and path.

Estimated review size: 100–250 changed lines.

## 6. R2 — Filesystem cycle with memory remote

Purpose: prove the actual product flow before adding service or network code.

### R2.1 Note-only observation assembly

Build the cycle's union of IDs from index, snapshot, and a complete remote
listing. Scan `.md` files only. Persist IDs for definite new notes before any
upload. Classify blocked, unstable, symlinked, and read-error notes as unknown.

Precompute path conflicts across live local and remote note records. Parent
directories are ordinary filesystem implementation details, not entities.

Exit gate: observation tests cover nested notes, unindexed notes, indexed
absence, external rename as old absence plus new identity, portable collisions,
and incomplete remote listing.

Estimated review size: 300–500 changed lines.

### R2.2 Non-destructive decisions

Wire `noop`, baseline establishment, conditional live upload, exact-path local
create, and local revision-CAS pull. Process Sync IDs in sorted order. Re-read a
remote key after precondition failure or uncertain response.

At cycle end, save consolidated index changes and replace the snapshot once.
An index save failure prevents snapshot commit. No cursor is read or written.

Exit gate: two temporary vaults converge for create, nested create, edit,
identical simultaneous edit, in-app path change, and restart after an accepted
write whose response or snapshot commit was lost.

Estimated review size: 300–500 changed lines.

### R2.3 Conflict preservation

Wire the three compound preservation outcomes. Required order:

1. derive conflict ID/path;
2. reserve and save it in the index;
3. create/verify the local conflict note;
4. create/verify the remote conflict record when required;
5. only then replace or delete the original;
6. record baselines only for final known-equal states.

Inject a stop before and after every boundary above. Restart must reuse the same
conflict note, not allocate another one.

Exit gate: concurrent edits and both edit/delete directions preserve all edited
Markdown through every injected restart.

Estimated review size: 300–500 changed lines.

### R2.4 Tombstones and recovery

Wire conditional tombstone upload and pulled tombstone application. Before a
local delete, atomically write the recovery copy. Revalidate the local revision
when deleting. A recovery failure or local CAS failure leaves the note intact
and its baseline unchanged.

Never call recursive folder deletion. Empty parent directories may remain.

Exit gate: deletion converges in both directions, recovery is idempotent, races
preserve newer local edits, and no test can delete an unrelated child file.

Estimated review size: 250–450 changed lines.

### R2.5 Remove the prototype executor

After all R2 tests pass, remove dead folder planner/executor/simulator code and
obsolete tests. Preserve reusable fault fixtures and historical docs. Run `rg`
for folder actions, fake cursor `c1`, `RemoveAll`, and uncalled old coordinator
entry points; inspect each remaining match rather than deleting mechanically.

Exit gate: the production coordinator has one per-note execution path and no
recursive directory deletion, action DAG, or cursor.

Estimated review size: mostly deletions; keep this separate from behavior work.

## 7. R3 — Manual product surface with memory remote

Purpose: validate lifecycle and UX before credentials and provider behavior.

### R3.1 Service and lock ownership

Add a service that owns provider selection, replica OS lock, serialized `Run`,
cancellation between note boundaries, and redacted status. The coordinator must
not be constructible for production without verified lock ownership.

Exit gate: concurrent manual runs do not overlap; a lock loser can still edit
notes; auth/permission/incomplete-list errors never report “synced”.

### R3.2 Minimal API and UI

Expose authenticated setup-test, enable, manual-run, status, disable, and
recovery-list/restore operations. Disable disconnects only; it never deletes
local or remote notes. Show the no-E2EE warning, last completed time, error,
conflict, and recovery state.

Keep the feature experimental. Do not add timers, watchers, online listeners,
or background retry state.

Exit gate: a user can exercise two local replicas against the memory remote
through the public service/API boundary.

## 8. R4 — One real provider

Implement S3-compatible storage first because the repository already has MinIO
and signing dependencies.

Required provider behavior:

- private bucket/prefix and secret-free profile fingerprint;
- read `repo.json`, full paginated `notes/` listing, and object read;
- create with `If-None-Match: *` and replace with `If-Match`;
- setup probe that rejects services which ignore either precondition;
- normalized auth, permission, rate-limit, quota, invalid-response, and
  retryable errors without leaking bodies or credentials;
- opt-in live tests limited to a random isolated prefix.

Exit gate: two filesystem replicas manually converge through S3 for every R2
scenario applicable to a live adapter. Do not start WebDAV, Dropbox, browser
sync, cursors, or scheduling in the same phase.

## 9. R5 — Scheduling and release hardening

Only after manual S3 synchronization is stable:

- run on explicit manual action and optionally application start;
- add one simple periodic interval while the process is alive;
- use in-memory backoff only; restart may forget it;
- document provider privacy, backups, state-root persistence, and the risks of
  layering another filesystem sync tool over the same vault;
- retain the experimental flag until recovery and conflict UX are reviewed on
  Windows, macOS, and Linux.

Filesystem watchers, delta cursors, parallel transfers, tombstone GC, and pure
frontend synchronization remain separate future proposals.

## 10. Verification gates

For each small assignment, run its narrow tests. At every reset-phase exit run:

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

The frontend gates ensure the existing application still works; they do not
authorize porting the new coordinator to TypeScript.

## 11. First handoff to a smaller agent

Start with R0.1 only:

```text
Read CLAUDE.md, docs/sync-spec-lite.md, and R0.1 of
docs/sync-lite-implementation-plan.md. Implement R0.1 only.

Add the Go schema-v2 versioned-note remote record and its fixtures/tests. The
record has syncId, complete portable .md path, full LF-normalized Markdown, and
deleted. It has no folder kind, parentId, cursor, or graph validation. Reuse the
existing strict canonical JSON, UUID, size, UTF-8, and path validation helpers
where appropriate. Keep the old entity type temporarily if current callers
need it to compile.

Do not change syncindex, snapshot, scanner, coordinator, TypeScript, providers,
API, UI, or scheduling. Run the narrow Go tests and then go test ./.... Stop at
the R0.1 exit gate and report any compatibility issue; do not continue to R0.2.
```

Expected review: one new contract type plus fixtures, with no runtime behavior
change.

# MemoDump Versioned-Note Sync — Implementation Plan

Status: R0–R5 implemented and reviewed
Date: 2026-08-09
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

Purpose: make the proven manual S3 cycle run predictably while MemoDump is open,
without turning synchronization into a second application framework.

R5 does not change reconciliation or the remote protocol. It schedules the same
full-scan cycle that R4 exposes manually. Because a cycle lists and reads the
remote note records, the interval is deliberately measured in minutes, not
seconds.

### R5 product contract

- The persistent `Connected` flag is the one opt-in. Do not add a second
  "automatic sync" setting: a connected replica synchronizes automatically;
  a disconnected replica does not.
- After backend initialization, a connected replica runs once after a
  **10-second startup delay**. A successful Enable requests one immediate
  asynchronous run instead of waiting for the first interval.
- After any completed attempt, the next ordinary run is scheduled **5 minutes
  after completion**. Use a resettable timer, not a wall-clock ticker, so slow
  cycles never overlap or cause catch-up bursts.
- `Run now` remains available, bypasses the timer/backoff delay, and uses the
  exact same run function and locks as startup, periodic, and retry attempts.
- Automatic sync exists in the Go server/Wails backend only. It continues when
  a browser tab is closed but the CLI server is still running; it stops when the
  Wails app or server process exits. The pure local/IndexedDB build has no cloud
  scheduler.
- This is eventual, not real-time, synchronization. A change on A is uploaded
  on A's next attempt and downloaded on B's next attempt. The worst normal
  latency is therefore about two intervals; users can choose `Run now` when
  they need immediate convergence.

Do not add a filesystem watcher, save hook, online listener, durable operation
queue, scheduler database, delta cursor, worker pool, or parallel transfer. The
periodic full scan remains the correctness mechanism.

### R5.1 One shared attempt boundary

Extract the state-mutating body of `handleSyncRun` into one backend function
used by every trigger (`manual`, `enable`, `startup`, `periodic`, `retry`). The
HTTP handler becomes a thin adapter; the scheduler must never call an HTTP
handler or make a loopback request.

The shared function must:

- take the existing process mutex and replica OS lock;
- read `connected.json`, validate provider profile and Repository ID, and run
  the cycle inside that same replica-lock critical section;
- preserve the existing manual HTTP response behavior;
- update one in-memory last-attempt record for both manual and automatic runs,
  including redacted result, completion time, and trigger;
- return internal retry metadata separately from the public/redacted result.
  Retry metadata must never be serialized, logged with provider bodies, or
  contain credentials;
- report cross-process lock contention as `locked`, not the generic `error`;
- let Disable and Reset use the same serialization boundary. They do not cancel
  a note mid-cycle: an in-process lifecycle request waits for the attempt to
  finish, then changes the connection state. Cross-process contention remains a
  visible refusal.

Keep `syncservice.Result` as the public status shape. A small private attempt
type may carry the raw typed error long enough to call
`cloudsync.ClassifyRetry`; do not expose the error afterward. A completed cycle
with `Retry > 0` is retryable even when no fatal error escaped the coordinator.
`Blocked > 0` is not a transport retry: it waits for the next ordinary interval
or a manual run.

Exit gate: manual and direct/background callers exercise one run function;
tests prove connection validation and the cycle share one lock, concurrent
triggers never overlap, and all public errors remain redacted.

Estimated review size: 150–300 changed lines.

### R5.2 Minimal in-memory scheduler

Add one scheduler owned by the backend process. It has one goroutine, one
resettable timer, a size-one/coalescing wake channel, and an injected clock/run
function for tests. It owns no filesystem state and writes no scheduler JSON.

Scheduling rules:

1. Wait 10 seconds after startup; run only if the connection record is valid
   and `Connected=true`.
2. Successful Enable coalesces an immediate wake. Multiple wakes while an
   attempt is running produce at most one later attempt, never parallel runs.
3. Success clears the failure count and schedules the next ordinary run in 5
   minutes.
4. A retryable provider failure or `Result.Retry > 0` uses delays
   `1m, 2m, 5m, 10m, 30m`, capped at 30 minutes. Honor a larger provider
   `Retry-After` instead of shortening it. Success resets the sequence.
5. `locked`, `cancelled`, and `Blocked > 0` do not increase the transport
   failure count; retry them only at the next ordinary interval.
6. Auth, permission, quota, invalid/incomplete response, unsupported capability,
   corrupt local state, and profile/Repository-ID mismatch pause automatic
   attempts for the rest of the process. `Run now` still works; a successful
   manual Run or successful Enable clears the pause. Restart may also forget the
   pause and all backoff state.
7. Disable or Reset clears pending wake/retry state and leaves the scheduler
   idle. A later successful Enable wakes it again.

Use context cancellation for shutdown and between-note cancellation already
provided by the service. Stopping the scheduler must stop its timer, cancel its
goroutine, and wait for it to exit. Do not use `time.Sleep` in implementation or
tests.

Exit gate: fake-clock tests cover startup, enable wake, five-minute periodic
execution, wake coalescing, no overlap, the exact backoff sequence,
`Retry-After`, permanent pause, manual recovery, Disable/Reset, and shutdown.
Restart tests prove there is no durable scheduler/backoff state.

Estimated review size: 250–400 changed lines.

### R5.3 Process lifecycle and status

Start the scheduler only after `dataDir`, repository, state root, sessions, and
provider configuration are ready:

- CLI: use a root cancellation context tied to process signals, stop the
  scheduler during HTTP-server shutdown, and do not start it in tests unless a
  test explicitly asks for it.
- Wails: start from `OnStartup` after repository initialization and stop/wait
  from `OnShutdown`.
- Local/IndexedDB build: retain the current disabled sync stubs and create no
  timer or polling loop.

Extend `/api/sync/status` with secret-free, in-memory scheduling fields:

```text
autoEnabled       connected and not permanently paused
autoIntervalSecs  300
syncRunning       one attempt currently owns the run boundary
lastTrigger       manual | enable | startup | periodic | retry
nextRun           RFC3339 UTC string or null
autoPaused        boolean
pauseReason       stable redacted label or empty
```

`lastRun` and `lastCompleted` must include automatic attempts. These fields are
process status, not durable history; they may be empty after restart until the
startup attempt completes. Never expose endpoint, bucket, prefix, credentials,
remote bodies, or raw local paths in scheduling status.

The settings panel must say that a connected replica synchronizes at startup
and every five minutes while MemoDump is running. Keep `Run now`, show running,
next-run/paused state, and do not show success toasts for routine background
runs.

Exit gate: CLI and Wails lifecycle tests leave no scheduler goroutine/timer
running; status tests cover idle, running, scheduled, retrying, paused,
disconnected, and post-restart states; manual and automatic attempts produce
the same redacted result shape.

Estimated review size: 200–350 changed lines.

### R5.4 Visible frontend updates

Backend sync can change local Markdown without a frontend request, so add one
lightweight **status poll**, not a sync engine, in server/Wails builds:

- poll `/api/sync/status` every 30 seconds only while the document is visible;
  stop on unmount/hidden and refresh once when visibility returns;
- use a dedicated lightweight status refresh. Do not call the existing full
  settings refresh on every poll, because it also reads recovery content;
  refresh recovery details only when the panel opens or `recoveryCount` changes;
- when `lastCompleted` advances after an automatic attempt, refresh the visible
  note/folder list through existing browser functions;
- re-read the open note without mutating the buffer. If it is clean and its
  revision changed, adopt the new revision/content; if it was deleted, close it
  with a notice;
- if the open note is dirty, saving, offline, or already in conflict, compare
  the fetched revision with its base revision but never replace/close its editor
  buffer. Only when the revision differs (or the file disappeared), show a
  non-blocking "synced version changed; save or reload to reconcile" notice and
  rely on the existing revision CAS to prevent overwrite;
- polling must never call `/api/sync/run` and must not exist in the local build.

Exit gate: frontend fake-timer tests cover visibility pause/resume, lightweight
polling without recovery downloads, list refresh, clean-note refresh/deletion,
and preservation of a dirty editor buffer. Local-build tests create no polling
timer and make no cloud API request.

Estimated review size: 200–350 changed lines.

### R5.5 Documentation and release review

Update English and Chinese documentation together. Cover:

- exact S3 configuration, private-bucket permissions, plaintext/no-E2EE
  warning, and where credentials live;
- startup/five-minute/manual behavior, expected two-device latency, in-memory
  backoff, permanent pause, and the fact that no sync runs while the process is
  closed;
- cloud sync is not a backup: deletes propagate, provider history/versioning is
  external, and recovery copies are local safety aids;
- the state root contains Device/Replica identity, connection pin, disposable
  snapshot, and recovery copies. It contains no WAL or durable scheduler queue,
  stays outside the vault, and must persist across container/app recreation;
- do not place the same vault under Dropbox/iCloud/OneDrive/git automation or
  another filesystem sync tool while MemoDump cloud sync is enabled;
- how to disable, reset/reconnect, inspect recovery copies, and run the opt-in
  random-prefix live S3 test without printing secrets.

Add an R5 section to the manual release checklist. On Windows, macOS, and Linux,
record application build, provider, date, and result for:

1. startup and periodic convergence between two replicas;
2. Run-now/automatic single-flight behavior and clean shutdown mid-cycle;
3. concurrent edit and both edit/delete conflict directions without duplicate
   conflict notes;
4. pulled deletion, durable recovery copy, and restore;
5. a remote update while an editor is clean and while it is dirty;
6. Unicode/case-portable paths and state-root persistence across restart;
7. auth failure, transient-network recovery, visible redacted status, Disable,
   and explicit Reset/reconnect.

Retain the experimental flag after R5. Removing it is a separate release
decision requiring all three platform checklists and one successful opt-in live
test against an actual supported S3-compatible service.

Exit gate: documentation matches the implementation and wire names; the normal
verification suite passes; the opt-in live test uses an isolated random prefix;
and the three platform review records contain no credentials or note content.

Estimated review size: mostly documentation/tests; keep it separate from the
scheduler implementation.

### R5 final verification gate

In addition to Section 10, run scheduler and frontend timing tests repeatedly
with fake clocks/timers and run the Go suite under `-race`. Inspect remaining
matches for `time.NewTicker`, `time.Sleep`, scheduler-state files, watchers,
cursors, worker pools, and loopback `/api/sync/run` calls; every match must be
unrelated or explicitly justified.

R5 is complete when two connected filesystem replicas, with no browser action,
converge through S3 after startup/periodic triggers; Run now remains immediate;
transient failures back off without surviving restart; permanent failures pause
without spinning; Disable stops future attempts; shutdown leaves no background
sync work; a dirty editor is never overwritten by a background pull; and all R4
data-loss protections remain unchanged.

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

## 11. Next handoff to a smaller agent

R0–R5 are complete and the experimental cloud-sync feature is fully implemented.
Any next small assignment should come from the R5.5 release-review checklist
(three-platform manual convergence, conflict/recovery UX, and one successful
opt-in live S3 test) or one of the explicitly deferred future proposals below.
The follow-up proposals remain separate: filesystem watchers, delta cursors,
parallel transfers, tombstone GC, pure frontend synchronization, and removing
the experimental flag.

```text
Read CLAUDE.md, docs/sync-spec-lite.md, and the R5.5 release checklist. Pick one
concrete deferred proposal, implement it against the existing R0–R5 stack, run
go test ./..., go test -race ./..., go vet ./..., and the frontend gates, and
stop at that proposal's own exit gate.
```

Expected review: one focused follow-up on top of the reviewed R0–R5 stack, with
no regression to the reviewed scheduler or lifecycle behavior.

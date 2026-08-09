# MemoDump Cloud Sync — Versioned Notes Specification

Status: proposed V1 implementation contract
Date: 2026-08-08

This document replaces the earlier “simplified” synchronization design. The
earlier design removed the WAL, but still modeled a general bidirectional file
system: folders were entities, paths formed a graph, moves retained identity,
and two runtimes implemented the same planner. That is not the V1 product.

MemoDump V1 synchronizes versioned notes through a cloud repository. It is not
a Dropbox replacement.

## 1. Product promise

When two MemoDump installations connect to the same cloud repository:

- a note uploaded by one installation becomes visible on the other;
- edits and deletions propagate in both directions;
- a stale device cannot silently overwrite a newer cloud edit;
- concurrent edits preserve both Markdown documents;
- interrupted synchronization can be run again safely.

Local editing remains available without the cloud. Sync is opt-in and manual
first. A vault that has never enabled it gains no sync metadata.

V1 deliberately does not promise:

- generic file or folder synchronization;
- empty-folder synchronization;
- identity-preserving external rename or move detection;
- Markdown merging, collaborative editing, CRDTs, or remote history;
- incremental cursors, parallel transfers, or work after a browser closes;
- simultaneous Go and pure-frontend implementations.

The Go filesystem implementation is completed first. A pure-frontend port is a
later product decision, after the Go behavior has proved useful.

## 2. The note model

Each synchronized Markdown note has a stable random UUID v4 `syncId`. The
portable index maps that ID to its current local path:

```json
{
  "schemaVersion": 2,
  "vaultId": "uuid-v4",
  "notes": {
    "sync-id": { "path": "Projects/idea.md" }
  }
}
```

The index lives at `<vault>/.memodump/sync-index.json` and is created only when
sync is enabled. It contains no note bodies, cloud credentials, remote versions,
or deletion state.

Folders have no identity. A note record carries its complete slash-relative
path. Downloading a note creates its parent directories as needed. MemoDump
does not upload empty folders and never deletes a directory merely because no
remote folder record exists.

An external rename that MemoDump did not record is interpreted conservatively
as deletion of the old note plus creation of a new note with a new `syncId`.
This may lose rename identity, but it does not lose Markdown. Automatic rename
inference is out of scope.

## 3. Remote repository

Every provider exposes this application-owned layout:

```text
repo.json
notes/<sync-id>.json
```

`repo.json` contains a schema version and random Repository ID. It is created
only-if-absent. A known repository becoming missing or changing ID stops sync;
it is never treated as a new empty repository.

A live note record has this logical shape:

```json
{
  "schemaVersion": 2,
  "syncId": "uuid-v4-or-derived-v5",
  "path": "Projects/idea.md",
  "markdown": "complete Markdown document\n",
  "deleted": false
}
```

A tombstone retains `schemaVersion`, `syncId`, `path`, and `deleted: true`, and
omits `markdown`. Tombstones are not garbage-collected in V1.

Records use deterministic canonical JSON and LF-normalized Markdown. Their
content hash covers `syncId`, portable path, Markdown, and `deleted`. Remote
timestamps and device labels may be diagnostic metadata, but they never choose
a winner.

Before materializing a record, validate its schema, UUID, canonical hash,
UTF-8, size, path traversal, reserved `.memodump` path, and current-platform
representability. A path collision blocks only the involved notes. There is no
parent graph, cycle validation, or folder ordering because folders are not
remote entities.

## 4. Why a remote version is required

Every successful cloud read returns an opaque provider version such as an ETag,
revision, or generation. The engine compares it only for equality.

Every cloud mutation is conditional:

- create only if the key is absent;
- replace only if its version still equals the version that was read.

If device A and device B both read version `5`, A may replace it and receive
version `6`. B's later replacement based on version `5` must fail instead of
overwriting A. B then re-reads and enters normal conflict handling.

A monotonically increasing integer is not required. “Remote version” means an
opaque compare-and-swap token. A provider that cannot enforce both conditional
create and conditional replace is unsupported. There is no unconditional-write
fallback.

## 5. Minimal device state

One disposable snapshot is stored outside the vault at:

```text
<app-data>/memodump/sync/<vault-id>/<replica-id>/state.json
```

It contains repository/profile identity and, for each note, only the last state
this replica knew was equal locally and remotely:

```json
{
  "schemaVersion": 2,
  "vaultId": "uuid-v4",
  "replicaId": "uuid-v4",
  "repositoryId": "uuid-v4",
  "providerProfile": "sha256-of-secret-free-location",
  "notes": {
    "sync-id": {
      "contentHash": "sha256",
      "deleted": false,
      "remoteVersion": "opaque-token"
    }
  }
}
```

This baseline is what distinguishes “I edited the note” from “the cloud edited
the note”, and “I deleted the note” from “I have never seen this note”. It is a
cache, not a journal:

- no WAL, operation queue, generation, cursor, or compaction;
- atomically replace it at most once at the end of a cycle;
- update a note baseline only when its final local and remote states are known
  equal;
- unresolved notes retain their previous baseline;
- a missing or corrupt snapshot triggers conservative onboarding, never inferred
  deletion;
- profile or Repository-ID mismatch stops before mutation;
- ordinary snapshot I/O errors stop the cycle.

The existing replica OS lock remains operational support. Exactly one sync cycle
may own a replica's index and snapshot at a time.

## 6. Full-list serialized cycle

V1 runs a single-threaded cycle:

1. Acquire the replica lock and load the provider profile, index, snapshot, and
   `repo.json`; stop on identity mismatch.
2. Stably scan Markdown notes. Assign IDs to definite unindexed notes and save
   the index before uploading them. Unsafe, unreadable, or unstable paths are
   unknown, never absent.
3. Fully list `notes/` on every cycle. Read and validate remote records whose
   versions are not already known, plus every remote-only record. Pagination is
   allowed; a delta cursor is not.
4. Detect portable and local-platform path collisions before any pull.
5. Reconcile each `syncId` independently in sorted order. There is no global
   action graph. Before replacing or deleting original local content, finish
   any required conflict preservation or recovery copy.
6. After a remote precondition failure or uncertain response, re-read that key.
   Never guess whether a write succeeded.
7. Save consolidated index changes. If that fails, do not commit the snapshot.
8. Atomically replace the snapshot once with baselines that are known equal,
   then release the lock.

Repeated work is acceptable. Correctness does not depend on remembering which
step ran before a crash.

## 7. Per-note reconciliation

Let `B` be the last known-equal baseline, `L` the current local observation,
and `R` the current remote record.

With a usable live baseline:

| Observation | Result |
|---|---|
| `L == R` | Refresh the baseline |
| only `L` differs from `B` | Conditional upload |
| only `R` differs from `B` | Local revision-CAS download |
| live `L` and live `R` both differ | Preserve local as a conflict note, then accept remote at the original ID |
| local absent, remote unchanged | Conditional remote tombstone |
| local unchanged, remote tombstone | Write recovery copy, then local revision-CAS delete |
| local edit versus remote tombstone | Preserve the edit as a conflict note, then accept deletion |
| local absent versus remote edit | Preserve the remote edit under a new derived note, then tombstone the original |

Without a usable baseline:

| Observation | Result |
|---|---|
| identical live states | Establish baseline |
| local-only note | Create remote only-if-absent |
| remote-only live note | Create local only-if-absent |
| divergent live states with the same ID | Keep both; remote remains the original |
| local live versus remote tombstone | Preserve local as a new conflict note; original remains deleted |
| local absent plus remote tombstone | Establish deleted baseline; delete nothing |

A physically missing remote object is not a tombstone. If a baseline expected
that object, report remote damage and leave local content untouched. Absence
alone never authorizes deletion.

Local writes use the existing filesystem revision CAS. If an editor changes a
note during a pull or delete, the local operation fails and the new edit
survives for the next cycle.

## 8. Conflict and deletion preservation

Conflict creation must be replay-safe without a journal. Derive the conflict
Sync ID as UUID v5 from the source ID and the ordered local/remote state hashes.
Derive a deterministic suffix from that ID:

```text
idea (conflict 12hexchars).md
```

Reserve and durably save the conflict ID/path in the index before changing the
original note. Create local and remote conflict notes only-if-absent. An existing
record is idempotent success only when its ID and canonical state match.

Before a remote tombstone deletes local Markdown, write the complete document
idempotently to:

```text
<replica-state>/recovery/<sync-id>/<state-hash>.md
```

Recovery failure prevents deletion. Recovery copies are not synchronization
state and do not influence decisions. Automatic cleanup is deferred.

## 9. Error policy and setup

Auth, permission, quota, unsupported capability, invalid remote data,
Repository-ID mismatch, and local state I/O errors stop the cycle. A retryable
transport error leaves the affected baseline unchanged. Unrelated valid notes
may synchronize only when the failure does not make the remote listing
potentially incomplete.

Initial setup distinguishes:

1. no remote repository: confirm, create `repo.json` only-if-absent, upload;
2. existing repository and empty local vault: join and download;
3. both non-empty: show a summary and apply the no-baseline rules;
4. matching snapshot and repository: normal sync;
5. profile or Repository-ID mismatch: stop and require explicit reconnect.

Credentials never enter the vault, remote records, snapshot, fixtures, or logs.
Provider URLs require HTTPS except explicit localhost development.

## 10. V1 acceptance tests

1. Two filesystem replicas converge for create, edit, delete, nested note path,
   and in-app path change.
2. An external rename converges as old-note deletion plus new-note creation; the
   Markdown survives.
3. Concurrent edits and both edit/delete directions preserve all edited content
   and create no duplicate conflict notes after retries.
4. A stale remote version never causes an unconditional overwrite.
5. An external edit racing pull/delete survives local revision-CAS failure.
6. Remote tombstone deletion cannot occur before a durable recovery copy.
7. Crash/restart around conflict reservation, local write/delete, remote write,
   index save, and snapshot replace converges without silent loss.
8. Missing/corrupt snapshots, physical remote deletion, incomplete listing,
   invalid records, repository mismatch, and path collision never delete local
   Markdown.
9. Empty folders are ignored, and synchronization never recursively deletes a
   directory.
10. A full cycle needs no cursor, folder graph, durable operation queue, or
    TypeScript coordinator.

## 11. Deferred until V1 proves useful

- pure-frontend synchronization;
- WebDAV and Dropbox after one provider works end-to-end;
- provider delta cursors and bandwidth optimization;
- empty-folder identity, identity-preserving external rename, and folder moves;
- managed private media, E2EE, automatic merging, remote history, and GC;
- watchers for correctness, parallel actions, background durable retry state.

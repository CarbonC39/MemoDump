# MemoDump Cloud Sync — Simplified Specification

Status: proposed replacement architecture
Date: 2026-08-05

This document replaces the architecture direction in `sync-spec.md` and
`sync-implementation-plan.md`. Those documents remain historical references;
new implementation work follows this specification. Existing WAL/compaction
code is transitional and must be removed or bypassed before provider work;
Phase 5 must not be built on top of it.

## 1. Product boundary

MemoDump sync is a small, local-first, two-way synchronization feature for
people who do not want to deploy a MemoDump server.

- Filesystem builds keep ordinary Markdown files and folders as the source of
  truth. Other editors may continue to modify them.
- The pure frontend keeps notes in IndexedDB and runs only while a page/PWA is
  active.
- Sync is opt-in. A vault that has never enabled sync gains no sync metadata.
- Initial providers are WebDAV, Dropbox, and S3-compatible object storage.
- E2EE, collaborative editing, CRDTs, and reliable closed-app background sync
  are out of scope.

Correctness means: never silently overwrite divergent user content. It does
not mean resuming every operation at the exact instruction after a crash.

## 2. The only two durable metadata records

### 2.1 Portable identity index

When sync is enabled, filesystem vaults contain:

```text
<vault>/.memodump/sync-index.json
```

It maps stable UUIDs to ordinary paths and kinds:

```json
{
  "schemaVersion": 1,
  "vaultId": "uuid",
  "entities": {
    "sync-id": { "kind": "note", "path": "Projects/idea.md" }
  }
}
```

The index changes only when identity or structure changes. Content edits do
not rewrite it. It is written with temp-file + fsync + atomic rename. Temporary
files or one recovery backup are implementation details, not additional state
models. Unknown or invalid identity never causes a Markdown file to be deleted.

The pure frontend stores the equivalent index in IndexedDB.

### 2.2 Disposable device snapshot

Each replica has one device-local snapshot outside the filesystem vault:

```text
<app-data>/sync/<replica-id>/state.json
```

It contains only information needed for three-way comparison:

```json
{
  "schemaVersion": 1,
  "repositoryId": "uuid",
  "providerProfile": "non-secret-fingerprint",
  "entities": {
    "sync-id": {
      "contentHash": "sha256",
      "remoteVersion": "opaque-provider-token",
      "deleted": false
    }
  },
  "cursor": "optional-opaque-token"
}
```

The snapshot contains no Markdown bodies, credentials, retry queue, operation
journal, or provider URL containing secrets.

`contentHash` covers the complete canonical entity state: kind, logical path,
Markdown content, and deletion state. It is not merely a hash of the note body.

The snapshot is a cache, not a log:

- There is no WAL, rotation, frozen generation, compaction, sequence number,
  watermark, or writer actor.
- One sync coordinator owns it. Sync cycles are serialized.
- It is atomically replaced at most once at the end of a sync cycle.
- A failed snapshot write makes the cycle incomplete and visible as an error,
  but never rolls back an already durable local or remote write.
- On the next run, local files plus a remote Probe reconstruct the truth.
- A missing, corrupt, copied, or mismatched snapshot is discarded. It never
  invalidates the portable identity index.

The pure frontend stores the same logical snapshot in one IndexedDB record and
replaces it in one transaction.

## 3. Remote repository

Remote entities are keyed by Sync ID, not by user-facing path. Each record
contains its schema version, repository ID, kind, logical path, content or
tombstone, and a canonical content hash. Provider revisions/ETags are opaque
CAS tokens and are not ordered or parsed by MemoDump.

Provider adapters expose only a small common interface:

- inspect repository identity;
- list/probe entity metadata;
- read one entity;
- create an entity only if absent;
- replace or tombstone an entity only if its expected remote version matches.

WebDAV may emulate these operations with conditional requests and a managed
manifest. Dropbox uses provider revisions. S3 uses conditional object writes.
If a provider cannot offer a safe precondition for an operation, MemoDump must
conflict or stop; it must not silently fall back to last-write-wins.

## 4. One serialized sync cycle

There is one sync cycle per vault/replica. No scan, cloud apply, or second sync
cycle mutates sync state concurrently.

Editing remains available during synchronization. A cloud pull applies through
the existing local revision CAS; if the user edited meanwhile, the apply is
deferred or becomes a conflict instead of overwriting the new local revision.

1. Load the portable identity index and the optional device snapshot.
2. Scan/read the current local vault.
3. Probe remote metadata. A cursor may reduce listing work but is never needed
   for correctness; cursor failure falls back to a full listing.
4. Decide actions using local state, remote state, and the known baseline.
5. Pull first. Materialize local files with temp-file + atomic rename or one
   IndexedDB transaction.
6. Push with provider CAS preconditions.
7. Re-probe objects whose result was uncertain.
8. Atomically replace the device snapshot once with the final observed state.

Network operations may be repeated after a crash. Creates and conflict copies
must therefore have deterministic identities derived from their source Sync
ID and relevant content/version hashes. Repetition may waste bandwidth but
must converge without duplicating or overwriting notes.

## 5. Reconciliation rules

Before consulting a baseline, identical local and remote canonical state always
establishes the new baseline. This rule makes a cycle idempotent after a crash
or failed final snapshot write.

For an entity with a known baseline whose local and remote states differ:

- Neither side changed: no-op.
- Only local changed: upload with the baseline remote version as CAS.
- Only remote changed: pull and atomically replace the local entity.
- Both changed: keep the accepted remote entity and create the local content
  as a normal synchronized conflict copy.
- Local deletion only: write a remote tombstone with CAS.
- Remote deletion only: delete locally through the trash/recoverable path.
- Edit versus delete: preserve the edited content as a conflict copy.

For an entity with no usable baseline:

- Identical local and remote content establishes a new baseline.
- Local-only content is created remotely only-if-absent.
- Remote-only content is downloaded locally without deleting anything.
- Divergent local and remote content creates a conflict; neither side is
  silently chosen.
- Absence is never interpreted as a deletion until a baseline is known.

Path collisions, invalid remote records, repository-ID mismatches, and folder
structure conflicts stop the affected entity/subtree and require a visible
user decision.

## 6. Filesystem policy

- Periodic full scans are the correctness mechanism. Watchers are optional
  latency hints.
- Symlinks are not synchronized. A scan that cannot safely classify a path
  defers it rather than reporting a deletion.
- UUIDs remain out of Markdown front matter.
- `.memodump` is created only after sync is enabled and is ignored by normal
  note listing/search.
- Ordinary content changes do not write the identity index or snapshot until
  a sync cycle actually runs.

The initial implementation favors understandable checks and recoverable
behavior over adversarial filesystem hardening. Descriptor-relative traversal
may be added later if the supported threat model requires it.

## 7. Images are a separate feature

External image hosting is not part of the note synchronization state machine.
Typora-style S3 upload stages a local blob, uploads and verifies it, inserts the
final HTTPS URL into Markdown, then removes the staging blob unless the user
keeps a cache.

Managed private media (`memodump-media:` references, provider-backed lazy
download, pinning, and cache eviction) is deferred until note sync is stable.
It must not add WAL or per-upload state to the note-sync snapshot.

## 8. Deliberately deferred

- WAL, journal replay, compaction, and exact mid-cycle resume;
- parallel entity mutation and multi-worker scheduling;
- automatic retry queues persisted across restarts;
- managed private image synchronization;
- automatic tombstone/history garbage collection;
- live multi-user collaboration and E2EE;
- provider-specific performance optimizations before the common behavior works.

## 9. Minimum acceptance tests

1. Sync-disabled vaults produce no sync metadata or background writes.
2. One-sided edits and known deletions converge across two replicas.
3. Concurrent edits and edit/delete races preserve both user versions.
4. A crash before or after every local write, remote write, and snapshot replace
   converges after restart without silent overwrite.
5. A missing/corrupt snapshot triggers full Probe and conservative onboarding.
6. Provider CAS failure becomes a conflict or retryable error, never LWW.
7. The same behavioral scenarios pass for Go filesystem and browser IndexedDB
   implementations.

Only after these tests pass against an in-memory remote should real provider
adapters or background scheduling be added.

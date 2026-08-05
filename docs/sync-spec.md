# MemoDump Cloud Sync Specification (Historical Proposal)

Status: superseded by [`sync-spec-lite.md`](sync-spec-lite.md)
Feature: V2 Feature 2
Date: 2026-08-04

This document is retained as design history. New implementation work must not
use its WAL, compaction, or scheduling architecture unless a later accepted
specification explicitly restores those features.

Repository-specific sequencing and file ownership are defined in
[`sync-implementation-plan.md`](sync-implementation-plan.md).

This document defines cloud synchronization for MemoDump. It is intentionally
more detailed than a product plan: persistence boundaries, wire formats,
conflict rules, browser limitations, recovery behavior, and acceptance tests
are normative unless a section is explicitly marked as future work.

Where this document and the Feature 1 image plan differ, this document takes
precedence only after sync is enabled or for the new image-destination modes.
Existing non-sync image behavior remains compatible until its explicit
migration runs.

## 1. Decision summary

- Notes remain **local-first**. A save completes when the local Markdown file
  or IndexedDB transaction is durable; cloud availability never gates editing.
- Sync is opt-in. A filesystem vault that has never enabled sync contains no
  `.memodump/` directory and no sync UUIDs.
- Enabling sync creates a small, versioned `.memodump/sync-index.json`. UUIDs
  exist only in the sync domain and are not written into Markdown front matter.
- SQLite is not used. Low-churn portable identity uses atomically replaced JSON;
  device state uses a snapshot plus a rotating, fsynced NDJSON WAL. Compaction
  never rewrites the active WAL in place.
- The first sync targets are Dropbox, WebDAV, and S3-compatible object storage.
  OneDrive and Google Drive are out of scope.
- The remote repository is application-managed. Other editors operate on the
  local Markdown vault, not on the provider's internal repository objects.
- Every synced note and folder has a stable UUID. Remote records are keyed by
  UUID, while local paths remain ordinary user-facing paths.
- Remote writes use provider revisions or ETags as compare-and-swap (CAS)
  preconditions. Wall-clock timestamps are never used to decide a winner.
- Concurrent note edits never use silent last-write-wins. V1 creates a normal,
  synchronized conflict copy. Structural folder conflicts pause the affected
  subtree for an explicit user decision.
- Images have two distinct remote-first modes:
  - **External URL hosting** uploads to a public S3-compatible image host, puts
    the final HTTPS URL in Markdown, and removes the temporary local blob after
    upload and read verification (Typora-style).
  - **Managed sync media** stores a private content-addressed blob in the active
    sync repository, puts `memodump-media:<key>` in Markdown, and keeps only an
    evictable or explicitly pinned local cache.
- "S3 image host" and "S3 sync target" are separate configurations and must
  use separate prefixes. The former is public and URL-oriented; the latter is
  a private repository for notes, metadata, and optionally media.
- The Wails and self-hosted server builds run the sync engine in Go. The pure
  frontend/PWA build runs a TypeScript implementation. Both consume the same
  wire-format fixtures and state-machine conformance tests.

## 2. Goals and non-goals

### Goals

1. Let Wails and pure-frontend users synchronize without deploying MemoDump's
   server.
2. Preserve ordinary Markdown files and folders as the Wails/server source of
   truth so external editors and file managers remain usable.
3. Include folders, empty folders, tags, deletions, moves, and images.
4. Continue working offline and survive crashes at every network boundary.
5. Prefer visible, recoverable duplication over invisible data loss.
6. Keep provider code behind a small capability-based interface.
7. Give every status and error a calm, actionable UI representation.

### Non-goals for V1

- End-to-end encryption.
- Collaborative real-time editing or CRDTs.
- Editing the provider's internal repository files by hand.
- Synchronizing application appearance, window layout, auth sessions, or
  provider credentials.
- OneDrive, Google Drive, Git, iCloud, or peer-to-peer synchronization.
- Reliable background synchronization after a pure browser/PWA page is closed.
- File System Access API integration for the pure frontend build.
- Automatic remote history or tombstone garbage collection.
- Running two different sync systems against the same vault at the same time.

## 3. Terms and identities

- **Vault**: the local collection of notes and folders.
- **Local ID**: the existing API's opaque ID. Its current filesystem/local-API
  representation may remain a relative path.
- **Sync ID**: a UUID v4 assigned to a note or folder when sync is enabled.
  Sync IDs never change after a rename or move and are internal to sync.
- **Device ID**: a UUID v4 for one MemoDump installation/profile. It is stored
  outside the vault and is used only for attribution and conflict names.
- **Replica ID**: a UUID v4 identifying one local checkout/copy of a vault on a
  device. Two paths containing copies of the same Vault ID must not share device
  state or WAL files.
- **Repository ID**: a UUID v4 identifying one remote sync repository.
- **Entity**: a note or folder identified by a Sync ID.
- **Media key**: `sha256(bytes) + canonicalExtension` using the image format
  detected from magic bytes.
- **Remote version**: an opaque provider revision, ETag, or equivalent CAS
  token. It must never be parsed or ordered by MemoDump.
- **Tombstone**: a valid entity record whose `deleted` flag propagates a
  deletion without physically removing the remote record.
- **Baseline**: the entity state and remote version last known to be shared by
  the local replica and remote repository.

Local IDs and Sync IDs deliberately remain separate. This prevents optional
sync from changing every existing API and filesystem behavior. Code must use
the explicit names `localId`/`path` and `syncId`; a generic unqualified `id`
must not cross the sync-core boundary.

## 4. Build matrix and ownership

MemoDump has three relevant runtime shapes. "Web" is not a single mode.

| Build | Local source of truth | Sync owner | Network path | Runs when UI is closed? |
|---|---|---|---|---|
| Wails desktop | Markdown filesystem vault | Go process inside Wails | Go HTTP client | No; app must be running |
| Self-hosted web server / Docker | Markdown filesystem vault | Go server process | Go HTTP client | Yes; server process owns the worker |
| Pure frontend / PWA (`VITE_LOCAL=1`) | IndexedDB | TypeScript in the active page/PWA | Browser `fetch` / provider SDK | No reliable V1 background sync |

Consequences:

- Wails and server builds are not subject to browser CORS when contacting a
  provider. Their frontend only calls same-origin MemoDump APIs/bindings.
- The self-hosted server has exactly one sync worker per vault. Browser tabs do
  not independently contact the provider.
- Pure frontend tabs coordinate so only one tab performs sync at a time.
- Provider behavior must be tested against both the Go and TypeScript engines.
- The common frontend presents the same status model, but the execution and
  credential-storage boundaries differ by build.

## 5. Local metadata and lifecycle

### 5.1 Sync disabled

For a filesystem vault that has never enabled sync:

- `.memodump/` does not exist.
- No Sync IDs, Device IDs, cursors, or journals are created in the vault.
- Filesystem watching performs only the work already required by the app.
- S3 external image hosting may be enabled independently and must not create
  sync metadata.

The pure frontend similarly creates no sync object stores until sync is first
enabled. Its existing note and media stores remain unaffected.

### 5.2 First enable

After the user confirms provider setup, the filesystem build creates:

```text
<vault>/.memodump/
  sync-index.json
  sync-index.json.bak
```

The note/folder listing and search APIs must ignore `.memodump` completely.
Symlinks within `.memodump` are not followed.

Initial `sync-index.json` shape:

```json
{
  "schemaVersion": 1,
  "vaultId": "dc56ad15-62c6-4fa7-bf7a-5c6337d574be",
  "entities": {
    "5d5d8b2c-94f7-4a38-8318-8cd4cb53dfa8": {
      "kind": "note",
      "path": "Projects/idea.md"
    }
  }
}
```

The portable index contains no content hashes, credentials, provider URLs,
cursors, remote versions, retry counters, Device IDs, or Replica IDs. It changes
only for entity creation, deletion, copy, rename, or move; ordinary note-content
saves do not rewrite it. Batch imports and scans commit one consolidated index
update instead of one rewrite per discovered entity.

The index preserves identity when a complete vault is copied. It is not uploaded
as a remote repository object; remote entity records are canonical for
synchronization.

Device-specific state is stored outside the vault:

```text
<OS application data>/memodump/sync/<vaultId>/<replicaId>/
  state.snapshot.json
  state.wal.ndjson
```

The pure frontend uses versioned IndexedDB stores for both the index and device
state. Device state includes:

- Device ID, Replica ID, and display name;
- selected provider profile/repository ID;
- provider cursor or sync token;
- per-entity baseline hash and remote version;
- durable coalesced sync journal;
- conflicts, retry state, and the last successful sync time.

Secrets are stored separately as described in Section 15.

### 5.3 Portable-index durability

Portable `sync-index.json` writes follow this sequence:

1. Serialize a complete new document with a schema version.
2. Write a uniquely named temporary file in the destination directory.
3. Flush the file to stable storage where the platform supports it.
4. Preserve the last known-good file as `.bak`.
5. Atomically rename the temporary file over the destination.
6. Flush the containing directory where the platform supports it.

On startup, invalid primary JSON falls back to `.bak`. If both fail, sync stops
without changing local notes and offers a rebuild. Rebuild scans local files,
then reconciles them with remote entities by any surviving UUID information,
exact path, and freshly computed content hash. Ambiguous matches become
conflicts; they are not guessed.

### 5.4 Replica identity and missing AppData

AppData is a cache of one replica's synchronization knowledge; it is never the
authority for whether a local entity exists. `sync-index.json` is authoritative
for the local Sync ID/path association, subject to validation against the actual
filesystem.

The installation keeps a small registry mapping canonical vault paths to Replica
IDs. Opening a copied vault at a second path creates a new Replica ID even when
the copy contains the same Vault ID. This prevents two local copies from sharing
baselines, cursors, or a WAL. If a vault was moved rather than copied, MemoDump
may offer to re-associate the one missing old path; ambiguous cases create a new
replica instead of guessing.

When a vault contains `sync-index.json` but its replica AppData is absent:

1. Keep every valid local entity and Sync ID from the index.
2. Mark the replica `baseline-unknown`; do not upload, delete, or tombstone yet.
3. Require/recover provider configuration, then probe
   `entities/<syncId>.json` individually or through a full listing.
4. Remote absent plus local present becomes a conditional create.
5. Remote present with identical canonical content establishes a baseline.
6. Remote present with different content is an onboarding conflict because no
   common baseline exists; preserve both.
7. Remote tombstone plus local present is also an onboarding conflict; never
   resurrect or delete automatically.
8. Index entry present but local path absent and no baseline is ambiguous. Probe
   remote and require an explicit deletion/recovery decision.

Missing AppData must never make a local note invalid. Conversely, a stale or
malicious index cannot override the filesystem: UUID/path uniqueness, path
containment, entity kind, and actual file presence are validated before use.

### 5.5 Device-state WAL and compaction

Device state uses two formats:

- `state.snapshot.json`: a compacted state with `lastAppliedSeq` and a canonical
  checksum over the compacted body (the data map plus `lastAppliedSeq` and
  `schemaVersion`). The checksum field is written last (it is unknown until the
  body is streamed), so the checksum covers a complete, self-contained body
  object and corruption that still parses as JSON (for example a cursor value
  changing) is detected. The implementation must verify it by re-encoding the
  decoded body into a hash — never by reading the whole document into memory —
  and a snapshot missing any of `checksum`, `data`, `lastAppliedSeq`, or
  `schemaVersion` is corruption.
- `state.wal.ndjson`: the only active append target.

Each WAL line is a complete JSON record with a monotonically increasing `seq`,
operation kind, payload, and checksum over the canonical schema version, seq,
operation, and payload (everything except the checksum itself). Writers open the
active WAL in append mode and serialize through one process-local writer. The
writer constructs one complete newline-terminated buffer, writes all bytes (a
short write is an error), and calls `file.Sync()` before reporting that state as
durable. The implementation must not promise a fixed fsync latency: local disks,
antivirus, network filesystems, and operating systems vary. WAL durability runs
off the user-visible note-save path.

Every filesystem build must also hold an OS-level exclusive lock for the
Replica ID before opening its device state. A second Wails/server process may
continue serving and editing Markdown but must disable sync and must not open or
repair that replica's WAL. The in-process writer actor is sufficient only after
this cross-process lock has been acquired.

Before accepting writers at startup, recovery derives the next sequence number
from the maximum valid sequence in the snapshot, every frozen generation, and
the active WAL. Sequence allocation and append happen inside the same writer
actor; a process restart must never reuse a durable sequence number.

An unterminated or syntactically partial tail after the last valid newline may be
truncated during recovery because it was never durably acknowledged. A complete
newline-terminated record with a bad checksum, or corruption anywhere earlier,
stops automatic recovery and surfaces a repair action; it is not silently
discarded as a presumed torn write.

Compaction uses log rotation, never in-place truncation:

1. Acquire the same writer mutex/actor used by append operations.
2. Sync and close the active WAL file descriptor.
3. Rename it to `state.wal.<generation>.frozen.ndjson` and sync the containing
   directory where supported.
4. Create and open a new `state.wal.ndjson` in append mode, then sync the
   directory where supported.
5. Release the writer lock. Subsequent records now go only to the new file.
6. In a background worker, read the old snapshot plus frozen WAL generations,
   applying only records with `seq > lastAppliedSeq`.
7. Stream a uniquely named temporary snapshot, including the greatest applied
   seq; sync it and atomically replace `state.snapshot.json` with the
   platform-specific durable-replace helper.
8. Sync the containing directory, then delete only frozen WAL generations whose
   maximum seq is covered by the durable snapshot, and sync the directory again
   where supported.

Closing and reopening the descriptor while holding the writer lock is mandatory:
on POSIX, a writer retaining the old descriptor after rename would otherwise
continue appending to the frozen inode.

Recovery replays the durable snapshot, every frozen WAL generation in sequence,
then the active WAL. The snapshot watermark makes replay idempotent if the app
crashed after snapshot replacement but before frozen-log deletion. A new
compaction must never overwrite an existing frozen generation; generation names
are unique and all surviving generations are recoverable.

Only one compactor may run for a Replica ID. If another threshold is reached
while compaction is active, the request is coalesced and evaluated again after
the current snapshot is durable. This prevents two compactors from publishing
snapshots from different watermarks or deleting one another's frozen inputs.

The compactor reads only snapshot/frozen files, not a concurrently mutated live
map. Active writers are blocked only for the short rotate/descriptor-reopen
critical section, not for snapshot construction.

Compaction is an allocation-sensitive background task, not an application-idle
promise. The Go implementation must decode and encode incrementally and must not
build the serialized snapshot with `os.ReadFile` plus `json.Unmarshal` plus one
whole-document `json.Marshal`. It may maintain the compact state map required
for key replacement, but streams the output through a buffered encoder, releases
temporary buffers between generations, and rate-limits compaction when the app
is under foreground load. The initial defaults trigger only when the WAL is at
least 1 MiB and either at least 25% of snapshot size or contains 10,000 records;
an operator/manual compaction may override the threshold. These are scheduling
defaults, not wire-format rules.

Filesystem note saves do not append a dirty record merely to announce that a
file changed: the Markdown file is already durable, and a restart scan can
compare it with the last baseline. WAL commits are required when remote/baseline
state, cursor state, conflict state, or other non-reconstructable device state
changes. Pure frontend uses IndexedDB transactions instead of this filesystem
WAL.

### 5.6 Disable, disconnect, and remove metadata

- **Pause** stops network work but retains the profile and journal.
- **Disconnect** removes provider credentials and stops sync, but retains the
  index and unresolved journal so reconnecting cannot silently change identity.
- **Remove sync metadata** is a separate destructive action with confirmation.
  It removes local sync JSON/state only; it never deletes the remote repository
  or user Markdown.
- A provider switch creates a new sync profile. Pending operations are scoped to
  their original profile and are never replayed into a different provider.

## 6. Remote repository format

All providers expose the same logical repository:

```text
repo.json
entities/<syncId>.json
media/<mediaKey>
```

`repo.json` is small and changes only during explicit format upgrades:

```json
{
  "formatVersion": 1,
  "repositoryId": "uuid-v4",
  "createdAt": 1785800000000,
  "minimumClientVersion": "2.0.0"
}
```

An entity record is one atomic provider object:

```json
{
  "schemaVersion": 1,
  "syncId": "uuid-v4",
  "kind": "note",
  "parentId": "folder-uuid-or-empty-root",
  "name": "idea",
  "markdown": "---\ntags: [\"project\"]\n---\n# Idea\n",
  "contentHash": "sha256-hex",
  "deleted": false,
  "updatedBy": "device-uuid",
  "updatedAt": 1785800000000
}
```

Folder records omit `markdown`. The root folder is implicit and uses an empty
`parentId`; it has no entity record.

Rules:

- The serialized JSON uses UTF-8, deterministic key order, and a trailing LF.
- `updatedAt` is informational only. Clock skew cannot change conflict results.
- `markdown` is the canonical UTF-8 Markdown document, including front matter.
  Line endings are normalized to LF at the local repository boundary. Unknown
  front-matter keys must be preserved when MemoDump edits tags; the sync layer
  must not reduce front matter to only fields understood by MemoDump.
- `contentHash` covers `kind`, `parentId`, `name`, and canonical `markdown` as
  defined by shared golden fixtures; it is not the provider ETag.
- A delete updates the existing record with `deleted: true`. Remote entity files
  are not physically removed in V1.
- Media objects are immutable. Re-uploading identical bytes is idempotent.
- An invalid or unknown-newer remote record is quarantined and reported. It is
  never interpreted as a deletion.
- Physical removal of an entity object outside MemoDump is repository damage,
  not a valid note deletion. Local data is retained and sync stops for that
  entity until the user repairs or explicitly accepts the loss.

This application-managed layout is not a cloud-editing interface. Users edit
the ordinary local vault. Direct manual edits under `entities/` or `media/` are
unsupported and must never be allowed to cause silent local deletion.

## 7. Provider interface

Both implementations expose the conceptual interface below:

```ts
interface RemoteStore {
  test(): Promise<Capabilities>
  read(key: string): Promise<{ bytes: Uint8Array, version: string }>
  list(prefix: string, cursor?: string): Promise<ChangePage>
  create(key: string, bytes: Uint8Array): Promise<{ version: string }>
  replace(
    key: string,
    bytes: Uint8Array,
    expectedVersion: string,
  ): Promise<{ version: string }>
}
```

Required semantics:

- `create` fails if the key already exists.
- `replace` fails if `expectedVersion` no longer matches.
- versions are opaque strings.
- retries are safe and idempotent.
- listing returns enough information to detect changed and missing keys.

Optional capabilities include delta cursors, server-side checksums, batching,
multipart uploads, and long polling. The sync algorithm must remain correct
without them.

Provider mapping:

- **Dropbox**: App Folder access; `list_folder` cursor for deltas; file revision
  for conditional replacement.
- **WebDAV**: ETag/`If-Match`; RFC 6578 `sync-collection` when available;
  `PROPFIND Depth: 1` fallback because repository collections are flat.
- **S3-compatible**: `If-None-Match: *` and `If-Match`; `ListObjectsV2` full
  prefix listing because no portable delta feed is assumed.

## 8. Sync algorithm

Only one sync cycle may run for a vault/device at a time. A cycle is pull-first:

1. Acquire the local single-flight/leader lock.
2. Load and validate local index, device state, and repository descriptor.
3. Recover any interrupted local or remote operation.
4. Obtain remote changes; if a cursor is rejected, perform a full listing.
5. Fetch changed entity records and validate all fields and hashes.
6. Compare every entity with its durable baseline:
   - neither changed: no-op;
   - remote only: apply remote locally;
   - local only: conditionally push local;
   - both changed to identical canonical content: converge without conflict;
   - both changed differently: follow Section 10.
7. Upload referenced managed media before publishing a note record that first
   introduces those media keys.
8. Apply folders before children and tombstones after conflict analysis.
9. After each applied remote/local result is durable, append and fsync its new
   baseline state. Advance and fsync the provider cursor only after every entity
   covered by that cursor is durable. A crash before cursor advancement safely
   replays already-idempotent changes.
10. Release the lock and publish one status/event update.

Local save and sync remain separate transactions. A UI may say
"Saved locally · 3 changes waiting to sync"; it must not report "Synced" merely
because a note save succeeded.

### Scheduling

- Immediately after a durable local change, debounced by 2 seconds.
- On startup/open.
- On a manual Sync action.
- On network recovery.
- Periodically while active: every 60 seconds initially, with provider backoff.
- The server worker continues without connected browser clients.
- Wails stops when the desktop process exits.
- Pure frontend stops when no active tab/PWA can own the leader lease.

Retries use jittered exponential backoff and honor `Retry-After`. Authentication,
permission, schema, quota, and CORS failures require user action and do not spin.

### Durable desired state and coalescing

Pending work represents desired entity state, not a replayable sequence of HTTP
requests. It is keyed by sync profile and Sync ID. Pure frontend persists this
state transactionally in IndexedDB. Filesystem builds derive reconstructable
local dirtiness from the working tree and persist only non-reconstructable
transition/baseline state in the WAL:

- repeated edits collapse to the newest complete local state;
- rename plus edit collapses to one entity update;
- create followed by delete before first upload removes the unsynced entity and
  needs no remote tombstone;
- update followed by delete collapses to a tombstone;
- a remote-applied local write advances the baseline atomically and must not
  enqueue an upload echo;
- a journal entry never changes provider profile after creation.

Coalescing must not discard the last durable local bytes needed to create a
conflict copy. A cloud operation is reported complete only after its resulting
baseline/WAL record is fsynced (or its IndexedDB transaction commits).

## 9. Filesystem and local-change detection

Filesystem builds combine notifications with periodic full scans. Notifications
reduce latency but are not correctness-critical.

- A path already present in the index retains its Sync ID when content changes.
- An in-app rename/move updates the index in the same logical transaction.
- A watcher-observed external rename retains its Sync ID.
- After downtime, exactly one disappeared path plus exactly one new path with
  the same content hash may be treated as a rename.
- If the original remains and an identical new file appears, the new file is a
  copy and receives a new Sync ID.
- Ambiguous identical hashes are not used for rename inference.
- A rename plus content edit while the app was closed may be treated as delete
  plus create. This limitation is the explicit cost of keeping UUIDs out of
  Markdown. It must remain lossless even when identity inference is impossible.
- Empty-folder rename detection uses a watcher event when available; otherwise
  an ambiguous offline rename is delete plus create.
- Symlinks are ignored by default. MemoDump never follows a symlink outside the
  vault or through a directory cycle.
- File reads are stable only after size/mtime settle across a short debounce.
- Local writes use a temporary sibling file and atomic rename.

The scanner ignores `.memodump`, existing `.images`, temporary files owned by
MemoDump/Syncthing, OS metadata files, and names already excluded by the note
repository. The exact ignore list is shared by scanning and initial import.

## 10. Conflicts and local optimistic concurrency

### 10.1 Note conflicts

When a note CAS fails and both local and remote differ from the baseline:

1. The already-accepted remote version remains the original entity.
2. The local version is materialized as a new note entity named:

   ```text
   <stem> (conflict <device> <YYYYMMDD-HHmmss>).md
   ```

3. The new conflict entity receives a new Sync ID and is uploaded normally.
4. Both files appear on every device.
5. The conflict center records the relationship and offers compare, keep one,
   or keep both actions.

Name sanitization follows the existing portable filename rules. A numeric
suffix is added on collision. V1 does not automatically merge Markdown.

### 10.2 Delete conflicts

- Remote delete versus unchanged local: apply the tombstone locally.
- Local delete versus unchanged remote: conditionally publish a tombstone.
- Delete versus concurrent edit: preserve the edited version as a conflict
  copy, then apply the tombstone to the original entity.
- Folder deletion is blocked while a descendant has an unresolved edit or
  structural conflict.

### 10.3 Structural folder conflicts

Concurrent folder rename/move operations can affect an entire subtree. V1 does
not automatically duplicate or re-parent the subtree. It records a structural
conflict, leaves the local working tree untouched, and pauses synchronization
for that folder and its descendants until the user chooses:

- use the remote location;
- use the local location; or
- create a second folder and explicitly move selected children.

Unrelated entities continue syncing.

### 10.4 Same-device and same-server concurrent edits

Cloud CAS cannot protect two browser tabs editing the same local/server note.
Therefore Feature 2 requires local optimistic concurrency:

- `NoteDocument` gains a local `revision` string.
- Updates and deletes include `baseRevision`.
- A mismatched revision returns `409 local_revision_conflict` and never writes.
- Applying a remote sync change or an external filesystem change advances the
  local revision.
- An editor with unsaved content remains intact and enters a local conflict
  flow instead of overwriting the new file.

This requirement applies even while cloud sync is disabled because it protects
self-hosted multi-tab/multi-client editing. The API contract must be revised
before implementation.

### 10.5 Portable path collisions

Remote structure is case-sensitive, but local platforms may not be. Paths are
compared using a portable collision key (Unicode NFC plus case folding) while
preserving the original display name. A collision such as `Note.md` versus
`note.md` creates a `path_conflict`; neither file overwrites the other. Reserved
Windows names, trailing dots/spaces, invalid separators, and maximum lengths use
the existing sanitizer and conflict suffix rules.

## 11. Image and media model

Image insertion and note synchronization are separate concerns. Settings expose
an explicit **Image destination** rather than deriving it invisibly from the
sync provider.

### 11.1 External S3 URL hosting (Typora-style)

Flow:

1. Validate magic bytes, type, and size; compute the media key.
2. Durably stage the blob before inserting the editor node.
3. Insert the final public HTTPS URL immediately.
4. Upload and verify anonymous readability using the existing media-outbox
   lifecycle.
5. Remove the local staged blob after verification.

The cloud sync engine treats the HTTPS URL as ordinary Markdown text and does
not copy the image. This mode minimizes local storage and is interoperable with
other Markdown renderers, but the URL is public and the note depends on that
object remaining available.

S3 image-host configuration and S3 sync-target configuration are independent.
The UI may offer to reuse endpoint/credentials, but their bucket prefixes must
not overlap and their privacy warnings remain different.

### 11.2 Managed sync media

Canonical Markdown syntax:

```markdown
![alt text](memodump-media:<sha256>.<ext>)
```

Flow:

1. Validate and durably stage the blob; derive the final media key.
2. Insert the final `memodump-media:` URI without later Markdown rewriting.
3. Save the note locally immediately.
4. Upload the immutable media object to the active sync repository.
5. Verify the remote object, then publish any note entity that first references
   it.
6. Delete the staging blob after remote verification unless the user enabled
   offline pinning.
7. Fetch media on demand on other devices and keep it in an evictable LRU cache.

The scheme is intentionally provider-independent and private. Other Markdown
editors can still edit the note but generally cannot render managed media.

Cache locations:

- Wails: OS cache directory keyed by repository ID, never the Markdown vault.
- Server: configured cache/temp directory outside the vault; requests are
  authenticated through MemoDump.
- Pure frontend: Cache Storage or IndexedDB with an explicit byte budget.

Cache eviction never changes Markdown or remote media. "Keep available offline"
pins selected or all media and displays the expected storage cost.

### 11.3 Existing image behavior and migration

- Existing public S3 URLs remain valid external URLs and are not rewritten.
- Existing `/api/images/<key>` URLs and `<vault>/.images/<key>` files continue
  rendering in compatibility mode.
- When sync is enabled, referenced local-vault images require a migration before
  that note is considered fully synced: upload as managed media, verify, then
  atomically rewrite the note to `memodump-media:<key>`.
- Migration never deletes `.images` automatically. Cleanup is a later explicit
  action after references and remote readability are verified.
- Managed-media notes are not pushed ahead of newly referenced media. External
  S3 URL notes may sync while the external media outbox is still pending; the UI
  must continue showing the separate pending-image state.
- V1 performs no automatic remote media GC. Content addressing provides
  deduplication; avoiding data loss is more important than reclaiming space.

The editor parser, raw editor, renderer, sanitizer, search, export, and import
must round-trip `memodump-media:` exactly. This is a release-blocking fixture.

## 12. Build-specific behavior

### 12.1 Wails desktop

- Go owns filesystem scanning, provider traffic, durable device state, and
  managed-media cache.
- OAuth opens the system browser and returns through a fixed desktop callback.
- Dropbox refresh tokens and WebDAV/S3 credentials use the OS credential store
  where available, with an explicit fallback warning if unavailable.
- Changing the data directory stops the current worker before switching. A new
  directory with no sync index starts with sync disabled. A directory containing
  an existing Vault ID creates/reuses the correct path-scoped Replica ID and
  offers to reconnect to its known repository. A copied vault does not reuse
  another local copy's device state.
- External editor changes are detected by watcher plus periodic scan.
- Wails acquires the same Replica-ID process lock as the server. A second app
  instance may edit the vault but cannot sync until it owns the lock.
- Closing Wails stops sync cleanly after the current atomic operation; it does
  not promise background operation after exit.

### 12.2 Self-hosted web server and Docker

- The Go server, not browser clients, owns one background worker per vault.
- Sync continues with no page open.
- Provider calls are server-side and therefore not subject to browser CORS.
- Credentials can come from environment/flags or a server-local config outside
  the vault. Secret fields returned to the UI are always redacted.
- Only an authenticated owner may change sync configuration or resolve a
  structural conflict. The current single-account auth model treats the logged-in
  account as owner; no multi-role model is introduced.
- Multiple server processes must not operate the same vault concurrently. A
  process lock protects the worker; failure to acquire it disables sync and
  reports `vault_already_syncing` without stopping note serving.
- Browser note outbox and server cloud journal remain separate:
  an offline browser change reaches cloud only after it is replayed and saved on
  the server.
- Multiple browser clients are protected by the local revision contract in
  Section 10.4.

### 12.3 Pure frontend / PWA

- Notes, sync index/state, journal, staged media, and cache use separate versioned
  IndexedDB stores. A migration failure leaves the old database untouched and
  starts the app in read-only recovery mode.
- On first enable, request persistent browser storage with
  `navigator.storage.persist()` when available. If persistence is denied, show
  a durable warning and an export action; never claim the local replica is safe
  from browser eviction.
- A Web Lock namespaced by vault/repository elects one sync leader across tabs.
  `BroadcastChannel` distributes status and invalidation events. A lease with
  expiry is the fallback where Web Locks are unavailable.
- A follower tab may edit and enqueue local changes but never calls the provider.
- Service-worker Background Sync is not a V1 correctness mechanism. Sync runs
  only while an eligible page/PWA is active.
- Dropbox uses OAuth code flow with PKCE and a short-lived access token. The
  exact redirect URI must be registered. V1 reauthorizes after expiry instead
  of storing a Dropbox refresh token in page-readable storage.
- The official hosted frontend uses its registered redirect URI. An arbitrary
  self-hosted static origin uses Dropbox's no-redirect copy/paste code flow in
  V1 unless that build supplies its own registered app key and redirect URI.
  It must not silently depend on a MemoDump-operated OAuth backend.
- WebDAV works only when the endpoint passes the browser capability probe for
  CORS, required methods/headers, and exposed ETag.
- S3 requires browser CORS and SigV4 credentials. Credentials are session-only
  by default. "Remember on this device" is explicit and warns that same-origin
  JavaScript/XSS can read stored credentials; encrypting them with a key stored
  beside them would not remove that risk.
- Browser quota exhaustion blocks new local saves/media staging before data is
  discarded. The UI offers sync/retry/export/cache eviction actions.
- IndexedDB eviction or site-data clearing after all changes were synced is a
  recoverable empty-device join. If unsynced data was evicted, no web-only design
  can recover it; the persistence warning must say so plainly.
- The pure frontend has no externally editable filesystem vault in V1. Its
  interoperability path is Markdown/vault import and export.

## 13. Provider details and capability probes

### Dropbox

- Request App Folder and minimum file metadata/content scopes.
- Wails uses PKCE with a refresh token in OS credential storage.
- Server uses authorization code flow through a server callback and stores the
  refresh token outside the vault.
- Pure frontend uses PKCE with a short-lived token and reauthorization.
- Follow `list_folder` pagination and cursor reset rules exactly.
- Create uses non-autorename/add semantics; replace uses the expected file
  revision. Dropbox autorename must never turn a CAS conflict into a new
  unnoticed repository key.
- Rate-limit responses honor provider backoff and remain single-flight.

### WebDAV

The setup test creates an isolated temporary collection/object and verifies:

1. authenticated `OPTIONS`/`PROPFIND` access;
2. collection creation;
3. create-if-absent behavior;
4. ETag retrieval;
5. a deliberately wrong `If-Match` returns precondition failure;
6. correct conditional replacement;
7. GET integrity; and
8. cleanup of only the temporary test object.

RFC 6578 sync tokens are optional acceleration. If unsupported or expired, list
the flat collection with `PROPFIND Depth: 1`. A server that ignores conditional
writes is rejected as unsafe rather than supported in degraded last-write-wins
mode.

For pure frontend, the preflight must also permit `Authorization`, `Content-Type`,
`If-Match`, `If-None-Match`, `Depth`, and the WebDAV methods used, and expose
`ETag`. Failure reports `webdav_browser_cors_unsupported`; it does not imply the
same endpoint is unusable from Wails/server.

### S3-compatible

- Sync uses a private dedicated bucket prefix.
- Create requires `If-None-Match: *`; replace requires `If-Match` with the last
  observed ETag/version.
- A provider that does not implement the required conditional semantics fails
  the setup probe.
- Full prefix listing is the baseline; continuation tokens are persisted only
  for the current scan, not as a change feed.
- Provider ETags are opaque and are not assumed to be MD5 hashes.
- Clock-skew authentication errors surface a system-clock action.
- The image-host prefix must not overlap the sync prefix, so media cleanup or a
  public bucket policy can never affect the private repository.

## 14. Onboarding and repository switching

Setup always identifies one of these cases:

1. **Local non-empty, remote absent/empty**: create a repository, assign UUIDs,
   and upload without changing local paths.
2. **Local empty, remote non-empty**: join and materialize all entities locally.
3. **Both non-empty**: show an import/merge summary before mutation.
4. **Known local index, matching repository ID**: resume using durable state.
5. **Repository ID mismatch or newer schema**: stop and require an explicit
   choice; never reinterpret it as an empty target.

Both-non-empty matching rules:

- same path and same canonical content: associate with the remote Sync ID;
- same path and different content: preserve the local version as a conflict
  copy and materialize the remote original;
- unique paths: coexist;
- same hash at different paths: remain separate because they may be deliberate
  copies;
- portable path collisions: pause for resolution before materializing.

"Replace local" and "replace remote" are not default setup actions. If added
later, they require a preview, typed confirmation, and a recoverable backup.

Switching provider is repository migration, not a config edit:

- pause the old profile;
- report pending/conflicting work;
- flush, abandon explicitly, or cancel;
- create/join the new repository;
- never reuse old provider versions/cursors;
- retain the same local Sync IDs where no collision exists.

## 15. Security and privacy

- E2EE is out of scope. Dropbox, WebDAV, or S3 operators may read managed notes
  and media; setup states this plainly.
- Provider credentials, OAuth tokens, and signed URLs never appear in Markdown,
  `.memodump`, remote entity JSON, logs, analytics, or error exports.
- Wails uses OS credential storage when available.
- Server secrets use environment/flags or a permission-restricted config outside
  the vault. Container documentation must identify which path needs persistence.
- Pure-frontend remembered WebDAV/S3 credentials are an explicit security
  tradeoff. Session-only is the default.
- Remote JSON is untrusted input: enforce size limits, UUID syntax, parent-cycle
  checks, path/name validation, media-key validation, UTF-8 validity, and maximum
  nesting before allocation/materialization.
- Provider endpoints require HTTPS except an explicit localhost/development
  override. Authentication headers are never forwarded across a cross-origin
  redirect.
- Never materialize a remote name using path separators, absolute paths, `..`,
  device paths, or symlink traversal.
- Downloaded managed images are revalidated by magic bytes, size, hash, and
  canonical extension before caching or serving. SVG remains unsupported.
- Managed media is private only to the extent the provider/prefix is private.
- External S3 image URLs are public by design; a content hash is not access
  control.

## 16. Failure and edge-case contract

| Situation | Required behavior |
|---|---|
| Network drops before remote write | Retain journal entry and retry |
| Network drops after write but before response | Read key/version and reconcile idempotently before retry |
| Crash after media upload but before note ref | Orphan media is harmless; retry note later |
| Crash after remote entity update but before local baseline | Pull remote, compare canonical content, and converge without duplicating |
| Crash while writing local Markdown | Atomic temp file prevents partial replacement |
| Corrupt local sync JSON | Use backup or rebuild; never change notes during detection |
| AppData absent but vault index exists | Preserve Sync IDs, mark baseline unknown, and probe remote before mutation |
| Same Vault ID opened from two local paths | Assign separate Replica IDs and state/WAL directories |
| Crash after WAL rename before new active WAL | Replay frozen generations and create a new active WAL |
| Crash after snapshot replacement before frozen deletion | Skip records at/below snapshot watermark, then delete when safe |
| Writer races with compaction | Writer lock closes/rotates/reopens the append descriptor; compaction never touches active WAL |
| Torn final WAL write | Truncate only an unterminated/syntactically partial tail after the last valid record; a complete bad-checksum line requires repair |
| Two filesystem processes open one replica | One OS-level lock owner writes sync state; the other disables sync without blocking note editing |
| Invalid provider cursor | Full remote listing; never reset the repository |
| Remote repository unexpectedly empty | Stop with `remote_reset_detected`; never mass-delete or blindly repopulate |
| Remote entity JSON invalid | Quarantine/report that entity; retain local data |
| Remote entity file physically removed | Treat as repository damage, not a note tombstone |
| Provider auth expires | Pause network work; keep local saves/journal working |
| Provider quota exhausted | Pause uploads, continue local saves where local quota permits |
| Local disk/browser quota exhausted | Refuse the new write before discarding existing data; offer recovery actions |
| Device clock is wrong | Never use it for conflict correctness; surface provider auth clock errors |
| Two pure-frontend tabs open | One leader syncs; both may edit with local revision CAS |
| Two server processes use one vault | One worker lock wins; the other serves notes with sync disabled/error |
| User edits a note externally while editor is dirty | Revision conflict; preserve editor buffer and new disk version |
| User deletes an open note externally | Save does not silently recreate it; offer conflict-copy recovery |
| Case-only rename crosses OSes | Apply only when representable; otherwise path conflict |
| Folder move conflicts with child edit | Pause affected subtree; do not cascade delete or duplicate automatically |
| Managed media missing remotely | Re-upload from staging/cache if verified bytes exist; otherwise mark broken without deleting note |
| External S3 image fails permanently | Keep final URL and error state; do not regress note content |
| Pending image target config changes | Entry remains bound to its original immutable target/profile |
| Sync is disabled during an active cycle | Finish or cancel only at an atomic boundary, then retain journal/index |
| App upgrades wire format | Migrate through versioned readers; newer unsupported format is read-only/blocking |
| Browser storage is evicted | Rejoin from remote if fully synced; never claim unsynced evicted data is recoverable |
| Service worker updates mid-cycle | Durable IDB state makes the next active client resume idempotently |
| User layers another sync tool on the same vault | Warn as unsupported; conflict detection remains lossless but behavior is not guaranteed |

## 17. UI and API contract

### Status model

```ts
interface SyncStatus {
  enabled: boolean
  phase: 'idle' | 'scanning' | 'pulling' | 'pushing' | 'paused' | 'error'
  localPending: number
  mediaPending: number
  conflicts: number
  lastSuccessAt: number | null
  nextRetryAt: number | null
  provider: 'dropbox' | 'webdav' | 's3' | null
  error?: { code: string; message: string; action?: string }
}
```

Primary states use plain language:

- `Saved locally`
- `Waiting to sync (N)`
- `Syncing`
- `Synced`
- `Sign in again`
- `Sync paused`
- `Conflict needs attention (N)`

The editor must never replace "Saved locally" with an alarming global error.
Provider failure belongs in the sync status area and settings/conflict center.

### Common operations

Server/Wails expose same-origin APIs or bindings equivalent to:

```text
GET    /api/v2/sync/status
GET    /api/v2/sync/config
PUT    /api/v2/sync/config
POST   /api/v2/sync/test
POST   /api/v2/sync/run
POST   /api/v2/sync/pause
POST   /api/v2/sync/resume
POST   /api/v2/sync/disconnect
GET    /api/v2/sync/conflicts
POST   /api/v2/sync/conflicts/{id}/resolve
```

Pure frontend implements the same repository/composable surface locally rather
than making these HTTP calls. Secret reads always return redacted placeholders.

Status changes use a lightweight event stream where available and polling as a
fallback. Correctness never depends on delivery of a UI event.

## 18. Testing and acceptance criteria

### Shared conformance fixtures

The Go and TypeScript implementations consume identical fixtures for:

- deterministic entity serialization and hashes;
- canonical Markdown/front-matter preservation and LF normalization;
- UUID/path mapping;
- snapshot/WAL rotation, watermark replay, torn-tail recovery, and fsync ordering;
- copied-vault Replica ID separation and missing-AppData remote probing;
- create/update/delete transitions;
- retry classification;
- name sanitization and portable collision keys;
- note and deletion conflicts;
- media reference extraction;
- provider cursor reset;
- interrupted-operation recovery;
- unsupported schema handling.

Each provider adapter also runs against an in-memory contract implementation.
Real-provider tests are opt-in and use isolated prefixes.

### Required scenarios before release

1. Two devices edit different notes offline and converge.
2. Two devices edit the same note offline and both contents survive.
3. Edit versus delete preserves the edit as a conflict copy.
4. Rename/move keeps identity when observed and remains lossless when ambiguous.
5. Empty folders synchronize and delete correctly.
6. Folder structural conflict pauses only the affected subtree.
7. A crash at every remote/local commit boundary resumes idempotently.
8. A crash at every WAL rotation/compaction boundary recovers without losing or
   double-applying state.
9. Copying a synced vault to a fresh device preserves UUIDs, creates a new
   Replica ID, and probes remote state before uploading or deleting.
10. Dropbox cursor reset performs a safe full scan.
11. WebDAV without RFC 6578 works; WebDAV ignoring `If-Match` is rejected.
12. S3 conditional failure produces a conflict, never overwrite.
13. Pure frontend multi-tab editing has one sync leader and local CAS protection.
14. Pure frontend storage-persistence denial and quota exhaustion have clear,
    non-destructive recovery UI.
15. Server sync continues with no browser open and rejects a second worker.
16. An external editor change while a MemoDump editor is dirty is preserved.
17. A Typora-style S3 image leaves no durable local blob after verification.
18. Managed media uploads before its first note reference and downloads on
    demand on another device.
19. Existing `/api/images/` notes render before and after migration.
20. `memodump-media:` round-trips through WYSIWYG, raw Markdown, import/export,
    search, and every build.
21. Disabling sync creates no new metadata; a never-enabled vault has no
    `.memodump` directory.
22. Disconnecting or switching provider cannot replay work into the wrong target.
23. A Go stress test appends during repeated compaction, injects a crash at every
    numbered boundary, and verifies sequence continuity, watermark idempotence,
    and zero missing durable records. A large-vault benchmark records compaction
    wall time, peak heap, allocation count, writer-lock hold time, and fsync
    latency distributions; releases compare these metrics against an explicit
    regression budget rather than assuming a fixed disk latency.

## 19. Rollout plan

1. Revise the local note API to add optimistic `revision`/`baseRevision`.
2. Add image-destination terminology and preserve existing image compatibility.
3. Implement opt-in sync-index creation, Replica IDs, rotating WAL/snapshot
   persistence, crash recovery, and rebuild.
4. Implement the Go and TypeScript sync cores against an in-memory RemoteStore.
5. Add filesystem watcher/full-scan reconciliation and IndexedDB multi-tab
   leadership.
6. Add managed media and legacy local-image migration.
7. Implement Dropbox, then WebDAV, then S3-compatible adapters.
8. Add onboarding, status, conflicts, and provider-switch UI.
9. Run crash, browser, cross-platform filename, and real-provider test matrices.
10. Enable the feature behind an experimental flag before making it default.

## 20. Industry references

- Obsidian keeps notes as ordinary Markdown in a vault, stores vault settings in
  a hidden directory, tracks a rebuildable metadata cache, treats attachments as
  regular files, updates links on rename, and offers conflict-copy behavior:
  <https://obsidian.md/help/Files%2Band%2Bfolders/How%2BObsidian%2Bstores%2Bdata>,
  <https://obsidian.md/help/Editing%2Band%2Bformatting/Attachments>,
  <https://obsidian.md/help/sync/troubleshoot>.
- Joplin separates its synchronizer from provider-specific file APIs and treats
  notes, folders, tags, and resources as sync items:
  <https://joplinapp.org/help/dev/spec/sync/>.
- Syncthing combines filesystem notifications with full scans, hashes content,
  writes through temporary files, and propagates conflict copies:
  <https://docs.syncthing.net/users/syncing.html>.
- Git separates a normal working tree from a hidden index/object database and
  uses immutable content-addressed objects:
  <https://git-scm.com/docs/gitrepository-layout>,
  <https://git-scm.com/docs/gitdatamodel.html>.
- Dropbox OAuth and incremental-listing behavior:
  <https://developers.dropbox.com/oauth-guide>,
  <https://dropbox.github.io/dropbox-sdk-js/Dropbox.html>.
- WebDAV conditional updates and optional collection synchronization:
  <https://www.rfc-editor.org/rfc/rfc4918.html>,
  <https://www.rfc-editor.org/rfc/rfc6578.html>.
- Browser CORS/preflight rules:
  <https://fetch.spec.whatwg.org/>.
- S3 conditional writes:
  <https://docs.aws.amazon.com/AmazonS3/latest/userguide/conditional-writes.html>.

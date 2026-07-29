# MemoDump Architecture Refactor Plan

Date: 2026-07-29  
Branch: `refactor/storage-architecture`  
Status: proposed — implementation must not begin before review

## Goals

1. Split `MainView.vue` into testable UI and state modules without changing user-visible behavior.
2. Give the Go server and browser-local backend one explicit behavioral contract.
3. Make folder/note loading incremental so cost grows with the opened view, not the whole vault.
4. Load Milkdown only when rich editing is actually requested.
5. Prepare stable boundaries for future Dropbox/WebDAV synchronization while keeping saves local and fast.

## Non-goals

- Implement Dropbox or WebDAV in this refactor.
- Change the on-disk Markdown format.
- Reintroduce autosave.
- Replace Vue, Milkdown, IndexedDB, or the Go HTTP server.
- Build a general distributed filesystem.

## Architectural direction

Keep the application local-first:

```text
Vue UI
  |
  v
Note application service (stable domain operations)
  |
  +-- browser-local repository (IndexedDB)
  |
  +-- HTTP repository client ------> Go application service
                                      |
                                      +-- local filesystem repository
                                      |
                                      +-- future sync coordinator
                                             |
                                             +-- Dropbox adapter
                                             +-- WebDAV adapter
```

Dropbox/WebDAV should not sit in the interactive save path. A Save operation commits to the local repository first. A future sync coordinator then pushes and pulls changes in the background, records provider revisions/ETags, and reports conflicts independently.

## 1. `MainView.vue` split

### Current issue

`MainView.vue` owns layout, routing, note editing, dirty state, persistence, search, folders, drag/drop, card layout, dialogs, imports, context menus, and substantial CSS. Changes in one workflow can accidentally affect another, and saving behavior is difficult to test without mounting the entire page.

### Target component tree

```text
MainView.vue                         orchestration and page-level layout only
  SidebarPanel.vue                  navigation shell and responsive sidebar
    FolderTree.vue                  folder loading/expansion
    FolderNode.vue                  one recursive folder node
  MainHeader.vue                    mode-aware header shell
    NoteEditorHeader.vue            title, folder, tags, save/delete actions
    BrowseHeader.vue                breadcrumb, sort, new-note action
    SearchHeader.vue
  BrowseNotesView.vue               note grid and empty states
    NoteWaterfall.vue
    NoteCard.vue
  SearchNotesView.vue
  NoteEditorView.vue                raw/rich editor selection
  SettingsPanel.vue
```

Do not split only by template size. State ownership should move with each workflow.

### Target composables/services

- `useNoteEditor`
  - open/create/save/delete note
  - editor snapshot, dirty/saving/error states
  - title/tag/folder edits
  - protection against stale save responses
  - navigation guard
- `useNoteBrowser`
  - active folder
  - current folder page
  - sort mode
  - local cache updates after create/move/rename
- `useFolderTree`
  - root folder list
  - expansion state
  - lazy child loading and invalidation
- `useNoteSearch`
  - debounced query
  - request cancellation or generation checks
  - results
- `noteRepository`
  - backend-independent domain methods
  - maps transport responses into stable domain objects

`MainView.vue` should ideally be under roughly 400 lines after extraction. This is a guide, not a mechanical acceptance criterion.

### Migration order

1. Extract `useNoteEditor` with characterization tests while leaving the template intact.
2. Extract browse/search state.
3. Extract `NoteEditorView` and its header.
4. Extract browse/search components.
5. Extract sidebar/folder tree after lazy-loading APIs exist.
6. Move page-specific CSS next to extracted components; retain shared tokens/utilities globally.

Each step must be independently buildable and revertible.

## 2. One frontend/backend semantic contract

### Canonical domain types

Define the contract once in `docs/api-contract.md` and encode it as fixtures/tests:

```ts
type NoteId = string // normalized slash-relative path; opaque to UI code

interface NoteSummary {
  id: NoteId
  name: string
  parentId: string
  tags: string[]
  modifiedAt: number
  preview: string
  revision?: string
}

interface NoteDocument extends NoteSummary {
  content: string
}

interface FolderSummary {
  id: string
  name: string
  parentId: string
  hasChildren: boolean
}

interface ApiError {
  code: string
  message: string
  details?: Record<string, unknown>
}
```

The UI should use `id`, not parse paths to derive state. Initially `id` may equal the path, but callers must treat it as opaque so a future stable UUID can be introduced.

### Required semantic decisions

Document and test:

- filename normalization, Unicode handling, maximum length, and reserved names;
- whether `.md` is accepted in user-entered names;
- collision behavior (`409 note_name_conflict`);
- missing objects (`404 note_not_found`);
- invalid paths/names (`400 invalid_note_name`);
- tag encoding, ordering, duplicates, empty tags, commas and quotes;
- root folder representation;
- timestamp and timezone units;
- create/update/move/rename atomicity;
- idempotency and retry behavior;
- maximum note/upload sizes;
- local and remote error equivalence.

### Contract test strategy

Create a backend-agnostic behavior suite expressed as JSON fixtures:

```text
testdata/contracts/
  note-create.json
  note-rename-conflict.json
  tags-roundtrip.json
  folder-move.json
  error-codes.json
```

- Go tests run fixtures against the filesystem repository/HTTP handlers.
- Vitest runs the same fixtures against IndexedDB `localApi`.
- HTTP client mapping tests ensure wire errors become the same domain errors.

Avoid duplicating business rules in `api.go` and `localApi.js`. Put normalization in repository/domain modules on each platform and prove equivalence through the shared fixtures.

## 3. Lazy-loading API

### Problem

`GET /api/folders` recursively walks the whole vault and embeds note summaries in every folder. Opening the app or refreshing after mutations therefore scales with total vault size.

### Proposed endpoints

```http
GET /api/folders?parent={folderId}
GET /api/notes?parent={folderId}&cursor={cursor}&limit=50&sort=modified-desc
GET /api/notes/{noteId}
GET /api/search?q=...&tag=...&cursor=...&limit=50
```

Example folder response:

```json
{
  "items": [
    {
      "id": "projects",
      "name": "projects",
      "parentId": "",
      "hasChildren": true
    }
  ],
  "revision": "optional-folder-list-revision"
}
```

Example note page:

```json
{
  "items": [],
  "nextCursor": null
}
```

### Cursor behavior

Use an opaque cursor derived from the sort key and a stable tie-breaker:

```text
(modifiedAt, noteId)
```

Do not expose an offset cursor. Offset pagination becomes unstable when saves reorder notes.

### Frontend caching

- Cache folder children by `parentId`.
- Cache note pages by `(parentId, sortMode)`.
- Deduplicate concurrent requests.
- Invalidate only the source/destination parent after move.
- Patch cached summaries from successful create/update responses.
- Keep stale data visible while revalidating.
- Use request generations or `AbortController` so late folder/search responses cannot replace newer state.

### Compatibility migration

1. Add paginated response endpoints or a versioned `/api/v2`.
2. Add repository client methods alongside the current methods.
3. Migrate folder tree and browse views.
4. Migrate search.
5. Remove recursive folder payload only after all clients, including Wails, use the new contract.

## 4. Milkdown code splitting

### Immediate implementation

Replace the static component import:

```js
import MilkdownEditor from '../components/MilkdownEditor.vue'
```

with:

```js
import { defineAsyncComponent } from 'vue'

const MilkdownEditor = defineAsyncComponent({
  loader: () => import('../components/MilkdownEditor.vue'),
  delay: 100,
  timeout: 15000,
})
```

Because Milkdown/Crepe imports live inside `MilkdownEditor.vue`, Vite can move that dependency graph into an editor chunk. The raw editor, login, browsing, and settings paths then avoid downloading it initially.

### UX details

- Show a lightweight editor skeleton while the chunk loads.
- Keep raw mode immediately available.
- Prefetch the editor chunk on strong intent:
  - pointer hover/focus on a note card;
  - clicking New Note;
  - browser idle callback after the initial note list is interactive.
- Do not preload on the login page or in local views that never open an editor.
- Add an error component with Retry and “open in raw mode”.

### Vite chunking

After measuring the async component result, optionally define stable vendor chunks:

```js
build: {
  rollupOptions: {
    output: {
      manualChunks(id) {
        if (id.includes('@milkdown') || id.includes('prosemirror')) {
          return 'editor-vendor'
        }
      }
    }
  }
}
```

Do not start with a large manual chunk table. First verify the async boundary using the build manifest/bundle visualizer. Language packages loaded by the code editor should also be checked for eager imports.

### Acceptance criteria

- Browsing entry chunk no longer contains Milkdown/ProseMirror.
- Login and note-list views work before the editor chunk finishes.
- Opening a note loads the editor once and preserves initial content.
- Editor chunk load failure has a recoverable raw-mode path.
- Compare initial transferred bytes and time-to-interactive before/after.

## 5. Future Dropbox/WebDAV readiness

### Domain boundary

Introduce a local repository interface before adding providers:

```go
type NoteRepository interface {
    Get(ctx context.Context, id string) (NoteDocument, error)
    List(ctx context.Context, query ListQuery) (NotePage, error)
    Create(ctx context.Context, command CreateNote) (NoteDocument, error)
    Update(ctx context.Context, command UpdateNote) (NoteDocument, error)
    Move(ctx context.Context, command MoveNote) (NoteDocument, error)
    Delete(ctx context.Context, id string) error
}
```

HTTP handlers depend on an application service, not directly on `os.*` or global `dataDir`. IndexedDB should expose the equivalent frontend repository interface.

### Separate remote sync interface

Do not force Dropbox/WebDAV into `NoteRepository`. Their concerns are different:

```go
type RemoteObjectStore interface {
    ListChanges(ctx context.Context, cursor string) (ChangePage, error)
    Download(ctx context.Context, key string, revision string) (Object, error)
    Upload(ctx context.Context, object Object, precondition Revision) (Revision, error)
    Delete(ctx context.Context, key string, precondition Revision) error
}
```

Provider adapters translate:

- Dropbox cursor/revision semantics;
- WebDAV `ETag`, `If-Match`, `PROPFIND`, and path behavior;
- authentication and token refresh;
- rate limits and retry hints.

### Metadata required later

Reserve an application-owned metadata database rather than putting provider state in Markdown front matter:

```text
note_id
local_path
content_hash
local_revision
remote_provider
remote_key
remote_revision_or_etag
last_synced_hash
sync_state
deleted_at (tombstone)
```

Do not expose provider revisions as the note's only identity.

### Conflict model

At minimum:

- unchanged locally + changed remotely → pull;
- changed locally + unchanged remotely → push;
- changed on both sides → create an explicit conflict record/copy;
- deleted on one side → apply a tombstone policy, never infer deletion from a transient listing failure.

Content hashes should be calculated from canonical stored bytes. Provider clocks must not be used as the sole conflict detector.

### Security/configuration preparation

- Keep provider credentials outside note files and exported archives.
- Define a credential-store interface; use OS keychain/credential storage in desktop builds where possible.
- Redact secrets from logs and diagnostics.
- Ensure provider-specific settings are versioned and migratable.
- Design cancellation/progress reporting for long syncs.

## Delivery phases

### Phase A — characterization and contract

- Add `docs/api-contract.md`.
- Add shared contract fixtures.
- Add Go and Vitest fixture runners.
- Freeze current intended behavior before moving code.

### Phase B — frontend state extraction

- Extract `useNoteEditor`.
- Extract repository adapters from `api.js`/`localApi.js`.
- Extract browser/search composables and components.
- Preserve all routes and visible behavior.

### Phase C — lazy-loading API

- Add folder children and cursor-paginated note/search APIs.
- Add client cache/invalidation.
- Migrate folder tree and browse/search views.

### Phase D — editor split

- Add async Milkdown boundary, loading/error states, and intent prefetch.
- Measure bundles and add minimal Vite chunk rules only if necessary.

### Phase E — storage boundary

- Extract Go filesystem repository and application service.
- Remove direct handler dependency on globals and `os.*`.
- Add revision/hash metadata hooks, but no remote provider.

## Commit strategy

Keep commits reviewable and behavior-focused:

1. `docs: define note and folder API semantics`
2. `test: run shared repository contract fixtures`
3. `refactor: extract note editor state from MainView`
4. `refactor: split browse and search views`
5. `feat: add lazy folder and note listing APIs`
6. `refactor: migrate folder tree to lazy loading`
7. `perf: lazy-load Milkdown editor`
8. `refactor: introduce filesystem note repository`
9. `docs: define future remote sync adapter contract`

Tests and build must pass at every commit. Avoid mixing API contract changes with component moves.

## Risks and controls

- **Behavior drift during split:** characterization and contract tests first.
- **Stale UI after removing full reloads:** explicit cache patch/invalidation tests.
- **Pagination duplicates/missing notes:** stable cursor tie-breaker tests.
- **Wails compatibility:** exercise browser HTTP, local IndexedDB, and Wails builds at phase boundaries.
- **Async editor content loss:** mount/unmount and initial-content tests.
- **Premature cloud abstraction:** implement only local repository boundaries now; keep provider APIs documented until cloud work is scheduled.

## Review decisions required before implementation

1. Keep path-based IDs temporarily, or introduce stable UUIDs in this refactor?
2. Add `/api/v2`, or evolve current endpoints with compatibility parameters?
3. Is 50 notes per page an acceptable initial default?
4. Should raw mode be the automatic fallback when the Milkdown chunk fails?
5. Should the storage-boundary phase be included now, or deferred until after UI/API performance work?

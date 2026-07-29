# MemoDump Architecture Refactor Plan

Date: 2026-07-29  
Branch: `refactor/storage-architecture`  
Status: reviewed — implementation may begin in the documented phases

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

### Current identity audit

Today `path` is not merely a location. It simultaneously acts as:

- the Go filesystem lookup key and HTTP route parameter;
- the IndexedDB primary key;
- the Vue list key and waterfall measurement/content-cache key;
- the URL deep-link value (`?note=...`);
- the offline outbox coalescing key;
- the drag/drop payload;
- the source for deriving note name and parent folder.

Folder paths additionally encode ancestry and are used for prefix-based move/delete checks. Renaming or moving therefore changes identity everywhere.

### Identity decision: opaque path IDs

This version does not introduce persistent UUIDs. MemoDump keeps the vault as a clean, ordinary folder tree containing ordinary Markdown files:

- no UUID in Markdown front matter;
- no hidden identity index inside the vault;
- no external UUID database required to interpret a vault;
- copying the folder remains sufficient to copy the notes.

The domain still uses an opaque `id` boundary:

```ts
type NoteId = string
```

For this version, `id` is the normalized relative path. UI code must not parse, concatenate, or derive parent/name data from it. Repository implementations own path semantics and return the new ID after a rename or move.

Consequences:

- rename/move changes the note ID;
- caches, selection, URL state and outbox entries must migrate from old ID to new ID explicitly;
- a future first-generation cloud sync may represent rename/move as delete-old + create-new;
- stable cross-device rename detection is deliberately deferred;
- UUIDs can be reconsidered only if future requirements such as stable sharing, note-to-note references, or multi-device rename identity justify metadata.

### Canonical domain types

Define the contract once in `docs/api-contract.md` and encode it as fixtures/tests:

```ts
type NoteId = string // opaque normalized relative path in this version

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

The UI uses `id` for identity, cache keys, selection, outbox coalescing and routes. Only repository/storage code interprets it as a path. Rename and move return a new ID and an explicit old-to-new identity transition.

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
})
```

Because Milkdown/Crepe imports live inside `MilkdownEditor.vue`, Vite can move that dependency graph into an editor chunk. The raw editor, login, browsing, and settings paths then avoid downloading it initially.

### UX details

- Show a lightweight editor skeleton for as long as the chunk is loading.
- Keep raw mode immediately available.
- Prefetch the editor chunk on strong intent:
  - pointer hover/focus on a note card;
  - clicking New Note;
  - browser idle callback after the initial note list is interactive.
- Do not preload on the login page or in local views that never open an editor.
- Add an error component with Retry and “open in raw mode”.
- Never infer failure from elapsed time and never switch modes automatically.

### Failure classification

Slow loading and failed loading are different states:

1. **Chunk loading failure:** the dynamic `import()` promise rejects. Typical Web/PWA causes are offline access before the chunk was cached, a corrupted/evicted browser cache, a proxy/CDN error, or an old open page requesting a hashed chunk removed by a newer deployment.
2. **Editor initialization failure:** the component chunk loaded, but `crepeInstance.create()` rejects because of an incompatible runtime, unexpected DOM/browser behavior, or an editor/plugin defect.
3. **Slow load:** the promise is still pending. This is not an error regardless of duration.

Expected frequency:

- Wails: chunk loading failure should be exceptionally rare because assets are packaged locally; initialization defects are the more relevant failure class.
- Normal Web with a stable deployment: rare, but possible on poor/offline networks.
- PWA across deployments: uncommon but materially more plausible if the service worker and hashed asset lifecycle are not coordinated.

Behavior:

- pending → keep the skeleton and retain note state;
- explicit rejection → show Retry and a user-selected Raw option;
- initialization rejection → preserve Markdown, show the same recovery UI, and log a sanitized diagnostic;
- no timeout-based automatic Raw fallback.

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

### Minimal future extension seam

This version does not add remote-sync behavior, provider metadata, conflict resolution, credentials, background jobs, or Dropbox/WebDAV dependencies.

The only preparation is:

- HTTP handlers depend on a local `NoteRepository`, not directly on `os.*`;
- rename/move identity transitions are returned explicitly;
- successful mutations can later emit an application event without changing handler semantics;
- repository methods accept `context.Context`;
- provider-specific concepts such as ETag, cursor and OAuth token do not leak into note domain types.

A future major version may introduce a separate remote interface such as:

```go
type RemoteObjectStore interface {
    ListChanges(ctx context.Context, cursor string) (ChangePage, error)
    Download(ctx context.Context, key string, revision string) (Object, error)
    Upload(ctx context.Context, object Object, precondition Revision) (Revision, error)
    Delete(ctx context.Context, key string, precondition Revision) error
}
```

This interface is documentation only in the current version. Future provider adapters would translate:

- Dropbox cursor/revision semantics;
- WebDAV `ETag`, `If-Match`, `PROPFIND`, and path behavior;
- authentication and token refresh;
- rate limits and retry hints.

### Metadata required later, not now

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

### Conflict model for the future major version

At minimum:

- unchanged locally + changed remotely → pull;
- changed locally + unchanged remotely → push;
- changed on both sides → create an explicit conflict record/copy;
- deleted on one side → apply a tombstone policy, never infer deletion from a transient listing failure.

Content hashes should be calculated from canonical stored bytes. Provider clocks must not be used as the sole conflict detector.

### Security/configuration for the future major version

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

### Phase E — minimal local storage boundary

- Extract Go filesystem repository and application service.
- Remove direct handler dependency on globals and `os.*`.
- Keep opaque path IDs and add a future mutation-event seam.
- Do not add remote implementations, sync metadata, provider configuration, credentials, or background workers.

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
9. `docs: record future remote sync extension seam`

Tests and build must pass at every commit. Avoid mixing API contract changes with component moves.

## Risks and controls

- **Behavior drift during split:** characterization and contract tests first.
- **Stale UI after removing full reloads:** explicit cache patch/invalidation tests.
- **Pagination duplicates/missing notes:** stable cursor tie-breaker tests.
- **Wails compatibility:** exercise browser HTTP, local IndexedDB, and Wails builds at phase boundaries.
- **Async editor content loss:** mount/unmount and initial-content tests.
- **Premature cloud abstraction:** implement only local repository boundaries now; keep provider APIs documented until cloud work is scheduled.

## Recorded decisions

1. Do not introduce persistent UUIDs; preserve the pure Markdown folder structure and use opaque path IDs.
2. Use 50 notes as the default page size.
3. Milkdown loading has no failure timeout and never automatically switches to Raw mode.
4. This version prepares only the minimum local repository/event seam for a future remote-sync major version.

5. Add `/api/v2` for paginated response envelopes and the opaque-ID contract, while retaining current endpoints during migration.

## Milkdown persistent-instance follow-up

The async editor boundary removes Milkdown from the initial application bundle,
but remounting it for every note still repeats Crepe and ProseMirror
initialization. Hiding that initialization removes visible reflow without
improving input latency.

Implement one editor instance for the lifetime of the editor view:

1. Mount `MilkdownEditor` without a per-note Vue key.
2. Watch an explicit document identity plus content and replace the Milkdown
   document through `replaceAll` when the selected note changes.
3. Suppress listener updates caused by programmatic replacement so opening a
   note cannot mark it dirty or overwrite it with a stale callback.
4. Preserve the current document snapshot when switching between Raw and rich
   modes.
5. Keep the initial hidden-layout guard only for the first Crepe creation;
   subsequent note switches must remain visible and update in place.

Tests must cover initial creation, consecutive document replacements, ignored
programmatic update events, and rapid selection changes. Production build
output must continue to keep Milkdown outside the main entry chunk.

## Milkdown feature-level bundling follow-up

The language modes listed by `@codemirror/language-data` are already emitted as
dynamic chunks and are fetched only when their language is selected. Removing
most languages would therefore reduce build output count but would not address
the main first-open cost, while unnecessarily reducing compatibility with
existing fenced code blocks.

Replace the aggregate `@milkdown/crepe` import with the public modular API:

1. Construct the editor with `CrepeBuilder`.
2. Import and register only the currently enabled Crepe features through their
   documented feature subpaths.
3. Keep CodeMirror's language descriptions so uncommon existing code blocks
   still load their grammar on demand.
4. Do not import the LaTeX feature or KaTeX because math is disabled in this
   version; remove its slash-menu and toolbar entry where applicable.
5. Preserve feature configuration, theme CSS order, error fallback, persistent
   editor behavior, and Markdown round trips.

Compare the minified/gzip size of the generated Milkdown entry before and after.
The main application entry must not absorb the editor dependencies, and tests
plus the production build must pass before committing.

## MainView extraction tranche: browse and search results

This tranche removes note-result rendering from `MainView.vue` without moving
request ownership yet:

1. Extract the repeated waterfall columns and note-card markup into
   `NoteWaterfall.vue`.
2. Keep measurement and overflow behavior inside that presentation boundary,
   using the existing provided card-layout service rather than passing DOM
   directives through `MainView`.
3. Extract `BrowseNotesView.vue` to own the browse empty state, load-more
   affordance and presentation-only events.
4. Extract `SearchNotesView.vue` to own search inputs, search empty states and
   result rendering. Query/tag values remain controlled by the parent in this
   tranche so request/debounce behavior does not change.
5. Move only component-specific CSS. Shared card tokens and genuinely
   page-level responsive layout may remain in `MainView` until their final
   owner is clear.

The extracted components must not call APIs, mutate note data, or own route
state. Existing open, drag, context-menu, expand, load-more and search events
must be forwarded explicitly. Tests and the production build must pass before
the implementation commit.

## MainView extraction tranche: page header

This tranche extracts the mode-dependent header while preserving state
ownership:

1. Add `MainHeader.vue` as the mode switch for settings, search, editing and
   browse headers, including the mobile-menu trigger.
2. Add `NoteEditorHeader.vue` for editable title/folder/tags and
   save/raw/delete actions. It receives controlled values and emits mutations;
   it does not save, delete, navigate or open dialogs itself.
3. Add `BrowseHeaderActions.vue` for sort-menu presentation and new-note
   intent. The parent continues to own the selected sort and note creation.
4. Keep title-width measurement in the editor-header component, next to the DOM
   it measures.
5. Move header-only CSS with each component and retain shared page shell styles
   in `MainView`.

All state transitions must remain explicit events. Keyboard behavior, mobile
layout, save status styling, folder selection, tag removal, Raw mode switching
and settings/search close behavior must remain unchanged.

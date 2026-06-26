# MainView Split + Note Duplicate — Design

Date: 2026-06-26
Status: Approved (brainstorming complete; pending implementation plan)

## Goals

1. **Split `MainView.vue`** (2636 lines: ~456 template, ~1193 `<script setup>`, ~983 scoped CSS) into a small shell plus focused child components and composables, preserving exact runtime behavior.
2. **Add a Duplicate note feature** to the right-click context menu and both backends (Go server + IndexedDB local build).

## Non-goals

- No new features beyond Duplicate. No UI/UX redesign — visuals and interactions stay identical.
- No build tooling, router, or data-model changes.
- No test framework added (repo has none); the only automated test surface is `frontend/src/localApi.test.js`.

## Decisions (confirmed with user)

- **Refactor depth: hybrid** — extract child components *and* composables; MainView becomes a shell (~300–400 lines).
- **Duplicate UX: refresh the list, stay on the current view** (do not auto-open the copy).
- **Duplicate naming: `"(copy)"` suffix with auto de-collision** (`(copy)`, `(copy 2)`, `(copy 3)`…), always into the same folder as the source.
- `useNotes` stays a single (larger) composable rather than being subdivided.

---

## Part A — Duplicate feature

### A.1 Backend (Go)

**New handler `handleDuplicateNote` in `api.go`**, registered as `POST /api/duplicate/{path...}` in `server.go` (alongside the existing `PUT /api/move/{path...}`).

Logic:
1. `notePath := r.PathValue("path")`; `fullPath, err := safePath(dataDir, notePath)` → 400 on illegal path.
2. `os.Stat(fullPath)` → 404 `{"error":"File not found"}` if missing.
3. `data, err := os.ReadFile(fullPath)` — copy **raw bytes** so YAML front matter and tags are preserved byte-for-byte.
4. Compute a unique destination name in the *same directory* as `fullPath`:
   - `base := strings.TrimSuffix(filepath.Base(fullPath), ".md")`
   - candidates: `base + " (copy).md"`, then `base + " (copy 2).md"`, `base + " (copy 3).md"` … (loop with `os.Stat`, stop at first non-existent).
   - `destPath, err := safePath(filepath.Dir(fullPath), candidate)` → 400 on illegal.
5. `os.WriteFile(destPath, data, 0644)` → 500 on failure. (Destination is a brand-new uniquely-named file, so no overwrite-corruption risk; this matches `handleCreateNote`, which also uses plain `WriteFile` rather than the tmp+rename atomic path reserved for in-place rewrites.)
6. `note, _ := readNote(destPath, dataDir, true)`; `writeJSON(w, http.StatusCreated, note)`.
7. No cache invalidation needed (path is new); `readNote` populates the cache.

The new file's mtime is "now", so the duplicate surfaces as the most-recently-modified note after `loadAll()`.

### A.2 Frontend remote API (`api.js`)

Add to `remoteApi`:
```js
duplicateNote(path) {
  return api.post(`/duplicate/${path}`)
}
```

### A.3 Frontend local API (`localApi.js`, IndexedDB)

Add to the `localApi` object:
```js
async duplicateNote(path) {
  const src = await getNoteRec(path)
  if (!src) return apiError(404, 'File not found')
  const dir = dirname(path)
  const base = noteName(path) // basename without .md
  let filename = `${base} (copy).md`
  let i = 2
  while (await getNoteRec(dir ? dir + '/' + filename : filename)) {
    filename = `${base} (copy ${i}).md`; i++
  }
  const newPath = dir ? dir + '/' + filename : filename
  const now = Date.now()
  const rec = { path: newPath, content: src.content || '', tags: plainTags(src.tags), modTime: now, created: now }
  await write((notes, folders) => { notes.put(rec); ensureFolders(folders, dir) })
  return { data: toFull(rec) }
}
```
`plainTags` strips reactivity so the structured clone into IndexedDB succeeds (existing pattern).

### A.4 Tests (`localApi.test.js`)

Add a `duplicateNote` suite:
- create a note with content + tags in a folder → duplicate → assert a second record exists with `(copy)` suffix, same content and tags, same folder.
- duplicate the original a second time → new record uses `(copy 2)`.
- duplicate a non-existent path → rejects with status 404.

### A.5 Context menu (frontend)

In `ContextMenu.vue` (extracted in Part B, or in `MainView` until then), insert after "Copy Full Text":
```html
<div class="context-menu-item" @click="menuDuplicateNote">
  <span class="material-icons-outlined">file_copy</span> Duplicate
</div>
```
`useContextMenu` gains:
```js
async function menuDuplicateNote() {
  const note = contextMenu.note
  closeContextMenu()
  if (!note) return
  try {
    await apiClient.duplicateNote(note.path)
    await loadAll()      // refresh; current view/selection unchanged
  } catch (e) { alert('Duplicate failed') }
}
```

---

## Part B — MainView split (hybrid)

### B.1 Target file tree

```
frontend/src/
├─ views/MainView.vue        shell: wires composables, hosts child components, lifecycle
├─ components/
│  ├─ AppSidebar.vue         left sidebar (wraps existing FolderNode.vue)
│  ├─ EditorPane.vue         title(auto-width)/tags/toolbar + MilkdownEditor/raw textarea
│  ├─ NoteCard.vue           one card (shared by search grid and notes grid)
│  ├─ ContextMenu.vue        right-click menu
│  ├─ FolderNode.vue         (exists, unchanged)
│  ├─ MilkdownEditor.vue     (exists, unchanged)
│  └─ modals/
│     ├─ ConfirmDialog.vue
│     ├─ PromptDialog.vue
│     ├─ CopyDialog.vue
│     └─ FolderPicker.vue
└─ composables/
   ├─ useAppInit.js
   ├─ useNotes.js
   ├─ useEditor.js
   ├─ useAutosave.js
   ├─ useFileImport.js
   ├─ useCardLayout.js
   ├─ useNoteHistory.js
   ├─ useDragDrop.js
   ├─ useFolderOps.js
   ├─ useDialogs.js
   └─ useContextMenu.js
```

### B.2 Composables — responsibility & surface

Each composable is a `useXxx(deps)` function returning the refs/computed/methods its consumers need. **Surfaces are designed to be name-disjoint** so the shell can destructure them into setup scope without collisions, keeping template binding names (`editingNote`, `loadAll`, `contextMenu`, …) largely unchanged.

| Composable | Owns | Key deps |
|---|---|---|
| `useAppInit` | `isWailsApp`, `isLocalBuild`, `wailsDataDir`, `serverNoAuth`, `mobileSidebar`, `openSections`, `toggleSection`, `initWails`, `changeDataDir`, `doLogout` | — |
| `useNotes` | data layer: `allNotes`, `folders`, `currentFolder`, `searchOpen`, `searchResults`, `searchQuery`, `searchTag`, `displayNotes`, `sortedDisplayNotes`, `sortMode`, `sortMenuOpen`, `setSort`, `loadAll`, `doSearch`, `selectFolder`, `handleAllClick`, `openSearchPanel`, `flatFolders`, `flatFoldersForPicker`, `enrichNotes`, `cardText`, `stripMarkdown`, `isTimestampName` | none |
| `useEditor` | `editingNote`, `editName`, `editTags`, `editFolder`, `editContent`, `tagInput`, `editorKey`, `isDirty`, `editorMode`, `toggleEditorMode`, `onEditorUpdate`, `addTag`, `saveNote`, `deleteCurrentNote`, `openNote`, `newNote`, `createNewNoteIn`, `_forceNewNote`, `confirmLeave`, `pickEditFolder`, title auto-width (`titleMirrorRef`, `titleInputWidth`, `updateTitleInputWidth`, `focusTitleInput`, `titleInputRef`) | `notes` (loadAll after save/delete) |
| `useAutosave` | `showDraftRestoredBanner`, `scheduleAutosave`, `runAutosave`, `persistDraftToLocalStorage`, `flushSaveOrFallback`, `handleBeforeUnload/VisibilityChange/PageHide`; registers/unregisters window listeners in onMounted/onBeforeUnmount | `editor` |
| `useFileImport` | `uploadingFiles`, `isFileDragOver`, `fileInputRef`, `triggerFileInput`, `onFileInputChange`, `onMainDragEnter/Leave/Over/Drop`, `uploadFiles` | `notes` |
| `useCardLayout` | `expandedCards`, `fullContentCache`, `overlongStates`, `cardHeights`, `columnCount`, `updateColumnCount`, `toggleExpand`, `estimateHeight`, `splitIntoColumns`, `observeMeasure`, `disconnectMeasure`, directives `vCheckOverflow` / `vMeasureCard` | `notes` |
| `useNoteHistory` | `prevView`, `hasPrevPage`, `goBack`, `updateUrl`, `restoreFromUrl`, `handleGlobalKeydown` | `router`, `route`, `notes`, `editor` |
| `useDragDrop` | `rootDropOver`, `hoveredNotePath`, `onNoteDragStart`, `onDropNote`, `onDropFolder`, `onDropOnRoot` | `notes` |
| `useFolderOps` | `promptNewFolder`, `promptRenameFolder`, `doDeleteFolder` | `notes`, `dialogs` |
| `useDialogs` | `confirmDialog` + `showConfirm/acceptConfirm/cancelConfirm`; `promptVisible/promptTitle/promptValue` + `showPrompt/submitPrompt/cancelPrompt`; `copyDialog` + `copyFromDialog`; `folderPicker` + `showFolderPicker/closeFolderPicker/confirmFolderPicker/startCreateFolderInPicker/cancelNewFolderInPicker/submitNewFolderInPicker` (Promise-based async dialogs) | `notes` (for `flatFoldersForPicker`) |
| `useContextMenu` | `contextMenu` state, `openContextMenuBtn`, `closeContextMenu`, `menuEditNote`, `menuCopyContent`, `menuDeleteNote`, `menuDownloadNote`, `menuMoveNote`, **`menuDuplicateNote`** | `editor`, `notes`, `dialogs` |

### B.3 Shell wiring (dependency injection)

```js
const app      = useAppInit()
const notes    = useNotes()
const dialogs  = useDialogs({ notes })     // needs flatFoldersForPicker from notes
const editor   = useEditor({ notes })
const layout   = useCardLayout({ notes })
const history  = useNoteHistory({ router, route, notes, editor })
const autosave = useAutosave({ editor })
const files    = useFileImport({ notes })
const dnd      = useDragDrop({ notes })
const folders  = useFolderOps({ notes, dialogs })
const menu     = useContextMenu({ editor, notes, dialogs })
```
This ordering makes the dependency direction unambiguous: `useDialogs` depends on `useNotes` (for `flatFoldersForPicker`), never the reverse — so there is no `notes ↔ dialogs` cycle. `useNotes` therefore exposes no dialog state and does not take `dialogs` as a dep. (Final emit/param names are settled at extraction time.)

### B.4 Child components — props / emits

- **`ContextMenu.vue`** — props: `visible`, `x`, `y`, `note`. emits: `edit`, `copyText`, `download`, `move`, `duplicate`, `delete`, `close`. Scoped `.context-menu*` CSS moves here.
- **`modals/ConfirmDialog.vue`** — prop: the shared reactive `confirmDialog` object; emits `accept`, `cancel`. (Single source of truth: pass the reactive object; child mutates visibility / calls emit.)
- **`modals/PromptDialog.vue`** — props: `visible`, `title`, `value` (v-model `value`); emits `submit`, `cancel`.
- **`modals/CopyDialog.vue`** — prop: shared reactive `copyDialog`; emits `copy`, `close`.
- **`modals/FolderPicker.vue`** — prop: shared reactive `folderPicker` + `flatFoldersForPicker` list; emits `confirm`, `cancel`, `newFolder`/`submitNewFolder`/`cancelNewFolder`.
- **`NoteCard.vue`** — props: `note`, `expanded`, `overlong`, `hovered`; custom directives (`v-check-overflow`, `v-measure-card`) applied here from `useCardLayout`; emits `open`, `menu`, `toggleExpand`, `dragstart`. **Unifies the two identical card blocks** (search grid lines 219–251 and notes grid 277–309), removing ~30 lines of duplication.
- **`AppSidebar.vue`** — props: `folders`, `currentFolder`, `openSections`, `searchOpen`, `isWailsApp`, `wailsDataDir`, `serverNoAuth`, `uploadingFiles`; emits: `newNote`, `openSearch`, `allClick`, `selectFolder`, `createNoteIn`, `newFolder`, `import`, `changeDataDir`, `logout`, `toggleSection`, plus drag-drop emits (`dropNote`, `dropFolder`) bridging to `FolderNode`. Highest fan-out; extracted last among the lower-coupling pieces.
- **`EditorPane.vue`** — props: editor state (`editingNote`, `editName`, `editTags`, `editFolder`, `isDirty`, `editorMode`, `editorKey`, sort state); emits the editor actions (`update:editName`, `addTag`, `removeTag`, `pickFolder`, `save`, `delete`, `toggleMode`, `createNew`, `setSort`, `back`, `editorUpdate`). Title auto-width logic lives inside this component.

### B.5 Guiding principle: minimize template churn

The refactor is staged so that **logic extraction (composables) happens before template extraction (components)**. During the composable stage the template is intentionally left untouched: each composable's return is destructured into setup scope with disjoint names, so existing bindings keep resolving. Template rewriting is isolated to the component-extraction stage, one region at a time.

---

## Part C — Migration & sequencing

Each step is independently build-verifiable and behavior-preserving.

1. **Duplicate first (Part A)** — small, self-contained, ships a user-visible feature immediately and pre-places the new menu item before the menu is extracted into `ContextMenu.vue`. Build + `localApi.test.js`.
2. **Composables, no template changes** — extract in low-coupling-first order: `useDialogs` → `useCardLayout` → `useAutosave` → `useFileImport` → `useDragDrop` → `useNoteHistory` → `useAppInit` → `useFolderOps` → `useEditor` → `useNotes` → `useContextMenu`. `npm run build` after each. Target: `<script setup>` ~1200 → ~400 lines.
3. **Components + scoped CSS** — extract in coupling order: `ContextMenu` + `modals/*` → `NoteCard` (dedupe the two card blocks) → `AppSidebar` → `EditorPane`, moving each region's scoped CSS into the child. `npm run build` after each.
4. **Finish** — audit the shell, remove dead imports, final `npm run build` + `go build` + `go vet ./...`.

---

## Part D — Risk & verification

- **No test suite.** Verification = `npm run build` (frontend compiles), `go build` + `go vet ./...` (backend), `localApi.test.js` (local backend), plus a manual smoke matrix: create / edit / save / autosave-draft / delete / move / **duplicate** / search / drag-drop into folder & root / `.md` import / folder create-rename-delete / mobile sidebar / login-logout.
- **Top risk: behavior regression** (missed ref exposure in destructuring, dropped emit wiring, CSS selectors that relied on non-scoped/parent scope breaking across component boundaries). Mitigations: incremental steps with a build after each; the composable stage deliberately avoids template edits; component CSS moves region-by-region so breakage is localized and visible.
- **Circular-dependency watch:** `useNotes` and `useDialogs` both touch folder data (`flatFoldersForPicker`). The plan resolves this by a single direction (e.g. `useDialogs` depends on `useNotes`, never the reverse).

---

## Open items resolved during planning (not blocking this spec)

- `useDialogs` takes `notes` as its dep (decided) and reads `flatFoldersForPicker` from it.
- Whether `hoveredNotePath`/`rootDropOver` live in `useDragDrop` or `useCardLayout` (currently listed under `useDragDrop`).
- Final emit naming for `AppSidebar` / `EditorPane` (mechanical, settled at extraction time).

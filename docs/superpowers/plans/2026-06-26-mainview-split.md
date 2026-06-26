# MainView Split Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Split the 2636-line `frontend/src/views/MainView.vue` into a small shell plus focused child components and composables, with zero behavior change.

**Architecture:** Hybrid split. (A) Extract `<script setup>` logic into ~11 `use*` composables under `frontend/src/composables/`, wiring them via dependency injection; the shell destructures each composable's return into setup scope so template bindings are unchanged. (B) Extract template regions into child components under `frontend/src/components/` (and `components/modals/`), moving each region's scoped CSS with it. Each step is independently build-verified.

**Tech Stack:** Vue 3 `<script setup>` (refs/computed/composables, provide/inject, custom directives), Vite, vue-router. No component test framework — verification is `npm run build` (compile) per step plus a manual smoke at the end.

## Global Constraints

- All commands run from `/home/cc/workspace/MemoDump`; frontend commands from `frontend/`.
- **No test suite** (CLAUDE.md). The verification gate for every task in this plan is `npm run build` (from `frontend/`) — it must exit 0. Do not invent test commands.
- `frontend/dist` exists; rebuild it (`npm run build`) before any `go build` that embeds the SPA.
- **Behavior is frozen.** No logic, markup semantics, or styling may change. This is a pure relocation refactor. After every task the app must work identically.
- **The core technique — verbatim moves via dep-destructuring.** Each composable is `export function useXxx(deps) { const { …bare names… } = <depObject>; /* moved code */ ; return { … } }`. By destructuring each dependency object into the *same bare names* the relocated code already references, function bodies move **verbatim** (no edits to their internals). Build is the safety net: if `npm run build` reports an undefined identifier inside a composable, that identifier is a cross-cluster reference — add it to that composable's top destructure line, pulling it from the matching dep object (and if that composable's parameter list does not yet include that dep object, add it — e.g. change `useAppInit()` to `useAppInit({ notes })` and pass `{ notes }` at the call site). Fix and rebuild; do not rewrite the function body.
- **Naming-map (avoid collisions when instantiating composables in the shell).** The shell holds composable *instances* as consts; their names must not collide with the ref/method names destructured into scope. Use exactly these instance names: `app`, `notes`, `dialogs`, `layout`, `editor`, `autosave`, `files`, `dnd`, `folderOps` (NOT `folders` — that name is the notes ref), `history`, `menu`.
- Symbols are referenced **by name** in this plan (line numbers shift after every task). Locate them with the editor/grep, not by stale line numbers.
- Every task ends with a commit. Commit messages end with a blank line then `Co-Authored-By: Claude <noreply@anthropic.com>`.

### Standard shell-edit pattern for every Phase A task

For each composable, the shell edit is the same three edits:
1. Add an `import { useXxx } from '../composables/useXxx'` to the imports.
2. Replace the cluster's inline declarations/functions with one line: `const xxx = useXxx({ …deps… })`.
3. Add a destructure line `const { …all names this cluster exposed… } = xxx` so the template and any still-inline code keep resolving the bare names.

Then `npm run build`; commit.

---

## Phase A — Composable extraction (logic only; template untouched)

Extract in this topological order so each composable's deps are already composables when it is extracted. The template is deliberately left untouched in Phase A — only the `<script setup>` changes.

### Task A1: `useNotes` (data layer, no deps)

**Files:**
- Create: `frontend/src/composables/useNotes.js`
- Modify: `frontend/src/views/MainView.vue` (`<script setup>`)

**Interfaces:**
- Produces (return these names): `allNotes`, `folders`, `searchOpen`, `searchResults`, `searchQuery`, `searchTag`, `currentFolder`, `displayNotes`, `sortedDisplayNotes`, `sortMode`, `sortMenuOpen`, `setSort`, `loadAll`, `doSearch`, `selectFolder`, `handleAllClick`, `openSearchPanel`, `flatFolders`, `flatFoldersForPicker`, `enrichNotes`, `cardText`, `stripMarkdown`, `isTimestampName`.

- [ ] **Step 1: Create the composable file**

Create `frontend/src/composables/useNotes.js`:

```js
import { ref, computed } from 'vue'
import apiClient from '../api'

export function useNotes() {
  // ---- moved verbatim from MainView <script setup> ----
  // Move these declarations and functions here unchanged:
  //   refs: allNotes, folders, searchResults, searchQuery, searchTag, currentFolder,
  //         searchOpen, displayNotes, sortMenuOpen, sortMode
  //   computeds: sortedDisplayNotes, flatFolders, flatFoldersForPicker
  //   functions: setSort, cardText, stripMarkdown, isTimestampName, enrichNotes,
  //              loadAll, doSearch, selectFolder, handleAllClick, openSearchPanel
  // (These reference only each other and apiClient, so they move verbatim.)

  return {
    allNotes, folders, searchOpen, searchResults, searchQuery, searchTag,
    currentFolder, displayNotes, sortedDisplayNotes, sortMode, sortMenuOpen,
    setSort, loadAll, doSearch, selectFolder, handleAllClick, openSearchPanel,
    flatFolders, flatFoldersForPicker, enrichNotes, cardText, stripMarkdown, isTimestampName,
  }
}
```

Paste the actual declarations/functions (found by name in `MainView.vue`) in place of the comment. Keep them byte-identical.

- [ ] **Step 2: Rewire the shell**

In `MainView.vue` `<script setup>`: add `import { useNotes } from '../composables/useNotes'`. Delete the inline declarations/functions listed above. Add:
```js
const notes = useNotes()
const { allNotes, folders, searchOpen, searchResults, searchQuery, searchTag, currentFolder, displayNotes, sortedDisplayNotes, sortMode, sortMenuOpen, setSort, loadAll, doSearch, selectFolder, handleAllClick, openSearchPanel, flatFolders, flatFoldersForPicker, enrichNotes, cardText, stripMarkdown, isTimestampName } = notes
```

- [ ] **Step 3: Build**

Run (from `frontend/`): `npm run build`. Expected: exit 0. (If a name is undefined inside `useNotes.js`, it is a cross-cluster ref — but `useNotes` has no deps, so any failure means you accidentally moved something that belongs elsewhere; restore it to the shell.)

- [ ] **Step 4: Commit**

```bash
git add frontend/src/composables/useNotes.js frontend/src/views/MainView.vue
git commit -m "refactor: extract useNotes composable from MainView

Co-Authored-By: Claude <noreply@anthropic.com>"
```

### Task A2: `useAppInit` (wails/local detection, sidebar/section UI, logout — no deps)

**Files:** Create `frontend/src/composables/useAppInit.js`; modify `MainView.vue`.

**Interfaces — produces:** `isWailsApp`, `isLocalBuild`, `wailsDataDir`, `serverNoAuth`, `mobileSidebar`, `openSections`, `toggleSection`, `initWails`, `changeDataDir`, `doLogout`.

- [ ] **Step 1: Create the composable**

```js
import { ref, reactive } from 'vue'
import apiClient from '../api'
import { useRouter } from 'vue-router'

export function useAppInit() {
  const router = useRouter()
  // ---- moved verbatim ----
  // consts: isWailsApp, isLocalBuild
  // refs: wailsDataDir, serverNoAuth, mobileSidebar
  // reactive: openSections
  // functions: initWails, changeDataDir, toggleSection, doLogout
  return { isWailsApp, isLocalBuild, wailsDataDir, serverNoAuth, mobileSidebar, openSections, toggleSection, initWails, changeDataDir, doLogout }
}
```
(`doLogout` calls `apiClient.logout()` then `router` — both available here. If `doLogout` references other cluster names, add them to a destructure; it has no deps, so build will tell you.)

- [ ] **Step 2: Rewire shell** — `import { useAppInit } from '../composables/useAppInit'`; delete inline; add `const app = useAppInit()` + `const { isWailsApp, isLocalBuild, wailsDataDir, serverNoAuth, mobileSidebar, openSections, toggleSection, initWails, changeDataDir, doLogout } = app`.

- [ ] **Step 3: Build** — `npm run build` (exit 0).

- [ ] **Step 4: Commit** — `git add …` message `"refactor: extract useAppInit composable from MainView"`.

### Task A3: `useDialogs` (prompt/confirm/copy/folder-picker — no deps)

**Files:** Create `frontend/src/composables/useDialogs.js`; modify `MainView.vue`.

**Interfaces — produces:** `confirmDialog`, `showConfirm`, `acceptConfirm`, `cancelConfirm`, `promptVisible`, `promptTitle`, `promptValue`, `promptInputRef`, `showPrompt`, `submitPrompt`, `cancelPrompt`, `copyDialog`, `copyDialogTextarea`, `copyFromDialog`, `folderPicker`, `showFolderPicker`, `closeFolderPicker`, `confirmFolderPicker`, `startCreateFolderInPicker`, `cancelNewFolderInPicker`, `submitNewFolderInPicker`, `newFolderInputRef`. (Also move the `folderPickerResolve` Promise resolver closure.)

Note: `flatFoldersForPicker` is rendered by the picker template but lives in `useNotes` and is destructured in the shell — `useDialogs` does **not** need it.

- [ ] **Step 1: Create composable** — wrapper `export function useDialogs() { …moved verbatim… ; return { …above names… } }`. This cluster is self-contained (Promise-based resolvers + reactive state).

- [ ] **Step 2: Rewire shell** — import; delete inline; `const dialogs = useDialogs()` + full destructure of the names above.

- [ ] **Step 3: Build** — `npm run build` (exit 0).

- [ ] **Step 4: Commit** — `"refactor: extract useDialogs composable from MainView"`.

### Task A4: `useCardLayout` (columns, measure directives — no deps)

**Files:** Create `frontend/src/composables/useCardLayout.js`; modify `MainView.vue`.

**Interfaces — produces:** `expandedCards`, `fullContentCache`, `overlongStates`, `cardHeights`, `columnCount`, `updateColumnCount`, `toggleExpand`, `estimateHeight`, `splitIntoColumns`, `observeMeasure`, `disconnectMeasure`, `vCheckOverflow`, `vMeasureCard`. The two custom directives (`v-check-overflow`, `v-measure-card`) must be exported under the names `vCheckOverflow` and `vMeasureCard` (Vue maps `v-measure-card` ↔ local `vMeasureCard`).

- [ ] **Step 1: Create composable** — `export function useCardLayout() { …moved verbatim… ; return { …above names… } }`. These operate on arguments / internal state only (no cross-cluster deps).

- [ ] **Step 2: Rewire shell** — import; delete inline; `const layout = useCardLayout()` + full destructure. **Important:** also add `provide('layout', layout)` in the shell (needed by `NoteCard.vue` in Phase B). Add `provide` to the existing `vue` import.

- [ ] **Step 3: Build** — `npm run build` (exit 0).

- [ ] **Step 4: Commit** — `"refactor: extract useCardLayout composable from MainView"`.

### Task A5: `useEditor` (editor state + note CRUD — deps: `notes`)

**Files:** Create `frontend/src/composables/useEditor.js`; modify `MainView.vue`.

**Interfaces — produces:** `editingNote`, `editName`, `editTags`, `editFolder`, `editContent`, `tagInput`, `editorKey`, `isDirty`, `editorMode`, `toggleEditorMode`, `onEditorUpdate`, `addTag`, `saveNote`, `deleteCurrentNote`, `openNote`, `newNote`, `createNewNoteIn`, `_forceNewNote`, `confirmLeave`, `pickEditFolder`, `titleInputRef`, `titleMirrorRef`, `titleInputWidth`, `updateTitleInputWidth`, `focusTitleInput`. Also move the `watch(editName, …)` and the `titleAutoWidth` mirror logic.

- [ ] **Step 1: Create composable**

```js
import { ref, watch, nextTick } from 'vue'
import apiClient from '../api'

export function useEditor({ notes }) {
  const { loadAll } = notes          // destructure cross-cluster deps into bare names
  // ---- moved verbatim ----
  // refs: editingNote, editName, editTags, editFolder, editContent, tagInput,
  //       editorKey, isDirty, editorMode, titleInputRef, titleMirrorRef, titleInputWidth
  // watch: watch(editName, () => nextTick(updateTitleInputWidth), { immediate: true })
  // functions: toggleEditorMode, updateTitleInputWidth, focusTitleInput, onEditorUpdate,
  //            addTag, saveNote, deleteCurrentNote, openNote, newNote, createNewNoteIn,
  //            _forceNewNote, confirmLeave, pickEditFolder
  return { editingNote, editName, editTags, editFolder, editContent, tagInput, editorKey, isDirty, editorMode, toggleEditorMode, onEditorUpdate, addTag, saveNote, deleteCurrentNote, openNote, newNote, createNewNoteIn, _forceNewNote, confirmLeave, pickEditFolder, titleInputRef, titleMirrorRef, titleInputWidth, updateTitleInputWidth, focusTitleInput }
}
```

- [ ] **Step 2: Rewire shell** — import; delete inline; `const editor = useEditor({ notes })` + full destructure of the names above.

- [ ] **Step 3: Build** — `npm run build`. If build flags an undefined name inside `useEditor.js` (e.g. `currentFolder`, `searchOpen`, `displayNotes`), it is a `notes` dep — add it to the `const { … } = notes` line. Rebuild until exit 0.

- [ ] **Step 4: Commit** — `"refactor: extract useEditor composable from MainView"`.

### Task A6: `useAutosave` (draft + window listeners — deps: `editor`)

**Files:** Create `frontend/src/composables/useAutosave.js`; modify `MainView.vue`.

**Interfaces — produces:** `showDraftRestoredBanner`, `scheduleAutosave`, `runAutosave`, `persistDraftToLocalStorage`, `flushSaveOrFallback`, `handleBeforeUnload`, `handleVisibilityChange`, `handlePageHide`.

- [ ] **Step 1: Create composable**

```js
import { ref, onMounted, onBeforeUnmount } from 'vue'

export function useAutosave({ editor }) {
  const { editingNote, isDirty, editContent, editName, saveNote } = editor
  const showDraftRestoredBanner = ref(false)
  // ---- moved verbatim: scheduleAutosave, runAutosave, persistDraftToLocalStorage,
  //                      flushSaveOrFallback, handleBeforeUnload, handleVisibilityChange, handlePageHide
  // Register the window listeners HERE (moved out of MainView's onMounted):
  onMounted(() => {
    window.addEventListener('beforeunload', handleBeforeUnload)
    document.addEventListener('visibilitychange', handleVisibilityChange)
    window.addEventListener('pagehide', handlePageHide)
  })
  onBeforeUnmount(() => {
    window.removeEventListener('beforeunload', handleBeforeUnload)
    document.removeEventListener('visibilitychange', handleVisibilityChange)
    window.removeEventListener('pagehide', handlePageHide)
  })
  return { showDraftRestoredBanner, scheduleAutosave, runAutosave, persistDraftToLocalStorage, flushSaveOrFallback, handleBeforeUnload, handleVisibilityChange, handlePageHide }
}
```
(Match the exact listener names/flags the original `onMounted`/`onBeforeUnmount` used — find them in `MainView` and relocate verbatim. If autosave reads `editName`/`editTags`/etc., add them to the editor destructure.)

- [ ] **Step 2: Rewire shell** — import; delete the moved functions AND the corresponding `addEventListener`/`removeEventListener` lines from the shell's own `onMounted`/`onBeforeUnmount` (they now live in the composable); `const autosave = useAutosave({ editor })` + destructure `showDraftRestoredBanner` (the only name the template uses).

- [ ] **Step 3: Build** — `npm run build` (exit 0).

- [ ] **Step 4: Commit** — `"refactor: extract useAutosave composable from MainView"`.

### Task A7: `useFileImport` (drag-drop import — deps: `notes`)

**Files:** Create `frontend/src/composables/useFileImport.js`; modify `MainView.vue`.

**Interfaces — produces:** `uploadingFiles`, `isFileDragOver`, `fileInputRef`, `triggerFileInput`, `onFileInputChange`, `onMainDragEnter`, `onMainDragLeave`, `onMainDragOver`, `onMainDrop`, `uploadFiles`.

- [ ] **Step 1: Create composable** — `export function useFileImport({ notes }) { const { loadAll, currentFolder } = notes; …moved verbatim… ; return { … } }`. (`uploadFiles` calls `loadAll` and may import into `currentFolder`; add either if build complains.)

- [ ] **Step 2: Rewire shell** — import; delete inline; `const files = useFileImport({ notes })` + full destructure.

- [ ] **Step 3: Build** — `npm run build` (exit 0).

- [ ] **Step 4: Commit** — `"refactor: extract useFileImport composable from MainView"`.

### Task A8: `useDragDrop` (card drag + drop into folder/root — deps: `notes`)

**Files:** Create `frontend/src/composables/useDragDrop.js`; modify `MainView.vue`.

**Interfaces — produces:** `rootDropOver`, `hoveredNotePath`, `onNoteDragStart`, `onDropNote`, `onDropFolder`, `onDropOnRoot`.

- [ ] **Step 1: Create composable** — `export function useDragDrop({ notes }) { const { loadAll } = notes; …moved verbatim… ; return { … } }`.

- [ ] **Step 2: Rewire shell** — import; delete inline; `const dnd = useDragDrop({ notes })` + full destructure. Also add `provide('dnd', dnd)` (needed by `NoteCard.vue` / `AppSidebar.vue` in Phase B).

- [ ] **Step 3: Build** — `npm run build` (exit 0).

- [ ] **Step 4: Commit** — `"refactor: extract useDragDrop composable from MainView"`.

### Task A9: `useFolderOps` (folder create/rename/delete — deps: `notes`, `dialogs`)

**Files:** Create `frontend/src/composables/useFolderOps.js`; modify `MainView.vue`.

**Interfaces — produces:** `promptNewFolder`, `promptRenameFolder`, `doDeleteFolder`.

- [ ] **Step 1: Create composable**

```js
import apiClient from '../api'

export function useFolderOps({ notes, dialogs }) {
  const { loadAll } = notes
  const { showPrompt, showConfirm } = dialogs
  // ---- moved verbatim: promptNewFolder, promptRenameFolder, doDeleteFolder
  return { promptNewFolder, promptRenameFolder, doDeleteFolder }
}
```

- [ ] **Step 2: Rewire shell** — import; delete inline; `const folderOps = useFolderOps({ notes, dialogs })` + `const { promptNewFolder, promptRenameFolder, doDeleteFolder } = folderOps`.

- [ ] **Step 3: Build** — `npm run build` (exit 0).

- [ ] **Step 4: Commit** — `"refactor: extract useFolderOps composable from MainView"`.

### Task A10: `useNoteHistory` (URL sync, back, global keydown — deps: `notes`, `editor`)

**Files:** Create `frontend/src/composables/useNoteHistory.js`; modify `MainView.vue`.

**Interfaces — produces:** `prevView`, `hasPrevPage`, `goBack`, `updateUrl`, `restoreFromUrl`, `handleGlobalKeydown`.

- [ ] **Step 1: Create composable**

```js
import { reactive, computed } from 'vue'
import { useRouter, useRoute } from 'vue-router'

export function useNoteHistory({ notes, editor }) {
  const router = useRouter()
  const route = useRoute()
  const { currentFolder, searchOpen } = notes
  const { editingNote, openNote } = editor
  // ---- moved verbatim: prevView, hasPrevPage (computed), goBack, updateUrl,
  //                      restoreFromUrl, handleGlobalKeydown
  return { prevView, hasPrevPage, goBack, updateUrl, restoreFromUrl, handleGlobalKeydown }
}
```
(`router`/`route` were obtained in the shell via `useRouter()`/`useRoute()`; the composable obtains its own. `onMounted`/`onBeforeUnmount` that register `popstate`/keydown stay in the shell and call `restoreFromUrl`/`handleGlobalKeydown` — or move them here; keep behavior identical. Prefer leaving shell lifecycle calling these functions.)

- [ ] **Step 2: Rewire shell** — import; delete inline; `const history = useNoteHistory({ notes, editor })` + `const { prevView, hasPrevPage, goBack, updateUrl, restoreFromUrl, handleGlobalKeydown } = history`. Ensure the shell's `onMounted` still calls `restoreFromUrl()` and the keydown listener still calls `handleGlobalKeydown`.

- [ ] **Step 3: Build** — `npm run build`. Add any missing dep names to the destructure lines; rebuild to exit 0.

- [ ] **Step 4: Commit** — `"refactor: extract useNoteHistory composable from MainView"`.

### Task A11: `useContextMenu` (right-click menu + handlers incl. duplicate — deps: `editor`, `notes`, `dialogs`)

**Files:** Create `frontend/src/composables/useContextMenu.js`; modify `MainView.vue`.

**Interfaces — produces:** `contextMenu`, `openContextMenuBtn`, `closeContextMenu`, `menuEditNote`, `menuCopyContent`, `menuDeleteNote`, `menuDownloadNote`, `menuMoveNote`, `menuDuplicateNote`.

- [ ] **Step 1: Create composable**

```js
import { reactive } from 'vue'
import apiClient from '../api'

export function useContextMenu({ editor, notes, dialogs }) {
  const { openNote, editingNote, _forceNewNote } = editor
  const { loadAll } = notes
  const { showConfirm, showFolderPicker, copyDialog } = dialogs
  const contextMenu = reactive({ visible: false, x: 0, y: 0, note: null })
  // ---- moved verbatim: openContextMenuBtn, closeContextMenu, menuEditNote,
  //                      menuCopyContent, menuDeleteNote, menuDownloadNote, menuMoveNote
  // ---- menuDuplicateNote (already added by the duplicate plan; if not present, add):
  async function menuDuplicateNote() {
    const note = contextMenu.note
    closeContextMenu()
    if (!note) return
    try { await apiClient.duplicateNote(note.path); await loadAll() }
    catch (e) { alert('Duplicate failed') }
  }
  return { contextMenu, openContextMenuBtn, closeContextMenu, menuEditNote, menuCopyContent, menuDeleteNote, menuDownloadNote, menuMoveNote, menuDuplicateNote }
}
```

- [ ] **Step 2: Rewire shell** — import; delete inline; `const menu = useContextMenu({ editor, notes, dialogs })` + `const { contextMenu, openContextMenuBtn, closeContextMenu, menuEditNote, menuCopyContent, menuDeleteNote, menuDownloadNote, menuMoveNote, menuDuplicateNote } = menu`.

- [ ] **Step 3: Build** — `npm run build` (exit 0).

- [ ] **Step 4: Commit** — `"refactor: extract useContextMenu composable from MainView"`.

### Task A12: Audit the shell

- [ ] **Step 1:** In `MainView.vue` `<script setup>`, remove now-unused imports (e.g. `useRouter`/`useRoute` if only used by moved code, `reactive`/`computed` if none remain). Keep `onMounted`/`onBeforeUnmount`/`nextTick` only if still used. Verify `provide('layout', layout)` and `provide('dnd', dnd)` are present.
- [ ] **Step 2: Build** — `npm run build` (exit 0).
- [ ] **Step 3: Commit** — `"refactor: trim MainView imports after composable extraction"`.

At this point the `<script setup>` should be ~80–150 lines (imports + composable instantiation + destructuring + a thin `onMounted` calling `initWails`/`loadAll`/`restoreFromUrl`). The template and `<style>` are still large — Phase B addresses them.

---

## Phase B — Component extraction (move template regions + their scoped CSS)

Convention for every component task: create `components/<Name>.vue` with `<template>`, `<script setup>` (props/emits/inject as specified), and `<style scoped>` containing the CSS for the classes used in that region (grepped out of `MainView`'s `<style scoped>`). Replace the region in `MainView` with `<Name :props @emits />`. Move CSS rules by class name — search `MainView`'s `<style scoped>` for each class the region uses and relocate those rules. After each task, `MainView`'s `<style scoped>` shrinks.

Custom-directive/shared-state coupling is handled with **provide/inject**: `layout` and `dnd` are provided in the shell (Tasks A4/A8); `NoteCard`/`AppSidebar` inject them.

### Task B1: `components/ContextMenu.vue`

**Files:** Create `frontend/src/components/ContextMenu.vue`; modify `MainView.vue`.

**Props:** `visible` (Boolean), `x` (Number), `y` (Number). **Emits:** `edit`, `copy`, `download`, `move`, `duplicate`, `delete` (each closes the menu; the parent decides). Also emit `close` from the overlay.

- [ ] **Step 1: Create component** — move the `context-menu-overlay` + `context-menu` block (Edit/Copy Full Text/Duplicate/Download/Move/Delete items) verbatim. Bind each item's `@click` to the matching emit (`@click="$emit('edit')"`, etc.). Keep the `:style="{ top: y+'px', left: x+'px' }"`. The duplicate item uses `$emit('duplicate')`.

- [ ] **Step 2: Move CSS** — relocate `.context-menu`, `.context-menu-overlay`, `.context-menu-item`, `.context-menu-item.text-danger` rules from `MainView`'s `<style>` into the component's `<style scoped>`.

- [ ] **Step 3: Rewire shell** — replace the block with:
```html
<ContextMenu v-if="contextMenu.visible" :visible="contextMenu.visible" :x="contextMenu.x" :y="contextMenu.y"
  @edit="menuEditNote" @copy="menuCopyContent" @download="menuDownloadNote"
  @move="menuMoveNote" @duplicate="menuDuplicateNote" @delete="menuDeleteNote" @close="closeContextMenu" />
```
Import the component.

- [ ] **Step 4: Build** — `npm run build` (exit 0). Smoke: right-click still positions and all six actions still work.
- [ ] **Step 5: Commit** — `"refactor: extract ContextMenu component"`.

### Task B2: `components/modals/ConfirmDialog.vue`, `PromptDialog.vue`, `CopyDialog.vue`, `FolderPicker.vue`

**Files:** Create the four components under `frontend/src/components/modals/`; modify `MainView.vue`.

Each modal renders from the shared reactive state object passed as a prop (single source of truth) and emits the action; the shell wires the emit to the composable method.

- **ConfirmDialog** — prop `model` (the `confirmDialog` reactive: `{ visible, title, message, okLabel, danger }`); emits `accept`, `cancel`. Move `.modal-overlay`, confirm markup, and `.modal*`/`.btn*` rules it uniquely uses.
- **PromptDialog** — props `visible`, `title`, `value` (use `defineModel` or `value`+`update:value`); also a ref for its input focus. emits `submit`, `cancel`.
- **CopyDialog** — prop `model` (the `copyDialog` reactive + the textarea ref via `bind`/expose); emits `copy`, `close`. The `copyFromDialog` focus logic: emit `copy`, parent calls `copyFromDialog`.
- **FolderPicker** — prop `model` (`folderPicker` reactive) and prop `folders` (`flatFoldersForPicker`); emits `confirm`, `cancel`, `startNew`, `submitNew`, `cancelNew`. (It reads the folder list from the `folders` prop, not from a composable.)

- [ ] **Step 1–4 (per modal, one commit each):** create component → move its template region → move its CSS classes → rewire shell to `<ConfirmDialog :model="confirmDialog" @accept="acceptConfirm" @cancel="cancelConfirm" />` etc. → `npm run build` → commit. Four small commits, e.g. `"refactor: extract ConfirmDialog component"`.

### Task B3: `components/NoteCard.vue` (dedupes the two identical card blocks)

**Files:** Create `frontend/src/components/NoteCard.vue` and `frontend/src/utils.js`; modify `MainView.vue` and `frontend/src/composables/useNotes.js`.

**Props:** `note` (Object). **Inject:** `layout` (for `vMeasureCard`, `vCheckOverflow`, `overlongStates`, `expandedCards`, `toggleExpand`) and `dnd` (for `hoveredNotePath`, `onNoteDragStart`). **Emits:** `open`, `menu`.

> Why a `utils.js`: `cardText`/`stripMarkdown` are pure functions of their argument (no reactive state). They must NOT be obtained by calling `useNotes()` inside `NoteCard` (that would create a second, separate instance of the notes state). They must NOT be read from the grid's shared state either. So they become standalone helpers that both `useNotes` and `NoteCard` import directly.

- [ ] **Step 1: Extract pure helpers to `frontend/src/utils.js`**

Create `frontend/src/utils.js` and move `cardText` and `stripMarkdown` there verbatim as named exports:
```js
// Pure helpers shared by useNotes and NoteCard (no reactive state).
export function stripMarkdown(text) { /* moved verbatim */ }
export function cardText(note) { /* moved verbatim (calls stripMarkdown) */ }
```
In `frontend/src/composables/useNotes.js`: remove the inline `cardText`/`stripMarkdown` definitions, add `import { cardText, stripMarkdown } from '../utils'`, and keep both names in the `return` (the shell still destructures `cardText`, and the template still calls `cardText(note)`). Build: `npm run build` (exit 0). Commit later with the component.

- [ ] **Step 2: Create the component**

Create `frontend/src/components/NoteCard.vue`:
```vue
<template>
  <div class="waterfall-card" v-measure-card="note.path"
       :draggable="hoveredNotePath !== note.path" @dragstart="onNoteDragStart($event, note)"
       @click="$emit('open', note)">
    <div class="card-header" v-if="note.hasCustomName">
      <div class="card-name">{{ note.name }}</div>
      <button class="btn btn-icon btn-ghost btn-sm card-menu-btn" @click.stop="$emit('menu', $event, note)">
        <span class="material-icons-outlined">more_vert</span>
      </button>
    </div>
    <button v-else class="btn btn-icon btn-ghost btn-sm card-menu-btn"
            style="position: absolute; top: 12px; right: 14px; margin: 0; z-index: 2"
            @click.stop="$emit('menu', $event, note)">
      <span class="material-icons-outlined">more_vert</span>
    </button>
    <div class="card-preview" draggable="false"
         @mouseenter="hoveredNotePath = note.path" @mouseleave="hoveredNotePath = null"
         @dragstart.stop v-check-overflow="note.path" :class="{ expanded: expandedCards.has(note.path) }">
      <template v-if="cardText(note)">{{ cardText(note) }}</template>
      <span v-else class="card-empty">Empty note</span>
    </div>
    <div class="card-expand-bar" v-if="overlongStates[note.path]" @click.stop="toggleExpand(note.path)">
      <span class="material-icons-outlined">{{ expandedCards.has(note.path) ? 'expand_less' : 'expand_more' }}</span>
    </div>
    <div class="card-footer" v-if="note.tags && note.tags.length">
      <div class="card-tags"><span class="tag" v-for="t in note.tags" :key="t">{{ t }}</span></div>
    </div>
  </div>
</template>
<script setup>
import { inject } from 'vue'
import { cardText } from '../utils'
defineProps({ note: { type: Object, required: true } })
defineEmits(['open', 'menu'])
const layout = inject('layout')
const { vMeasureCard, vCheckOverflow, overlongStates, expandedCards, toggleExpand } = layout
const dnd = inject('dnd')
const { hoveredNotePath, onNoteDragStart } = dnd
</script>
```

- [ ] **Step 3: Move CSS** — relocate `.waterfall-card`, `.card-header`, `.card-name`, `.card-menu-btn`, `.card-preview`, `.card-empty`, `.card-expand-bar`, `.card-footer`, `.card-tags`, `.tag`, and the `.expanded` rules into `NoteCard.vue` `<style scoped>`. (If any of these are also used outside cards, leave a copy in `MainView` — scoped CSS is per-component, duplicates are acceptable.)

- [ ] **Step 4: Rewire shell** — replace BOTH card blocks (the search-grid `waterfall-card` loop and the notes-grid `waterfall-card` loop) with:
```html
<NoteCard v-for="note in col" :key="note.path" :note="note" @open="openNote" @menu="openContextMenuBtn" />
```
inside each `<div class="waterfall-col" v-for="(col, ci) in splitIntoColumns(...)">`. Import `NoteCard`.

- [ ] **Step 5: Build** — `npm run build` (exit 0). Smoke: both the search results grid and the notes grid render identically; expand/menu/open/drag all work.
- [ ] **Step 6: Commit** — `git add frontend/src/utils.js frontend/src/composables/useNotes.js frontend/src/components/NoteCard.vue frontend/src/views/MainView.vue` — `"refactor: extract NoteCard component + pure cardText helpers (dedupe card blocks)"`.

### Task B4: `components/AppSidebar.vue`

**Files:** Create `frontend/src/components/AppSidebar.vue`; modify `MainView.vue`.

**Props:** `folders` (the tree from `flatFolders`), `currentFolder`, `openSections`, `searchOpen`, `isWailsApp`, `wailsDataDir`, `serverNoAuth`, `uploadingFiles`. **Inject:** `dnd` (for `onDropNote`, `onDropFolder`, `onDropOnRoot`, `rootDropOver`), and `layout` if it uses `columnCount` (it does not — omit). **Emits:** `new`, `openSearch`, `all`, `selectFolder` (`(path)`), `createNoteIn` (`(folder)`), `newFolder` (`(parent)`), `import` (trigger file input), `changeDataDir`, `logout`, `toggleSection` (`(section)`), and the drag-drop emits bridging to `FolderNode`.

- [ ] **Step 1: Create component** — move the entire `<aside>` sidebar block verbatim, rebinding handler calls to emits (e.g. `@click="newNote"` → `@click="$emit('new')"`; `@select-folder="selectFolder"` stays but `selectFolder` is now a prop or emit — bridge `FolderNode`'s `@select-folder`/`@drop-note`/`@drop-folder` to emits). The sidebar's own buttons (New Note, New Folder, Import, storage header actions, wails/logout) emit the actions above.

- [ ] **Step 2: Move CSS** — relocate all sidebar-related rules (`.sidebar`, `.sidebar-action`, `.nav-item`, `.storage-*`, `.fa-btn-sm`, `.sidebar-overlay`, etc.) into the component.

- [ ] **Step 3: Rewire shell** — `<AppSidebar :folders="flatFolders" :current-folder="currentFolder" :open-sections="openSections" :search-open="searchOpen" :is-wails-app="isWailsApp" :wails-data-dir="wailsDataDir" :server-no-auth="serverNoAuth" :uploading-files="uploadingFiles" @new="newNote" @open-search="openSearchPanel" @all="handleAllClick" @select-folder="selectFolder" @create-note-in="createNewNoteIn" @new-folder="promptNewFolder" @import="triggerFileInput" @change-data-dir="changeDataDir" @logout="doLogout" @toggle-section="toggleSection" @drop-note="onDropNote" @drop-folder="onDropFolder" @drop-root="onDropOnRoot" />`. Import it. (`flatFolders` is the tree used by the sidebar's `FolderNode` list — confirm by reading the current sidebar template which `v-for`s folders.)

- [ ] **Step 4: Build** — `npm run build` (exit 0). Smoke: folder tree expand/collapse, new note/folder, import, drag-drop into folders and root, wails/logout all work.
- [ ] **Step 5: Commit** — `"refactor: extract AppSidebar component"`.

### Task B5: `components/EditorPane.vue`

**Files:** Create `frontend/src/components/EditorPane.vue`; modify `MainView.vue`.

**Props:** `editingNote`, `editName`, `editTags`, `editFolder`, `editContent`, `isDirty`, `editorMode`, `editorKey`, `sortMode`, `sortMenuOpen`, `hasPrevPage`. Use `defineModel` for two-way bound fields (`editName`, `editContent`, `sortMenuOpen`). **Emits:** `back`, `save`, `delete`, `toggleMode`, `createNew`, `setSort` (`(mode)`), `pickFolder`, `addTag`, `removeTag` (`(i)`), `editorUpdate` (`(markdown)`). Title auto-width (`titleMirrorRef`, `titleInputWidth`, `updateTitleInputWidth`, `focusTitleInput`) lives **inside** this component (move those refs/functions out of `useEditor` back into the component, or keep them in `useEditor` and pass needed pieces — simplest: move the title-width logic into `EditorPane` since it only touches the title input DOM).

- [ ] **Step 1: Create component** — move the `editor-wrap` header + `MilkdownEditor`/`raw-editor` block verbatim. `import MilkdownEditor from './MilkdownEditor.vue'`. Rebind: title input `v-model` to the `editName` model; `@update` on MilkdownEditor to `$emit('editorUpdate', $event)`; buttons to emits; tag remove to `$emit('removeTag', i)`; sort items to `$emit('setSort', 'modified-desc')`. Move the title auto-width logic + its `watch(editName,…)` into the component (operate on a local `editName` model).

- [ ] **Step 2: Move CSS** — relocate `.editor-wrap`, `.raw-editor`, `.note-folder-btn`, `.tag`, `.remove`, `.save-btn`, `.header-sort-btn`, `.sort-menu*`, `.title*`/`.note-title*`, `.header-new-btn`, `.editor-back-btn`, `.btn-toggle*` rules into the component.

- [ ] **Step 3: Rewire shell** — `<EditorPane v-if="editingNote" v-model:edit-name="editName" v-model:edit-content="editContent" v-model:sort-menu-open="sortMenuOpen" :editing-note="editingNote" :edit-tags="editTags" :edit-folder="editFolder" :is-dirty="isDirty" :editor-mode="editorMode" :editor-key="editorKey" :sort-mode="sortMode" :has-prev-page="hasPrevPage" @back="goBack" @save="saveNote" @delete="deleteCurrentNote" @toggle-mode="toggleEditorMode" @create-new="createNewNoteIn(currentFolder)" @set-sort="setSort" @pick-folder="pickEditFolder" @add-tag="addTag" @remove-tag="(i) => { editTags.splice(i,1); isDirty = true }" @editor-update="onEditorUpdate" />`. Import it.

- [ ] **Step 4: Build** — `npm run build` (exit 0). Smoke: edit title/tags/content, toggle raw/WYSIWYG, sort menu, save, delete, back, new note all work.
- [ ] **Step 5: Commit** — `"refactor: extract EditorPane component"`.

---

## Phase C — Finalize

### Task C1: Audit + final verification

- [ ] **Step 1:** Open `MainView.vue`. Confirm it is a shell: imports + composable instantiation + destructuring + `provide('layout'/'dnd')` + a thin `onMounted` (`initWails(); loadAll(); restoreFromUrl()` and any remaining listener registrations not moved into composables) + `<template>` referencing the child components + a much smaller `<style scoped>` holding only shell-level classes (layout, `.main`, `.app`, modals-overlay leftovers, etc.).
- [ ] **Step 2:** Remove any CSS rules left in `MainView` that are now unused (grep each remaining class against the template; drop orphans).
- [ ] **Step 3: Build** — from `frontend/`: `npm run build` (exit 0). Then from repo root: `go build .` and `go vet ./...` (exit 0). Then `npm test` (the localApi duplicate tests still pass).
- [ ] **Step 4: Full manual smoke** — build & run (`go build -o /tmp/memodump . && /tmp/memodump --data /tmp/md-refactor`) and walk the whole matrix: create / edit / save / autosave-restore / delete / move / duplicate / search / drag into folder & root / `.md` import / folder create-rename-delete / mobile sidebar / login-logout. Every behavior must match pre-refactor.
- [ ] **Step 5: Commit** — `"refactor: finalize MainView shell after component extraction"`.

---

## Definition of Done

- `frontend/src/views/MainView.vue` is a shell (~300–400 lines): no inline logic clusters, template composed of child components, only shell-level CSS remains.
- `frontend/src/composables/` holds the 11 composables; `frontend/src/components/` holds `AppSidebar`, `EditorPane`, `NoteCard`, `ContextMenu`, and `modals/{ConfirmDialog,PromptDialog,CopyDialog,FolderPicker}`; `frontend/src/utils.js` holds `cardText`/`stripMarkdown`.
- `npm run build`, `go build .`, `go vet ./...`, and `npm test` all pass.
- Full manual smoke matches pre-refactor behavior exactly.

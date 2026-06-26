# MainView Split Implementation Plan (Lighter Scope)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans. Steps use checkbox (`- [ ]`) syntax.

> **Revision note.** This replaces the earlier full-hybrid plan. A body-level dependency analysis of `MainView.vue`'s `<script setup>` (the map is committed alongside execution in `.superpowers/sdd/`) showed the notes/editor/history clusters are tightly interwoven by glue functions (`handleAllClick`, `goBack`, `openNote`, `_forceNewNote`, `selectFolder`, `restoreFromUrl`) and cannot be cleanly separated without heavy injection and high regression risk. Per the user's decision, this lighter plan extracts only the **cleanly bounded** composables and presentational components, leaving the core inline. Boundaries below are taken verbatim from the dependency map — not inferred from names.

**Goal:** Substantially shrink `MainView.vue` by extracting its cleanly-bounded logic into composables and its self-contained UI regions into child components, with zero behavior change. The notes/editor/history core stays in `MainView`.

**Architecture:** Extract 6 composables (`useAppInit`, `useCardLayout`, `useDialogs`, `useAutosave`, `useFileImport`, `useContextMenu`) + 2 pure helpers (`utils.js`), and 3 component groups (`ContextMenu`, the 4 modals, `NoteCard`). `MainView` instantiates the composables, passing exact dependency sets, and destructures their returns into setup scope so template bindings are unchanged. The core (notes/editor/history + glue) remains inline in `MainView`.

**Tech Stack:** Vue 3 `<script setup>`, Vite, vue-router. No test framework — verification is `npm run build` + a per-task map-correctness check + a final runtime smoke.

## Global Constraints

- All commands run from `/home/cc/workspace/MemoDump`; frontend commands from `frontend/`.
- **Behavior is frozen.** Pure relocation refactor; no logic/markup/styling change.
- **The map is the spec for dependencies.** For every composable, the task lists the exact set of bindings it moves and the exact set of deps it must receive. Verification rule for each move: every identifier referenced in the moved code is EITHER declared inside the composable OR present in the injected deps. `npm run build` catches syntax/import errors but NOT undefined globals (plain JS, no typecheck) — so this map-correctness check is the real gate for missing-binding regressions.
- **Symbol references are by name** (line numbers shift across tasks). Locate with grep.
- **Destructure into shell scope.** `MainView` instantiates each composable and destructures its return so existing bare-name template bindings keep resolving.
- The notes/editor/history **core stays inline in `MainView`**. Do not attempt to extract `loadAll`, `openNote`, `_forceNewNote`, `confirmLeave`, `updateUrl`, `handleAllClick`, `goBack`, `selectFolder`, `restoreFromUrl`, `handleGlobalKeydown`, `saveNote`, `deleteCurrentNote`, the editor refs, or the notes refs.
- Commit messages end with a blank line then `Co-Authored-By: Claude <noreply@anthropic.com>`.

---

## Task L0: `utils.js` — pure helpers

**Files:** Create `frontend/src/utils.js`; modify `MainView.vue` and (later) `useCardLayout`.

`stripMarkdown` and `isTimestampName` are pure (the map shows `-> (none)`). They are used by both the core (`enrichNotes`, `saveNote`, `openNote`, `_forceNewNote`, `restoreFromUrl`) and `useCardLayout` (`toggleExpand`). Move them to a shared module so neither the core nor the composable duplicates them.

- [ ] Create `frontend/src/utils.js` exporting `stripMarkdown` and `isTimestampName` verbatim (move the function bodies from `MainView`).
- [ ] In `MainView.vue`: `import { stripMarkdown, isTimestampName } from '../utils'`; delete the two inline definitions. (They remain in scope for the core code that uses them.)
- [ ] `npm run build` (exit 0). Commit: `"refactor: extract pure helpers stripMarkdown/isTimestampName to utils.js"`.

## Task L1: `useAppInit` (no deps)

**Moves:** `isWailsApp`, `isLocalBuild`, `wailsDataDir`, `serverNoAuth`, `mobileSidebar`, `openSections`, `toggleSection`, `initWails`, `changeDataDir`, `doLogout`, `keepaliveInterval`. Per the map, the only internal cross-refs are `initWails→isWailsApp,wailsDataDir`, `changeDataDir→isWailsApp,wailsDataDir`, `toggleSection→openSections`. `doLogout` uses `router`/`apiClient` — obtain `router` via `useRouter()` inside the composable; `apiClient` is imported.

- [ ] Create `frontend/src/composables/useAppInit.js`: `export function useAppInit() { const router = useRouter(); <moved verbatim> ; return { isWailsApp, isLocalBuild, wailsDataDir, serverNoAuth, mobileSidebar, openSections, toggleSection, initWails, changeDataDir, doLogout } }`.
- [ ] Shell: import; delete inline; `const app = useAppInit()` + destructure the returned names.
- [ ] `npm run build` (exit 0). Commit: `"refactor: extract useAppInit composable"`.

## Task L2: `useCardLayout` (imports `stripMarkdown` from utils)

**Moves:** `expandedCards`, `fullContentCache`, `overlongStates`, `cardHeights`, `columnCount`, `updateColumnCount`, `toggleExpand`, `estimateHeight`, `splitIntoColumns`, `observeMeasure`, `disconnectMeasure`, `vCheckOverflow`, `vMeasureCard`, `cardText`. Internal cross-refs only EXCEPT `toggleExpand→stripMarkdown` (now imported from `../utils`). `cardText→expandedCards,fullContentCache` (internal). The directives export as `vCheckOverflow`/`vMeasureCard`.

- [ ] Create `frontend/src/composables/useCardLayout.js`: `import { stripMarkdown } from '../utils'`; `export function useCardLayout() { <moved verbatim> ; return { expandedCards, fullContentCache, overlongStates, cardHeights, columnCount, updateColumnCount, toggleExpand, estimateHeight, splitIntoColumns, observeMeasure, disconnectMeasure, vCheckOverflow, vMeasureCard, cardText } }`. Move the resize listener registration for `updateColumnCount` into the composable's `onMounted`/`onBeforeUnmount` (find it in `MainView`'s onMounted and relocate).
- [ ] Shell: import; delete inline; `const layout = useCardLayout()` + destructure ALL returned names. **Add `provide('layout', layout)`** (needed by `NoteCard`).
- [ ] `npm run build` (exit 0). Commit: `"refactor: extract useCardLayout composable"`.

## Task L3: `useDialogs({ folders })`

**Moves:** `confirmDialog`, `confirmResolve`, `showConfirm`, `acceptConfirm`, `cancelConfirm`; `promptVisible`, `promptTitle`, `promptValue`, `promptInputRef`, `promptResolve`, `showPrompt`, `submitPrompt`, `cancelPrompt`; `copyDialog`, `copyDialogTextarea`, `copyFromDialog`; `folderPicker`, `folderPickerResolve`, `newFolderInputRef`, `showFolderPicker`, `closeFolderPicker`, `confirmFolderPicker`, `startCreateFolderInPicker`, `cancelNewFolderInPicker`, `submitNewFolderInPicker`. Only external ref: `submitNewFolderInPicker→folders`. Inject `folders`.

- [ ] Create `frontend/src/composables/useDialogs.js`: `export function useDialogs({ folders }) { <moved verbatim> ; return { confirmDialog, showConfirm, acceptConfirm, cancelConfirm, promptVisible, promptTitle, promptValue, promptInputRef, showPrompt, submitPrompt, cancelPrompt, copyDialog, copyDialogTextarea, copyFromDialog, folderPicker, showFolderPicker, closeFolderPicker, confirmFolderPicker, startCreateFolderInPicker, cancelNewFolderInPicker, submitNewFolderInPicker, newFolderInputRef } }`.
- [ ] Shell: import; delete inline; `const dialogs = useDialogs({ folders })` + destructure all returned names.
- [ ] `npm run build` (exit 0). Commit: `"refactor: extract useDialogs composable"`.

## Task L4: `useAutosave({ editor })`

**Moves:** `autosaveTimer`, `autosaving`, `scheduleAutosave`, `runAutosave`, `persistDraftToLocalStorage`, `flushSaveOrFallback`, `handleBeforeUnload`, `handleVisibilityChange`, `handlePageHide`, `showDraftRestoredBanner`. External refs: `scheduleAutosave→isDirty,editingNote`, `runAutosave→saveNote`, `persistDraftToLocalStorage→editContent,editName,editTags,editFolder,editingNote`, `flushSaveOrFallback→isDirty,editingNote,saveNote`. Inject `{ editingNote, isDirty, editContent, editName, editTags, editFolder, saveNote }`.

- [ ] Create `frontend/src/composables/useAutosave.js`: `export function useAutosave(editor) { const { editingNote, isDirty, editContent, editName, editTags, editFolder, saveNote } = editor; <moved verbatim> ; register beforeunload/visibilitychange/pagehide in onMounted, remove in onBeforeUnmount; return { showDraftRestoredBanner, scheduleAutosave, runAutosave, flushSaveOrFallback } }`. (Only `showDraftRestoredBanner` is used by the template; expose the handlers the shell still needs, if any.)
- [ ] Shell: import; delete inline AND remove the corresponding addEventListener/removeEventListener lines from the shell's onMounted/onBeforeUnmount (they now live in the composable); `const autosave = useAutosave({ editingNote, isDirty, editContent, editName, editTags, editFolder, saveNote })` + destructure `showDraftRestoredBanner` (and any handlers the shell references).
- [ ] `npm run build` (exit 0). Commit: `"refactor: extract useAutosave composable"`.

## Task L5: `useFileImport({ editFolder, currentFolder, loadAll, openNote, editingNote, updateUrl })`

Combines file import + card drag/drop. **Moves:** `rootDropOver`, `hoveredNotePath`, `onNoteDragStart`, `onDropNote`, `onDropFolder`, `onDropOnRoot`, `uploadingFiles`, `isFileDragOver`, `fileDragCounter`, `fileInputRef`, `triggerFileInput`, `onFileInputChange`, `onMainDragEnter`, `onMainDragLeave`, `onMainDragOver`, `onMainDrop`, `uploadFiles`. External refs: `uploadFiles→editFolder,currentFolder,loadAll,openNote`, `onDropNote→loadAll,editingNote,openNote`, `onDropFolder→currentFolder,loadAll,updateUrl`. Inject exactly `{ editFolder, currentFolder, loadAll, openNote, editingNote, updateUrl }`.

- [ ] Create `frontend/src/composables/useFileImport.js`: destructure the 6 deps at top; `<moved verbatim>`; `return { rootDropOver, hoveredNotePath, onNoteDragStart, onDropNote, onDropFolder, onDropOnRoot, uploadingFiles, isFileDragOver, fileInputRef, triggerFileInput, onFileInputChange, onMainDragEnter, onMainDragLeave, onMainDragOver, onMainDrop, uploadFiles }`.
- [ ] Shell: import; delete inline; `const dnd = useFileImport({ editFolder, currentFolder, loadAll, openNote, editingNote, updateUrl })` + destructure all returned names. **Add `provide('dnd', dnd)`** (needed by `NoteCard`/sidebar).
- [ ] `npm run build` (exit 0). Commit: `"refactor: extract useFileImport composable (import + drag/drop)"`.

## Task L6: `useContextMenu({ openNote, isDirty, loadAll, editingNote, _forceNewNote, showConfirm, showFolderPicker, copyDialog })`

**Moves:** `contextMenu`, `openContextMenuBtn`, `closeContextMenu`, `menuEditNote`, `menuCopyContent`, `menuDuplicateNote`, `menuDeleteNote`, `menuDownloadNote`, `menuMoveNote`. External refs (from map): `menuEditNote→openNote`, `menuDeleteNote→showConfirm,isDirty,loadAll,editingNote,_forceNewNote`, `menuMoveNote→showFolderPicker,loadAll,editingNote,openNote`, `menuDuplicateNote→loadAll`, `menuCopyContent→copyDialog`. Inject exactly the 8 named deps.

- [ ] Create `frontend/src/composables/useContextMenu.js`: destructure the 8 deps at top; `<moved verbatim>`; `return { contextMenu, openContextMenuBtn, closeContextMenu, menuEditNote, menuCopyContent, menuDuplicateNote, menuDeleteNote, menuDownloadNote, menuMoveNote }`.
- [ ] Shell: import; delete inline; `const ctxMenu = useContextMenu({ openNote, isDirty, loadAll, editingNote, _forceNewNote, showConfirm, showFolderPicker, copyDialog })` + destructure all returned names.
- [ ] `npm run build` (exit 0). Commit: `"refactor: extract useContextMenu composable"`.

## Task L7: `components/ContextMenu.vue`

Props `visible/x/y`; emits `edit/copy/download/move/duplicate/delete/close`. Move the context-menu overlay + menu block and the `.context-menu*` CSS. Shell: `<ContextMenu v-if="contextMenu.visible" :visible :x :y @edit="menuEditNote" @copy="menuCopyContent" @download="menuDownloadNote" @move="menuMoveNote" @duplicate="menuDuplicateNote" @delete="menuDeleteNote" @close="closeContextMenu" />`. `npm run build`; smoke (menu positions + all 6 actions). Commit.

## Task L8: `components/modals/{ConfirmDialog,PromptDialog,CopyDialog,FolderPicker}.vue`

Each renders from the shared reactive state (prop) and emits the action; shell wires emits to `useDialogs` methods. Move each modal block + its CSS. ConfirmDialog prop `confirmDialog` emits accept/cancel; PromptDialog props visible/title/value emits submit/cancel; CopyDialog prop `copyDialog` emits copy/close; FolderPicker props `folderPicker` + `flatFoldersForPicker` emits confirm/cancel/startNew/submitNew/cancelNew. One commit per modal. `npm run build` each.

## Task L9: `components/NoteCard.vue` (dedupes the two card blocks)

Props `note`; **inject** `layout` (`vMeasureCard`, `vCheckOverflow`, `overlongStates`, `expandedCards`, `toggleExpand`, `cardText`) and `dnd` (`hoveredNotePath`, `onNoteDragStart`); emits `open`, `menu`. (No `utils.js` import needed — `cardText` comes from `layout`, corrected from the earlier plan.) Move `.waterfall-card`/`.card-*`/`.tag` CSS. Replace BOTH card blocks (search grid + notes grid) with `<NoteCard v-for="note in col" :key="note.path" :note="note" @open="openNote" @menu="openContextMenuBtn" />`. `npm run build`; smoke both grids. Commit.

## Task L10: Finalize + runtime smoke

- [ ] Audit `MainView`: imports + 6 composable instantiations (with exact deps) + destructure + `provide('layout'/'dnd')` + the core (notes/editor/history + glue) inline + thin onMounted (`initWails(); loadAll(); restoreFromUrl()` + remaining listeners) + smaller `<style>`. Prune dead CSS.
- [ ] `npm run build`, `go build .`, `go vet ./...`, `npm test` — all pass.
- [ ] **Runtime smoke** (build + run; build alone cannot catch missing-binding regressions): create/edit/save/autosave-restore/delete/move/duplicate/search/drag into folder & root/`.md` import/folder create-rename-delete/mobile sidebar/login-logout. Every behavior must match pre-refactor.

## Definition of Done

- 6 composables + `utils.js` extracted; `ContextMenu`, 4 modals, `NoteCard` extracted; core remains inline in `MainView`.
- `MainView.vue` meaningfully smaller (logic clusters + card/modal CSS relocated).
- Every moved reference verified internal-or-injected against the dependency map; `npm run build`, `go build`, `go vet`, `npm test` pass; runtime smoke matches pre-refactor.

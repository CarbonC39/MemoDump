<template>
  <div class="main-layout">
    <SidebarPanel
      v-model:mobile-open="mobileSidebar"
      v-model:root-drop-over="rootDropOver"
      :theme-icon="themeIcon"
      :all-notes-active="!searchOpen && !currentFolder && !editingNote"
      :storage-expanded="openSections.storage"
      :folders="folders"
      :current-folder="currentFolder"
      :uploading-files="uploadingFiles"
      :is-wails-app="isWailsApp"
      :wails-data-dir="wailsDataDir"
      :server-no-auth="serverNoAuth"
      :settings-active="showSettings"
      :is-local-build="isLocalBuild"
      @toggle-theme="toggleTheme"
      @new-note="newNote"
      @open-search="openSearchPanel"
      @open-all="handleAllClick"
      @toggle-storage="toggleSection('storage')"
      @new-folder="promptNewFolder"
      @new-note-in="createNewNoteIn"
      @file-change="onFileInputChange"
      @drop-root="onDropOnRoot"
      @select-folder="selectFolder"
      @rename-folder="promptRenameFolder"
      @delete-folder="doDeleteFolder"
      @open-note="openNote"
      @drop-note="onDropNote"
      @drop-folder="onDropFolder"
      @expand-folder="loadFolderNode"
      @change-data-dir="changeDataDir"
      @logout="doLogout"
      @toggle-settings="showSettings = !showSettings"
    />

    <!-- Main content -->
    <main class="main-content"
      @dragenter="onMainDragEnter"
      @dragleave="onMainDragLeave"
      @dragover="onMainDragOver"
      @drop="onMainDrop"
    >
      <MainHeader
        :show-settings="showSettings"
        :editing="Boolean(editingNote)"
        :search-open="searchOpen"
        :has-prev-page="hasPrevPage"
        :current-folder="currentFolder"
        :name="editName"
        :folder="editFolder"
        :tags="editTags"
        :tag-input="tagInput"
        :editor-mode="editorMode"
        :is-saving="isSaving"
        :can-delete="Boolean(editingNote?.path)"
        :save-button-class="saveBtnClass"
        :save-button-title="saveBtnTitle"
        :save-problem="saveStatus === 'error' || saveStatus === 'offline'"
        :sort-mode="sortMode"
        @toggle-mobile-menu="mobileSidebar = !mobileSidebar"
        @close-settings="showSettings = false"
        @back="goBack"
        @update:name="editName = $event"
        @update:tags="editTags = $event"
        @update:tag-input="tagInput = $event"
        @dirty="isDirty = true"
        @pick-folder="pickEditFolder"
        @add-tag="addTag"
        @toggle-mode="toggleEditorMode"
        @save="saveNote"
        @delete="deleteCurrentNote"
        @close-search="searchOpen = false"
        @sort="setSort"
        @new-note="createNewNoteIn(currentFolder)"
      />

      <div class="content-area" :class="{ 'is-editing': editingNote }">
        <!-- Settings page (v-show: editor stays mounted behind) -->
        <SettingsPanel v-show="showSettings" @close="showSettings = false" />

        <!-- Normal content: hidden when settings is open -->
        <div v-show="!showSettings" class="content-inner">
          <div v-if="isInitializing" class="app-init-state" aria-busy="true"></div>

          <!-- Search results (right-side panel) -->
          <SearchNotesView
            v-if="!isInitializing && searchOpen"
            v-model:query="searchQuery"
            v-model:tag="searchTag"
            v-model:hovered-note-path="hoveredNotePath"
            :notes="searchResults"
            @search="doSearch"
            @dragstart="onNoteDragStart"
            @contextmenu="openContextMenuBtn"
          />

        <!-- Editor -->
        <NoteEditorView
          v-if="!isInitializing && editorEverMounted"
          v-show="editingNote && !searchOpen"
          :mode="editorMode"
          :editor-key="editorKey"
          :initial-content="editingNote?.content || editContent"
          :content="editContent"
          @update="onEditorUpdate"
          @document-ready="onEditorReady"
          @update:mode="editorMode = $event"
          @update:content="editContent = $event; isDirty = true"
        />

        <!-- Waterfall notes view -->
        <BrowseNotesView
          v-if="!isInitializing && !searchOpen && !editingNote"
          v-model:hovered-note-path="hoveredNotePath"
          :notes="sortedDisplayNotes"
          :has-more="Boolean(nextNotesCursor)"
          :loading-more="loadingMoreNotes"
          @load-more="loadMoreNotes"
          @dragstart="onNoteDragStart"
          @contextmenu="openContextMenuBtn"
        />
        </div>
      </div>

      <!-- File drop overlay -->
      <div v-if="isFileDragOver" class="file-drop-overlay">
        <div class="file-drop-inner">
          <span class="material-icons-outlined">upload_file</span>
          <p>{{ t('notes.dropToImport') }}</p>
        </div>
      </div>
    </main>

    <!-- Draft Restored Banner -->
    <div v-if="showDraftRestoredBanner" class="draft-banner">
      <span class="material-icons-outlined" style="font-size:18px;flex-shrink:0">restore</span>
      <span>{{ t('notes.draftRestored') }}</span>
      <button class="draft-banner-close" @click="showDraftRestoredBanner = false">
        <span class="material-icons-outlined">close</span>
      </button>
    </div>

    <FolderPickerDialog
      :visible="folderPicker.visible"
      :selected="folderPicker.selected"
      :new-folder-active="folderPicker.newFolderActive"
      :new-folder-name="folderPicker.newFolderName"
      :folders="flatFoldersForPicker"
      @update:selected="folderPicker.selected = $event"
      @update:new-folder-name="folderPicker.newFolderName = $event"
      @close="closeFolderPicker"
      @confirm="confirmFolderPicker"
      @start-create="startCreateFolderInPicker"
      @cancel-create="cancelNewFolderInPicker"
      @submit-create="submitNewFolderInPicker"
    />

    <BasicDialogs
      :prompt-visible="promptVisible"
      :prompt-title="promptTitle"
      :prompt-value="promptValue"
      :confirm-dialog="confirmDialog"
      :copy-dialog="copyDialog"
      @update:prompt-value="promptValue = $event"
      @submit-prompt="submitPrompt"
      @cancel-prompt="cancelPrompt"
      @accept-confirm="acceptConfirm"
      @cancel-confirm="cancelConfirm"
      @close-copy="copyDialog.visible = false"
      @copy="copyFromDialog"
    />

    <!-- Context Menu -->
    <ContextMenu v-if="contextMenu.visible" :visible="contextMenu.visible" :x="contextMenu.x" :y="contextMenu.y"
      @edit="menuEditNote" @copy="menuCopyContent" @duplicate="menuDuplicateNote"
      @download="menuDownloadNote" @move="menuMoveNote" @delete="menuDeleteNote" @close="closeContextMenu" />

  </div>
</template>

<script setup>
import { ref, computed, onMounted, onBeforeUnmount, reactive, watch, provide } from 'vue'
import { useRouter, useRoute } from 'vue-router'
import apiClient from '../api'
import NoteEditorView from '../components/NoteEditorView.vue'
import MainHeader from '../components/MainHeader.vue'
import BrowseNotesView from '../components/BrowseNotesView.vue'
import SearchNotesView from '../components/SearchNotesView.vue'
import SidebarPanel from '../components/SidebarPanel.vue'
import BasicDialogs from '../components/BasicDialogs.vue'
import FolderPickerDialog from '../components/FolderPickerDialog.vue'
import SettingsPanel from '../components/SettingsPanel.vue'
import ContextMenu from '../components/ContextMenu.vue'
import { useI18n } from '../i18n'
import { useAppInit } from '../composables/useAppInit'
import { useCardLayout } from '../composables/useCardLayout'
import { useDialogs } from '../composables/useDialogs'
import { useAutosave } from '../composables/useAutosave'
import { useFileImport } from '../composables/useFileImport'
import { useContextMenu } from '../composables/useContextMenu'
import { outboxAll } from '../composables/outbox.js'
import { useTheme } from '../composables/useTheme.js'
import { useNoteEditor } from '../composables/useNoteEditor.js'
import { useNoteBrowser } from '../composables/useNoteBrowser.js'
import { useNoteSearch } from '../composables/useNoteSearch.js'
import { useNotePersistence } from '../composables/useNotePersistence.js'
import { useFolderActions } from '../composables/useFolderActions.js'
import { preloadMilkdownEditor } from '../components/milkdownLoader.js'

const router = useRouter()
const route = useRoute()
const { t } = useI18n()
const { themeIcon, setTheme, theme } = useTheme()

const { isWailsApp, isLocalBuild, wailsDataDir, serverNoAuth, mobileSidebar, openSections, toggleSection, initWails, changeDataDir, doLogout } = useAppInit()

const showSettings = ref(false)
const isInitializing = ref(true)
// Start fetching the editor chunk as soon as the main view is evaluated. This
// runs in parallel with note/folder and IndexedDB initialization.
preloadMilkdownEditor().catch(() => {})
const layout = useCardLayout()
provide('layout', layout)

const {
  searchOpen, searchResults, searchQuery, searchTag, doSearch,
} = useNoteSearch({ api: apiClient })

const {
  allNotes, folders, currentFolder, displayNotes,
  nextNotesCursor, loadingMoreNotes, sortMode,
  sortedDisplayNotes, flatFoldersForPicker,
  setSort, loadFolderNode, loadMoreNotes, loadFolderPage,
  loadAll, refreshRootFolders,
} = useNoteBrowser({ api: apiClient })

const {
  confirmDialog, showConfirm, acceptConfirm, cancelConfirm,
  promptVisible, promptTitle, promptValue, showPrompt, submitPrompt, cancelPrompt,
  copyDialog, copyFromDialog,
  folderPicker, showFolderPicker, closeFolderPicker, confirmFolderPicker,
  startCreateFolderInPicker, cancelNewFolderInPicker, submitNewFolderInPicker,
} = useDialogs({ folders })

const noteEditor = useNoteEditor()
const {
  editingNote, editName, editTags, editFolder, editContent, tagInput,
  editorKey, isDirty, isSaving, editorMode,
  loadDocument, restoreDraft, createDocument, clearDocument,
  onEditorUpdate, onEditorReady, addTag, toggleEditorMode,
} = noteEditor
const editorEverMounted = ref(false)
watch(editingNote, (note) => {
  if (note) editorEverMounted.value = true
}, { immediate: true })

// View context captured before entering the editor, used by the back button.
const prevView = reactive({ folder: '', search: false })

// True when there is a prior view (folder/search) to return to; otherwise the
// back button acts as a Home button that goes to All Notes.
const hasPrevPage = computed(() => prevView.search || !!prevView.folder)

const {
  saveError, openDocument, saveNote, deleteCurrent,
} = useNotePersistence({
  api: apiClient,
  editor: noteEditor,
  onSaved: updateUrl,
})

const {
  promptNewFolder,
  promptRenameFolder,
  deleteFolder: doDeleteFolder,
} = useFolderActions({
  api: apiClient,
  currentFolder,
  loadAll,
  loadFolderNode,
  refreshRootFolders,
  showPrompt,
  showConfirm,
  t,
  updateUrl,
})

const {
  contextMenu, openContextMenuBtn, closeContextMenu, menuEditNote, menuCopyContent,
  menuDuplicateNote, menuDeleteNote, menuDownloadNote, menuMoveNote,
} = useContextMenu({ openNote, isDirty, loadAll, editingNote, _forceNewNote, showConfirm, showFolderPicker, copyDialog })

async function handleAllClick() {
  if (!confirmLeave()) return
  showSettings.value = false
  editingNote.value = null
  isDirty.value = false
  searchOpen.value = false
  currentFolder.value = ''
  updateUrl()
  await loadAll()
}

async function goBack() {
  if (!confirmLeave()) return
  editingNote.value = null
  isDirty.value = false
  if (prevView.search) {
    searchOpen.value = true
    currentFolder.value = ''
    updateUrl()
  } else if (prevView.folder) {
    await selectFolder(prevView.folder)
  } else {
    await handleAllClick()
  }
}

function openSearchPanel() {
  if (!confirmLeave()) return
  showSettings.value = false
  searchOpen.value = true
  editingNote.value = null
  isDirty.value = false
  updateUrl()
}

const { showDraftRestoredBanner, saveStatus, replayAll } = useAutosave({
  editingNote, isDirty, saveNote,
  reload: loadAll, ping: () => apiClient.ping(), saveError,
})

const saveBtnClass = computed(() => {
  // Clean/synced: outlined. Dirty/error/offline: filled blue.
  return (saveStatus.value === 'synced') ? 'save-btn-clean' : 'save-btn-dirty'
})

const saveBtnTitle = computed(() => {
  switch (saveStatus.value) {
    case 'offline': return t('status.offlineTitle')
    case 'dirty': return t('status.dirtyTitle')
    case 'error': return saveError.value || t('status.errorTitle')
    default: return t('status.syncedTitle')
  }
})

function toggleTheme() {
  // If currently dark (DOM has data-theme="dark"), switch to light.
  // If currently light, switch to dark. Simple binary toggle.
  const isDark = document.documentElement.getAttribute('data-theme') === 'dark'
  setTheme(isDark ? 'light' : 'dark')
}

function handleGlobalKeydown(e) {
  if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 's') {
    e.preventDefault()
    if (editingNote.value) {
      saveNote()
    }
  }
  if (e.key === 'Escape') {
    closeContextMenu()
  }
}

// ======= URL STATE =======
// Encode current view state into URL query params for shareable/bookmarkable links
function updateUrl() {
  const q = {}
  if (editingNote.value?.path) q.note = editingNote.value.path
  else if (currentFolder.value) q.folder = currentFolder.value
  else if (searchOpen.value) q.search = '1'
  router.replace({ query: q })
}

async function restoreFromUrl() {
  const { note, folder } = route.query
  if (note) {
    try {
      const res = await apiClient.getNote(note)
      const data = res.data
      loadDocument(data)
      searchOpen.value = false
      return
    } catch (_) { /* fall through */ }
  }
  if (folder) {
    openSections.storage = true
    try {
      await loadFolderPage(folder)
    } catch (_) {
      displayNotes.value = allNotes.value
    }
    return
  }
  // Default: show all notes, open new note bypassing confirmLeave (startup)
  _forceNewNote()
}
onMounted(async () => {
  window.addEventListener('keydown', handleGlobalKeydown)
  const listLoad = loadAll()

  // Restore pending offline writes from a previous session (replaces the old
  // single-slot localStorage draft). Most recent entry goes back into the
  // editor with a banner; everything (including it) is then replayed if online.
  let restored = false
  try {
    const entries = await outboxAll()
    if (entries.length) {
      const latest = entries[entries.length - 1]
      restoreDraft(latest)
      showDraftRestoredBanner.value = true
      restored = true
    }
  } catch (_) {}

  if (!restored) await restoreFromUrl()
  isInitializing.value = false
  // Listing continues in parallel with editor startup. Await it only so an
  // initialization rejection is contained before this lifecycle task ends.
  await listLoad
  if (typeof navigator === 'undefined' || navigator.onLine) replayAll()
})

onBeforeUnmount(() => {
  window.removeEventListener('keydown', handleGlobalKeydown)
})

function confirmLeave() {
  if (!isDirty.value) return true
  return confirm(t('modals.unsavedChanges'))
}

// Internal helper: create new note without confirmLeave check (used after delete/startup)
function _forceNewNote() {
  showSettings.value = false
  prevView.folder = currentFolder.value
  prevView.search = searchOpen.value
  createDocument(currentFolder.value)
  searchOpen.value = false
  mobileSidebar.value = false
  updateUrl()
}

function newNote() {
  if (!confirmLeave()) return
  _forceNewNote()
}

function createNewNoteIn(folderPath) {
  if (!confirmLeave()) return
  _forceNewNote()
  editFolder.value = folderPath
  openSections.storage = true
}

async function openNote(note) {
  if (!confirmLeave()) return
  prevView.folder = currentFolder.value
  prevView.search = searchOpen.value
  try {
    await openDocument(note)
    searchOpen.value = false
    mobileSidebar.value = false
    updateUrl()
  } catch (e) {
    console.error('Failed to open note', e)
  }
}

async function deleteCurrentNote() {
  if (!(await showConfirm({
    title: t('modals.deleteNote'),
    message: t('modals.deleteNoteMsg'),
    okLabel: t('modals.delete'),
    danger: true,
  }))) return
  try {
    await deleteCurrent()
    await loadAll()
    _forceNewNote()
  } catch (e) {
    alert(t('errors.deleteFailed'))
  }
}

async function selectFolder(folderPath) {
  if (!confirmLeave()) return
  showSettings.value = false
  editingNote.value = null
  isDirty.value = false
  searchOpen.value = false
  mobileSidebar.value = false
  try {
    await loadFolderPage(folderPath)
  } catch (_) {}
  updateUrl()
}

// ===== FOLDER PICKER FOR META PANEL =====
async function pickEditFolder() {
  const dest = await showFolderPicker(editFolder.value)
  if (dest !== null) editFolder.value = dest
}

const dnd = useFileImport({ editFolder, currentFolder, loadAll, openNote, editingNote, updateUrl })
const {
  rootDropOver, hoveredNotePath, onNoteDragStart, onDropNote, onDropFolder, onDropOnRoot,
  uploadingFiles, isFileDragOver, onFileInputChange,
  onMainDragEnter, onMainDragLeave, onMainDragOver, onMainDrop,
} = dnd
provide('dnd', dnd)
</script>

<style scoped>
.main-layout {
  display: flex;
  height: 100%;
  width: 100%;
}

/* ======= MAIN CONTENT ======= */
.main-content {
  flex: 1;
  display: flex;
  flex-direction: column;
  overflow: hidden;
  min-width: 0;
}
/* ======= CONTENT AREA ======= */
.content-area {
  flex: 1;
  overflow-y: auto;
  scrollbar-gutter: stable;
  background: var(--bg);
}
.content-area.is-editing {
  background: var(--bg-card);
  display: flex;
  flex-direction: column;
}

/* Editing mode: only change height so raw textarea fills viewport.
   Do NOT change the waterfall/search layout — those remain block. */
.content-area.is-editing .content-inner {
  height: 100%;
}
.app-init-state {
  min-height: 100%;
  background: var(--bg);
}

/* Draft Restored Banner */
.draft-banner {
  position: fixed;
  bottom: 16px;
  left: 50%;
  transform: translateX(-50%);
  display: flex;
  align-items: center;
  gap: 10px;
  background: var(--primary-dark, #3a6bc4);
  color: #fff;
  padding: 10px 16px;
  border-radius: 10px;
  font-size: 13px;
  box-shadow: 0 4px 16px rgba(0,0,0,0.18);
  z-index: 2000;
  max-width: calc(100vw - 32px);
}
.draft-banner-close {
  background: none;
  border: none;
  color: #fff;
  cursor: pointer;
  padding: 0;
  margin-left: 4px;
  display: flex;
  align-items: center;
  opacity: 0.8;
}
.draft-banner-close:hover { opacity: 1; }

@media (max-width: 768px) {
  /* Prevent iOS zoom on input focus by ensuring font-size >= 16px */
  .input,
  select { font-size: 16px !important; }
}

@media (min-width: 769px) and (max-width: 1100px) {
  /* 2-col handled by JS columnCount, CSS just ensures gap stays */
}

/* ===== FILE DROP OVERLAY ===== */
.file-drop-overlay {
  position: fixed;
  inset: 0;
  z-index: 500;
  background: rgba(100, 149, 237, 0.10);
  backdrop-filter: blur(2px);
  display: flex;
  align-items: center;
  justify-content: center;
  pointer-events: none;
}
.file-drop-inner {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 12px;
  background: var(--bg-card);
  border: 2px dashed var(--primary);
  border-radius: var(--radius-lg);
  padding: 48px 64px;
  color: var(--primary-dark);
  font-size: 15px;
  font-weight: 600;
  box-shadow: var(--shadow-md);
}
.file-drop-inner .material-icons-outlined {
  font-size: 48px;
  color: var(--primary);
}
</style>

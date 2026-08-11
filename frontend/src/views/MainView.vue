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
      @toggle-settings="toggleSettings"
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
        :save-problem="saveStatus === 'error' || saveStatus === 'offline' || saveStatus === 'conflict'"
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

      <div v-if="syncChangedNotice" class="sync-changed-notice" role="status">
        <span>{{ syncChangedNotice }}</span>
        <button type="button" class="btn btn-sm" @click="dismissSyncNotice">✕</button>
      </div>

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

    <div v-if="mediaNotice" class="draft-banner media-notice" role="status">
      <span class="material-icons-outlined" style="font-size:18px;flex-shrink:0">image_not_supported</span>
      <span>{{ t('media.' + mediaNotice.code) }}</span>
      <button class="draft-banner-close" @click="mediaNotice = null">
        <span class="material-icons-outlined">close</span>
      </button>
    </div>

    <div v-if="pendingImageCount > 0" class="draft-banner media-pending" role="status">
      <span class="material-icons-outlined" style="font-size:18px;flex-shrink:0">cloud_upload</span>
      <span>{{ pendingImageCount }} {{ t('media.pendingImages') }}</span>
      <button class="draft-banner-action" :disabled="mediaRetrying" @click="retryPendingImages">
        {{ mediaRetrying ? t('media.retrying') : t('media.retry') }}
      </button>
      <button class="draft-banner-action" @click="openImageSettings">
        {{ t('media.openSettings') }}
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
import { ref, computed, onMounted, onBeforeUnmount, watch, provide } from 'vue'
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
import { initImageSettings, refreshImageSettings } from '../composables/useImageSettings'
import {
  initMediaOutbox,
  startMediaFlushLoop,
  mediaNotice,
  pendingImageCount,
  retryAllPending,
} from '../composables/mediaOutbox'
import { outboxAll } from '../composables/outbox.js'
import { useTheme } from '../composables/useTheme.js'
import { useNoteEditor } from '../composables/useNoteEditor.js'
import { useNoteBrowser } from '../composables/useNoteBrowser.js'
import { useNoteSearch } from '../composables/useNoteSearch.js'
import { useNotePersistence } from '../composables/useNotePersistence.js'
import { useFolderActions } from '../composables/useFolderActions.js'
import { useWorkspaceNavigation } from '../composables/useWorkspaceNavigation.js'
import { useSyncPolling } from '../composables/useSyncPolling.js'
import { refreshSyncSettings, setOnSyncChanged } from '../composables/useSyncSettings.js'
import { cloudSyncAvailable } from '../composables/runtime.js'
import { preloadMilkdownEditor } from '../components/milkdownLoader.js'

const router = useRouter()
const route = useRoute()
const { t } = useI18n()
const { themeIcon, setTheme } = useTheme()

const { isWailsApp, isLocalBuild, wailsDataDir, serverNoAuth, mobileSidebar, openSections, toggleSection, initWails, changeDataDir, doLogout } = useAppInit()

const showSettings = ref(false)
const mediaRetrying = ref(false)
const isInitializing = ref(true)
// Start fetching the editor chunk as soon as the main view is evaluated. This
// runs in parallel with note/folder and IndexedDB initialization.
preloadMilkdownEditor().catch(() => {})

async function retryPendingImages() {
  if (mediaRetrying.value) return
  mediaRetrying.value = true
  try {
    await retryAllPending()
  } finally {
    mediaRetrying.value = false
  }
}

async function openImageSettings() {
  showSettings.value = true
  await refreshImageSettings()
  // Refresh cloud-sync recovery details whenever the panel opens, so a stale or
  // temporarily-failed recovery fetch is retried on every open (R5.4).
  refreshSyncSettings()
}

async function toggleSettings() {
  if (showSettings.value) {
    showSettings.value = false
    return
  }
  await openImageSettings()
}
const layout = useCardLayout()
provide('layout', layout)

const noteBrowser = useNoteBrowser({ api: apiClient })
const {
  searchOpen, searchResults, searchQuery, searchTag, doSearch,
} = useNoteSearch({ api: apiClient })

const {
  folders, currentFolder,
  nextNotesCursor, loadingMoreNotes, sortMode,
  sortedDisplayNotes, flatFoldersForPicker,
  setSort, loadFolderNode, loadMoreNotes,
  loadAll, refreshRootFolders, loadFolderTreeForPicker,
} = noteBrowser

const {
  confirmDialog, showConfirm, acceptConfirm, cancelConfirm,
  promptVisible, promptTitle, promptValue, showPrompt, submitPrompt, cancelPrompt,
  copyDialog, copyFromDialog,
  folderPicker, showFolderPicker: showFolderPickerDialog, closeFolderPicker, confirmFolderPicker,
  startCreateFolderInPicker, cancelNewFolderInPicker, submitNewFolderInPicker,
} = useDialogs({ folders })

async function showFolderPicker(...args) {
  try { await loadFolderTreeForPicker() } catch (_) {}
  return showFolderPickerDialog(...args)
}

const noteEditor = useNoteEditor()
const {
  editingNote, editName, editTags, editFolder, editContent, tagInput,
  editorKey, isDirty, isSaving, editorMode,
  onEditorUpdate, onEditorReady, addTag, toggleEditorMode,
} = noteEditor
const editorEverMounted = ref(false)
watch(editingNote, (note) => {
  if (note) editorEverMounted.value = true
}, { immediate: true })

let updateUrlHandler = () => {}

const {
  saveError, conflict, openDocument, saveNote, deleteCurrent,
} = useNotePersistence({
  api: apiClient,
  editor: noteEditor,
  onSaved: (...args) => updateUrlHandler(...args),
})

const { showDraftRestoredBanner, saveStatus, replayAll } = useAutosave({
  editingNote, isDirty, saveNote,
  reload: loadAll, ping: () => apiClient.ping(), saveError,
  api: apiClient, conflict,
})

const {
  hasPrevPage, updateUrl, forceNewNote: _forceNewNote,
  newNote, createNewNoteIn, openNote, selectFolder,
  handleAllClick, openSearchPanel, goBack, deleteCurrentNote,
  initialize,
} = useWorkspaceNavigation({
  router, route,
  editor: noteEditor,
  browser: noteBrowser,
  searchOpen, showSettings, mobileSidebar, openSections,
  openDocument, deleteCurrent, loadAll,
  readOutbox: outboxAll,
  replayAll, showDraftRestoredBanner, showConfirm, t,
})
updateUrlHandler = updateUrl

// R5.4: lightweight automatic-sync poller. A connected replica's backend can
// change local Markdown without a frontend request, so while the document is
// visible we poll the redacted status every 30s, refresh the list when an
// automatic attempt completes, adopt a clean open note's new revision, close a
// remotely-deleted note, and never replace a dirty buffer.
const syncChangedNotice = ref('')
const syncPolling = useSyncPolling({
  api: apiClient,
  editor: {
    ...noteEditor,
    // An offline (unsaved outbox) or conflicting buffer is protected too: the
    // poller must never replace or close a clean-looking editor that still has
    // queued or conflicting changes. saveStatus incorporates both states.
    isOffline: computed(() => saveStatus.value === 'offline'),
    isConflict: computed(() => saveStatus.value === 'conflict'),
  },
  available: cloudSyncAvailable(),
  onAutoSync: loadAll,
  onNotice: () => {
    syncChangedNotice.value = t('sync.syncedVersionChanged')
  },
  onNoteClosed: () => {
    syncChangedNotice.value = t('sync.noteDeletedBySync')
  },
  onRecoveryChanged: () => {
    refreshSyncSettings()
  },
})
function dismissSyncNotice() {
  syncChangedNotice.value = ''
}

// A successful manual Run-now (or a recovery restore) refreshes the list and
// the open note through the same safe logic the auto-sync poller uses, so a
// manual pull or restore is never stale — even when the cycle reports
// Synced=false after pulling some notes.
setOnSyncChanged(() => {
  loadAll()
  syncPolling.refreshOpenNote()
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

const saveBtnClass = computed(() => {
  // Clean/synced: outlined. Dirty/error/offline: filled blue.
  return (saveStatus.value === 'synced') ? 'save-btn-clean' : 'save-btn-dirty'
})

const saveBtnTitle = computed(() => {
  switch (saveStatus.value) {
    case 'offline': return t('status.offlineTitle')
    case 'dirty': return t('status.dirtyTitle')
    case 'conflict': return t('status.conflictTitle')
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

onMounted(async () => {
  window.addEventListener('keydown', handleGlobalKeydown)
  // Hard bootstrap order: image settings and media-outbox hydration must
  // complete before the editor mounts, so the proxyDomURL mapping exists
  // before any note markdown is rendered.
  await initImageSettings()
  await initMediaOutbox()
  startMediaFlushLoop()
  await initialize({
    onReady: () => { isInitializing.value = false },
  })
  syncPolling.start()
})

onBeforeUnmount(() => {
  window.removeEventListener('keydown', handleGlobalKeydown)
  syncPolling.stop()
})

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
.sync-changed-notice {
  display: flex;
  align-items: center;
  gap: 12px;
  justify-content: space-between;
  padding: 8px 16px;
  font-size: 13px;
  background: var(--bg-card);
  border-bottom: 1px solid var(--border);
  color: var(--text-secondary);
}
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

.media-notice { bottom: 64px; }
.media-pending { bottom: 64px; }
.media-notice + .media-pending { bottom: 116px; }
.draft-banner-action {
  border: 0;
  background: transparent;
  color: inherit;
  font: inherit;
  font-weight: 600;
  cursor: pointer;
  padding: 2px 4px;
  text-decoration: underline;
  text-underline-offset: 2px;
}
.draft-banner-action:disabled { cursor: default; opacity: 0.6; }

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

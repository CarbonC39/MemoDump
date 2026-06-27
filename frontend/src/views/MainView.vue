<template>
  <div class="main-layout">
    <!-- Mobile overlay -->
    <div v-if="mobileSidebar" class="sidebar-overlay" @click="mobileSidebar = false"></div>

    <!-- Fixed-width accordion sidebar -->
    <aside class="sidebar" :class="{ 'mobile-open': mobileSidebar }">
      <div class="sidebar-header">
        <img src="/favicon.ico" width="22" height="22" alt="Logo" style="border-radius: 4px; margin-right: 8px;" />
        <span class="brand">{{ t('login.brand') }}</span>
      </div>

      <div class="sidebar-scroll">
        <!-- New Note button -->
        <button class="sidebar-action" @click="newNote">
          <span class="material-icons-outlined">edit_note</span>
          {{ t('sidebar.newNote') }}
        </button>

        <div class="sidebar-nav">
          <div class="nav-item" @click="openSearchPanel()">
            <span class="material-icons-outlined">search</span>
            <span class="nav-text">{{ t('sidebar.search') }}</span>
          </div>

          <div class="nav-item" :class="{ active: !searchOpen && !currentFolder && !editingNote }" @click="handleAllClick()">
            <span class="material-icons-outlined">sticky_note_2</span>
            <span class="nav-text">{{ t('sidebar.allNotes') }}</span>
          </div>

          <div class="nav-item storage-nav-item" @click="toggleSection('storage')">
            <span class="material-icons-outlined">folder_open</span>
            <span class="nav-text">{{ t('sidebar.storage') }}</span>
            <div class="storage-header-actions" @click.stop>
              <button class="fa-btn-sm" @click="promptNewFolder('')" :title="t('modals.newFolder')">
                <span class="material-icons-outlined">create_new_folder</span>
              </button>
              <button class="fa-btn-sm" @click="createNewNoteIn('')" :title="t('editor.newNote')">
                <span class="material-icons-outlined">note_add</span>
              </button>
              <button class="fa-btn-sm" @click="triggerFileInput" :disabled="uploadingFiles" :title="uploadingFiles ? t('notes.importing') : t('notes.importMd')">
                <span class="material-icons-outlined">upload_file</span>
              </button>
              <input
                ref="fileInputRef"
                type="file"
                accept=".md,.txt"
                multiple
                style="display:none"
                @change="onFileInputChange"
              />
            </div>
            <span class="material-icons-outlined chevron" :class="{ 'expanded': openSections.storage }">chevron_right</span>
          </div>

          <div v-show="openSections.storage" class="nav-children">
            <!-- Root drop zone -->
            <div
              class="root-drop-zone"
              :class="{ 'drag-over': rootDropOver }"
              @dragover.prevent="rootDropOver = true"
              @dragleave="rootDropOver = false"
              @drop.prevent="onDropOnRoot"
            >
              <span class="material-icons-outlined">home</span>
              {{ t('notes.root') }}
            </div>

            <div v-if="folders.length === 0" class="empty-hint">{{ t('notes.noFolders') }}</div>

            <FolderNode
              v-for="f in folders"
              :key="f.path"
              :folder="f"
              :active-folder="currentFolder"
              @select="selectFolder"
              @new-folder="promptNewFolder"
              @rename="promptRenameFolder"
              @delete-folder="doDeleteFolder"
              @open-note="openNote"
              @new-note="createNewNoteIn"
              @drop-note="onDropNote"
              @drop-folder="onDropFolder"
            />
          </div>
        </div>
      </div>

      <div class="sidebar-footer">
        <button class="sidebar-action" @click="showSettings = !showSettings">
          <span class="material-icons-outlined">settings</span>
          {{ t('sidebar.settings') }}
        </button>
        <button v-if="isWailsApp" class="sidebar-action" @click="changeDataDir" :title="wailsDataDir">
          <span class="material-icons-outlined">folder_open</span>
          {{ t('sidebar.dataFolder') }}
        </button>
        <button v-if="!serverNoAuth" class="sidebar-action logout" @click="doLogout">
          <span class="material-icons-outlined">logout</span>
          {{ t('sidebar.signOut') }}
        </button>
        <div v-if="isLocalBuild" class="local-hint" :title="t('sidebar.savedInBrowserTitle')">
          <span class="material-icons-outlined">cloud_off</span>
          <span>{{ t('sidebar.savedInBrowser') }}</span>
        </div>
      </div>
    </aside>

    <!-- Main content -->
    <main class="main-content"
      @dragenter="onMainDragEnter"
      @dragleave="onMainDragLeave"
      @dragover="onMainDragOver"
      @drop="onMainDrop"
    >
      <!-- Header -->
      <header class="main-header">
        <button class="btn btn-icon btn-ghost menu-toggle" @click="mobileSidebar = !mobileSidebar">
          <span class="material-icons-outlined">menu</span>
        </button>

        <!-- Back button: always visible when editing (desktop + PWA).
             With no previous view to return to, it becomes a Home button → All Notes. -->
        <button v-if="editingNote" class="btn btn-icon btn-ghost editor-back-btn" @click="goBack" :title="hasPrevPage ? t('editor.back') : t('editor.allNotes')">
          <span class="material-icons-outlined">{{ hasPrevPage ? 'arrow_back' : 'home' }}</span>
        </button>

        <!-- Editing: horizontally scrollable metadata (title · folder · tags) -->
        <div v-if="editingNote" class="header-meta-scroll">
          <input
            ref="titleInputRef"
            class="header-title-input"
            v-model="editName"
            :style="{ width: titleInputWidth + 'px' }"
            :placeholder="t('editor.untitled')"
            @input="isDirty = true"
          />
          <span ref="titleMirrorRef" class="header-title-mirror" aria-hidden="true">{{ editName || t('editor.untitled') }}</span>
          <span class="header-meta-sep">·</span>
          <button class="note-folder-btn" @click="pickEditFolder">
            <span class="material-icons-outlined">{{ editFolder ? 'folder' : 'home' }}</span>
            <span class="note-folder-label">{{ editFolder || t('notes.root') }}</span>
          </button>
          <span class="header-meta-sep">·</span>
          <div class="note-tags-inline">
            <span class="tag" v-for="(t, i) in editTags" :key="i">
              {{ t }}<span class="remove" @click="editTags.splice(i, 1); isDirty = true">×</span>
            </span>
            <input
              class="tag-inline-input"
              v-model="tagInput"
              :placeholder="t('notes.tagPlaceholder')"
              @keydown.enter.prevent="addTag"
            />
          </div>
        </div>

        <!-- Not editing: static breadcrumb -->
        <div v-else class="header-left">
          <span v-if="currentFolder" class="header-folder-display">
            <span class="material-icons-outlined" style="font-size:16px;opacity:0.6">folder_open</span>
            {{ currentFolder }}
          </span>
        </div>

        <!-- Right: sort order + new note shortcut when browsing waterfall -->
        <div class="header-right" v-if="!editingNote && !searchOpen">
          <div class="sort-control">
            <button class="btn btn-icon header-sort-btn" :class="{ active: sortMenuOpen }" @click.stop="sortMenuOpen = !sortMenuOpen" :title="t('notes.sortOrder')">
              <span class="material-icons-outlined">sort</span>
            </button>
            <div v-if="sortMenuOpen" class="sort-overlay" @click="sortMenuOpen = false"></div>
            <div v-if="sortMenuOpen" class="sort-menu">
              <div class="sort-menu-item" :class="{ active: sortMode === 'modified-desc' }" @click="setSort('modified-desc')">
                <span class="material-icons-outlined sort-check">check</span><span>{{ t('notes.recentlyModified') }}</span>
              </div>
              <div class="sort-menu-item" :class="{ active: sortMode === 'modified-asc' }" @click="setSort('modified-asc')">
                <span class="material-icons-outlined sort-check">check</span><span>{{ t('notes.oldestModified') }}</span>
              </div>
            </div>
          </div>
          <button class="btn btn-icon header-new-btn" @click="createNewNoteIn(currentFolder)" :title="t('editor.newNote')">
            <span class="material-icons-outlined">add</span>
          </button>
        </div>

        <!-- Fixed right: save & delete — do NOT scroll -->
        <div class="header-right" v-else-if="editingNote">
          <button class="btn btn-sm btn-icon btn-ghost" @click="toggleEditorMode" :title="editorMode === 'wysiwyg' ? t('editor.switchToRaw') : t('editor.switchToRich')">
            <span class="material-icons-outlined" style="font-size:16px">{{ editorMode === 'wysiwyg' ? 'code' : 'visibility' }}</span>
          </button>
          <button class="save-btn" :class="{ dirty: isDirty }" @click="saveNote">
            <span class="save-dot" v-if="isDirty"></span>
            {{ t('editor.save') }}
          </button>
          <button class="btn btn-sm btn-icon btn-danger-subtle" v-if="editingNote.path" @click="deleteCurrentNote" :title="t('editor.deleteNote')">
            <span class="material-icons-outlined" style="font-size:16px">delete_outline</span>
          </button>
        </div>
      </header>

      <div class="content-area" :class="{ 'is-editing': editingNote }">
        <!-- Search results (right-side panel) -->
        <div v-if="searchOpen" class="search-results-view">
          <div class="search-results-header">
            <h2>{{ t('search.searchNotes') }}</h2>
            <button class="btn btn-icon btn-ghost" @click="searchOpen = false">
              <span class="material-icons-outlined">close</span>
            </button>
          </div>
          <div class="search-inputs-wrap">
            <input v-model="searchQuery" class="input" :placeholder="t('search.searchContent')" @input="doSearch" />
            <input v-model="searchTag" class="input" :placeholder="t('search.searchTags')" @input="doSearch" />
          </div>
          <div v-if="!searchQuery && !searchTag" class="empty-state-big">
            <span class="material-icons-outlined" style="font-size:48px;color:var(--border)">search</span>
            <p>{{ t('search.typeToSearch') }}</p>
          </div>
          <div v-else-if="searchResults.length === 0" class="empty-state-big">
            <span class="material-icons-outlined" style="font-size:48px;color:var(--border)">search_off</span>
            <p>{{ t('search.noResults') }}</p>
          </div>
          <div class="waterfall-grid">
            <div class="waterfall-col" v-for="(col, ci) in splitIntoColumns(searchResults)" :key="ci">
              <div v-for="note in col" :key="note.path" class="waterfall-card" v-measure-card="note.path"
                :draggable="hoveredNotePath !== note.path" @dragstart="onNoteDragStart($event, note)">
                <div class="card-header" v-if="note.hasCustomName">
                  <div class="card-name">{{ note.name }}</div>
                  <button class="btn btn-icon btn-ghost btn-sm card-menu-btn" @click.stop="openContextMenuBtn($event, note)">
                    <span class="material-icons-outlined">more_vert</span>
                  </button>
                </div>
                <button v-else class="btn btn-icon btn-ghost btn-sm card-menu-btn" style="position: absolute; top: 12px; right: 14px; margin: 0; z-index: 2" @click.stop="openContextMenuBtn($event, note)">
                  <span class="material-icons-outlined">more_vert</span>
                </button>
                <div class="card-preview" draggable="false"
                  @mouseenter="hoveredNotePath = note.path"
                  @mouseleave="hoveredNotePath = null"
                  @dragstart.stop v-check-overflow="note.path" :class="{ expanded: expandedCards.has(note.path) }">
                  <template v-if="cardText(note)">{{ cardText(note) }}</template>
                  <span v-else class="card-empty">{{ t('notes.emptyNote') }}</span>
                </div>
                <div class="card-expand-bar" v-if="overlongStates[note.path]" @click.stop="toggleExpand(note.path)">
                  <span class="material-icons-outlined">
                    {{ expandedCards.has(note.path) ? 'expand_less' : 'expand_more' }}
                  </span>
                </div>
                <div class="card-footer" v-if="note.tags && note.tags.length">
                  <div class="card-tags">
                    <span class="tag" v-for="t in note.tags" :key="t">{{ t }}</span>
                  </div>
                </div>
              </div>
            </div>
          </div>
        </div>

        <!-- Editor -->
        <div v-else-if="editingNote" class="editor-wrap">
          <MilkdownEditor
            v-if="editorMode === 'wysiwyg'"
            :key="editorKey"
            :initial-content="editingNote.content || ''"
            @update="onEditorUpdate"
          />
          <textarea
            v-else
            class="raw-editor"
            v-model="editContent"
            @input="isDirty = true"
            :placeholder="t('editor.rawMarkdown')"
          ></textarea>
        </div>

        <!-- Waterfall notes view -->
        <div v-else class="waterfall-view">
          <div v-if="displayNotes.length === 0" class="empty-state-big">
            <span class="material-icons-outlined" style="font-size:56px;color:var(--border)">description</span>
            <p>{{ t('notes.noNotes') }}</p>
          </div>
          <div v-else class="waterfall-grid">
            <div class="waterfall-col" v-for="(col, ci) in splitIntoColumns(sortedDisplayNotes)" :key="ci">
              <div v-for="note in col" :key="note.path" class="waterfall-card" v-measure-card="note.path"
                :draggable="hoveredNotePath !== note.path" @dragstart="onNoteDragStart($event, note)">
                <div class="card-header" v-if="note.hasCustomName">
                  <div class="card-name">{{ note.name }}</div>
                  <button class="btn btn-icon btn-ghost btn-sm card-menu-btn" @click.stop="openContextMenuBtn($event, note)">
                    <span class="material-icons-outlined">more_vert</span>
                  </button>
                </div>
                <button v-else class="btn btn-icon btn-ghost btn-sm card-menu-btn" style="position: absolute; top: 12px; right: 14px; margin: 0; z-index: 2" @click.stop="openContextMenuBtn($event, note)">
                  <span class="material-icons-outlined">more_vert</span>
                </button>
                <div class="card-preview" draggable="false"
                  @mouseenter="hoveredNotePath = note.path"
                  @mouseleave="hoveredNotePath = null"
                  @dragstart.stop v-check-overflow="note.path" :class="{ expanded: expandedCards.has(note.path) }">
                  <template v-if="cardText(note)">{{ cardText(note) }}</template>
                  <span v-else class="card-empty">{{ t('notes.emptyNote') }}</span>
                </div>
                <div class="card-expand-bar" v-if="overlongStates[note.path]" @click.stop="toggleExpand(note.path)">
                  <span class="material-icons-outlined">
                    {{ expandedCards.has(note.path) ? 'expand_less' : 'expand_more' }}
                  </span>
                </div>
                <div class="card-footer" v-if="note.tags && note.tags.length">
                  <div class="card-tags">
                    <span class="tag" v-for="t in note.tags" :key="t">{{ t }}</span>
                  </div>
                </div>
              </div>
            </div>
          </div>
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

    <!-- Folder Picker Modal -->
    <div v-if="folderPicker.visible" class="modal-overlay" @click.self="closeFolderPicker">
      <div class="folder-picker-modal">
        <div class="folder-picker-head">
          <h3>{{ t('modals.moveToFolder') }}</h3>
          <button class="btn-new-folder" @click="startCreateFolderInPicker" :title="t('modals.newFolder')">
            <span class="material-icons-outlined">create_new_folder</span>
            {{ t('modals.newFolder') }}
          </button>
        </div>
        <div v-if="folderPicker.newFolderActive" class="folder-picker-new-row">
          <span class="material-icons-outlined">create_new_folder</span>
          <span class="folder-picker-new-parent">
            {{ folderPicker.selected ? folderPicker.selected + '/' : '' }}
          </span>
          <input
            ref="newFolderInputRef"
            v-model="folderPicker.newFolderName"
            class="folder-picker-new-input"
            :placeholder="t('modals.folderName')"
            @keydown.enter.prevent="submitNewFolderInPicker"
            @keydown.esc.prevent="cancelNewFolderInPicker"
          />
          <button class="fa-btn-sm" @click="submitNewFolderInPicker" :title="t('modals.create')">
            <span class="material-icons-outlined">check</span>
          </button>
          <button class="fa-btn-sm" @click="cancelNewFolderInPicker" :title="t('modals.cancel')">
            <span class="material-icons-outlined">close</span>
          </button>
        </div>
        <div class="folder-picker-list">
          <div
            class="folder-picker-item"
            :class="{ active: folderPicker.selected === '' }"
            @click="folderPicker.selected = ''"
          >
            <span class="material-icons-outlined">home</span>
            {{ t('notes.root') }}
          </div>
          <div
            v-for="f in flatFoldersForPicker"
            :key="f.path"
            class="folder-picker-item"
            :class="{ active: folderPicker.selected === f.path }"
            :style="{ paddingLeft: (12 + f.depth * 16) + 'px' }"
            @click="folderPicker.selected = f.path"
          >
            <span class="material-icons-outlined">folder</span>
            {{ f.name }}
          </div>
          <div v-if="flatFoldersForPicker.length === 0" class="folder-picker-empty">
            {{ t('notes.noFolders') }}
          </div>
        </div>
        <div class="prompt-actions">
          <button class="btn btn-ghost" @click="closeFolderPicker">{{ t('modals.cancel') }}</button>
          <button class="btn btn-primary" @click="confirmFolderPicker">{{ t('modals.moveHere') }}</button>
        </div>
      </div>
    </div>

    <!-- Prompt Modal -->
    <div v-if="promptVisible" class="modal-overlay" @click.self="cancelPrompt">
      <div class="prompt-modal">
        <h3>{{ promptTitle }}</h3>
        <input v-model="promptValue" class="input" :placeholder="promptTitle" @keydown.enter="submitPrompt" ref="promptInputRef" />
        <div class="prompt-actions">
          <button class="btn btn-ghost" @click="cancelPrompt">{{ t('modals.cancel') }}</button>
          <button class="btn btn-primary" @click="submitPrompt">{{ t('modals.confirm') }}</button>
        </div>
      </div>
    </div>

    <!-- Confirm Modal -->
    <div v-if="confirmDialog.visible" class="modal-overlay" @click.self="cancelConfirm">
      <div class="prompt-modal">
        <h3>{{ confirmDialog.title }}</h3>
        <p class="confirm-message" v-if="confirmDialog.message">{{ confirmDialog.message }}</p>
        <div class="prompt-actions">
          <button class="btn btn-ghost" @click="cancelConfirm">{{ t('modals.cancel') }}</button>
          <button class="btn" :class="confirmDialog.danger ? 'btn-danger' : 'btn-primary'" @click="acceptConfirm">{{ confirmDialog.okLabel }}</button>
        </div>
      </div>
    </div>

    <!-- Copy Dialog (iOS PWA fallback) -->
    <div v-if="copyDialog.visible" class="modal-overlay" @click.self="copyDialog.visible = false">
      <div class="prompt-modal">
        <h3>{{ t('modals.copyText') }}</h3>
        <p style="font-size:13px;color:var(--text-secondary);margin-bottom:10px">{{ t('modals.copyInstruction') }}</p>
        <textarea
          ref="copyDialogTextarea"
          class="input"
          :value="copyDialog.content"
          readonly
          style="width:100%;height:180px;resize:vertical;font-size:13px;font-family:monospace;box-sizing:border-box;"
          @focus="e => e.target.setSelectionRange(0, e.target.value.length)"
        />
        <div class="prompt-actions">
          <button class="btn btn-ghost" @click="copyDialog.visible = false">{{ t('modals.close') }}</button>
          <button class="btn btn-primary" @click="copyFromDialog">{{ t('modals.copy') }}</button>
        </div>
      </div>
    </div>

    <!-- Context Menu -->
    <div v-if="contextMenu.visible" class="context-menu-overlay" @click="closeContextMenu" @contextmenu.prevent="closeContextMenu"></div>
    <div v-if="contextMenu.visible" class="context-menu" :style="{ top: contextMenu.y + 'px', left: contextMenu.x + 'px' }">
      <div class="context-menu-item" @click="menuEditNote">
        <span class="material-icons-outlined">edit</span> {{ t('actions.edit') }}
      </div>
      <div class="context-menu-item" @click="menuCopyContent">
        <span class="material-icons-outlined">content_copy</span> {{ t('actions.copyFullText') }}
      </div>
      <div class="context-menu-item" @click="menuDuplicateNote">
        <span class="material-icons-outlined">file_copy</span> Duplicate
      </div>
      <div class="context-menu-item" @click="menuDownloadNote">
        <span class="material-icons-outlined">download</span> {{ t('actions.download') }}
      </div>
      <div class="context-menu-item" @click="menuMoveNote">
        <span class="material-icons-outlined">drive_file_move</span> {{ t('actions.move') }}
      </div>
      <div class="context-menu-item text-danger" @click="menuDeleteNote">
        <span class="material-icons-outlined">delete_outline</span> {{ t('actions.delete') }}
      </div>
    </div>

    <!-- Settings Panel -->
    <SettingsPanel
      :visible="showSettings"
      @close="showSettings = false"
      @changed="applySettings"
    />
  </div>
</template>

<script setup>
import { ref, computed, onMounted, onBeforeUnmount, reactive, nextTick, watch, provide } from 'vue'
import { useRouter, useRoute } from 'vue-router'
import apiClient from '../api'
import { stripMarkdown, isTimestampName } from '../utils'
import MilkdownEditor from '../components/MilkdownEditor.vue'
import FolderNode from '../components/FolderNode.vue'
import SettingsPanel from '../components/SettingsPanel.vue'
import { useI18n } from '../i18n'
import { useAppInit } from '../composables/useAppInit'
import { useCardLayout } from '../composables/useCardLayout'
import { useDialogs } from '../composables/useDialogs'

const router = useRouter()
const route = useRoute()
const { t } = useI18n()

const { isWailsApp, isLocalBuild, wailsDataDir, serverNoAuth, mobileSidebar, openSections, toggleSection, initWails, changeDataDir, doLogout } = useAppInit()

const showSettings = ref(false)
const layout = useCardLayout()
const { expandedCards, fullContentCache, overlongStates, cardHeights, columnCount, updateColumnCount, toggleExpand, estimateHeight, splitIntoColumns, observeMeasure, disconnectMeasure, vCheckOverflow, vMeasureCard, cardText } = layout
provide('layout', layout)

const searchOpen = ref(false)

const titleInputRef = ref(null)

function focusTitleInput() {
  nextTick(() => { if (titleInputRef.value) titleInputRef.value.focus() })
}

// Data
const allNotes = ref([])
const folders = ref([])
const searchResults = ref([])
const searchQuery = ref('')
const searchTag = ref('')
const currentFolder = ref('')

const {
  confirmDialog, showConfirm, acceptConfirm, cancelConfirm,
  promptVisible, promptTitle, promptValue, promptInputRef, showPrompt, submitPrompt, cancelPrompt,
  copyDialog, copyDialogTextarea, copyFromDialog,
  folderPicker, showFolderPicker, closeFolderPicker, confirmFolderPicker,
  startCreateFolderInPicker, cancelNewFolderInPicker, submitNewFolderInPicker, newFolderInputRef,
} = useDialogs({ folders })

// Editor state
const editingNote = ref(null)
const editName = ref('')
const editTags = ref([])
const editFolder = ref('')
const editContent = ref('')
const tagInput = ref('')
const editorKey = ref(0)
// Dirty state: tracks whether the editor has unsaved changes
const isDirty = ref(false)

const editorMode = ref('wysiwyg') // 'wysiwyg' | 'raw'

async function toggleEditorMode() {
  // Wait a tick so any in-flight Milkdown `update` emit (which sets
  // editContent synchronously on every keystroke) has been processed by Vue
  // before we unmount the wysiwyg editor — otherwise the very last keystroke
  // before the click could be dropped.
  await nextTick()
  const switchingToWysiwyg = editorMode.value === 'raw'
  editorMode.value = editorMode.value === 'wysiwyg' ? 'raw' : 'wysiwyg'
  if (switchingToWysiwyg) {
    editingNote.value.content = editContent.value
    editorKey.value++
  }
}

// Title input width tracks the actual rendered text width (via a hidden
// mirror span) instead of the HTML `size` attribute, which only approximates
// width by character count and drifts badly with a proportional font.
const titleMirrorRef = ref(null)
const titleInputWidth = ref(80)

function updateTitleInputWidth() {
  if (!titleMirrorRef.value) return
  // +12px so the caret has room past the last character
  titleInputWidth.value = Math.max(60, titleMirrorRef.value.scrollWidth + 12)
}

watch(editName, () => nextTick(updateTitleInputWidth), { immediate: true })

let searchDebounceTimer = null

// Context Menu State
const hoveredNotePath = ref(null)
const contextMenu = reactive({
  visible: false,
  x: 0,
  y: 0,
  note: null
})

// Draft restored banner
const showDraftRestoredBanner = ref(false)

// View context captured before entering the editor, used by the back button.
const prevView = reactive({ folder: '', search: false })

// True when there is a prior view (folder/search) to return to; otherwise the
// back button acts as a Home button that goes to All Notes.
const hasPrevPage = computed(() => prevView.search || !!prevView.folder)

// Display notes
const displayNotes = ref([])

// ===== Waterfall sort order =====
const sortMenuOpen = ref(false)
const sortMode = ref('modified-desc')
try {
  const saved = localStorage.getItem('memodump_sort')
  if (saved) sortMode.value = saved
} catch (_) {}

function setSort(mode) {
  sortMode.value = mode
  sortMenuOpen.value = false
  try { localStorage.setItem('memodump_sort', mode) } catch (_) {}
}

const sortedDisplayNotes = computed(() => {
  const arr = displayNotes.value.slice()
  if (sortMode.value === 'modified-asc') {
    arr.sort((a, b) => (a.modTime || 0) - (b.modTime || 0))
  } else {
    arr.sort((a, b) => (b.modTime || 0) - (a.modTime || 0))
  }
  return arr
})

const flatFolders = computed(() => {
  const result = []
  function walk(list) {
    for (const f of list) {
      result.push(f.path)
      if (f.children) walk(f.children)
    }
  }
  walk(folders.value)
  return result
})

const flatFoldersForPicker = computed(() => {
  const result = []
  function walk(list, depth) {
    for (const f of list) {
      result.push({ path: f.path, name: f.name, depth })
      if (f.children && f.children.length) walk(f.children, depth + 1)
    }
  }
  walk(folders.value, 0)
  return result
})


function enrichNotes(notes) {
  return notes.map(n => ({
    ...n,
    hasCustomName: !isTimestampName(n.name),
    plainPreview: stripMarkdown(n.preview),
  }))
}

// Context Menu Handlers
// Called from three-dot button — position near the button element
function openContextMenuBtn(e, note) {
  e.stopPropagation()
  contextMenu.note = note
  contextMenu.visible = true
  // Use button bounding rect for reliable position on both desktop and mobile
  const rect = e.currentTarget.getBoundingClientRect()
  let x = rect.right
  let y = rect.bottom + 4
  // Keep menu within viewport
  const menuW = 160, menuH = 175
  if (x + menuW > window.innerWidth) x = rect.left - menuW
  if (y + menuH > window.innerHeight) y = rect.top - menuH
  if (x < 0) x = 4
  if (y < 0) y = 4
  contextMenu.x = x
  contextMenu.y = y
}

function closeContextMenu() {
  contextMenu.visible = false
  contextMenu.note = null
}

function menuEditNote() {
  if (contextMenu.note) openNote(contextMenu.note)
  closeContextMenu()
}

async function menuCopyContent() {
  const note = contextMenu.note
  closeContextMenu()
  if (!note) return
  try {
    const res = await apiClient.getNote(note.path)
    const content = res.data.content || ''

    // Modern clipboard API (works on iOS 16.4+ PWA even after async)
    if (navigator.clipboard?.writeText) {
      try {
        await navigator.clipboard.writeText(content)
        return
      } catch (_) {}
    }

    // Legacy fallback — setSelectionRange required for iOS (ta.select() is unreliable)
    const ta = document.createElement('textarea')
    ta.value = content
    ta.style.cssText = 'position:fixed;top:0;left:0;width:1px;height:1px;padding:0;border:none;outline:none;font-size:16px;opacity:0.01;'
    document.body.appendChild(ta)
    ta.focus({ preventScroll: true })
    ta.setSelectionRange(0, content.length)
    const ok = document.execCommand('copy')
    document.body.removeChild(ta)
    if (ok) return

    // Final fallback: show dialog so user can manually long-press → copy
    copyDialog.content = content
    copyDialog.visible = true
  } catch (e) {
    alert(t('errors.copyFailed'))
  }
}

async function menuDuplicateNote() {
  const note = contextMenu.note
  closeContextMenu()
  if (!note) return
  try {
    await apiClient.duplicateNote(note.path)
    await loadAll()
  } catch (e) { alert('Duplicate failed') }
}

async function menuDeleteNote() {
  const note = contextMenu.note
  closeContextMenu()
  if (!note) return
  if (!(await showConfirm({
    title: t('modals.deleteNote'),
    message: t('modals.deleteNoteMsg'),
    okLabel: t('modals.delete'),
    danger: true,
  }))) return
  try {
    await apiClient.deleteNote(note.path)
    isDirty.value = false
    await loadAll()
    if (editingNote.value && editingNote.value.path === note.path) _forceNewNote()
  } catch (e) { alert(t('errors.deleteFailed')) }
}

async function menuDownloadNote() {
  const note = contextMenu.note
  closeContextMenu()
  if (!note) return
  try {
    const res = await apiClient.getNote(note.path)
    const content = res.data.content || ''
    const blob = new Blob([content], { type: 'text/markdown;charset=utf-8' })
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = note.name + '.md'
    document.body.appendChild(a)
    a.click()
    document.body.removeChild(a)
    URL.revokeObjectURL(url)
  } catch (e) {
    alert(t('errors.downloadFailed'))
  }
}

async function menuMoveNote() {
  const note = contextMenu.note
  closeContextMenu()
  if (!note) return
  const noteParts = note.path.split('/')
  const curFolder = noteParts.length > 1 ? noteParts.slice(0, -1).join('/') : ''
  const dest = await showFolderPicker(curFolder)
  if (dest === null) return
  try {
    await apiClient.moveNote(note.path, dest)
    await loadAll()
    if (editingNote.value && editingNote.value.path === note.path) {
      const newPath = dest ? dest + '/' + note.path.split('/').pop() : note.path.split('/').pop()
      openNote({ path: newPath })
    }
  } catch (e) { alert(t('errors.moveFailed')) }
}

async function handleAllClick() {
  if (!confirmLeave()) return
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
  searchOpen.value = true
  editingNote.value = null
  isDirty.value = false
  updateUrl()
}

function handleBeforeUnload(e) {
  if (isDirty.value) {
    e.preventDefault()
    e.returnValue = ''
  }
}

// ======= AUTOSAVE (iOS PWA data-loss prevention) =======
// iOS Safari/PWA frequently suspends or evicts a backgrounded tab without
// ever firing beforeunload, so manual-save-only loses unsaved edits. Save
// debounced shortly after each edit, and immediately when the tab is hidden
// or about to be torn down.
let autosaveTimer = null
let autosaving = false

function scheduleAutosave() {
  if (autosaveTimer) clearTimeout(autosaveTimer)
  autosaveTimer = setTimeout(() => {
    if (isDirty.value && editingNote.value && !autosaving) {
      runAutosave()
    }
  }, 3000)
}

async function runAutosave() {
  autosaving = true
  try {
    await saveNote()
  } finally {
    autosaving = false
  }
}

watch(isDirty, (dirty) => {
  if (dirty) scheduleAutosave()
})

function persistDraftToLocalStorage() {
  try {
    localStorage.setItem('memodump_draft', JSON.stringify({
      content: editContent.value,
      name: editName.value,
      tags: editTags.value,
      folder: editFolder.value,
      path: editingNote.value?.path || '',
    }))
  } catch (_) {}
}

async function flushSaveOrFallback() {
  if (!isDirty.value || !editingNote.value || autosaving) return
  autosaving = true
  try {
    await saveNote({ silent: true })
  } catch (_) {
    // Network unavailable (e.g. backgrounded PWA with no connectivity) —
    // fall back to the same localStorage draft mechanism already used for
    // the 401/session-expiry case, so the next launch can restore it.
    persistDraftToLocalStorage()
  } finally {
    autosaving = false
  }
}

function handleVisibilityChange() {
  if (document.hidden) flushSaveOrFallback()
}

function handlePageHide() {
  flushSaveOrFallback()
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
    sortMenuOpen.value = false
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
      _editorReady = false
      editingNote.value = data
      editName.value = isTimestampName(data.name) ? '' : (data.name || '')
      editTags.value = [...(data.tags || [])]
      editContent.value = data.content || ''
      isDirty.value = false
      const parts = data.path.split('/')
      editFolder.value = parts.length > 1 ? parts.slice(0, -1).join('/') : ''
      editorKey.value++
      searchOpen.value = false
      return
    } catch (_) { /* fall through */ }
  }
  if (folder) {
    currentFolder.value = folder
    openSections.storage = true
    try {
      const res = await apiClient.listNotes(folder)
      displayNotes.value = enrichNotes(res.data)
    } catch (_) {
      displayNotes.value = allNotes.value
    }
    return
  }
  // Default: show all notes, open new note bypassing confirmLeave (startup)
  _forceNewNote()
}

// --- Apply font settings to CSS variables ---
function applySettings() {
  try {
    const raw = localStorage.getItem('memodump_settings')
    if (!raw) return
    const s = JSON.parse(raw)
    const root = document.documentElement

    root.style.setProperty('--app-zoom', ((s.appFontSize || 14) / 14).toFixed(2))
    root.style.setProperty('--editor-wysiwyg-font-size', (s.editorWysiwygFontSize || 16) + 'px')
    root.style.setProperty('--editor-raw-font-size', (s.editorRawFontSize || 14) + 'px')

    if (s.editorFonts) {
      // Build proportional font-family: pick each system's chosen serif or sans-serif
      const proportionalParts = []
      for (const key of ['latin', 'sc', 'tcHK', 'tcTW']) {
        const fs = s.editorFonts[key]
        if (!fs) continue
        const fontName = fs.proportional === 'serif' ? fs.serif : fs.sansSerif
        if (fontName) proportionalParts.push(fontName.includes(' ') ? `"${fontName}"` : fontName)
      }
      proportionalParts.push('sans-serif')
      root.style.setProperty('--editor-font-proportional', proportionalParts.join(', '))
    }

    // Global monospace font
    if (s.editorMonospace) {
      const name = s.editorMonospace.includes(' ') ? `"${s.editorMonospace}"` : s.editorMonospace
      root.style.setProperty('--editor-font-monospace', `${name}, monospace`)
    }
  } catch (e) { console.warn('Failed to apply font settings:', e) }
}

onMounted(async () => {
  applySettings()
  window.addEventListener('keydown', handleGlobalKeydown)
  window.addEventListener('beforeunload', handleBeforeUnload)
  document.addEventListener('visibilitychange', handleVisibilityChange)
  window.addEventListener('pagehide', handlePageHide)
  await loadAll()

  // Restore draft saved before session-expiry redirect
  const rawDraft = localStorage.getItem('memodump_draft')
  if (rawDraft) {
    try {
      const draft = JSON.parse(rawDraft)
      localStorage.removeItem('memodump_draft')
      _editorReady = false
      editingNote.value = { content: draft.content || '', path: draft.path || '' }
      editName.value = draft.name || ''
      editTags.value = draft.tags || []
      editContent.value = draft.content || ''
      editFolder.value = draft.folder || ''
      isDirty.value = true
      editorKey.value++
      showDraftRestoredBanner.value = true
      return  // skip URL-based restore so draft takes priority
    } catch (_) {}
  }

  await restoreFromUrl()
})

onBeforeUnmount(() => {
  window.removeEventListener('keydown', handleGlobalKeydown)
  window.removeEventListener('beforeunload', handleBeforeUnload)
  document.removeEventListener('visibilitychange', handleVisibilityChange)
  window.removeEventListener('pagehide', handlePageHide)
  if (searchDebounceTimer) clearTimeout(searchDebounceTimer)
  if (autosaveTimer) clearTimeout(autosaveTimer)
})

async function loadAll() {
  try {
    const [notesRes, foldersRes] = await Promise.all([
      apiClient.listNotes(''),
      apiClient.listFolders(),
    ])
    allNotes.value = enrichNotes(notesRes.data)
    folders.value = foldersRes.data || []
    if (currentFolder.value) {
      const folderNotesRes = await apiClient.listNotes(currentFolder.value)
      displayNotes.value = enrichNotes(folderNotesRes.data)
    } else {
      displayNotes.value = allNotes.value
    }
  } catch (e) {
    // 401 is handled globally by the api interceptor (redirects to login).
  }
}

// Flag: whether editor has finished its initial load (suppress first markdownUpdated)
let _editorReady = false

function confirmLeave() {
  if (!isDirty.value) return true
  return confirm(t('modals.unsavedChanges'))
}

// Internal helper: create new note without confirmLeave check (used after delete/startup)
function _forceNewNote() {
  prevView.folder = currentFolder.value
  prevView.search = searchOpen.value
  _editorReady = false
  editingNote.value = { content: '', path: '' }
  editName.value = ''
  editTags.value = []
  editFolder.value = currentFolder.value
  editContent.value = ''
  isDirty.value = false
  editorKey.value++
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
    const res = await apiClient.getNote(note.path)
    const data = res.data
    // Temporarily disable dirty tracking while the editor loads initial content
    _editorReady = false
    editingNote.value = data
    editName.value = isTimestampName(data.name) ? '' : (data.name || '')
    editTags.value = [...(data.tags || [])]
    editContent.value = data.content || ''
    isDirty.value = false
    const parts = data.path.split('/')
    editFolder.value = parts.length > 1 ? parts.slice(0, -1).join('/') : ''
    editorKey.value++
    searchOpen.value = false
    mobileSidebar.value = false
    updateUrl()
  } catch (e) {
    console.error('Failed to open note', e)
  }
}

function onEditorUpdate(markdown) {
  if (!_editorReady) {
    // First event after (re)mount is the initial content load — skip it
    _editorReady = true
    editContent.value = markdown
    return
  }
  editContent.value = markdown
  isDirty.value = true
}

function addTag() {
  const t = tagInput.value.trim()
  if (t && !editTags.value.includes(t)) editTags.value.push(t)
  tagInput.value = ''
}

async function saveNote({ silent = false } = {}) {
  try {
    let resultNode;
    if (editingNote.value.path) {
      const originalTitle = isTimestampName(editingNote.value.name) ? '' : (editingNote.value.name || '');
      const payload = {
        content: editContent.value,
        tags: editTags.value,
      };
      if (editName.value !== originalTitle) {
        payload.rename = editName.value;
      }
      let res = await apiClient.updateNote(editingNote.value.path, payload)
      resultNode = res.data
      const parts = resultNode.path.split('/')
      const curDir = parts.length > 1 ? parts.slice(0, -1).join('/') : ''
      if (editFolder.value !== curDir) {
        res = await apiClient.moveNote(resultNode.path, editFolder.value)
        resultNode = res.data
      }
    } else {
      let res = await apiClient.createNote({
        content: editContent.value,
        name: editName.value || '',
        folder: editFolder.value,
        tags: editTags.value,
      })
      resultNode = res.data
    }
    await loadAll()
    // Keep it open, just update metadata properly so further saves work
    editingNote.value.path = resultNode.path
    editName.value = isTimestampName(resultNode.name) ? '' : (resultNode.name || '')
    const parts = resultNode.path.split('/')
    editFolder.value = parts.length > 1 ? parts.slice(0, -1).join('/') : ''
    isDirty.value = false
    updateUrl()
  } catch (e) {
    if (e.response?.status === 401) {
      // Session expired — persist draft before the interceptor redirects to login
      try {
        localStorage.setItem('memodump_draft', JSON.stringify({
          content: editContent.value,
          name: editName.value,
          tags: editTags.value,
          folder: editFolder.value,
          path: editingNote.value?.path || '',
        }))
      } catch (_) {}
      // The api interceptor will redirect to /login automatically
      return
    }
    if (silent) throw e
    alert(t('errors.saveFailed') + (e.response?.data?.error || e.message))
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
    await apiClient.deleteNote(editingNote.value.path)
    isDirty.value = false
    await loadAll()
    _forceNewNote()
  } catch (e) {
    alert(t('errors.deleteFailed'))
  }
}

function doSearch() {
  clearTimeout(searchDebounceTimer)
  if (!searchQuery.value && !searchTag.value) {
    searchResults.value = []
    return
  }
  searchDebounceTimer = setTimeout(async () => {
    try {
      const res = await apiClient.search(searchQuery.value, searchTag.value)
      searchResults.value = enrichNotes(res.data)
    } catch (e) {
      searchResults.value = []
    }
  }, 300)
}

async function selectFolder(folderPath) {
  if (!confirmLeave()) return
  currentFolder.value = folderPath
  editingNote.value = null
  isDirty.value = false
  searchOpen.value = false
  mobileSidebar.value = false
  try {
    const res = await apiClient.listNotes(folderPath)
    displayNotes.value = enrichNotes(res.data)
  } catch (e) {
    displayNotes.value = []
  }
  updateUrl()
}

async function promptNewFolder(parentPath) {
  const name = await showPrompt(t('modals.folderName'))
  if (!name) return
  const path = parentPath ? parentPath + '/' + name : name
  try {
    await apiClient.createFolder(path)
    const res = await apiClient.listFolders()
    folders.value = res.data || []
  } catch (e) { alert(t('errors.failed')) }
}

async function promptRenameFolder(folderPath) {
  const currentName = folderPath.split('/').pop()
  const name = await showPrompt(t('modals.newName'), currentName)
  if (!name || name === currentName) return
  try {
    await apiClient.renameFolder(folderPath, name)
    // Update currentFolder path if it was inside the renamed folder
    if (currentFolder.value === folderPath || currentFolder.value.startsWith(folderPath + '/')) {
      const parentDir = folderPath.substring(0, folderPath.lastIndexOf('/'))
      const newFolderBase = parentDir ? parentDir + '/' + name : name
      currentFolder.value = currentFolder.value.replace(folderPath, newFolderBase)
    }
    await loadAll()
    updateUrl()
  } catch (e) { alert(t('errors.failed')) }
}

async function doDeleteFolder(folderPath) {
  if (!(await showConfirm({
    title: t('modals.deleteFolder'),
    message: t('modals.deleteFolderMsg'),
    okLabel: t('modals.delete'),
    danger: true,
  }))) return
  try {
    await apiClient.deleteFolder(folderPath)
    // Reset currentFolder if it was inside the deleted folder
    if (currentFolder.value === folderPath || currentFolder.value.startsWith(folderPath + '/')) {
      currentFolder.value = ''
    }
    await loadAll()
    updateUrl()
  } catch (e) { alert(t('errors.failed')) }
}

// ===== FOLDER PICKER FOR META PANEL =====
async function pickEditFolder() {
  const dest = await showFolderPicker(editFolder.value)
  if (dest !== null) editFolder.value = dest
}

// ===== DRAG AND DROP =====
const rootDropOver = ref(false)

function onNoteDragStart(e, note) {
  e.dataTransfer.effectAllowed = 'move'
  e.dataTransfer.setData('memodump-type', 'note')
  e.dataTransfer.setData('memodump-path', note.path)
}

async function onDropNote({ notePath, destFolder }) {
  try {
    await apiClient.moveNote(notePath, destFolder)
    await loadAll()
    if (editingNote.value && editingNote.value.path === notePath) {
      const filename = notePath.split('/').pop()
      const newPath = destFolder ? destFolder + '/' + filename : filename
      openNote({ path: newPath })
    }
  } catch (e) {
    if (e.response?.status === 409) alert(t('errors.nameExists'))
    else alert(t('errors.moveFailed'))
  }
}

async function onDropFolder({ folderPath, destFolder }) {
  try {
    await apiClient.moveFolder(folderPath, destFolder)
    if (currentFolder.value === folderPath || currentFolder.value.startsWith(folderPath + '/')) {
      const folderName = folderPath.split('/').pop()
      currentFolder.value = destFolder ? destFolder + '/' + folderName : folderName
    }
    await loadAll()
    updateUrl()
  } catch (e) {
    if (e.response?.status === 409) alert(t('errors.folderExists'))
    else if (e.response?.status === 400) alert(e.response.data?.error || t('errors.moveFailed'))
    else alert(t('errors.moveFailed'))
  }
}

async function onDropOnRoot(e) {
  rootDropOver.value = false
  const type = e.dataTransfer.getData('memodump-type')
  const path = e.dataTransfer.getData('memodump-path')
  if (!path) return
  if (type === 'note') {
    await onDropNote({ notePath: path, destFolder: '' })
  } else if (type === 'folder') {
    await onDropFolder({ folderPath: path, destFolder: '' })
  }
}

// ===== FILE UPLOAD =====
const uploadingFiles = ref(false)
const isFileDragOver = ref(false)
let fileDragCounter = 0
const fileInputRef = ref(null)

function triggerFileInput() {
  if (fileInputRef.value) fileInputRef.value.click()
}

function onFileInputChange(e) {
  const files = Array.from(e.target.files || [])
  e.target.value = ''
  if (files.length) uploadFiles(files)
}

function onMainDragEnter(e) {
  if (!e.dataTransfer.types.includes('Files')) return
  fileDragCounter++
  isFileDragOver.value = true
  e.preventDefault()
}

function onMainDragLeave(e) {
  if (!e.dataTransfer.types.includes('Files')) return
  fileDragCounter--
  if (fileDragCounter <= 0) {
    fileDragCounter = 0
    isFileDragOver.value = false
  }
}

function onMainDragOver(e) {
  if (!e.dataTransfer.types.includes('Files')) return
  e.preventDefault()
  e.dataTransfer.dropEffect = 'copy'
}

function onMainDrop(e) {
  if (!e.dataTransfer.types.includes('Files')) return
  e.preventDefault()
  fileDragCounter = 0
  isFileDragOver.value = false
  const files = Array.from(e.dataTransfer.files)
  if (files.length) uploadFiles(files)
}

async function uploadFiles(files) {
  const allowed = files.filter(f => /\.(md|txt)$/i.test(f.name))
  if (!allowed.length) {
    alert(t('errors.fileTypeUnsupported'))
    return
  }
  uploadingFiles.value = true
  let lastOpened = null
  for (const file of allowed) {
    const fd = new FormData()
    fd.append('file', file)
    try {
      const res = await apiClient.uploadNote(fd, editFolder.value || currentFolder.value || '')
      lastOpened = res.data
    } catch (e) {
      const msg = e.response?.data?.error || e.message
      alert(t('errors.importFailed').replace('{name}', file.name).replace('{msg}', msg))
    }
  }
  uploadingFiles.value = false
  await loadAll()
  if (lastOpened) openNote(lastOpened)
}
</script>

<style scoped>
.main-layout {
  display: flex;
  height: 100%;
  width: 100%;
}

/* ======= SIDEBAR ======= */
.sidebar {
  width: 240px;
  flex-shrink: 0;
  background: var(--bg-sidebar);
  border-right: 1px solid var(--border);
  display: flex;
  flex-direction: column;
  overflow: hidden;
}
.sidebar-header {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 14px 16px;
  font-size: 15px;
  font-weight: 700;
  color: var(--text);
  border-bottom: 1px solid var(--border-light);
  flex-shrink: 0;
}
.sidebar-scroll {
  flex: 1;
  overflow-y: auto;
  padding: 8px 0;
}

/* New Note / Logout button */
.sidebar-action {
  display: flex;
  align-items: center;
  gap: 8px;
  width: calc(100% - 16px);
  margin: 0 8px 4px;
  padding: 8px 12px;
  border-radius: 8px;
  font-size: 13px;
  font-weight: 500;
  color: var(--primary-dark);
  background: var(--primary-bg);
  border: none;
  cursor: pointer;
  transition: background 0.15s, transform 0.1s;
}
.sidebar-action:hover {
  background: var(--border-light);
}
.sidebar-action:active {
  transform: scale(0.98);
}
.sidebar-action .material-icons-outlined { font-size: 18px; color: var(--primary); }
.sidebar-action.logout {
  color: var(--text-secondary);
  background: none;
}
.sidebar-action.logout:hover {
  background: var(--danger-light);
  color: var(--danger);
}
.sidebar-action.logout .material-icons-outlined { color: inherit; }

/* Fluid list styling */
.sidebar-search {
  padding: 8px 12px;
}

.sidebar-nav {
  display: flex;
  flex-direction: column;
}

.nav-item {
  display: flex;
  align-items: center;
  gap: 6px;
  width: calc(100% - 16px);
  margin: 1px 8px;
  padding: 6px 10px;
  border-radius: 6px;
  font-size: 13px;
  font-weight: 500;
  color: var(--text);
  cursor: pointer;
  transition: background 0.1s;
}
.nav-item:hover { background: var(--primary-bg); }
.nav-item.active { background: var(--primary-bg); color: var(--primary-dark); }
.nav-item .material-icons-outlined { font-size: 18px; color: var(--text-secondary); }
.nav-item.active .material-icons-outlined { color: var(--primary); }

.chevron {
  font-size: 16px !important;
  color: var(--text-muted) !important;
  transition: transform 0.2s;
  margin-left: auto;
}
.chevron.expanded { transform: rotate(90deg); }

.nav-text {
  flex: 1;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.nav-children {
  padding-left: 12px;
  padding-right: 8px;
  margin-top: 4px;
}
.empty-hint {
  font-size: 12px;
  color: var(--text-muted);
  padding: 8px 4px;
}
/* Storage nav-item action buttons (appear on hover, before the chevron) */
.storage-nav-item { position: relative; }
.storage-header-actions {
  display: flex;
  gap: 2px;
  opacity: 0;
  transition: opacity 0.1s;
  margin-right: 2px;
}
.storage-nav-item:hover .storage-header-actions { opacity: 1; }
.fa-btn-sm {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 22px;
  height: 22px;
  border-radius: 4px;
  border: none;
  background: none;
  color: var(--text-secondary);
  cursor: pointer;
}
.fa-btn-sm:hover { background: var(--border); }
.fa-btn-sm .material-icons-outlined { font-size: 15px; }

/* Root drop zone */
.root-drop-zone {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 4px 8px;
  margin-bottom: 4px;
  border-radius: 6px;
  font-size: 12px;
  color: var(--text-muted);
  border: 1px dashed transparent;
  transition: all 0.15s;
  cursor: default;
}
.root-drop-zone .material-icons-outlined { font-size: 15px; }
.root-drop-zone.drag-over {
  border-color: var(--primary);
  background: var(--primary-bg);
  color: var(--primary-dark);
}

.tree-note {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 5px 8px;
  border-radius: 6px;
  cursor: pointer;
  font-size: 13px;
  color: var(--text);
  transition: background 0.1s;
}
.tree-note:hover { background: var(--primary-bg); color: var(--primary-dark); }
.tree-note .material-icons-outlined { font-size: 16px; color: var(--primary); opacity: 0.8; }
.tree-note .note-name {
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  flex: 1;
}

.sidebar-footer {
  padding: 8px;
  border-top: 1px solid var(--border-light);
  flex-shrink: 0;
}
/* Local build hint — informational, not alarming */
.local-hint {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 8px 12px;
  font-size: 12px;
  font-weight: 500;
  line-height: 1.4;
  color: var(--text-muted);
}
.local-hint .material-icons-outlined {
  font-size: 16px;
  color: var(--text-muted);
}

/* ======= MAIN CONTENT ======= */
.main-content {
  flex: 1;
  display: flex;
  flex-direction: column;
  overflow: hidden;
  min-width: 0;
}
.main-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 0 12px 0 8px;
  border-bottom: 1px solid var(--border);
  background: var(--bg-card);
  gap: 8px;
  height: var(--header-height);
  flex-shrink: 0;
}
.header-left {
  display: flex;
  align-items: center;
  gap: 6px;
  flex: 1;
  min-width: 0;
}
.menu-toggle { display: none; }
.header-right {
  display: flex;
  align-items: center;
  gap: 6px;
  flex-shrink: 0;
}

/* Title / folder shown inline in header */
.header-title-display {
  font-size: 14px;
  font-weight: 500;
  color: var(--text);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  cursor: pointer;
  padding: 4px 6px;
  border-radius: 6px;
  transition: background 0.15s;
}
.header-title-display:hover {
  background: var(--border-light);
}
.header-folder-display {
  display: flex;
  align-items: center;
  gap: 4px;
  font-size: 13px;
  color: var(--text-secondary);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

/* Back button in editor header — always visible on desktop and mobile */
.editor-back-btn {
  display: flex;
  flex-shrink: 0;
}

/* New note shortcut in header (waterfall view) */
.header-new-btn {
  width: 28px;
  height: 28px;
  color: var(--primary);
  border-radius: var(--radius);
}
.header-new-btn:hover {
  background: var(--primary-bg);
}
.header-new-btn .material-icons-outlined {
  font-size: 22px;
}

/* Danger-subtle button (delete, not as alarming as red bg) */
.btn-danger-subtle {
  color: var(--text-muted);
}
.btn-danger-subtle:hover {
  color: var(--danger);
  background: var(--danger-light);
}

/* ======= SAVE BUTTON ======= */
.save-btn {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 5px 14px;
  border: 1.5px solid var(--primary);
  border-radius: 100px;
  background: transparent;
  color: var(--primary-dark);
  font-size: 13px;
  font-weight: 700;
  cursor: pointer;
  transition: background 0.15s, color 0.15s, border-color 0.15s;
  flex-shrink: 0;
}
.save-btn:hover {
  background: var(--primary-bg);
}
.save-btn.dirty {
  background: var(--primary);
  color: #fff;
}
.save-btn.dirty:hover {
  background: var(--primary-dark);
}
.save-dot {
  display: inline-block;
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: rgba(255, 255, 255, 0.85);
  flex-shrink: 0;
}

/* ======= HEADER METADATA SCROLL ======= */
.header-meta-scroll {
  flex: 1;
  min-width: 0;
  overflow-x: auto;
  overflow-y: hidden;
  display: flex;
  align-items: center;
  scrollbar-width: none;
  -webkit-overflow-scrolling: touch;
}
.header-meta-scroll::-webkit-scrollbar { display: none; }

.header-title-input {
  border: none;
  outline: none;
  background: transparent;
  font-size: 14px;
  font-weight: 600;
  color: var(--text);
  padding: 4px 4px;
  flex-shrink: 0;
  font-family: inherit;
  caret-color: var(--primary);
  min-width: 60px;
  transition: width 0.1s ease;
}
.header-title-mirror {
  position: absolute;
  visibility: hidden;
  white-space: pre;
  font-size: 14px;
  font-weight: 600;
  font-family: inherit;
  padding: 4px 4px;
  pointer-events: none;
}
.header-title-input::placeholder {
  color: var(--text-muted);
}
.header-meta-sep {
  color: var(--border);
  font-size: 14px;
  margin: 0 6px;
  flex-shrink: 0;
  user-select: none;
}
.note-folder-btn {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  padding: 2px 8px 2px 4px;
  border: none;
  border-radius: 5px;
  background: transparent;
  color: var(--text-secondary);
  font-size: 13px;
  font-weight: 500;
  cursor: pointer;
  flex-shrink: 0;
  transition: background 0.12s, color 0.12s;
  font-family: inherit;
}
.note-folder-btn:hover {
  background: var(--primary-bg);
  color: var(--primary-dark);
}
.note-folder-btn .material-icons-outlined {
  font-size: 14px;
  color: var(--primary);
}
.note-folder-label {
  max-width: 140px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.note-tags-inline {
  display: flex;
  flex-wrap: nowrap;
  align-items: center;
  gap: 4px;
  flex-shrink: 0;
}
.note-tags-inline :deep(.tag),
.note-tags-inline .tag {
  font-size: 13px;
}
.tag-inline-input {
  border: none;
  outline: none;
  background: transparent;
  font-size: 13px;
  font-weight: 500;
  color: var(--text-muted);
  width: 48px;
  padding: 2px 4px;
  font-family: inherit;
  flex-shrink: 0;
  transition: color 0.12s, width 0.15s;
}
.tag-inline-input::placeholder {
  color: var(--text-muted);
  opacity: 0.6;
}
.tag-inline-input:focus {
  color: var(--primary-dark);
  width: 72px;
}

/* ======= CONTENT AREA ======= */
.content-area {
  flex: 1;
  overflow-y: auto;
  scrollbar-gutter: stable;
  background: var(--bg);
}
.content-area.is-editing {
  background: #FFFFFF;
}

.editor-wrap {
  max-width: 860px;
  margin: 0 auto;
  padding: 20px 60px;
  background: #FFFFFF;
  min-height: 100%;
  display: flex;
  flex-direction: column;
}
.raw-editor {
  flex: 1;
  width: 100%;
  height: 100%;
  border: none;
  outline: none;
  resize: none;
  padding: 16px;
  font-family: var(--editor-font-monospace);
  font-size: var(--editor-raw-font-size);
  line-height: 1.7;
  color: var(--text);
  background: var(--bg-card);
}

/* Search results */
.search-results-view {
  padding: 20px 24px;
}
.search-results-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 16px;
}
.search-results-header h2 {
  font-size: 18px;
  font-weight: 600;
}
.search-inputs-wrap {
  display: flex;
  gap: 12px;
  margin-bottom: 24px;
  flex-wrap: wrap;
}
.search-inputs-wrap .input {
  flex: 1;
  min-width: 160px;
}

/* Waterfall */
.waterfall-view {
  padding: 20px 24px;
}
.empty-state-big {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  padding: 80px 20px;
  color: var(--text-muted);
  gap: 12px;
}
.empty-state-big p { font-size: 14px; }
.waterfall-grid {
  display: flex;
  gap: 14px;
  align-items: flex-start;
}
.waterfall-col {
  flex: 1;
  min-width: 0;
  display: flex;
  flex-direction: column;
}
.waterfall-card {
  position: relative;
  background: #FFFFFF;
  border: 1px solid rgba(0, 0, 0, 0.04);
  border-radius: 14px;
  padding: 16px 18px;
  margin-bottom: 16px;
  box-shadow: 0 4px 14px rgba(0, 0, 0, 0.03);
  transition: box-shadow 0.2s ease, background 0.2s ease;
}
.card-header {
  display: flex;
  justify-content: space-between;
  align-items: flex-start;
  margin-bottom: 6px;
}
.card-name {
  font-size: 14px;
  font-weight: 600;
  color: var(--waterfall-title);
  margin-bottom: 0px;
  flex: 1;
  word-break: break-all;
}
.card-menu-btn {
  margin-left: 8px;
  margin-top: -4px;
  margin-right: -4px;
  color: var(--text-muted);
  /* Increase touch target on mobile */
  min-width: 36px;
  min-height: 36px;
}
.card-menu-btn:hover {
  background: var(--border-light);
  color: var(--text);
}
/* Card preview — no click action, just display */
.card-preview {
  font-size: 13px;
  color: var(--text-secondary);
  line-height: 1.6;
  white-space: pre-line;
  word-break: break-word;
  display: -webkit-box;
  -webkit-box-orient: vertical;
  -webkit-line-clamp: 6;
  line-clamp: 6;
  overflow: hidden;
  transition: max-height 0.25s ease, overflow 0.25s;
  cursor: text;
  user-select: text;
  -webkit-user-select: text;
  -webkit-user-drag: none;
}
.card-preview.expanded {
  display: block;
  -webkit-line-clamp: unset;
  line-clamp: unset;
}
.card-expand-bar {
  display: flex;
  justify-content: center;
  align-items: center;
  padding: 4px 0;
  margin-top: 4px;
  margin-bottom: -8px;
  cursor: pointer;
  color: var(--primary);
  border-radius: 6px;
  transition: background 0.2s;
}
.card-expand-bar:hover {
  background: var(--bg);
  color: var(--primary-dark);
}
.card-expand-bar .material-icons-outlined {
  font-size: 20px;
}
.card-footer { margin-top: 8px; }
.card-tags { display: flex; flex-wrap: wrap; gap: 4px; }
.card-empty {
  color: var(--text-muted);
  font-style: italic;
}

/* ======= SORT CONTROL ======= */
.sort-control {
  position: relative;
  display: flex;
  align-items: center;
}
.header-sort-btn {
  width: 28px;
  height: 28px;
  color: var(--text-secondary);
  border-radius: var(--radius);
}
.header-sort-btn:hover,
.header-sort-btn.active {
  background: var(--primary-bg);
  color: var(--primary-dark);
}
.header-sort-btn .material-icons-outlined { font-size: 20px; }
.sort-overlay {
  position: fixed;
  inset: 0;
  z-index: 1000;
}
.sort-menu {
  position: absolute;
  top: calc(100% + 4px);
  right: 0;
  background: #fff;
  border: 1px solid var(--border);
  box-shadow: var(--shadow-md);
  border-radius: 8px;
  padding: 4px 0;
  min-width: 184px;
  z-index: 1001;
}
.sort-menu-item {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 9px 14px 9px 8px;
  font-size: 13px;
  color: var(--text);
  cursor: pointer;
  white-space: nowrap;
}
.sort-menu-item:hover { background: var(--primary-bg); }
.sort-menu-item.active { color: var(--primary-dark); font-weight: 500; }
.sort-check {
  font-size: 16px;
  opacity: 0;
  color: var(--primary);
}
.sort-menu-item.active .sort-check { opacity: 1; }

/* Context Menu */
.context-menu-overlay {
  position: fixed;
  top: 0; left: 0; right: 0; bottom: 0;
  z-index: 1000;
}
.context-menu {
  position: fixed;
  background: #fff;
  border: 1px solid var(--border);
  box-shadow: 0 4px 12px rgba(0,0,0,0.12);
  border-radius: 8px;
  padding: 4px 0;
  min-width: 170px;
  z-index: 1001;
}
.context-menu-item {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 10px 16px;
  font-size: 14px;
  cursor: pointer;
  color: var(--text);
}
.context-menu-item:hover {
  background: var(--bg);
}
.context-menu-item.text-danger {
  color: var(--danger);
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

/* Folder Picker Modal */
.folder-picker-modal {
  background: var(--bg-card);
  padding: 24px;
  border-radius: var(--radius-lg);
  width: 320px;
  max-width: 92vw;
  box-shadow: var(--shadow-md);
  max-height: 80vh;
  display: flex;
  flex-direction: column;
}
.folder-picker-modal h3 {
  margin-bottom: 0;
  font-size: 16px;
  font-weight: 600;
  flex-shrink: 0;
}
.folder-picker-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 12px;
}
.btn-new-folder {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  padding: 4px 8px;
  border: 1px solid var(--border);
  border-radius: var(--radius);
  background: transparent;
  color: var(--primary-dark);
  font-size: 12px;
  font-weight: 500;
  cursor: pointer;
  transition: background 0.12s, border-color 0.12s;
}
.btn-new-folder:hover {
  background: var(--primary-bg);
  border-color: var(--primary);
}
.btn-new-folder .material-icons-outlined {
  font-size: 16px;
  color: var(--primary);
}
.folder-picker-new-row {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 8px 10px;
  margin-bottom: 8px;
  border: 1px dashed var(--primary);
  border-radius: var(--radius);
  background: var(--primary-bg);
}
.folder-picker-new-row .material-icons-outlined {
  font-size: 16px;
  color: var(--primary);
}
.folder-picker-new-parent {
  font-size: 12px;
  color: var(--text-muted);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  max-width: 40%;
}
.folder-picker-new-input {
  flex: 1;
  min-width: 0;
  border: none;
  outline: none;
  background: transparent;
  font-size: 13px;
  font-family: inherit;
  color: var(--text);
  padding: 2px 0;
}
.folder-picker-list {
  overflow-y: auto;
  flex: 1;
  border: 1px solid var(--border);
  border-radius: var(--radius);
  margin-bottom: 4px;
}
.folder-picker-item {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 9px 12px;
  font-size: 13px;
  cursor: pointer;
  color: var(--text);
  transition: background 0.1s;
}
.folder-picker-item:hover { background: var(--primary-bg); }
.folder-picker-item.active {
  background: var(--primary-bg);
  color: var(--primary-dark);
  font-weight: 500;
}
.folder-picker-item .material-icons-outlined { font-size: 16px; color: var(--primary); }
.folder-picker-empty {
  padding: 20px;
  text-align: center;
  font-size: 13px;
  color: var(--text-muted);
}

/* Prompt Modal */
.modal-overlay {
  position: fixed;
  inset: 0;
  z-index: 999;
  background: rgba(0,0,0,0.3);
  display: flex;
  align-items: center;
  justify-content: center;
}
.prompt-modal {
  background: var(--bg-card);
  padding: 24px;
  border-radius: var(--radius-lg);
  width: 340px;
  max-width: 90%;
  box-shadow: var(--shadow-md);
}
.prompt-modal h3 {
  margin-bottom: 16px;
  font-size: 16px;
  font-weight: 600;
}
.prompt-actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  margin-top: 20px;
}
.confirm-message {
  font-size: 14px;
  color: var(--text-secondary);
  line-height: 1.5;
  margin-top: -6px;
}
.prompt-modal .btn-danger {
  background: var(--danger);
  color: #fff;
}
.prompt-modal .btn-danger:hover {
  background: var(--danger);
  filter: brightness(0.92);
}

/* Mobile overlay */
.sidebar-overlay { display: none; }

@media (max-width: 768px) {
  .sidebar {
    position: fixed;
    left: -260px;
    top: 0;
    bottom: 0;
    z-index: 100;
    width: 240px;
    transition: left 0.2s ease;
  }
  .sidebar.mobile-open { left: 0; }
  .sidebar-overlay {
    display: block;
    position: fixed;
    inset: 0;
    z-index: 99;
    background: rgba(0,0,0,0.25);
  }
  .menu-toggle { display: flex; }
  /* single column on mobile */
  .waterfall-grid { flex-direction: column; }
  .waterfall-col { flex: none; width: 100%; }
  /* Header stays single row on mobile */
  .main-header {
    padding: 0 8px;
  }
  .editor-wrap { padding: 16px 14px; }
  .waterfall-view { padding: 10px 12px; }
  .search-results-view { padding: 14px 12px; }
  /* Search inputs stack vertically on small screens */
  .search-inputs-wrap { flex-direction: column; gap: 8px; }
  .search-inputs-wrap .input { min-width: unset; }
  /* Prevent iOS zoom on input focus by ensuring font-size >= 16px */
  .input,
  .header-title-input,
  .tag-inline-input,
  select { font-size: 16px !important; }
  /* Larger touch targets for context menu items */
  .context-menu-item {
    padding: 14px 16px;
  }
  /* Context menu max width on narrow screens */
  .context-menu {
    min-width: 140px;
    max-width: calc(100vw - 16px);
  }
  /* Wider cards on mobile since single column */
  .waterfall-card {
    border-radius: 10px;
    padding: 14px 16px;
  }
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
  background: rgba(255, 255, 255, 0.92);
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
.sidebar-action:disabled {
  opacity: 0.55;
  cursor: not-allowed;
  pointer-events: none;
}
</style>

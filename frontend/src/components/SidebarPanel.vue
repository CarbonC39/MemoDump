<template>
  <div v-if="mobileOpen" class="sidebar-overlay" @click="$emit('update:mobile-open', false)"></div>

  <aside class="sidebar" :class="{ 'mobile-open': mobileOpen }">
    <div class="sidebar-header">
      <img class="brand-icon" src="/favicon.ico" width="22" height="22" alt="Logo" />
      <span class="brand">{{ t('login.brand') }}</span>
      <button
        class="btn btn-icon btn-ghost theme-toggle-sidebar"
        :title="themeIcon === 'light_mode' ? 'Switch to light' : 'Switch to dark'"
        @click="$emit('toggle-theme')"
      >
        <span class="material-icons-outlined">{{ themeIcon }}</span>
      </button>
    </div>

    <div class="sidebar-scroll">
      <button class="sidebar-action" @click="$emit('new-note')">
        <span class="material-icons-outlined">edit_note</span>
        {{ t('sidebar.newNote') }}
      </button>

      <div class="sidebar-nav">
        <div class="nav-item" @click="$emit('open-search')">
          <span class="material-icons-outlined">search</span>
          <span class="nav-text">{{ t('sidebar.search') }}</span>
        </div>

        <div class="nav-item" :class="{ active: allNotesActive }" @click="$emit('open-all')">
          <span class="material-icons-outlined">sticky_note_2</span>
          <span class="nav-text">{{ t('sidebar.allNotes') }}</span>
        </div>

        <div class="nav-item storage-nav-item" @click="$emit('toggle-storage')">
          <span class="material-icons-outlined">folder_open</span>
          <span class="nav-text">{{ t('sidebar.storage') }}</span>
          <div class="storage-header-actions" @click.stop>
            <button class="fa-btn-sm" :title="t('modals.newFolder')" @click="$emit('new-folder', '')">
              <span class="material-icons-outlined">create_new_folder</span>
            </button>
            <button class="fa-btn-sm" :title="t('editor.newNote')" @click="$emit('new-note-in', '')">
              <span class="material-icons-outlined">note_add</span>
            </button>
            <button
              class="fa-btn-sm"
              :disabled="uploadingFiles"
              :title="uploadingFiles ? t('notes.importing') : t('notes.importMd')"
              @click="triggerFileInput"
            >
              <span class="material-icons-outlined">upload_file</span>
            </button>
            <input
              ref="fileInputRef"
              class="hidden-file-input"
              type="file"
              accept=".md,.txt"
              multiple
              @change="$emit('file-change', $event)"
            />
          </div>
          <span class="material-icons-outlined chevron" :class="{ expanded: storageExpanded }">
            chevron_right
          </span>
        </div>

        <div v-show="storageExpanded" class="nav-children">
          <div
            class="root-drop-zone"
            :class="{ 'drag-over': rootDropOver }"
            @dragover.prevent="$emit('update:root-drop-over', true)"
            @dragleave="$emit('update:root-drop-over', false)"
            @drop.prevent="$emit('drop-root', $event)"
          >
            <span class="material-icons-outlined">home</span>
            {{ t('notes.root') }}
          </div>

          <div v-if="folders.length === 0" class="empty-hint">{{ t('notes.noFolders') }}</div>

          <FolderNode
            v-for="folder in folders"
            :key="folder.path"
            :folder="folder"
            :active-folder="currentFolder"
            @select="$emit('select-folder', $event)"
            @new-folder="$emit('new-folder', $event)"
            @rename="$emit('rename-folder', $event)"
            @delete-folder="$emit('delete-folder', $event)"
            @open-note="$emit('open-note', $event)"
            @new-note="$emit('new-note-in', $event)"
            @drop-note="(...args) => $emit('drop-note', ...args)"
            @drop-folder="(...args) => $emit('drop-folder', ...args)"
            @expand="$emit('expand-folder', $event)"
          />
        </div>
      </div>
    </div>

    <div class="sidebar-footer">
      <div class="footer-icons">
        <button v-if="isWailsApp" class="btn btn-icon" :title="wailsDataDir" @click="$emit('change-data-dir')">
          <span class="material-icons-outlined">folder_open</span>
        </button>
        <button v-if="!serverNoAuth" class="btn btn-icon" :title="t('sidebar.signOut')" @click="$emit('logout')">
          <span class="material-icons-outlined">logout</span>
        </button>
        <button
          class="btn btn-icon"
          :class="{ active: settingsActive }"
          :title="t('sidebar.settings')"
          @click="$emit('toggle-settings')"
        >
          <span class="material-icons-outlined">settings</span>
        </button>
      </div>
      <div v-if="isLocalBuild" class="local-hint" :title="t('sidebar.savedInBrowserTitle')">
        <span class="material-icons-outlined">cloud_off</span>
        <span>{{ t('sidebar.savedInBrowser') }}</span>
      </div>
    </div>
  </aside>
</template>

<script setup>
import { ref } from 'vue'
import { useI18n } from '../i18n'
import FolderNode from './FolderNode.vue'

defineProps({
  mobileOpen: { type: Boolean, default: false },
  themeIcon: { type: String, required: true },
  allNotesActive: { type: Boolean, default: false },
  storageExpanded: { type: Boolean, default: false },
  rootDropOver: { type: Boolean, default: false },
  folders: { type: Array, default: () => [] },
  currentFolder: { type: String, default: '' },
  uploadingFiles: { type: Boolean, default: false },
  isWailsApp: { type: Boolean, default: false },
  wailsDataDir: { type: String, default: '' },
  serverNoAuth: { type: Boolean, default: false },
  settingsActive: { type: Boolean, default: false },
  isLocalBuild: { type: Boolean, default: false },
})
defineEmits([
  'update:mobile-open',
  'toggle-theme',
  'new-note',
  'open-search',
  'open-all',
  'toggle-storage',
  'new-folder',
  'new-note-in',
  'file-change',
  'update:root-drop-over',
  'drop-root',
  'select-folder',
  'rename-folder',
  'delete-folder',
  'open-note',
  'drop-note',
  'drop-folder',
  'expand-folder',
  'change-data-dir',
  'logout',
  'toggle-settings',
])

const { t } = useI18n()
const fileInputRef = ref(null)

function triggerFileInput() {
  fileInputRef.value?.click()
}
</script>

<style scoped>
.sidebar-overlay { display: none; }
.sidebar {
  display: flex;
  flex-direction: column;
  flex-shrink: 0;
  width: 240px;
  overflow: hidden;
  background: var(--bg-sidebar);
  border-right: 1px solid var(--border);
}
.sidebar-header {
  display: flex;
  align-items: center;
  flex-shrink: 0;
  gap: 10px;
  padding: 14px 16px;
  color: var(--text);
  font-size: 15px;
  font-weight: 700;
  border-bottom: 1px solid var(--border-light);
}
.brand-icon {
  margin-right: 8px;
  border-radius: 4px;
}
.brand { color: var(--waterfall-title); }
.sidebar-scroll {
  flex: 1;
  padding: 8px 0;
  overflow-y: auto;
}
.sidebar-action {
  display: flex;
  align-items: center;
  gap: 8px;
  width: calc(100% - 16px);
  margin: 0 8px 4px;
  padding: 8px 12px;
  color: var(--primary-dark);
  font-size: 13px;
  font-weight: 500;
  cursor: pointer;
  background: var(--primary-bg);
  border: none;
  border-radius: 8px;
  transition: background 0.15s, transform 0.1s;
}
.sidebar-action:hover { background: var(--border-light); }
.sidebar-action:active { transform: scale(0.98); }
.sidebar-action .material-icons-outlined {
  color: var(--primary);
  font-size: 18px;
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
  color: var(--text);
  font-size: 13px;
  font-weight: 500;
  cursor: pointer;
  border-radius: 6px;
  transition: background 0.1s;
}
.nav-item:hover,
.nav-item.active { background: var(--primary-bg); }
.nav-item.active { color: var(--primary-dark); }
.nav-item .material-icons-outlined {
  color: var(--text-secondary);
  font-size: 18px;
}
.nav-item.active .material-icons-outlined { color: var(--primary); }
.nav-text {
  flex: 1;
  overflow: hidden;
  white-space: nowrap;
  text-overflow: ellipsis;
}
.chevron {
  margin-left: auto;
  color: var(--text-muted) !important;
  font-size: 16px !important;
  transition: transform 0.2s;
}
.chevron.expanded { transform: rotate(90deg); }
.nav-children {
  margin-top: 4px;
  padding-right: 8px;
  padding-left: 12px;
}
.empty-hint {
  padding: 8px 4px;
  color: var(--text-muted);
  font-size: 12px;
}
.storage-nav-item { position: relative; }
.storage-header-actions {
  display: flex;
  gap: 2px;
  margin-right: 2px;
  opacity: 0;
  transition: opacity 0.1s;
}
.storage-nav-item:hover .storage-header-actions { opacity: 1; }
.fa-btn-sm {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 22px;
  height: 22px;
  color: var(--text-secondary);
  cursor: pointer;
  background: none;
  border: none;
  border-radius: 4px;
}
.fa-btn-sm:hover { background: var(--border); }
.fa-btn-sm .material-icons-outlined { font-size: 15px; }
.hidden-file-input { display: none; }
.root-drop-zone {
  display: flex;
  align-items: center;
  gap: 6px;
  margin-bottom: 4px;
  padding: 4px 8px;
  color: var(--text-muted);
  font-size: 12px;
  cursor: default;
  border: 1px dashed transparent;
  border-radius: 6px;
  transition: all 0.15s;
}
.root-drop-zone .material-icons-outlined { font-size: 15px; }
.root-drop-zone.drag-over {
  color: var(--primary-dark);
  background: var(--primary-bg);
  border-color: var(--primary);
}
.sidebar-footer {
  flex-shrink: 0;
  padding: 8px;
  border-top: 1px solid var(--border-light);
}
.footer-icons {
  display: flex;
  align-items: center;
  justify-content: space-evenly;
  padding: 8px;
}
.footer-icons .btn-icon { color: var(--text-secondary); }
.footer-icons .btn-icon:hover,
.footer-icons .btn-icon.active {
  color: var(--primary-dark);
  background: var(--primary-bg);
}
.local-hint {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 8px 12px;
  color: var(--text-muted);
  font-size: 12px;
  font-weight: 500;
  line-height: 1.4;
}
.local-hint .material-icons-outlined {
  color: var(--text-muted);
  font-size: 16px;
}

@media (max-width: 768px) {
  .sidebar {
    position: fixed;
    z-index: 100;
    top: 0;
    bottom: 0;
    left: -260px;
    width: 240px;
    transition: left 0.2s ease;
  }
  .sidebar.mobile-open { left: 0; }
  .sidebar-overlay {
    position: fixed;
    z-index: 99;
    inset: 0;
    display: block;
    background: rgba(0, 0, 0, 0.25);
  }
}
</style>

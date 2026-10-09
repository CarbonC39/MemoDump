<template>
  <header class="main-header">
    <button class="btn btn-icon btn-ghost menu-toggle" @click="$emit('toggle-mobile-menu')">
      <span class="material-icons-outlined">menu</span>
    </button>

    <template v-if="showSettings">
      <h2 class="settings-title">{{ t('settings.title') }}</h2>
      <div class="header-actions">
        <button class="btn btn-icon btn-ghost" @click="$emit('close-settings')">
          <span class="material-icons-outlined">close</span>
        </button>
      </div>
    </template>

    <NoteEditorHeader
      v-else-if="editing"
      :has-prev-page="hasPrevPage"
      :name="name"
      :folder="folder"
      :tags="tags"
      :tag-input="tagInput"
      :editor-mode="editorMode"
      :is-saving="isSaving"
      :can-delete="canDelete"
      :save-button-class="saveButtonClass"
      :save-button-title="saveButtonTitle"
      :save-problem="saveProblem"
      @back="$emit('back')"
      @update:name="$emit('update:name', $event)"
      @update:tags="$emit('update:tags', $event)"
      @update:tag-input="$emit('update:tag-input', $event)"
      @dirty="$emit('dirty')"
      @pick-folder="$emit('pick-folder')"
      @add-tag="$emit('add-tag')"
      @toggle-mode="$emit('toggle-mode')"
      @save="$emit('save')"
      @delete="$emit('delete')"
    />

    <template v-else-if="searchOpen">
      <div class="header-left">
        <h2 class="mode-title">{{ t('search.searchNotes') }}</h2>
      </div>
      <div class="header-actions">
        <button class="btn btn-icon btn-ghost" @click="$emit('close-search')">
          <span class="material-icons-outlined">close</span>
        </button>
      </div>
    </template>

    <template v-else>
      <div class="header-left">
        <span v-if="currentFolder" class="header-folder-display">
          <span class="material-icons-outlined folder-icon">folder_open</span>
          {{ currentFolder }}
        </span>
      </div>
      <BrowseHeaderActions
        :sort-mode="sortMode"
        @sort="$emit('sort', $event)"
        @new-note="$emit('new-note')"
      />
    </template>
  </header>
</template>

<script setup>
import { useI18n } from '../i18n'
import BrowseHeaderActions from './BrowseHeaderActions.vue'
import NoteEditorHeader from './NoteEditorHeader.vue'

defineProps({
  showSettings: { type: Boolean, default: false },
  editing: { type: Boolean, default: false },
  searchOpen: { type: Boolean, default: false },
  hasPrevPage: { type: Boolean, default: false },
  currentFolder: { type: String, default: '' },
  name: { type: String, default: '' },
  folder: { type: String, default: '' },
  tags: { type: Array, default: () => [] },
  tagInput: { type: String, default: '' },
  editorMode: { type: String, required: true },
  isSaving: { type: Boolean, default: false },
  canDelete: { type: Boolean, default: false },
  saveButtonClass: { type: String, required: true },
  saveButtonTitle: { type: String, default: '' },
  saveProblem: { type: Boolean, default: false },
  sortMode: { type: String, required: true },
})
defineEmits([
  'toggle-mobile-menu',
  'close-settings',
  'back',
  'update:name',
  'update:tags',
  'update:tag-input',
  'dirty',
  'pick-folder',
  'add-tag',
  'toggle-mode',
  'save',
  'delete',
  'close-search',
  'sort',
  'new-note',
])

const { t } = useI18n()
</script>

<style scoped>
.main-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  flex-shrink: 0;
  gap: 8px;
  height: var(--header-height);
  padding: 0 12px 0 8px;
  background: var(--bg-card);
  border-bottom: 1px solid var(--border);
}
.menu-toggle { display: none; }
.settings-title {
  flex: 1;
  margin: 0;
}
.mode-title { margin: 0; }
.header-left {
  display: flex;
  align-items: center;
  flex: 1;
  min-width: 0;
  gap: 6px;
}
.header-actions {
  display: flex;
  align-items: center;
  flex-shrink: 0;
  gap: 6px;
}
.header-folder-display {
  display: flex;
  align-items: center;
  gap: 4px;
  overflow: hidden;
  color: var(--text-secondary);
  font-size: 13px;
  white-space: nowrap;
  text-overflow: ellipsis;
}
.folder-icon {
  font-size: 16px;
  opacity: 0.6;
}

@media (max-width: 768px) {
  .menu-toggle { display: flex; }
  .main-header { padding: 0 8px; }
}
</style>

<template>
  <div v-if="visible" class="modal-overlay" @click.self="$emit('close')">
    <div class="folder-picker-modal">
      <div class="folder-picker-head">
        <h2 class="picker-title">{{ t('modals.moveToFolder') }}</h2>
        <button class="btn-new-folder" :title="t('modals.newFolder')" @click="$emit('start-create')">
          <span class="material-icons-outlined">create_new_folder</span>
          {{ t('modals.newFolder') }}
        </button>
      </div>
      <div v-if="newFolderActive" class="folder-picker-new-row">
        <span class="material-icons-outlined">create_new_folder</span>
        <span class="folder-picker-new-parent">{{ selected ? selected + '/' : '' }}</span>
        <input
          ref="newFolderInput"
          class="folder-picker-new-input"
          :value="newFolderName"
          :placeholder="t('modals.folderName')"
          @input="$emit('update:new-folder-name', $event.target.value)"
          @keydown.enter.prevent="$emit('submit-create')"
          @keydown.esc.prevent="$emit('cancel-create')"
        />
        <button class="fa-btn-sm" :title="t('modals.create')" @click="$emit('submit-create')">
          <span class="material-icons-outlined">check</span>
        </button>
        <button class="fa-btn-sm" :title="t('modals.cancel')" @click="$emit('cancel-create')">
          <span class="material-icons-outlined">close</span>
        </button>
      </div>
      <div class="folder-picker-list">
        <div
          class="folder-picker-item"
          :class="{ active: selected === '' }"
          @click="$emit('update:selected', '')"
        >
          <span class="material-icons-outlined">home</span>
          {{ t('notes.root') }}
        </div>
        <div
          v-for="folder in folders"
          :key="folder.path"
          class="folder-picker-item"
          :class="{ active: selected === folder.path }"
          :style="{ paddingLeft: `${12 + folder.depth * 16}px` }"
          @click="$emit('update:selected', folder.path)"
        >
          <span class="material-icons-outlined">folder</span>
          {{ folder.name }}
        </div>
        <div v-if="folders.length === 0" class="folder-picker-empty">{{ t('notes.noFolders') }}</div>
      </div>
      <div class="prompt-actions">
        <button class="btn btn-ghost" @click="$emit('close')">{{ t('modals.cancel') }}</button>
        <button class="btn btn-primary" @click="$emit('confirm')">{{ t('modals.moveHere') }}</button>
      </div>
    </div>
  </div>
</template>

<script setup>
import { nextTick, ref, watch } from 'vue'
import { useI18n } from '../i18n'

const props = defineProps({
  visible: { type: Boolean, default: false },
  selected: { type: String, default: '' },
  newFolderActive: { type: Boolean, default: false },
  newFolderName: { type: String, default: '' },
  folders: { type: Array, default: () => [] },
})
defineEmits([
  'close',
  'confirm',
  'start-create',
  'cancel-create',
  'submit-create',
  'update:selected',
  'update:new-folder-name',
])

const { t } = useI18n()
const newFolderInput = ref(null)

watch(() => props.newFolderActive, (active) => {
  if (active) nextTick(() => newFolderInput.value?.focus())
})
</script>

<style scoped>
.modal-overlay {
  position: fixed;
  z-index: 999;
  inset: 0;
  display: flex;
  align-items: center;
  justify-content: center;
  background: rgba(0, 0, 0, 0.3);
}
.folder-picker-modal {
  display: flex;
  flex-direction: column;
  width: 320px;
  max-width: 92vw;
  max-height: 80vh;
  padding: 24px;
  background: var(--bg-card);
  border-radius: var(--radius-lg);
  box-shadow: var(--shadow-md);
}
.folder-picker-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 12px;
}
.picker-title { margin: 0; }
.btn-new-folder {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  padding: 4px 8px;
  color: var(--primary-dark);
  font-size: 12px;
  font-weight: 500;
  cursor: pointer;
  background: transparent;
  border: 1px solid var(--border);
  border-radius: var(--radius);
  transition: background 0.12s, border-color 0.12s;
}
.btn-new-folder:hover {
  background: var(--primary-bg);
  border-color: var(--primary);
}
.btn-new-folder .material-icons-outlined {
  color: var(--primary);
  font-size: 16px;
}
.folder-picker-new-row {
  display: flex;
  align-items: center;
  gap: 6px;
  margin-bottom: 8px;
  padding: 8px 10px;
  background: var(--primary-bg);
  border: 1px dashed var(--primary);
  border-radius: var(--radius);
}
.folder-picker-new-row .material-icons-outlined {
  color: var(--primary);
  font-size: 16px;
}
.folder-picker-new-parent {
  max-width: 40%;
  overflow: hidden;
  color: var(--text-muted);
  font-size: 12px;
  white-space: nowrap;
  text-overflow: ellipsis;
}
.folder-picker-new-input {
  flex: 1;
  min-width: 0;
  padding: 2px 0;
  color: var(--text);
  font-family: inherit;
  font-size: 13px;
  background: transparent;
  border: none;
  outline: none;
}
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
.folder-picker-list {
  flex: 1;
  margin-bottom: 4px;
  overflow-y: auto;
  border: 1px solid var(--border);
  border-radius: var(--radius);
}
.folder-picker-item {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 9px 12px;
  color: var(--text);
  font-size: 13px;
  cursor: pointer;
  transition: background 0.1s;
}
.folder-picker-item:hover { background: var(--primary-bg); }
.folder-picker-item.active {
  color: var(--primary-dark);
  font-weight: 500;
  background: var(--primary-bg);
}
.folder-picker-item .material-icons-outlined {
  color: var(--primary);
  font-size: 16px;
}
.folder-picker-empty {
  padding: 20px;
  color: var(--text-muted);
  font-size: 13px;
  text-align: center;
}
.prompt-actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  margin-top: 20px;
}

@media (max-width: 768px) {
  .folder-picker-new-input { font-size: 16px; }
}
</style>

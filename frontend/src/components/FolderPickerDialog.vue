<template>
  <div v-if="visible" class="modal-overlay" @click.self="$emit('close')">
    <div class="folder-picker-modal" role="dialog" aria-modal="true" aria-labelledby="folder-picker-title">
      <div class="folder-picker-head">
        <h2 id="folder-picker-title" class="picker-title">
          {{ mode === 'move' ? t('modals.moveToFolder') : t('modals.selectFolder') }}
        </h2>
        <button class="picker-close" :title="t('modals.close')" :aria-label="t('modals.close')" @click="$emit('close')">
          <span class="material-icons-outlined">close</span>
        </button>
      </div>

      <div class="folder-picker-destination">
        <span class="material-icons-outlined" aria-hidden="true">near_me</span>
        <span class="destination-copy">
          <span class="destination-label">{{ t('modals.destination') }}</span>
          <strong>{{ selectedPathLabel }}</strong>
        </span>
      </div>

      <div class="folder-picker-list">
        <button
          type="button"
          class="folder-picker-item"
          :class="{ active: selected === '' }"
          @click="$emit('update:selected', '')"
        >
          <span class="material-icons-outlined folder-picker-icon root-icon">home</span>
          <span class="folder-picker-name">{{ t('notes.root') }}</span>
          <span v-if="selected === ''" class="material-icons-outlined selected-check">check_circle</span>
        </button>
        <button
          v-for="folder in folders"
          :key="folder.path"
          type="button"
          class="folder-picker-item"
          :class="[{ active: selected === folder.path }, `folder-tone-${folder.depth % 3}`]"
          :style="{ paddingLeft: `${14 + folder.depth * 20}px` }"
          @click="$emit('update:selected', folder.path)"
        >
          <span class="material-icons-outlined folder-picker-icon">folder</span>
          <span class="folder-picker-name">{{ folder.name }}</span>
          <span v-if="selected === folder.path" class="material-icons-outlined selected-check">check_circle</span>
        </button>
        <div v-if="folders.length === 0" class="folder-picker-empty">{{ t('notes.noFolders') }}</div>
      </div>

      <button v-if="!newFolderActive" type="button" class="btn-new-folder" @click="$emit('start-create')">
        <span class="material-icons-outlined">create_new_folder</span>
        {{ t('modals.newFolderIn').replace('{folder}', selectedLabel) }}
      </button>
      <div v-else class="folder-picker-new-row">
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

      <div class="prompt-actions">
        <button class="btn btn-ghost" @click="$emit('close')">{{ t('modals.cancel') }}</button>
        <button class="btn btn-primary" :disabled="sameDestination" @click="$emit('confirm')">
          {{ mode === 'move' ? t('modals.moveHere') : t('modals.selectHere') }}
        </button>
      </div>
    </div>
  </div>
</template>

<script setup>
import { computed, nextTick, ref, watch } from 'vue'
import { useI18n } from '../i18n'

const props = defineProps({
  visible: { type: Boolean, default: false },
  selected: { type: String, default: '' },
  newFolderActive: { type: Boolean, default: false },
  newFolderName: { type: String, default: '' },
  folders: { type: Array, default: () => [] },
  mode: { type: String, default: 'move' },
  currentFolder: { type: String, default: '' },
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
const selectedLabel = computed(() => props.selected || t('notes.root'))
const selectedPathLabel = computed(() => {
  if (!props.selected) return t('notes.root')
  return `${t('notes.root')} / ${props.selected.split('/').join(' / ')}`
})
const sameDestination = computed(() => props.mode === 'move' && props.selected === props.currentFolder)

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
  width: 420px;
  max-width: calc(100vw - 24px);
  max-height: min(640px, calc(100vh - 40px));
  padding: 22px;
  background: var(--bg-card);
  border-radius: var(--radius-lg);
  box-shadow: 0 18px 48px rgba(30, 41, 59, 0.18);
}
.folder-picker-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 16px;
}
.picker-title { margin: 0; color: var(--text); font-size: 20px; }
.picker-close {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 32px;
  height: 32px;
  color: var(--text-muted);
  border-radius: 50%;
}
.picker-close:hover { color: var(--text); background: var(--border-light); }
.picker-close .material-icons-outlined { font-size: 20px; }
.folder-picker-destination {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-bottom: 14px;
  padding: 10px 12px;
  color: var(--md-link);
  background: var(--md-quote-bg);
  border-radius: var(--radius);
}
.folder-picker-destination > .material-icons-outlined { font-size: 19px; }
.destination-copy { display: flex; min-width: 0; flex-direction: column; gap: 1px; }
.destination-label { color: var(--text-muted); font-size: 11px; }
.destination-copy strong {
  overflow: hidden;
  color: var(--text-secondary);
  font-size: 13px;
  font-weight: 600;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.btn-new-folder {
  display: flex;
  align-items: center;
  gap: 7px;
  width: 100%;
  margin-top: 8px;
  padding: 8px 10px;
  color: var(--md-link);
  font-size: 12px;
  font-weight: 500;
  cursor: pointer;
  background: transparent;
  border: none;
  border-radius: var(--radius);
  transition: background 0.12s;
}
.btn-new-folder:hover { background: var(--md-quote-bg); }
.btn-new-folder .material-icons-outlined {
  font-size: 18px;
}
.folder-picker-new-row {
  display: flex;
  align-items: center;
  gap: 6px;
  margin-top: 8px;
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
  min-height: 190px;
  max-height: 340px;
  overflow-y: auto;
  border-top: 1px solid var(--border-light);
  border-bottom: 1px solid var(--border-light);
}
.folder-picker-item {
  display: flex;
  width: 100%;
  align-items: center;
  gap: 8px;
  min-height: 40px;
  padding: 8px 12px;
  color: var(--text);
  font-size: 13px;
  cursor: pointer;
  text-align: left;
  border-radius: 7px;
  transition: background 0.1s, color 0.1s;
}
.folder-picker-item:hover { background: var(--border-light); }
.folder-picker-item.active {
  color: var(--primary-dark);
  font-weight: 600;
  background: var(--primary-bg);
}
.folder-picker-icon {
  flex-shrink: 0;
  color: var(--primary);
  font-size: 19px;
}
.folder-tone-1 .folder-picker-icon { color: var(--md-link); }
.folder-tone-2 .folder-picker-icon { color: var(--folder-tone-teal); }
.root-icon { color: var(--folder-tone-orange); }
.folder-picker-name { flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.selected-check { flex-shrink: 0; color: var(--primary); font-size: 18px; }
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
  margin-top: 18px;
}
.prompt-actions .btn-primary:disabled { cursor: default; opacity: 0.45; }

@media (max-width: 768px) {
  .folder-picker-modal { padding: 18px; }
  .folder-picker-new-input { font-size: 16px; }
}
</style>

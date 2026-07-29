<template>
  <button
    class="btn btn-icon btn-ghost editor-back-btn"
    :title="hasPrevPage ? t('editor.back') : t('editor.allNotes')"
    @click="$emit('back')"
  >
    <span class="material-icons-outlined">{{ hasPrevPage ? 'arrow_back' : 'home' }}</span>
  </button>

  <div class="header-meta-scroll">
    <input
      class="header-title-input"
      :value="name"
      :style="{ width: titleInputWidth + 'px' }"
      :placeholder="t('editor.untitled')"
      @input="updateName"
    />
    <span ref="titleMirrorRef" class="header-title-mirror" aria-hidden="true">
      {{ name || t('editor.untitled') }}
    </span>
    <span class="header-meta-sep">·</span>
    <button class="note-folder-btn" @click="$emit('pick-folder')">
      <span class="material-icons-outlined">{{ folder ? 'folder' : 'home' }}</span>
      <span class="note-folder-label">{{ folder || t('notes.root') }}</span>
    </button>
    <span class="header-meta-sep">·</span>
    <div class="note-tags-inline">
      <span v-for="(item, index) in tags" :key="`${item}-${index}`" class="tag">
        {{ item }}<span class="remove" @click="removeTag(index)">×</span>
      </span>
      <input
        class="tag-inline-input"
        :value="tagInput"
        :placeholder="t('notes.tagPlaceholder')"
        @input="$emit('update:tag-input', $event.target.value)"
        @keydown.enter.prevent="$emit('add-tag')"
      />
    </div>
  </div>

  <div class="header-actions">
    <button
      class="btn btn-sm btn-icon btn-ghost"
      :title="editorMode === 'wysiwyg' ? t('editor.switchToRaw') : t('editor.switchToRich')"
      @click="$emit('toggle-mode')"
    >
      <span class="material-icons-outlined action-icon">
        {{ editorMode === 'wysiwyg' ? 'code' : 'visibility' }}
      </span>
    </button>
    <button
      class="save-btn"
      :class="saveButtonClass"
      :disabled="isSaving"
      :title="saveButtonTitle"
      @click="$emit('save')"
    >
      <span v-if="saveProblem" class="material-icons-outlined save-btn-icon">cloud_off</span>
      {{ t('editor.save') }}
    </button>
    <button
      v-if="canDelete"
      class="btn btn-sm btn-icon btn-danger-subtle"
      :title="t('editor.deleteNote')"
      @click="$emit('delete')"
    >
      <span class="material-icons-outlined action-icon">delete_outline</span>
    </button>
  </div>
</template>

<script setup>
import { nextTick, ref, watch } from 'vue'
import { useI18n } from '../i18n'

const props = defineProps({
  hasPrevPage: { type: Boolean, default: false },
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
})
const emit = defineEmits([
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
])

const { t } = useI18n()
const titleMirrorRef = ref(null)
const titleInputWidth = ref(80)

function updateTitleInputWidth() {
  if (!titleMirrorRef.value) return
  titleInputWidth.value = Math.max(60, titleMirrorRef.value.scrollWidth + 12)
}

function updateName(event) {
  emit('update:name', event.target.value)
  emit('dirty')
}

function removeTag(index) {
  emit('update:tags', props.tags.filter((_, itemIndex) => itemIndex !== index))
  emit('dirty')
}

watch(() => props.name, () => nextTick(updateTitleInputWidth), { immediate: true })
</script>

<style scoped>
.editor-back-btn {
  display: flex;
  flex-shrink: 0;
}
.header-meta-scroll {
  display: flex;
  align-items: center;
  flex: 1;
  min-width: 0;
  overflow-x: auto;
  overflow-y: hidden;
  scrollbar-width: none;
  -webkit-overflow-scrolling: touch;
}
.header-meta-scroll::-webkit-scrollbar { display: none; }
.header-title-input {
  flex-shrink: 0;
  min-width: 60px;
  padding: 4px;
  color: var(--text);
  font-family: inherit;
  font-size: 14px;
  font-weight: 600;
  caret-color: var(--primary);
  background: transparent;
  border: none;
  outline: none;
  transition: width 0.1s ease;
}
.header-title-mirror {
  position: absolute;
  visibility: hidden;
  padding: 4px;
  font-family: inherit;
  font-size: 14px;
  font-weight: 600;
  white-space: pre;
  pointer-events: none;
}
.header-title-input::placeholder { color: var(--text-muted); }
.header-meta-sep {
  flex-shrink: 0;
  margin: 0 6px;
  color: var(--border);
  font-size: 14px;
  user-select: none;
}
.note-folder-btn {
  display: inline-flex;
  align-items: center;
  flex-shrink: 0;
  gap: 4px;
  padding: 2px 8px 2px 4px;
  color: var(--text-secondary);
  font-family: inherit;
  font-size: 13px;
  font-weight: 500;
  cursor: pointer;
  background: transparent;
  border: none;
  border-radius: 5px;
  transition: background 0.12s, color 0.12s;
}
.note-folder-btn:hover {
  color: var(--primary-dark);
  background: var(--primary-bg);
}
.note-folder-btn .material-icons-outlined {
  color: var(--primary);
  font-size: 14px;
}
.note-folder-label {
  max-width: 140px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.note-tags-inline {
  display: flex;
  align-items: center;
  flex-wrap: nowrap;
  flex-shrink: 0;
  gap: 4px;
}
.note-tags-inline .tag { font-size: 13px; }
.tag-inline-input {
  flex-shrink: 0;
  width: 48px;
  padding: 2px 4px;
  color: var(--text-muted);
  font-family: inherit;
  font-size: 13px;
  font-weight: 500;
  background: transparent;
  border: none;
  outline: none;
  transition: color 0.12s, width 0.15s;
}
.tag-inline-input::placeholder {
  color: var(--text-muted);
  opacity: 0.6;
}
.tag-inline-input:focus {
  width: 72px;
  color: var(--primary-dark);
}
.header-actions {
  display: flex;
  align-items: center;
  flex-shrink: 0;
  gap: 6px;
}
.action-icon { font-size: 16px; }
.btn-danger-subtle { color: var(--text-muted); }
.btn-danger-subtle:hover {
  color: var(--danger);
  background: var(--danger-light);
}
.save-btn {
  display: inline-flex;
  align-items: center;
  flex-shrink: 0;
  gap: 4px;
  padding: 5px 14px;
  font-size: 13px;
  font-weight: 700;
  cursor: pointer;
  border-radius: 100px;
  transition: background 0.15s, color 0.15s, border-color 0.15s;
}
.save-btn:disabled {
  cursor: wait;
  opacity: 0.65;
}
.save-btn-clean {
  color: var(--primary-dark);
  background: transparent;
  border: 1.5px solid var(--primary);
}
.save-btn-clean:hover { background: var(--primary-bg); }
.save-btn-dirty {
  color: #fff;
  background: var(--primary);
  border: 1.5px solid var(--primary);
}
.save-btn-dirty:hover {
  background: var(--primary-dark);
  border-color: var(--primary-dark);
}
.save-btn-icon { font-size: 14px; }

@media (max-width: 768px) {
  .header-title-input,
  .tag-inline-input { font-size: 16px !important; }
}
</style>

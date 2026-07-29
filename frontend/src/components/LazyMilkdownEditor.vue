<template>
  <component
    :is="editorComponent"
    v-if="editorComponent"
    :initial-content="initialContent"
    @update="$emit('update', $event)"
    @error="handleInitError"
  />
  <div v-else-if="loadError" class="editor-load-state editor-load-error" role="alert">
    <span class="material-icons-outlined">error_outline</span>
    <p>{{ t('editor.richEditorLoadFailed') }}</p>
    <div class="editor-load-actions">
      <button class="btn btn-primary" @click="loadEditor">{{ t('editor.retry') }}</button>
      <button class="btn btn-ghost" @click="$emit('fallback-raw')">{{ t('editor.openRaw') }}</button>
    </div>
  </div>
  <div v-else class="editor-load-state" aria-live="polite">
    <span class="editor-load-spinner" aria-hidden="true"></span>
    <p>{{ t('editor.loadingRichEditor') }}</p>
    <button class="btn btn-ghost" @click="$emit('fallback-raw')">{{ t('editor.openRaw') }}</button>
  </div>
</template>

<script setup>
import { shallowRef, ref, onMounted } from 'vue'
import { useI18n } from '../i18n'

defineProps({
  initialContent: { type: String, default: '' },
})
defineEmits(['update', 'fallback-raw'])

const { t } = useI18n()
const editorComponent = shallowRef(null)
const loadError = ref(null)
let loadGeneration = 0

async function loadEditor() {
  const generation = ++loadGeneration
  editorComponent.value = null
  loadError.value = null
  try {
    const module = await import('./MilkdownEditor.vue')
    if (generation === loadGeneration) editorComponent.value = module.default
  } catch (error) {
    if (generation === loadGeneration) loadError.value = error
  }
}

function handleInitError(error) {
  editorComponent.value = null
  loadError.value = error || new Error('Milkdown initialization failed')
}

onMounted(loadEditor)
</script>

<style scoped>
.editor-load-state {
  flex: 1;
  min-height: 240px;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 12px;
  color: var(--text-muted);
}
.editor-load-error .material-icons-outlined {
  font-size: 32px;
  color: var(--danger);
}
.editor-load-state p {
  margin: 0;
}
.editor-load-actions {
  display: flex;
  gap: 8px;
}
.editor-load-spinner {
  width: 24px;
  height: 24px;
  border: 2px solid var(--border);
  border-top-color: var(--primary);
  border-radius: 50%;
  animation: editor-load-spin 0.8s linear infinite;
}
@keyframes editor-load-spin {
  to { transform: rotate(360deg); }
}
</style>

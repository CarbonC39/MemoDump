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
      <button class="btn btn-icon btn-ghost" :title="t('editor.openRaw')" :aria-label="t('editor.openRaw')" @click="$emit('fallback-raw')">
        <span class="material-icons-outlined">code</span>
      </button>
    </div>
  </div>
  <div v-else class="editor-load-state" aria-busy="true">
    <span class="editor-load-spinner" aria-hidden="true"></span>
    <button
      v-if="showRawFallback"
      class="btn btn-icon btn-ghost delayed-raw-button"
      :title="t('editor.openRaw')"
      :aria-label="t('editor.openRaw')"
      @click="$emit('fallback-raw')"
    >
      <span class="material-icons-outlined">code</span>
    </button>
  </div>
</template>

<script setup>
import { shallowRef, ref, onMounted, onBeforeUnmount } from 'vue'
import { useI18n } from '../i18n'
import { preloadMilkdownEditor } from './milkdownLoader'

defineProps({
  initialContent: { type: String, default: '' },
})
defineEmits(['update', 'fallback-raw'])

const { t } = useI18n()
const editorComponent = shallowRef(null)
const loadError = ref(null)
const showRawFallback = ref(false)
let loadGeneration = 0
let fallbackTimer = null

function startFallbackTimer() {
  clearTimeout(fallbackTimer)
  showRawFallback.value = false
  fallbackTimer = setTimeout(() => { showRawFallback.value = true }, 1800)
}

async function loadEditor() {
  const generation = ++loadGeneration
  editorComponent.value = null
  loadError.value = null
  startFallbackTimer()
  try {
    const module = await preloadMilkdownEditor()
    if (generation === loadGeneration) {
      clearTimeout(fallbackTimer)
      editorComponent.value = module.default
    }
  } catch (error) {
    if (generation === loadGeneration) {
      clearTimeout(fallbackTimer)
      loadError.value = error
    }
  }
}

function handleInitError(error) {
  editorComponent.value = null
  loadError.value = error || new Error('Milkdown initialization failed')
}

onMounted(loadEditor)
onBeforeUnmount(() => clearTimeout(fallbackTimer))
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
.editor-load-actions {
  display: flex;
  gap: 8px;
}
.delayed-raw-button {
  position: absolute;
  margin-top: 72px;
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

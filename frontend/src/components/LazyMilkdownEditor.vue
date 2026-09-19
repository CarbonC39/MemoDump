<template>
  <div v-if="editorComponent" class="editor-host">
    <component
      :is="editorComponent"
      :document-version="documentVersion"
      :initial-content="initialContent"
      :active="active"
      @update="$emit('update', $event)"
      @document-ready="$emit('document-ready', $event)"
      @error="handleInitError"
      @ready="handleReady"
    />
    <div v-if="!editorReady" class="editor-load-state editor-load-overlay" aria-busy="true">
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
  </div>
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
  documentVersion: { type: Number, required: true },
  initialContent: { type: String, default: '' },
  active: { type: Boolean, default: true },
})
defineEmits(['update', 'document-ready', 'fallback-raw'])

const { t } = useI18n()
const editorComponent = shallowRef(null)
const loadError = ref(null)
const showRawFallback = ref(false)
const editorReady = ref(false)
let loadGeneration = 0
let fallbackTimer = null
const RAW_FALLBACK_DELAY_MS = 6000

function startFallbackTimer() {
  clearTimeout(fallbackTimer)
  showRawFallback.value = false
  fallbackTimer = setTimeout(() => { showRawFallback.value = true }, RAW_FALLBACK_DELAY_MS)
}

async function loadEditor() {
  const generation = ++loadGeneration
  editorComponent.value = null
  editorReady.value = false
  loadError.value = null
  startFallbackTimer()
  try {
    const module = await preloadMilkdownEditor()
    if (generation === loadGeneration) {
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
  clearTimeout(fallbackTimer)
  editorReady.value = false
  editorComponent.value = null
  loadError.value = error || new Error('Milkdown initialization failed')
}

function handleReady() {
  clearTimeout(fallbackTimer)
  editorReady.value = true
}

onMounted(loadEditor)
onBeforeUnmount(() => clearTimeout(fallbackTimer))
</script>

<style scoped>
.editor-load-state {
  flex: 1;
  min-height: 240px;
  position: relative;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 12px;
  color: var(--text-muted);
}
.editor-host {
  position: relative;
  display: flex;
  flex: 1 0 auto;
  min-height: 100%;
}
.editor-load-overlay {
  position: absolute;
  inset: 0;
  z-index: 1;
  min-height: 0;
  background: var(--bg-card);
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

<template>
  <div class="editor-wrap">
    <LazyMilkdownEditor
      v-if="richEditorMounted"
      v-show="mode === 'wysiwyg'"
      :document-version="editorKey"
      :initial-content="initialContent"
      @update="$emit('update', $event)"
      @document-ready="$emit('document-ready', $event)"
      @fallback-raw="$emit('update:mode', 'raw')"
    />
    <textarea
      ref="rawEditor"
      v-show="mode === 'raw'"
      class="raw-editor"
      :value="content"
      :placeholder="t('editor.rawMarkdown')"
      @input="handleRawInput"
    ></textarea>
  </div>
</template>

<script setup>
import { nextTick, onMounted, ref, watch } from 'vue'
import LazyMilkdownEditor from './LazyMilkdownEditor.vue'
import { fitTextareaToContent } from './rawEditorLayout'
import { useI18n } from '../i18n'

const props = defineProps({
  mode: { type: String, required: true },
  editorKey: { type: Number, required: true },
  initialContent: { type: String, default: '' },
  content: { type: String, default: '' },
})
const emit = defineEmits(['update', 'document-ready', 'update:mode', 'update:content'])

const { t } = useI18n()
const richEditorMounted = ref(props.mode === 'wysiwyg')
const rawEditor = ref(null)

function resizeRawEditor() {
  const element = rawEditor.value
  if (!element || props.mode !== 'raw') return
  fitTextareaToContent(element)
}

function handleRawInput(event) {
  resizeRawEditor()
  emit('update:content', event.target.value)
}

watch(() => props.mode, (mode) => {
  if (mode === 'wysiwyg') richEditorMounted.value = true
  else nextTick(resizeRawEditor)
})

watch(() => props.content, () => nextTick(resizeRawEditor))
onMounted(() => nextTick(resizeRawEditor))
</script>

<style scoped>
.editor-wrap {
  max-width: 860px;
  margin: 0 auto;
  padding: 20px 60px;
  background: var(--bg-card);
  min-height: 100%;
  display: flex;
  flex-direction: column;
}
.raw-editor {
  flex: 1 0 auto;
  width: 100%;
  height: auto;
  min-height: 0;
  border: none;
  outline: none;
  resize: none;
  overflow-y: hidden;
  padding: 16px;
  font-family: var(--editor-font-monospace);
  font-size: var(--editor-raw-font-size);
  line-height: 1.7;
  color: var(--text);
  background: var(--bg-card);
}
@media (max-width: 768px) {
  .editor-wrap { padding: 16px 14px; }
}
</style>

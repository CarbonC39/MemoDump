<template>
  <div ref="editorEl" class="crepe-editor"></div>
</template>

<script setup>
import { ref, onMounted, onBeforeUnmount } from 'vue'
import { Crepe } from '@milkdown/crepe'
import '@milkdown/crepe/theme/common/style.css'
import '@milkdown/crepe/theme/frame.css'

const props = defineProps({
  initialContent: { type: String, default: '' },
})

const emit = defineEmits(['update'])
const editorEl = ref(null)
let crepeInstance = null

onMounted(async () => {
  if (!editorEl.value) return

  crepeInstance = new Crepe({
    root: editorEl.value,
    defaultValue: props.initialContent,
    features: {
      'latex': false,
    },
    featureConfigs: {
      'placeholder': {
        text: 'Type / to open the quick menu, or just start typing...',
      }
    }
  })

  crepeInstance.on((listener) => {
    listener.markdownUpdated((_, markdown) => {
      emit('update', markdown)
    })
  })

  await crepeInstance.create()
})

onBeforeUnmount(() => {
  if (crepeInstance) {
    crepeInstance.destroy()
  }
})
</script>

<style scoped>
.crepe-editor {
  flex: 1;
  overflow-y: auto;
  min-height: 0;
}
.crepe-editor :deep(.milkdown) {
  height: 100%;
}
.crepe-editor :deep(.editor) {
  min-height: 100%;
  outline: none;
}

/* ===== Fix Milkdown heading display anomaly ===== */
/* Reset any conflicting styles on heading nodes */
.crepe-editor :deep(.editor h1),
.crepe-editor :deep(.editor h2),
.crepe-editor :deep(.editor h3),
.crepe-editor :deep(.editor h4),
.crepe-editor :deep(.editor h5),
.crepe-editor :deep(.editor h6) {
  display: block;
  visibility: visible;
  opacity: 1;
  position: relative;
  white-space: normal;
  word-break: break-word;
  overflow-wrap: break-word;
  /* Ensure heading markers don't get swallowed */
  min-height: 1em;
}
/* Fix heading prefix # marker display (sometimes hidden by Crepe) */
.crepe-editor :deep(.editor h1::before),
.crepe-editor :deep(.editor h2::before),
.crepe-editor :deep(.editor h3::before),
.crepe-editor :deep(.editor h4::before) {
  display: none;
}

/* ===== Fix ordered list number alignment ===== */
.crepe-editor :deep(.editor ol) {
  list-style-type: decimal;
  padding-left: 1.6em;
  margin: 0.4em 0;
}
.crepe-editor :deep(.editor ol li) {
  padding-left: 0.2em;
  margin-bottom: 0.15em;
  line-height: 1.7;
}
/* Nested ordered lists */
.crepe-editor :deep(.editor ol ol) {
  list-style-type: lower-alpha;
  margin: 0.1em 0;
}

/* ===== Fix unordered list alignment ===== */
.crepe-editor :deep(.editor ul) {
  list-style-type: disc;
  padding-left: 1.6em;
  margin: 0.4em 0;
}
.crepe-editor :deep(.editor ul li) {
  padding-left: 0.2em;
  margin-bottom: 0.15em;
  line-height: 1.7;
}
.crepe-editor :deep(.editor ul ul) {
  list-style-type: circle;
  margin: 0.1em 0;
}

/* ===== Fix task list / checkbox alignment ===== */
.crepe-editor :deep(.editor ul[data-type="taskList"]),
.crepe-editor :deep(.editor .task-list-item),
.crepe-editor :deep(.editor li[data-type="taskItem"]) {
  list-style: none;
  padding-left: 0;
  position: relative;
}
.crepe-editor :deep(.editor ul[data-type="taskList"]) {
  padding-left: 0.2em;
}
/* The checkbox input inside task list items */
.crepe-editor :deep(.editor li[data-type="taskItem"] > label),
.crepe-editor :deep(.editor .task-list-item > label) {
  display: inline-flex;
  align-items: center;
  margin-right: 0.4em;
  vertical-align: middle;
}
.crepe-editor :deep(.editor li[data-type="taskItem"] input[type="checkbox"]),
.crepe-editor :deep(.editor .task-list-item input[type="checkbox"]) {
  margin: 0 0.4em 0 0;
  width: 15px;
  height: 15px;
  vertical-align: middle;
  position: relative;
  top: -1px;
  accent-color: var(--primary, #6495ED);
}
/* Ensure text after checkbox is aligned to baseline */
.crepe-editor :deep(.editor li[data-type="taskItem"] > div),
.crepe-editor :deep(.editor .task-list-item > div) {
  display: inline;
}

/* ===== General list item paragraph spacing ===== */
.crepe-editor :deep(.editor li p) {
  margin: 0;
  line-height: 1.7;
}

/* ===== Fix blockquote spacing ===== */
.crepe-editor :deep(.editor blockquote) {
  margin: 0.5em 0;
  padding: 0.2em 0 0.2em 1em;
}
.crepe-editor :deep(.editor blockquote p) {
  margin: 0.15em 0;
}

/* ===== Fix code block spacing ===== */
.crepe-editor :deep(.editor pre) {
  margin: 0.6em 0;
}

/* ===== Fix horizontal rule spacing ===== */
.crepe-editor :deep(.editor hr) {
  margin: 1em 0;
}

/* ===== Fix table cell alignment ===== */
.crepe-editor :deep(.editor table) {
  border-spacing: 0;
}
.crepe-editor :deep(.editor th),
.crepe-editor :deep(.editor td) {
  vertical-align: top;
  line-height: 1.6;
}

/* ===== Mobile: prevent zoom on editor focus ===== */
@media (max-width: 768px) {
  .crepe-editor :deep(.editor) {
    font-size: 16px;
  }
  /* Ensure Milkdown input elements don't trigger zoom */
  .crepe-editor :deep(input),
  .crepe-editor :deep(textarea),
  .crepe-editor :deep(select) {
    font-size: 16px !important;
  }
}
</style>

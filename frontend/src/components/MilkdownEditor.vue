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
let _destroyed = false

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
      if (!_destroyed) emit('update', markdown)
    })
  })

  try {
    await crepeInstance.create()
  } catch (e) {
    // Editor creation failed (e.g. component was unmounted mid-creation)
    if (crepeInstance) { crepeInstance.destroy(); crepeInstance = null }
    return
  }

  // Component was unmounted while creation was in-flight
  if (_destroyed && crepeInstance) {
    crepeInstance.destroy()
    crepeInstance = null
  }
})

onBeforeUnmount(() => {
  _destroyed = true
  if (crepeInstance) {
    crepeInstance.destroy()
    crepeInstance = null
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
  min-height: 1em;
}
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

/* ===== Fix heading font sizes ===== */
.crepe-editor :deep(.editor h1) { font-size: 2em;   font-weight: 700; line-height: 1.25; margin: 0.6em 0 0.3em; }
.crepe-editor :deep(.editor h2) { font-size: 1.5em;  font-weight: 700; line-height: 1.3;  margin: 0.6em 0 0.3em; }
.crepe-editor :deep(.editor h3) { font-size: 1.25em; font-weight: 600; line-height: 1.35; margin: 0.5em 0 0.25em; }
.crepe-editor :deep(.editor h4) { font-size: 1.1em;  font-weight: 600; line-height: 1.4;  margin: 0.4em 0 0.2em; }
.crepe-editor :deep(.editor h5) { font-size: 1em;    font-weight: 600; line-height: 1.4;  margin: 0.4em 0 0.2em; }
.crepe-editor :deep(.editor h6) { font-size: 0.9em;  font-weight: 600; line-height: 1.4;  margin: 0.4em 0 0.2em; }

/* ===== Fix table display ===== */
.crepe-editor :deep(.editor .tableWrapper),
.crepe-editor :deep(.editor .milkdown-table-wrapper) {
  overflow-x: auto;
  -webkit-overflow-scrolling: touch;
  margin: 0.8em 0;
}
.crepe-editor :deep(.editor table) {
  border-collapse: collapse;
  border-spacing: 0;
  width: 100%;
  font-size: 0.95em;
}
.crepe-editor :deep(.editor th),
.crepe-editor :deep(.editor td) {
  vertical-align: top;
  line-height: 1.6;
  border: 1px solid var(--border, #dde1e9);
  padding: 6px 12px;
  text-align: left;
  min-width: 80px;
}
.crepe-editor :deep(.editor th) {
  background: var(--bg-sidebar, #f5f6fa);
  font-weight: 600;
  white-space: nowrap;
}
.crepe-editor :deep(.editor tr:nth-child(even) td) {
  background: var(--bg, #fafbfc);
}

/* ===== Mobile: prevent zoom on editor focus ===== */
@media (max-width: 768px) {
  .crepe-editor :deep(.editor) {
    font-size: 16px;
  }
  .crepe-editor :deep(input),
  .crepe-editor :deep(textarea),
  .crepe-editor :deep(select) {
    font-size: 16px !important;
  }
}
</style>

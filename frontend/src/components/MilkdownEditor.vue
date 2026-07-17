<template>
  <div ref="editorEl" class="crepe-editor"></div>
</template>

<script setup>
import { ref, onMounted, onBeforeUnmount } from 'vue'
import { useI18n } from '../i18n'
import { Crepe } from '@milkdown/crepe'
import { $prose } from '@milkdown/utils'
import { Plugin, PluginKey } from '@milkdown/prose/state'
import '@milkdown/crepe/theme/common/style.css'
import '@milkdown/crepe/theme/frame.css'

const props = defineProps({
  initialContent: { type: String, default: '' },
})

const { t } = useI18n()

// Milkdown's task-list `checked` attr lives on the list_item node, separate
// from its text content. If a checked item's text is fully cleared and new
// text typed in, the node is reused and `checked` survives untouched. Force
// `checked` back to null the moment an item's content becomes empty, so a
// freshly emptied line never silently "inherits" a previous done state.
const resetEmptiedTaskItemPlugin = $prose(() => {
  return new Plugin({
    key: new PluginKey('reset-emptied-task-item'),
    appendTransaction(transactions, _oldState, newState) {
      if (!transactions.some((tr) => tr.docChanged)) return null
      let tr = null
      newState.doc.descendants((node, pos) => {
        if (
          node.type.name === 'list_item' &&
          node.attrs.checked === true &&
          node.textContent.length === 0
        ) {
          tr = (tr || newState.tr).setNodeMarkup(pos, undefined, {
            ...node.attrs,
            checked: null,
          })
        }
      })
      return tr
    },
  })
})

const emit = defineEmits(['update'])
const editorEl = ref(null)
let crepeInstance = null
let _destroyed = false
let _editorElRef = null
let _handleKeyScroll = null

function doTypewriterScroll() {
  if (_destroyed || !_editorElRef) return
  const sel = window.getSelection()
  if (!sel || !sel.rangeCount) return
  const rect = sel.getRangeAt(0).getBoundingClientRect()
  if (!rect.height) return
  const containerRect = _editorElRef.getBoundingClientRect()
  const cursorY = rect.top - containerRect.top
  const threshold = containerRect.height * 0.60
  const targetY = containerRect.height * 0.42
  if (cursorY > threshold) {
    _editorElRef.scrollBy({ top: cursorY - targetY, behavior: 'smooth' })
  }
}

onMounted(async () => {
  if (!editorEl.value) return
  _editorElRef = editorEl.value

  crepeInstance = new Crepe({
    root: editorEl.value,
    defaultValue: props.initialContent,
    features: {
      'latex': false,
    },
    featureConfigs: {
      'placeholder': {
        text: t('editorPlaceholder'),
      }
    }
  })
  crepeInstance.editor.use(resetEmptiedTaskItemPlugin)

  crepeInstance.on((listener) => {
    listener.markdownUpdated((_, markdown) => {
      if (!_destroyed) {
        emit('update', markdown)
        requestAnimationFrame(doTypewriterScroll)
      }
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
    return
  }

  // Typewriter scroll on arrow/cursor key navigation
  _handleKeyScroll = () => requestAnimationFrame(doTypewriterScroll)
  _editorElRef.addEventListener('keydown', _handleKeyScroll)
})

onBeforeUnmount(() => {
  _destroyed = true
  if (_editorElRef && _handleKeyScroll) {
    _editorElRef.removeEventListener('keydown', _handleKeyScroll)
  }
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
  padding-bottom: 45vh;
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

/* ===== Completed task: gray + strikethrough =====
   Crepe renders task items via its listItemBlockComponent, not the
   `data-type`/`<input>` shape the old selectors here assumed:
   <li class="list-item"><div class="label-wrapper"><span class="label checked|unchecked">...
   so match on that real DOM instead. */
.crepe-editor :deep(.editor li.list-item:has(> .label-wrapper .label.checked)) {
  color: var(--text-muted, #9aa0a6);
  text-decoration: line-through;
}
.crepe-editor :deep(.editor li.list-item:has(> .label-wrapper .label.checked) > .children) {
  color: inherit;
  text-decoration: inherit;
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

/* ===== Fix code block spacing + font ===== */
.crepe-editor :deep(.editor code) {
  font-family: var(--editor-font-monospace);
}

/* ===== Code block: match inline code color scheme =====
   Background goes on .codemirror-host (sibling of .tools, parent of .cm-editor)
   so the tools bar keeps its native surface bg while the code area gets the
   inline-code purple tint. Tool bar buttons inherit font from here. */
.crepe-editor :deep(.milkdown-code-block) {
  border-radius: 8px;
  margin: 0.7em 0;
}

.crepe-editor :deep(.milkdown-code-block .codemirror-host) {
  background: var(--md-code-bg);
  border-radius: 8px;
}

.crepe-editor :deep(.milkdown-code-block .cm-editor) {
  background: transparent !important;
}
.crepe-editor :deep(.milkdown-code-block .cm-editor.cm-focused) {
  outline: none;
}
.crepe-editor :deep(.milkdown-code-block .cm-scroller) {
  font-family: var(--editor-font-monospace) !important;
  font-size: 13.5px;
  line-height: 1.7;
}
.crepe-editor :deep(.milkdown-code-block .cm-content),
.crepe-editor :deep(.milkdown-code-block .cm-content .cm-line) {
  font-family: var(--editor-font-monospace) !important;
  color: var(--md-code-fg);
  caret-color: var(--primary);
}
.crepe-editor :deep(.milkdown-code-block .cm-gutters) {
  background: transparent !important;
  border-right: 1px solid var(--border);
  color: var(--text-muted);
}
.crepe-editor :deep(.milkdown-code-block .cm-activeLineGutter) {
  background: var(--crepe-color-hover) !important;
}
.crepe-editor :deep(.milkdown-code-block .cm-activeLine) {
  background: var(--crepe-color-selected) !important;
}
.crepe-editor :deep(.milkdown-code-block .cm-cursor) {
  border-left-color: var(--primary);
}

/* Tools bar: use code font for language/copy labels */
.crepe-editor :deep(.milkdown-code-block .tools .language-button) {
  font-family: var(--editor-font-monospace);
}

/* ===== Fix horizontal rule: one solid blue line, not a translucent double line ===== */
.crepe-editor :deep(.editor hr) {
  margin: 1em 0;
  background-color: var(--md-blue, #5979bf) !important;
  background-clip: content-box !important;
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

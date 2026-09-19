import { readFileSync } from 'node:fs'
import { compile } from '@vue/compiler-dom'
import { parse } from '@vue/compiler-sfc'
import { describe, expect, it } from 'vitest'
import * as Vue from 'vue'
import { createRenderer, defineComponent, h, nextTick, ref, watch } from 'vue'

const componentSource = readFileSync(new URL('./NoteEditorView.vue', import.meta.url), 'utf8')
const { descriptor } = parse(componentSource)
const render = new Function('Vue', compile(descriptor.template.content, {
  mode: 'function',
}).code)(Vue)

const LazyMilkdownEditorStub = defineComponent({
  name: 'LazyMilkdownEditorStub',
  emits: ['fallback-raw'],
  render() {
    return h('div', {
      class: 'rich-editor',
      onClick: () => this.$emit('fallback-raw'),
    })
  },
})

const NoteEditorView = defineComponent({
  props: {
    mode: { type: String, required: true },
    editorKey: { type: Number, required: true },
    initialContent: { type: String, default: '' },
    content: { type: String, default: '' },
  },
  emits: ['update', 'document-ready', 'update:mode', 'update:content'],
  components: { LazyMilkdownEditor: LazyMilkdownEditorStub },
  setup(props) {
    const richEditorMounted = ref(props.mode === 'wysiwyg')
    const rawEditor = ref(null)
    watch(() => props.mode, mode => {
      if (mode === 'wysiwyg') richEditorMounted.value = true
    })
    return {
      richEditorMounted,
      rawEditor,
      handleRawInput: () => {},
      t: key => key,
    }
  },
  render,
})

function createHostNode(type, text = '') {
  return {
    type,
    text,
    children: [],
    parent: null,
    props: {},
    style: {},
  }
}

const renderer = createRenderer({
  patchProp(element, key, _previousValue, nextValue) {
    if (key === 'style' && nextValue && typeof nextValue === 'object') {
      Object.assign(element.style, nextValue)
      return
    }
    element.props[key] = nextValue
  },
  insert(child, parent, anchor) {
    child.parent = parent
    const index = anchor ? parent.children.indexOf(anchor) : -1
    if (index === -1) parent.children.push(child)
    else parent.children.splice(index, 0, child)
  },
  remove(child) {
    const index = child.parent?.children.indexOf(child) ?? -1
    if (index !== -1) child.parent.children.splice(index, 1)
    child.parent = null
  },
  createElement(type) {
    return createHostNode(type)
  },
  createText(text) {
    return createHostNode('text', text)
  },
  createComment(text) {
    return createHostNode('comment', text)
  },
  setText(node, text) {
    node.text = text
  },
  setElementText(node, text) {
    node.text = text
    node.children = []
  },
  parentNode(node) {
    return node.parent
  },
  nextSibling(node) {
    const siblings = node.parent?.children ?? []
    return siblings[siblings.indexOf(node) + 1] ?? null
  },
})

function findByClass(node, className) {
  if (node.props?.class?.split(' ').includes(className)) return node
  for (const child of node.children ?? []) {
    const match = findByClass(child, className)
    if (match) return match
  }
  return null
}

function mountEditor(initialMode = 'wysiwyg') {
  const mode = ref(initialMode)
  const root = createHostNode('root')
  const app = renderer.createApp(defineComponent({
    setup() {
      return () => h(NoteEditorView, {
        mode: mode.value,
        editorKey: 1,
        initialContent: 'hello',
        content: 'hello',
        'onUpdate:mode': value => { mode.value = value },
      })
    },
  }))
  app.mount(root)
  return { mode, root }
}

describe('NoteEditorView', () => {
  it('shows the raw editor after switching from the mounted rich editor', async () => {
    const { mode, root } = mountEditor()
    const richEditor = findByClass(root, 'rich-editor')
    const rawEditor = findByClass(root, 'raw-editor')

    expect(richEditor).not.toBeNull()
    expect(rawEditor).not.toBeNull()
    expect(richEditor.style.display).not.toBe('none')
    expect(rawEditor.style.display).toBe('none')

    mode.value = 'raw'
    await nextTick()

    expect(findByClass(root, 'rich-editor')).toBe(richEditor)
    expect(richEditor.style.display).toBe('none')
    expect(rawEditor.style.display).not.toBe('none')
  })

  it('mounts the rich editor lazily and reuses it on later mode switches', async () => {
    const { mode, root } = mountEditor('raw')
    const rawEditor = findByClass(root, 'raw-editor')

    expect(findByClass(root, 'rich-editor')).toBeNull()
    expect(rawEditor.style.display).not.toBe('none')

    mode.value = 'wysiwyg'
    await nextTick()
    const richEditor = findByClass(root, 'rich-editor')
    expect(richEditor).not.toBeNull()
    expect(rawEditor.style.display).toBe('none')

    mode.value = 'raw'
    await nextTick()
    expect(findByClass(root, 'rich-editor')).toBe(richEditor)
    expect(rawEditor.style.display).not.toBe('none')
  })

  it('opens the raw editor when the rich editor requests a fallback', async () => {
    const { mode, root } = mountEditor()
    const richEditor = findByClass(root, 'rich-editor')

    richEditor.props.onClick()
    await nextTick()

    expect(mode.value).toBe('raw')
    expect(findByClass(root, 'raw-editor').style.display).not.toBe('none')
  })
})

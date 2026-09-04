import { describe, expect, it } from 'vitest'
import { Schema } from '@milkdown/prose/model'
import { EditorState } from '@milkdown/prose/state'
import { buildTaskItemResetPlugin } from './taskItemReset'

// Minimal schema mirroring Milkdown's list_item (label/listType/spread/checked)
// so the plugin's appendTransaction logic runs against real ProseMirror docs.
const schema = new Schema({
  nodes: {
    doc: { content: 'block+' },
    paragraph: {
      group: 'block',
      content: 'text*',
      toDOM: () => ['p', 0],
    },
    bullet_list: {
      group: 'block',
      content: 'list_item+',
      toDOM: () => ['ul', 0],
    },
    list_item: {
      group: 'listItem',
      content: 'paragraph block*',
      attrs: {
        label: { default: '•' },
        listType: { default: 'bullet' },
        spread: { default: true },
        checked: { default: null, validate: 'boolean|null' },
      },
      toDOM: () => ['li', 0],
    },
    text: {},
  },
})

function para(value) {
  return schema.nodes.paragraph.create(null, value ? schema.text(value) : undefined)
}
function item(checked, content) {
  return schema.nodes.list_item.create({ checked }, content)
}
function list(...items) {
  return schema.nodes.bullet_list.create(null, items)
}
function docOf(...blocks) {
  return schema.nodes.doc.create(null, blocks)
}
function makeState(doc) {
  return EditorState.create({ schema, doc, plugins: [buildTaskItemResetPlugin()] })
}
function checkedItems(state) {
  const out = []
  state.doc.descendants((node) => {
    if (node.type.name === 'list_item') out.push(node.attrs.checked)
  })
  return out
}

function endPosOfText($at) {
  const $from = $at.doc.resolve($at.pos)
  return $from.end()
}

describe('taskItemReset plugin', () => {
  it('turns the empty second item into an unchecked task item when Enter splits a checked item', () => {
    const doc = docOf(list(item(true, para('done task'))))
    const state = makeState(doc)
    // Cursor at the end of the item's paragraph text, like pressing Enter.
    const pos = endPosOfText(doc.resolve(3))
    const tr = state.tr.split(pos, 2, [null, { type: schema.nodes.paragraph }])
    const next = state.apply(tr)

    expect(checkedItems(next)).toEqual([true, false])
    expect(next.doc.child(0).childCount).toBe(2)
  })

  it('clearing a checked item text resets it to an unchecked task item', () => {
    const doc = docOf(list(item(true, para('done task'))))
    const state = makeState(doc)
    const $p = doc.resolve(4)
    const tr = state.tr.delete($p.start(), $p.end())
    const next = state.apply(tr)

    expect(checkedItems(next)).toEqual([false])
  })

  it('leaves a fresh [x] input-rule conversion checked (was a bullet before)', () => {
    // Before: an unchecked bullet item whose text is exactly "[x] ". The input
    // rule deletes the text AND sets checked=true in one transaction.
    const doc = docOf(list(item(null, para('[x] '))))
    const state = makeState(doc)
    const $p = doc.resolve(3)
    const start = $p.start()
    const end = $p.end()
    const itemPos = start - 2 // the list_item node start
    const tr = state.tr.deleteRange(start, end).setNodeMarkup(itemPos, undefined, {
      checked: true,
    })
    const next = state.apply(tr)

    expect(checkedItems(next)).toEqual([true])
  })

  it('leaves a fresh [ ] input-rule conversion as an unchecked task item', () => {
    const doc = docOf(list(item(null, para('[ ] '))))
    const state = makeState(doc)
    const $p = doc.resolve(3)
    const start = $p.start()
    const end = $p.end()
    const itemPos = start - 2
    const tr = state.tr.deleteRange(start, end).setNodeMarkup(itemPos, undefined, {
      checked: false,
    })
    const next = state.apply(tr)

    expect(checkedItems(next)).toEqual([false])
  })

  it('does not touch an already-unchecked empty item when another edit flows', () => {
    // Clearing text from an unchecked task item keeps it unchecked (never a
    // plain bullet and never a checked carry-over).
    const doc = docOf(list(item(false, para('pending task'))))
    const state = makeState(doc)
    const $p = doc.resolve(5)
    const tr = state.tr.delete($p.start(), $p.end())
    const next = state.apply(tr)

    expect(checkedItems(next)).toEqual([false])
  })

  it('keeps a split from a checked item with mid-text cursor as two checked items', () => {
    // Splitting mid-text leaves non-empty content on BOTH items, so neither is
    // "emptied" and the original done state survives on both halves.
    const doc = docOf(list(item(true, para('123456'))))
    const state = makeState(doc)
    // Cursor after the third character, inside the paragraph.
    const $from = doc.resolve(7)
    const tr = state.tr.split($from.pos, 2, [null, { type: schema.nodes.paragraph }])
    const next = state.apply(tr)

    expect(checkedItems(next)).toEqual([true, true])
  })
})

// Task-list `checked` state repair for Milkdown's list_item node.
//
// Milkdown keeps the task-list `checked` attr on the list_item node, separate
// from its text content. An empty, checked list_item should only keep its done
// state when the user just asked for it (the `[x] ` input rule turns a bullet
// item checked in the same step that empties it). In every other case a line
// that BECOMES empty while checked drops back to unchecked-but-still-a-task:
//
//   1. A checked item's text is fully cleared and new text typed in — the node
//      is reused and `checked` survives, so new text must not inherit "done".
//   2. Enter splits a checked item — splitListItem copies the parent attrs, so
//      the freshly created empty item starts checked; every task-list editor
//      starts the next line as an UNCHECKED task item.
//
// The discriminator is the item's state in the PREVIOUS document: the input
// rule's item was an unchecked bullet before, while both cases above map back
// to a checked item. Resetting to `false` (never `null`) keeps the line a task
// item — a `null` reset is what used to turn the next line of a task list into
// a plain bullet item.
import { Plugin, PluginKey } from '@milkdown/prose/state'
import { Mapping } from '@milkdown/prose/transform'

export function buildTaskItemResetPlugin() {
  return new Plugin({
    key: new PluginKey('reset-emptied-task-item'),
    appendTransaction(transactions, oldState, newState) {
      if (!transactions.some((tr) => tr.docChanged)) return null
      const backward = new Mapping()
      for (const tr of transactions) backward.appendMapping(tr.mapping)
      const inverse = backward.invert()
      const wasCheckedBefore = (pos) => {
        try {
          const oldPos = inverse.map(pos, -1)
          if (oldPos < 0 || oldPos > oldState.doc.content.size) return false
          const $old = oldState.doc.resolve(oldPos)
          // The mapped position can land exactly on a node boundary (e.g. the
          // start of the list_item itself after its text was deleted), where
          // the item is not an ancestor of the resolved position.
          const before = $old.nodeBefore
          const after = $old.nodeAfter
          if (before && before.type.name === 'list_item') return before.attrs.checked === true
          if (after && after.type.name === 'list_item') return after.attrs.checked === true
          for (let depth = $old.depth; depth > 0; depth--) {
            const ancestor = $old.node(depth)
            if (ancestor.type.name === 'list_item') return ancestor.attrs.checked === true
          }
        } catch (_) {}
        return false
      }
      let tr = null
      newState.doc.descendants((node, pos) => {
        if (
          node.type.name === 'list_item' &&
          node.attrs.checked === true &&
          node.textContent.length === 0 &&
          wasCheckedBefore(pos)
        ) {
          tr = (tr || newState.tr).setNodeMarkup(pos, undefined, {
            ...node.attrs,
            checked: false,
          })
        }
      })
      return tr
    },
  })
}

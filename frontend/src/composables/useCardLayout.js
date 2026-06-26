import { ref, reactive, onMounted, onBeforeUnmount } from 'vue'
import apiClient from '../api'
import { stripMarkdown } from '../utils'

export function useCardLayout() {
  // Card Expansion State
  const expandedCards = ref(new Set())
  const fullContentCache = reactive({})

  // Resolve the text shown in a waterfall card (expanded full content when
  // available, otherwise the preview). Empty string → empty-note placeholder.
  function cardText(note) {
    if (expandedCards.value.has(note.path) && fullContentCache[note.path]) {
      return fullContentCache[note.path]
    }
    return note.plainPreview
  }

  const overlongStates = reactive({})

  // Shared scaffolding for the card-measuring directives below: run `measure` now,
  // once more after layout settles, and on every resize, then clean up on unmount.
  // Each directive passes a unique key so its timer/observer don't collide on el.
  function observeMeasure(el, measure, key) {
    measure()
    el[`_${key}Timer`] = setTimeout(measure, 50) // slight delay for layout
    if (window.ResizeObserver) {
      const ro = new ResizeObserver(measure)
      ro.observe(el)
      el[`_${key}Ro`] = ro
    }
  }
  function disconnectMeasure(el, key) {
    if (el[`_${key}Ro`]) el[`_${key}Ro`].disconnect()
    if (el[`_${key}Timer`]) clearTimeout(el[`_${key}Timer`])
  }

  // Flags a card whose preview overflows ~6 lines so the expand bar can show.
  const vCheckOverflow = {
    mounted(el, binding) {
      const path = binding.value
      const check = () => {
        // 6 lines at 1.6 line-height & 13px font-size = ~124.8px
        const isOver = el.scrollHeight > 126
        if (overlongStates[path] !== isOver) overlongStates[path] = isOver
      }
      observeMeasure(el, check, 'overflow')
    },
    updated(el, binding) {
      const path = binding.value
      const isOver = el.scrollHeight > 126
      if (overlongStates[path] !== isOver) overlongStates[path] = isOver
    },
    unmounted(el, binding) {
      disconnectMeasure(el, 'overflow')
      delete overlongStates[binding.value]
    }
  }

  const cardHeights = reactive({})

  // Measures each waterfall card's rendered height so splitIntoColumns can
  // balance columns by actual height instead of just round-robin count. Skips
  // updates while the card is expanded so expanding one card never triggers a
  // reflow of other columns (existing, intentional behavior).
  const vMeasureCard = {
    mounted(el, binding) {
      const path = binding.value
      const measure = () => {
        if (expandedCards.value.has(path)) return
        const h = el.offsetHeight
        if (h > 0 && cardHeights[path] !== h) cardHeights[path] = h
      }
      observeMeasure(el, measure, 'measure')
    },
    unmounted(el) {
      disconnectMeasure(el, 'measure')
    }
  }

  async function toggleExpand(path) {
    const newSet = new Set(expandedCards.value)
    if (newSet.has(path)) {
      newSet.delete(path)
    } else {
      newSet.add(path)
      // Fetch full content if not cached
      if (!fullContentCache[path]) {
        try {
          const res = await apiClient.getNote(path)
          fullContentCache[path] = stripMarkdown(res.data.content || '')
        } catch (e) {
          console.error('Failed to fetch full content', e)
        }
      }
    }
    expandedCards.value = newSet
  }

  // Distribute notes into N columns, greedily assigning each note to the
  // currently-shortest column using measured heights (cardHeights), so all
  // columns end up roughly equal in total height instead of equal in count.
  // Falls back to a text-length estimate for notes that haven't been measured
  // yet (e.g. first render, before any ResizeObserver has fired).
  function estimateHeight(note) {
    const textLen = (note.plainPreview || '').length + (note.hasCustomName ? note.name.length : 0)
    return 80 + textLen * 0.6 // rough: card padding/header + ~0.6px per char of preview
  }

  function splitIntoColumns(notes) {
    const n = columnCount.value
    const cols = Array.from({ length: n }, () => [])
    const colHeights = Array.from({ length: n }, () => 0)
    notes.forEach((note) => {
      let shortest = 0
      for (let i = 1; i < n; i++) {
        if (colHeights[i] < colHeights[shortest]) shortest = i
      }
      cols[shortest].push(note)
      colHeights[shortest] += cardHeights[note.path] || estimateHeight(note)
    })
    return cols
  }

  // Reactive column count driven by viewport width
  const columnCount = ref(3)
  function updateColumnCount() {
    const w = window.innerWidth
    columnCount.value = w <= 768 ? 1 : w <= 1100 ? 2 : 3
  }
  updateColumnCount()

  onMounted(() => {
    window.addEventListener('resize', updateColumnCount)
  })

  onBeforeUnmount(() => {
    window.removeEventListener('resize', updateColumnCount)
  })

  return {
    expandedCards,
    fullContentCache,
    overlongStates,
    cardHeights,
    columnCount,
    updateColumnCount,
    toggleExpand,
    estimateHeight,
    splitIntoColumns,
    observeMeasure,
    disconnectMeasure,
    vCheckOverflow,
    vMeasureCard,
    cardText,
  }
}

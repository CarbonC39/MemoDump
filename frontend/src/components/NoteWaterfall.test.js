// @vitest-environment happy-dom
import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { ref } from 'vue'
import NoteWaterfall from './NoteWaterfall.vue'

const note = {
  path: '2026-08-11_064149.md',
  name: '2026-08-11_064149',
  hasCustomName: false,
  plainPreview: 'saved content',
  tags: [],
}

function mountWaterfall() {
  return mount(NoteWaterfall, {
    props: { notes: [note] },
    global: {
      provide: {
        layout: {
          expandedCards: ref(new Set()),
          overlongStates: {},
          toggleExpand: vi.fn(),
          splitIntoColumns: notes => [notes],
          vCheckOverflow: {},
          vMeasureCard: {},
          cardText: note => note.plainPreview,
        },
      },
    },
  })
}

describe('NoteWaterfall note opening', () => {
  it('opens a saved note by clicking its card', async () => {
    const wrapper = mountWaterfall()
    await wrapper.find('.waterfall-card').trigger('click')
    expect(wrapper.emitted('open-note')).toEqual([[note]])
  })

  it('opens a saved note from the keyboard', async () => {
    const wrapper = mountWaterfall()
    await wrapper.find('.waterfall-card').trigger('keydown', { key: 'Enter' })
    expect(wrapper.emitted('open-note')).toEqual([[note]])
  })

  it('keeps the menu control from opening the note', async () => {
    const wrapper = mountWaterfall()
    await wrapper.find('.card-menu-btn').trigger('click')
    expect(wrapper.emitted('open-note')).toBeUndefined()
  })
})

// @vitest-environment happy-dom
import { afterEach, describe, expect, it } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import InfoTooltip from './InfoTooltip.vue'

describe('InfoTooltip viewport positioning', () => {
  const originalWidth = window.innerWidth
  const originalHeight = window.innerHeight

  afterEach(() => {
    Object.defineProperty(window, 'innerWidth', { configurable: true, value: originalWidth })
    Object.defineProperty(window, 'innerHeight', { configurable: true, value: originalHeight })
  })

  it('keeps a tooltip inside a narrow viewport', async () => {
    Object.defineProperty(window, 'innerWidth', { configurable: true, value: 320 })
    Object.defineProperty(window, 'innerHeight', { configurable: true, value: 480 })

    const wrapper = mount(InfoTooltip, { props: { text: 'A long synchronization notice' } })
    const trigger = wrapper.find('.info-tooltip-trigger').element
    const popover = wrapper.find('.info-tooltip-pop').element
    trigger.getBoundingClientRect = () => ({
      left: 280, right: 300, top: 100, bottom: 120, width: 20, height: 20,
    })
    Object.defineProperty(popover, 'offsetWidth', { configurable: true, value: 296 })
    Object.defineProperty(popover, 'offsetHeight', { configurable: true, value: 80 })

    await wrapper.find('.info-tooltip-trigger').trigger('click')
    await flushPromises()

    const left = Number.parseFloat(popover.style.left)
    const maxWidth = Number.parseFloat(popover.style.maxWidth)
    expect(left).toBeGreaterThanOrEqual(12)
    expect(left + maxWidth).toBeLessThanOrEqual(308)
    wrapper.unmount()
  })
})

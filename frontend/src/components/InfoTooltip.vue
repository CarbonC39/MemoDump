<template>
  <span class="info-tooltip">
    <button
      ref="triggerEl"
      type="button"
      class="info-tooltip-trigger"
      :aria-label="label || text"
      :aria-expanded="open"
      @click.stop="toggle"
      @mouseenter="openNow"
      @mouseleave="scheduleClose"
      @focus="openNow"
      @blur="scheduleClose"
    >
      <span class="material-icons-outlined">{{ icon }}</span>
    </button>
    <transition name="info-tooltip">
      <div
        ref="popoverEl"
        v-show="open"
        class="info-tooltip-pop"
        :class="{ 'info-tooltip-pop-right': align === 'right', 'info-tooltip-pop-top': placement === 'top' }"
        :style="popoverStyle"
        role="tooltip"
        @mouseenter="openNow"
        @mouseleave="scheduleClose"
      >
        <slot>{{ text }}</slot>
      </div>
    </transition>
  </span>
</template>

<script setup>
import { nextTick, ref, watch, onBeforeUnmount } from 'vue'

const props = defineProps({
  text: { type: String, default: '' },
  label: { type: String, default: '' },
  align: { type: String, default: 'left' }, // 'left' | 'right' — popover edge alignment
  icon: { type: String, default: 'help_outline' },
  placement: { type: String, default: 'bottom' }, // 'top' | 'bottom'
})

const open = ref(false)
const triggerEl = ref(null)
const popoverEl = ref(null)
const popoverStyle = ref({})
let leaveTimer = null

function positionPopover() {
  if (!open.value || !triggerEl.value || !popoverEl.value) return

  const margin = 12
  const gap = 6
  const maxWidth = Math.max(0, Math.min(340, window.innerWidth - margin * 2))
  const trigger = triggerEl.value.getBoundingClientRect()
  const width = Math.min(popoverEl.value.offsetWidth, maxWidth)
  const height = popoverEl.value.offsetHeight
  const preferredLeft = props.align === 'right' ? trigger.right - width : trigger.left
  const maxLeft = Math.max(margin, window.innerWidth - margin - width)
  const left = Math.min(Math.max(preferredLeft, margin), maxLeft)
  const preferredTop = props.placement === 'top'
    ? trigger.top - gap - height
    : trigger.bottom + gap
  const maxTop = Math.max(margin, window.innerHeight - margin - height)
  const top = Math.min(Math.max(preferredTop, margin), maxTop)

  popoverStyle.value = {
    left: `${left}px`,
    right: 'auto',
    top: `${top}px`,
    bottom: 'auto',
    maxWidth: `${maxWidth}px`,
  }
}

function openNow() {
  if (leaveTimer) clearTimeout(leaveTimer)
  open.value = true
}

function scheduleClose() {
  if (leaveTimer) clearTimeout(leaveTimer)
  leaveTimer = setTimeout(() => { open.value = false }, 150)
}

function toggle() {
  if (open.value) {
    open.value = false
  } else {
    openNow()
  }
}

function onDocClick(e) {
  if (!e.target.closest('.info-tooltip')) open.value = false
}

function onDocKey(e) {
  if (e.key === 'Escape') open.value = false
}

watch(open, async (v) => {
  if (v) {
    document.addEventListener('click', onDocClick)
    document.addEventListener('keydown', onDocKey)
    window.addEventListener('resize', positionPopover)
    window.addEventListener('scroll', positionPopover, true)
    popoverStyle.value = { maxWidth: `${Math.max(0, Math.min(340, window.innerWidth - 24))}px` }
    await nextTick()
    positionPopover()
  } else {
    document.removeEventListener('click', onDocClick)
    document.removeEventListener('keydown', onDocKey)
    window.removeEventListener('resize', positionPopover)
    window.removeEventListener('scroll', positionPopover, true)
  }
})

onBeforeUnmount(() => {
  document.removeEventListener('click', onDocClick)
  document.removeEventListener('keydown', onDocKey)
  window.removeEventListener('resize', positionPopover)
  window.removeEventListener('scroll', positionPopover, true)
  if (leaveTimer) clearTimeout(leaveTimer)
})
</script>

<style scoped>
.info-tooltip {
  position: relative;
  display: inline-flex;
  vertical-align: middle;
}

.info-tooltip-trigger {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 20px;
  height: 20px;
  padding: 0;
  border: none;
  border-radius: 50%;
  background: transparent;
  color: var(--text-muted);
  cursor: help;
  transition: color 0.15s, background 0.15s;
}
.info-tooltip-trigger:hover,
.info-tooltip-trigger:focus-visible {
  color: var(--primary-dark);
  background: var(--primary-bg);
}
.info-tooltip-trigger .material-icons-outlined {
  font-size: 16px;
}

.info-tooltip-pop {
  position: fixed;
  top: 0;
  left: 0;
  z-index: 50;
  width: max-content;
  max-width: 340px;
  max-height: calc(100vh - 24px);
  padding: 8px 10px;
  background: var(--bg-card);
  border: 1px solid var(--border);
  border-radius: var(--radius);
  box-shadow: var(--shadow-md);
  font-size: 12px;
  line-height: 1.6;
  font-weight: 400;
  color: var(--text-secondary);
  text-align: left;
  white-space: normal;
  overflow-wrap: anywhere;
  overflow-y: auto;
}
.info-tooltip-pop-right {
  left: auto;
  right: 0;
}
.info-tooltip-pop-top {
  top: auto;
  bottom: calc(100% + 6px);
}

.info-tooltip-enter-active,
.info-tooltip-leave-active {
  transition: opacity 0.12s ease;
}
.info-tooltip-enter-from,
.info-tooltip-leave-to {
  opacity: 0;
}
</style>

<template>
  <span class="info-tooltip">
    <button
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
      <span class="material-icons-outlined">help_outline</span>
    </button>
    <transition name="info-tooltip">
      <div v-show="open" class="info-tooltip-pop" role="tooltip" @mouseenter="openNow" @mouseleave="scheduleClose">
        <slot>{{ text }}</slot>
      </div>
    </transition>
  </span>
</template>

<script setup>
import { ref, watch, onBeforeUnmount } from 'vue'

defineProps({
  text: { type: String, default: '' },
  label: { type: String, default: '' },
})

const open = ref(false)
let leaveTimer = null

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

watch(open, (v) => {
  if (v) {
    document.addEventListener('click', onDocClick)
    document.addEventListener('keydown', onDocKey)
  } else {
    document.removeEventListener('click', onDocClick)
    document.removeEventListener('keydown', onDocKey)
  }
})

onBeforeUnmount(() => {
  document.removeEventListener('click', onDocClick)
  document.removeEventListener('keydown', onDocKey)
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
  position: absolute;
  top: calc(100% + 6px);
  left: 0;
  z-index: 50;
  max-width: 280px;
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

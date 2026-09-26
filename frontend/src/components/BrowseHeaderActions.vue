<template>
  <div class="header-actions">
    <div class="sort-control">
      <button
        class="btn btn-icon header-sort-btn"
        :class="{ active: sortMenuOpen }"
        :title="t('notes.sortOrder')"
        @click.stop="sortMenuOpen = !sortMenuOpen"
      >
        <span class="material-icons-outlined">sort</span>
      </button>
      <div v-if="sortMenuOpen" class="sort-overlay" @click="sortMenuOpen = false"></div>
      <div v-if="sortMenuOpen" class="sort-menu">
        <div
          class="sort-menu-item"
          :class="{ active: sortMode === 'modified-desc' }"
          @click="selectSort('modified-desc')"
        >
          <span class="material-icons-outlined sort-check">check</span>
          <span>{{ t('notes.recentlyModified') }}</span>
        </div>
        <div
          class="sort-menu-item"
          :class="{ active: sortMode === 'modified-asc' }"
          @click="selectSort('modified-asc')"
        >
          <span class="material-icons-outlined sort-check">check</span>
          <span>{{ t('notes.oldestModified') }}</span>
        </div>
      </div>
    </div>
    <button class="btn btn-icon header-new-btn" :title="t('editor.newNote')" @click="$emit('new-note')">
      <span class="material-icons-outlined">add</span>
    </button>
  </div>
</template>

<script setup>
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { useI18n } from '../i18n'

defineProps({
  sortMode: { type: String, required: true },
})
const emit = defineEmits(['sort', 'new-note'])

const { t } = useI18n()
const sortMenuOpen = ref(false)

function selectSort(mode) {
  sortMenuOpen.value = false
  emit('sort', mode)
}

function closeOnEscape(event) {
  if (event.key === 'Escape') sortMenuOpen.value = false
}

onMounted(() => window.addEventListener('keydown', closeOnEscape))
onBeforeUnmount(() => window.removeEventListener('keydown', closeOnEscape))
</script>

<style scoped>
.header-actions {
  display: flex;
  align-items: center;
  gap: 6px;
  flex-shrink: 0;
}
.sort-control {
  position: relative;
  display: flex;
  align-items: center;
}
.header-sort-btn {
  width: 28px;
  height: 28px;
  color: var(--text-secondary);
  border-radius: var(--radius);
}
.header-sort-btn:hover,
.header-sort-btn.active {
  color: var(--primary-dark);
  background: var(--primary-bg);
}
.header-sort-btn .material-icons-outlined { font-size: 20px; }
.sort-overlay {
  position: fixed;
  inset: 0;
  z-index: 1000;
}
.sort-menu {
  position: absolute;
  z-index: 1001;
  top: calc(100% + 4px);
  right: 0;
  min-width: 184px;
  padding: 4px 0;
  background: var(--bg-card);
  border: 1px solid var(--border);
  border-radius: 8px;
  box-shadow: var(--shadow-md);
}
.sort-menu-item {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 9px 14px 9px 8px;
  color: var(--text);
  font-size: 13px;
  white-space: nowrap;
  cursor: pointer;
}
.sort-menu-item:hover { background: var(--primary-bg); }
.sort-menu-item.active {
  color: var(--primary-dark);
  font-weight: 500;
}
.sort-check {
  color: var(--primary);
  font-size: 16px;
  opacity: 0;
}
.sort-menu-item.active .sort-check { opacity: 1; }
.header-new-btn {
  width: 28px;
  height: 28px;
  color: var(--primary);
  border-radius: var(--radius);
}
.header-new-btn:hover { background: var(--primary-bg); }
.header-new-btn .material-icons-outlined { font-size: 22px; }
</style>

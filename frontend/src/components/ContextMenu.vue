<template>
  <div v-if="visible" class="context-menu-overlay" @click="$emit('close')" @contextmenu.prevent="$emit('close')"></div>
  <div v-if="visible" class="context-menu" :style="{ top: y + 'px', left: x + 'px' }">
    <div class="context-menu-item" @click="$emit('edit')">
      <span class="material-icons-outlined">edit</span> {{ t('actions.edit') }}
    </div>
    <div class="context-menu-item" @click="$emit('copy')">
      <span class="material-icons-outlined">content_copy</span> {{ t('actions.copyFullText') }}
    </div>
    <div class="context-menu-item" @click="$emit('duplicate')">
      <span class="material-icons-outlined">library_add</span> {{ t('actions.duplicate') }}
    </div>
    <div class="context-menu-item" @click="$emit('download')">
      <span class="material-icons-outlined">download</span> {{ t('actions.download') }}
    </div>
    <div class="context-menu-item" @click="$emit('move')">
      <span class="material-icons-outlined">drive_file_move</span> {{ t('actions.move') }}
    </div>
    <div class="context-menu-item text-danger" @click="$emit('delete')">
      <span class="material-icons-outlined">delete_outline</span> {{ t('actions.delete') }}
    </div>
  </div>
</template>

<script setup>
import { useI18n } from '../i18n'

const { t } = useI18n()

defineProps({
  visible: Boolean,
  x: { type: Number, default: 0 },
  y: { type: Number, default: 0 },
})
defineEmits(['edit', 'copy', 'duplicate', 'download', 'move', 'delete', 'close'])
</script>

<style scoped>
.context-menu-overlay {
  position: fixed;
  top: 0; left: 0; right: 0; bottom: 0;
  z-index: 1000;
}
.context-menu {
  position: fixed;
  background: var(--bg-card);
  border: 1px solid var(--border);
  box-shadow: var(--shadow-md);
  border-radius: 8px;
  padding: 4px 0;
  min-width: 170px;
  z-index: 1001;
}
.context-menu-item {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 10px 16px;
  font-size: 14px;
  cursor: pointer;
  color: var(--text);
}
.context-menu-item:hover {
  background: var(--bg);
}
.context-menu-item.text-danger {
  color: var(--danger);
}
@media (max-width: 768px) {
  /* Larger touch targets for context menu items */
  .context-menu-item {
    padding: 14px 16px;
  }
  /* Context menu max width on narrow screens */
  .context-menu {
    min-width: 140px;
    max-width: calc(100vw - 16px);
  }
}
</style>

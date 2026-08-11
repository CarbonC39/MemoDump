<template>
  <div class="waterfall-view">
    <div v-if="notes.length === 0" class="empty-state-big">
      <span class="material-icons-outlined empty-icon">description</span>
      <p>{{ t('notes.noNotes') }}</p>
    </div>
    <NoteWaterfall
      v-else
      :notes="notes"
      :hovered-note-path="hoveredNotePath"
      @dragstart="(...args) => $emit('dragstart', ...args)"
      @contextmenu="(...args) => $emit('contextmenu', ...args)"
      @open-note="$emit('open-note', $event)"
      @update:hovered-note-path="$emit('update:hovered-note-path', $event)"
    />
    <div v-if="hasMore" class="load-more-row">
      <button class="btn btn-ghost" :disabled="loadingMore" @click="$emit('load-more')">
        {{ loadingMore ? t('notes.loading') : t('notes.loadMore') }}
      </button>
    </div>
  </div>
</template>

<script setup>
import { useI18n } from '../i18n'
import NoteWaterfall from './NoteWaterfall.vue'

defineProps({
  notes: { type: Array, default: () => [] },
  hoveredNotePath: { type: String, default: null },
  hasMore: { type: Boolean, default: false },
  loadingMore: { type: Boolean, default: false },
})
defineEmits(['dragstart', 'contextmenu', 'open-note', 'update:hovered-note-path', 'load-more'])

const { t } = useI18n()
</script>

<style scoped>
.waterfall-view {
  padding: 20px 24px;
}
.empty-state-big {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  padding: 80px 20px;
  color: var(--text-muted);
  gap: 12px;
}
.empty-state-big p { font-size: 14px; }
.empty-icon {
  color: var(--border);
  font-size: 56px;
}

@media (max-width: 768px) {
  .waterfall-view { padding: 10px 12px; }
}
</style>

<template>
  <div class="waterfall-grid">
    <div v-for="(column, index) in columns" :key="index" class="waterfall-col">
      <div
        v-for="note in column"
        :key="note.path"
        v-measure-card="note.path"
        class="waterfall-card"
        :draggable="hoveredNotePath !== note.path"
        @dragstart="$emit('dragstart', $event, note)"
      >
        <div v-if="note.hasCustomName" class="card-header">
          <div class="card-name">{{ note.name }}</div>
          <button
            class="btn btn-icon btn-ghost btn-sm card-menu-btn"
            @click.stop="$emit('contextmenu', $event, note)"
          >
            <span class="material-icons-outlined">more_vert</span>
          </button>
        </div>
        <button
          v-else
          class="btn btn-icon btn-ghost btn-sm card-menu-btn unnamed-card-menu"
          @click.stop="$emit('contextmenu', $event, note)"
        >
          <span class="material-icons-outlined">more_vert</span>
        </button>
        <div
          v-check-overflow="note.path"
          class="card-preview"
          :class="{ expanded: expandedCards.has(note.path) }"
          draggable="false"
          @mouseenter="$emit('update:hovered-note-path', note.path)"
          @mouseleave="$emit('update:hovered-note-path', null)"
          @dragstart.stop
        >
          <template v-if="cardText(note)">{{ cardText(note) }}</template>
          <span v-else class="card-empty">{{ t('notes.emptyNote') }}</span>
        </div>
        <div
          v-if="overlongStates[note.path]"
          class="card-expand-bar"
          @click.stop="toggleExpand(note.path)"
        >
          <span class="material-icons-outlined">
            {{ expandedCards.has(note.path) ? 'expand_less' : 'expand_more' }}
          </span>
        </div>
        <div v-if="note.tags && note.tags.length" class="card-footer">
          <div class="card-tags">
            <span v-for="tag in note.tags" :key="tag" class="tag">{{ tag }}</span>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup>
import { computed, inject } from 'vue'
import { useI18n } from '../i18n'

const props = defineProps({
  notes: { type: Array, default: () => [] },
  hoveredNotePath: { type: String, default: null },
})
defineEmits(['dragstart', 'contextmenu', 'update:hovered-note-path'])

const { t } = useI18n()
const layout = inject('layout')
if (!layout) throw new Error('NoteWaterfall requires the card layout provider')

const {
  expandedCards,
  overlongStates,
  toggleExpand,
  splitIntoColumns,
  vCheckOverflow,
  vMeasureCard,
  cardText,
} = layout

const columns = computed(() => splitIntoColumns(props.notes))
</script>

<style scoped>
.waterfall-grid {
  display: flex;
  gap: 14px;
  align-items: flex-start;
}
.waterfall-col {
  flex: 1;
  min-width: 0;
  display: flex;
  flex-direction: column;
}
.waterfall-card {
  position: relative;
  background: var(--bg-card);
  border: 1px solid rgba(0, 0, 0, 0.04);
  border-radius: 14px;
  padding: 16px 18px;
  margin-bottom: 16px;
  box-shadow: 0 4px 14px rgba(0, 0, 0, 0.03);
  transition: box-shadow 0.2s ease, background 0.2s ease;
}
.card-header {
  display: flex;
  justify-content: space-between;
  align-items: flex-start;
  margin-bottom: 6px;
}
.card-name {
  flex: 1;
  margin-bottom: 0;
  color: var(--waterfall-title);
  font-size: 14px;
  font-weight: 600;
  word-break: break-all;
}
.card-menu-btn {
  min-width: 36px;
  min-height: 36px;
  margin-top: -4px;
  margin-right: -4px;
  margin-left: 8px;
  color: var(--text-muted);
}
.card-menu-btn:hover {
  color: var(--text);
  background: var(--border-light);
}
.unnamed-card-menu {
  position: absolute;
  z-index: 2;
  top: 12px;
  right: 14px;
  margin: 0;
}
.card-preview {
  display: -webkit-box;
  overflow: hidden;
  color: var(--text-secondary);
  font-size: 13px;
  line-height: 1.6;
  white-space: pre-line;
  word-break: break-word;
  cursor: text;
  user-select: text;
  -webkit-user-select: text;
  -webkit-user-drag: none;
  -webkit-box-orient: vertical;
  -webkit-line-clamp: 6;
  line-clamp: 6;
  transition: max-height 0.25s ease, overflow 0.25s;
}
.card-preview.expanded {
  display: block;
  -webkit-line-clamp: unset;
  line-clamp: unset;
}
.card-expand-bar {
  display: flex;
  align-items: center;
  justify-content: center;
  margin-top: 4px;
  margin-bottom: -8px;
  padding: 4px 0;
  color: var(--primary);
  cursor: pointer;
  border-radius: 6px;
  transition: background 0.2s;
}
.card-expand-bar:hover {
  color: var(--primary-dark);
  background: var(--bg);
}
.card-expand-bar .material-icons-outlined {
  font-size: 20px;
}
.card-footer { margin-top: 8px; }
.card-tags { display: flex; flex-wrap: wrap; gap: 4px; }
.card-empty {
  color: var(--text-muted);
  font-style: italic;
}

@media (max-width: 768px) {
  .waterfall-grid { flex-direction: column; }
  .waterfall-col { flex: none; width: 100%; }
  .waterfall-card {
    padding: 14px 16px;
    border-radius: 10px;
  }
}
</style>

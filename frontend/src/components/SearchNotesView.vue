<template>
  <div class="search-results-view">
    <div class="search-inputs-wrap">
      <input
        class="input"
        :value="query"
        :placeholder="t('search.searchContent')"
        @input="updateQuery"
      />
      <input
        class="input"
        :value="tag"
        :placeholder="t('search.searchTags')"
        @input="updateTag"
      />
    </div>
    <div v-if="!query && !tag" class="empty-state-big">
      <span class="material-icons-outlined empty-icon">search</span>
      <p>{{ t('search.typeToSearch') }}</p>
    </div>
    <div v-else-if="notes.length === 0" class="empty-state-big">
      <span class="material-icons-outlined empty-icon">search_off</span>
      <p>{{ t('search.noResults') }}</p>
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
  </div>
</template>

<script setup>
import { useI18n } from '../i18n'
import NoteWaterfall from './NoteWaterfall.vue'

defineProps({
  query: { type: String, default: '' },
  tag: { type: String, default: '' },
  notes: { type: Array, default: () => [] },
  hoveredNotePath: { type: String, default: null },
})
const emit = defineEmits([
  'update:query',
  'update:tag',
  'search',
  'dragstart',
  'contextmenu',
  'open-note',
  'update:hovered-note-path',
])

const { t } = useI18n()

function updateQuery(event) {
  emit('update:query', event.target.value)
  emit('search')
}

function updateTag(event) {
  emit('update:tag', event.target.value)
  emit('search')
}
</script>

<style scoped>
.search-results-view {
  padding: 20px 24px;
}
.search-inputs-wrap {
  display: flex;
  flex-wrap: wrap;
  gap: 12px;
  margin-bottom: 24px;
}
.search-inputs-wrap .input {
  flex: 1;
  min-width: 160px;
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
  font-size: 48px;
}

@media (max-width: 768px) {
  .search-results-view { padding: 14px 12px; }
  .search-inputs-wrap { flex-direction: column; gap: 8px; }
  .search-inputs-wrap .input {
    min-width: unset;
    font-size: 16px !important;
  }
}
</style>

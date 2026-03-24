<template>
  <div class="folder-node">
    <div
      class="folder-row"
      :class="{ active: activeFolder === folder.path }"
      @click="$emit('select', folder.path)"
      @dblclick="$emit('rename', folder.path)"
    >
      <span class="material-icons-outlined folder-chevron" @click.stop="toggleExpand">
        {{ expanded ? 'expand_more' : 'chevron_right' }}
      </span>
      <span class="material-icons-outlined folder-ico">folder</span>
      <span class="folder-name">{{ folder.name }}</span>
      <div class="folder-actions">
        <button class="fa-btn" @click.stop="$emit('new-note', folder.path)" title="New note here">
          <span class="material-icons-outlined">note_add</span>
        </button>
        <button class="fa-btn" @click.stop="$emit('new-folder', folder.path)" title="New subfolder">
          <span class="material-icons-outlined">create_new_folder</span>
        </button>
        <button class="fa-btn delete" @click.stop="$emit('delete-folder', folder.path)" title="Delete">
          <span class="material-icons-outlined">delete_outline</span>
        </button>
      </div>
    </div>
    <div v-if="expanded" class="folder-children">
      <div v-if="folder.notes && folder.notes.length">
        <div v-for="note in folder.notes.filter(n => !/^\d{4}-\d{2}-\d{2}_\d{6}/.test(n.name))" :key="note.path" class="tree-note" @click.stop="$emit('open-note', note)">
          <span class="material-icons-outlined">description</span>
          <span class="note-name">{{ note.name }}</span>
        </div>
      </div>
      <div v-if="folder.children && folder.children.length">
        <FolderNode
          v-for="child in folder.children"
          :key="child.path"
          :folder="child"
          :active-folder="activeFolder"
          @select="$emit('select', $event)"
          @new-folder="$emit('new-folder', $event)"
          @rename="$emit('rename', $event)"
          @delete-folder="$emit('delete-folder', $event)"
          @open-note="$emit('open-note', $event)"
          @new-note="$emit('new-note', $event)"
        />
      </div>
    </div>
  </div>
</template>

<script setup>
import { ref } from 'vue'

const props = defineProps({
  folder: Object,
  activeFolder: String,
})

defineEmits(['select', 'new-folder', 'rename', 'delete-folder', 'open-note', 'new-note'])

const expanded = ref(false)

function toggleExpand() {
  expanded.value = !expanded.value
}
</script>

<style scoped>
.folder-node {
  font-size: 13px;
}
.folder-row {
  display: flex;
  align-items: center;
  padding: 5px 8px;
  border-radius: 6px;
  cursor: pointer;
  gap: 4px;
  transition: background 0.1s;
}
.folder-row:hover {
  background: var(--border-light);
}
.folder-row.active {
  background: var(--primary-bg);
  color: var(--primary-dark);
}
.folder-chevron {
  font-size: 18px;
  color: var(--text-muted);
  flex-shrink: 0;
  cursor: pointer;
}
.folder-ico {
  font-size: 18px;
  color: var(--primary);
  flex-shrink: 0;
}
.folder-name {
  flex: 1;
  font-weight: 500;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.folder-actions {
  display: flex;
  gap: 2px;
  opacity: 0;
  transition: opacity 0.1s;
}
.folder-row:hover .folder-actions {
  opacity: 1;
}
.fa-btn {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 24px;
  height: 24px;
  border-radius: 4px;
  border: none;
  background: none;
  color: var(--text-secondary);
  cursor: pointer;
}
.fa-btn:hover { background: var(--border); }
.fa-btn.delete:hover { color: var(--danger); background: var(--danger-light); }
.fa-btn .material-icons-outlined { font-size: 16px; }
.folder-children {
  padding-left: 20px;
}
.tree-note {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 5px 8px;
  border-radius: 6px;
  cursor: pointer;
  color: var(--text);
  transition: background 0.1s;
}
.tree-note:hover { background: var(--border-light); }
.tree-note .material-icons-outlined { font-size: 16px; color: var(--text-secondary); }
.tree-note .note-name {
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  flex: 1;
}
</style>

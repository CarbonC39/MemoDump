<template>
  <div class="folder-node">
    <div
      class="folder-row"
      :class="{ active: activeFolder === folder.path, 'drag-over': isDragOver }"
      @click="$emit('select', folder.path)"
      @dblclick="$emit('rename', folder.path)"
      draggable="true"
      @dragstart.stop="onFolderDragStart"
      @dragover.prevent.stop="onDragOver"
      @dragleave.stop="onDragLeave"
      @drop.prevent.stop="onDrop"
    >
      <span class="material-icons-outlined folder-chevron" @click.stop="toggleExpand">
        {{ expanded ? 'expand_more' : 'chevron_right' }}
      </span>
      <span class="material-icons-outlined folder-ico">folder</span>
      <span class="folder-name">{{ folder.name }}</span>
      <div class="folder-actions">
        <button class="fa-btn" @click.stop="$emit('new-note', folder.path)" :title="t('folder.newNoteHere')">
          <span class="material-icons-outlined">note_add</span>
        </button>
        <button class="fa-btn" @click.stop="$emit('new-folder', folder.path)" :title="t('folder.newSubfolder')">
          <span class="material-icons-outlined">create_new_folder</span>
        </button>
        <button class="fa-btn delete" @click.stop="$emit('delete-folder', folder.path)" :title="t('modals.delete')">
          <span class="material-icons-outlined">delete_outline</span>
        </button>
      </div>
    </div>
    <div v-if="expanded" class="folder-children">
      <div v-if="folder.loading" class="folder-loading">{{ t('notes.loading') }}</div>
      <div v-if="folder.notes && folder.notes.length">
        <div
          v-for="note in folder.notes.filter(n => !/^\d{4}-\d{2}-\d{2}_\d{6}/.test(n.name))"
          :key="note.path"
          class="tree-note"
          draggable="true"
          @dragstart.stop="onNoteDragStart($event, note)"
          @click.stop="$emit('open-note', note)"
        >
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
          @drop-note="$emit('drop-note', $event)"
          @drop-folder="$emit('drop-folder', $event)"
          @expand="$emit('expand', $event)"
        />
      </div>
    </div>
  </div>
</template>

<script setup>
import { ref } from 'vue'
import { useI18n } from '../i18n'

const { t } = useI18n()

const props = defineProps({
  folder: Object,
  activeFolder: String,
})

const emit = defineEmits(['select', 'new-folder', 'rename', 'delete-folder', 'open-note', 'new-note', 'drop-note', 'drop-folder', 'expand'])

const expanded = ref(false)
const isDragOver = ref(false)
let dragLeaveTimer = null

function toggleExpand() {
  expanded.value = !expanded.value
  if (expanded.value && !props.folder.loaded) {
    emit('expand', props.folder.path)
  }
}

function onFolderDragStart(e) {
  e.dataTransfer.effectAllowed = 'move'
  e.dataTransfer.setData('memodump-type', 'folder')
  e.dataTransfer.setData('memodump-path', props.folder.path)
}

function onNoteDragStart(e, note) {
  e.dataTransfer.effectAllowed = 'move'
  e.dataTransfer.setData('memodump-type', 'note')
  e.dataTransfer.setData('memodump-path', note.path)
}

function onDragOver(e) {
  clearTimeout(dragLeaveTimer)
  isDragOver.value = true
  e.dataTransfer.dropEffect = 'move'
  // Auto-expand folder while hovering so user can drop into sub-folders
  if (!expanded.value) {
    dragLeaveTimer = setTimeout(() => { expanded.value = true }, 600)
  }
}

function onDragLeave() {
  clearTimeout(dragLeaveTimer)
  isDragOver.value = false
}

function onDrop(e) {
  clearTimeout(dragLeaveTimer)
  isDragOver.value = false
  const type = e.dataTransfer.getData('memodump-type')
  const path = e.dataTransfer.getData('memodump-path')
  if (!path || path === props.folder.path) return
  if (type === 'note') {
    emit('drop-note', { notePath: path, destFolder: props.folder.path })
  } else if (type === 'folder') {
    // Prevent dropping a folder into its own subtree
    if (props.folder.path.startsWith(path + '/')) return
    emit('drop-folder', { folderPath: path, destFolder: props.folder.path })
  }
}
</script>

<style scoped>
.folder-node {
  font-size: 13px;
}
.folder-loading {
  padding: 6px 28px;
  color: var(--text-muted);
  font-size: 12px;
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
.folder-row.drag-over {
  background: var(--primary-bg);
  outline: 2px solid var(--primary);
  outline-offset: -2px;
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
.tree-note .material-icons-outlined { font-size: 16px; color: var(--text-secondary); flex-shrink: 0; }
.tree-note .note-name {
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  flex: 1;
}
</style>

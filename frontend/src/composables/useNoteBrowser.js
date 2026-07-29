import { computed, ref } from 'vue'
import { isTimestampName, stripMarkdown } from '../utils'

export function presentV2Note(note) {
  const mapped = {
    path: note.id,
    name: note.name,
    tags: note.tags || [],
    modTime: note.modifiedAt || 0,
    preview: note.preview || '',
  }
  return {
    ...mapped,
    hasCustomName: !isTimestampName(mapped.name),
    plainPreview: stripMarkdown(mapped.preview),
  }
}

export function presentV2Folder(folder) {
  return {
    path: folder.id,
    name: folder.name,
    hasChildren: folder.hasChildren,
    loaded: false,
    loading: false,
    children: [],
    notes: [],
  }
}

function findFolderNode(nodes, path) {
  for (const node of nodes) {
    if (node.path === path) return node
    const nested = findFolderNode(node.children || [], path)
    if (nested) return nested
  }
  return null
}

export function useNoteBrowser({ api, storage = globalThis.localStorage } = {}) {
  if (!api) throw new Error('useNoteBrowser requires an API implementation')
  const allNotes = ref([])
  const folders = ref([])
  const currentFolder = ref('')
  const displayNotes = ref([])
  const nextNotesCursor = ref(null)
  const loadingMoreNotes = ref(false)
  const sortMode = ref('modified-desc')

  try {
    const saved = storage?.getItem('memodump_sort')
    if (saved) sortMode.value = saved
  } catch (_) {}

  const sortedDisplayNotes = computed(() => {
    const notes = displayNotes.value.slice()
    const direction = sortMode.value === 'modified-asc' ? 1 : -1
    notes.sort((a, b) => direction * ((a.modTime || 0) - (b.modTime || 0)))
    return notes
  })

  const flatFolders = computed(() => {
    const result = []
    function walk(nodes) {
      for (const folder of nodes) {
        result.push(folder.path)
        if (folder.children) walk(folder.children)
      }
    }
    walk(folders.value)
    return result
  })

  const flatFoldersForPicker = computed(() => {
    const result = []
    function walk(nodes, depth) {
      for (const folder of nodes) {
        result.push({ path: folder.path, name: folder.name, depth })
        if (folder.children?.length) walk(folder.children, depth + 1)
      }
    }
    walk(folders.value, 0)
    return result
  })

  function setSort(mode) {
    sortMode.value = mode
    try { storage?.setItem('memodump_sort', mode) } catch (_) {}
  }

  async function loadFolderNode(path, { force = false } = {}) {
    const node = findFolderNode(folders.value, path)
    if (!node || (node.loaded && !force)) return
    node.loading = true
    try {
      const [foldersRes, notesRes] = await Promise.all([
        api.listFoldersV2(path),
        api.listNotesV2(path),
      ])
      node.children = foldersRes.data.items.map(presentV2Folder)
      node.notes = notesRes.data.items.map(presentV2Note)
      node.loaded = true
    } finally {
      node.loading = false
    }
  }

  async function loadMoreNotes() {
    if (!nextNotesCursor.value || loadingMoreNotes.value) return
    loadingMoreNotes.value = true
    try {
      const response = await api.listNotesV2(currentFolder.value, {
        cursor: nextNotesCursor.value,
      })
      displayNotes.value = [
        ...displayNotes.value,
        ...response.data.items.map(presentV2Note),
      ]
      if (!currentFolder.value) allNotes.value = displayNotes.value
      nextNotesCursor.value = response.data.nextCursor
    } finally {
      loadingMoreNotes.value = false
    }
  }

  async function loadFolderPage(path) {
    currentFolder.value = path
    try {
      const response = await api.listNotesV2(path)
      displayNotes.value = response.data.items.map(presentV2Note)
      nextNotesCursor.value = response.data.nextCursor
    } catch (error) {
      displayNotes.value = []
      nextNotesCursor.value = null
      throw error
    }
  }

  async function loadAll() {
    try {
      const [notesRes, foldersRes] = await Promise.all([
        api.listNotesV2(''),
        api.listFoldersV2(''),
      ])
      allNotes.value = notesRes.data.items.map(presentV2Note)
      nextNotesCursor.value = notesRes.data.nextCursor
      folders.value = foldersRes.data.items.map(presentV2Folder)
      if (currentFolder.value) {
        const folderNotesRes = await api.listNotesV2(currentFolder.value)
        displayNotes.value = folderNotesRes.data.items.map(presentV2Note)
        nextNotesCursor.value = folderNotesRes.data.nextCursor
      } else {
        displayNotes.value = allNotes.value
      }
    } catch (_) {
      // Authentication failures are handled by the shared API interceptor.
    }
  }

  async function refreshRootFolders() {
    const response = await api.listFoldersV2('')
    folders.value = response.data.items.map(presentV2Folder)
  }

  function showAllNotes() {
    currentFolder.value = ''
    displayNotes.value = allNotes.value
  }

  return {
    allNotes,
    folders,
    currentFolder,
    displayNotes,
    nextNotesCursor,
    loadingMoreNotes,
    sortMode,
    sortedDisplayNotes,
    flatFolders,
    flatFoldersForPicker,
    setSort,
    loadFolderNode,
    loadMoreNotes,
    loadFolderPage,
    loadAll,
    refreshRootFolders,
    showAllNotes,
  }
}

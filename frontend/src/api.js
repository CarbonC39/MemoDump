import axios from 'axios'
import router from './router'
import localApi from './localApi'

const api = axios.create({
    baseURL: '/api',
    withCredentials: true,
})

api.interceptors.response.use(
    res => res,
    err => {
        if (err.response && err.response.status === 401) {
            // Navigate via the router so the redirect honours the active history
            // mode (clean URLs in the browser, hash under Wails). Guarding on the
            // route name prevents the redirect loop that a hardcoded path caused.
            const current = router.currentRoute.value
            if (current.name !== 'Login') {
                router.replace({ name: 'Login', query: { redirect: current.fullPath } })
            }
        }
        return Promise.reject(err)
    }
)

// Normalize a v2 note document to the shape the rest of the UI reads: `id`
// becomes `path`, and the optimistic-concurrency `revision` is carried through.
const toLegacyNote = (doc) => ({ ...doc, path: doc.id })

const remoteApi = {
    login(username, password) {
        return api.post('/login', { username, password })
    },
    logout() {
        return api.post('/logout')
    },
    listNotes(folder) {
        const params = folder ? { folder } : {}
        return api.get('/notes', { params })
    },
    getNote(path) {
        return api.get(`/v2/notes/${path}`).then(res => ({ ...res, data: toLegacyNote(res.data) }))
    },
    createNote(data) {
        return api.post('/v2/notes', data).then(res => ({ ...res, data: toLegacyNote(res.data) }))
    },
    updateNote(path, data) {
        // v2 requires baseRevision; callers pass it via data.baseRevision.
        return api.put(`/v2/notes/${path}`, data).then(res => ({ ...res, data: toLegacyNote(res.data) }))
    },
    deleteNote(path, baseRevision) {
        if (baseRevision) {
            return api.delete(`/v2/notes/${path}`, { params: { baseRevision } })
        }
        // Fetch the current revision so every delete is CAS-protected. A note
        // that is already gone counts as deleted.
        return this.getNote(path)
            .then(doc => api.delete(`/v2/notes/${path}`, { params: { baseRevision: doc.data.revision } }))
            .catch(err => {
                if (err?.response?.status === 404) return { data: { status: 'ok' } }
                throw err
            })
    },
    moveNote(path, destination) {
        return api.put(`/v2/move/${path}`, { destination }).then(res => ({ ...res, data: toLegacyNote(res.data) }))
    },
    duplicateNote(path) {
        return api.post(`/v2/duplicate/${path}`).then(res => ({ ...res, data: toLegacyNote(res.data) }))
    },
    moveFolder(path, destination) {
        return api.put(`/move/folder/${path}`, { destination })
    },
    listFolders() {
        return api.get('/folders')
    },
    listNotesV2(parent = '', { cursor = '', limit = 50, sort = 'modified-desc' } = {}) {
        const params = { parent, limit, sort }
        if (cursor) params.cursor = cursor
        return api.get('/v2/notes', { params })
    },
    listFoldersV2(parent = '') {
        return api.get('/v2/folders', { params: { parent } })
    },
    searchV2(q, tag, { cursor = '', limit = 50, sort = 'modified-desc' } = {}) {
        const params = { q, tag, limit, sort }
        if (cursor) params.cursor = cursor
        return api.get('/v2/search', { params })
    },
    createFolder(path) {
        return api.post('/folders', { path })
    },
    renameFolder(path, newName) {
        return api.put(`/folders/${path}`, { newName })
    },
    deleteFolder(path) {
        return api.delete(`/folders/${path}`)
    },
    search(q, tag) {
        const params = {}
        if (q) params.q = q
        if (tag) params.tag = tag
        return api.get('/search', { params })
    },
    ping() {
        return api.get('/ping')
    },
    config() {
        return api.get('/config')
    },
    uploadNote(formData, folder) {
        if (folder) formData.append('folder', folder)
        return api.post('/upload', formData, {
            headers: { 'Content-Type': 'multipart/form-data' },
        })
    },
    imageUpload(key, blob, contentType, targetId) {
        return api.put(`/images/${key}`, blob, {
            headers: {
                'Content-Type': contentType || 'application/octet-stream',
                ...(targetId ? { 'X-MemoDump-Image-Target': targetId } : {}),
            },
        })
    },
    imageConfigGet() {
        return api.get('/config/image')
    },
    imageConfigSave(config) {
        return api.put('/config/image', config)
    },
    imageConfigTest(config) {
        return api.post('/config/image/test', config)
    },
    syncStatus() {
        return api.get('/sync/status')
    },
    syncEnable() {
        return api.post('/sync/enable')
    },
    syncRun() {
        return api.post('/sync/run')
    },
    syncDisable() {
        return api.post('/sync/disable')
    },
    syncTest() {
        return api.post('/sync/test')
    },
    syncRecovery() {
        return api.get('/sync/recovery')
    },
}

// Select backend: localApi (IndexedDB, no server) when built with VITE_LOCAL=1,
// otherwise the axios client that talks to the Go server.
export default import.meta.env.VITE_LOCAL === '1' ? localApi : remoteApi

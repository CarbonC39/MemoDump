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
        return api.get(`/notes/${path}`)
    },
    createNote(data) {
        return api.post('/notes', data)
    },
    updateNote(path, data) {
        return api.put(`/notes/${path}`, data)
    },
    deleteNote(path) {
        return api.delete(`/notes/${path}`)
    },
    moveNote(path, destination) {
        return api.put(`/move/${path}`, { destination })
    },
    duplicateNote(path) {
        return api.post(`/duplicate/${path}`)
    },
    moveFolder(path, destination) {
        return api.put(`/move/folder/${path}`, { destination })
    },
    listFolders() {
        return api.get('/folders')
    },
    listNotesV2(parent = '', { cursor = '', limit = 50 } = {}) {
        const params = { parent, limit }
        if (cursor) params.cursor = cursor
        return api.get('/v2/notes', { params })
    },
    listFoldersV2(parent = '') {
        return api.get('/v2/folders', { params: { parent } })
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
}

// Select backend: localApi (IndexedDB, no server) when built with VITE_LOCAL=1,
// otherwise the axios client that talks to the Go server.
export default import.meta.env.VITE_LOCAL === '1' ? localApi : remoteApi

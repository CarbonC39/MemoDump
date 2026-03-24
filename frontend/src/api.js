import axios from 'axios'

const api = axios.create({
    baseURL: '/api',
    withCredentials: true,
})

api.interceptors.response.use(
    res => res,
    err => {
        if (err.response && err.response.status === 401) {
            // Don't redirect if we're already on the login page (e.g. wrong password)
            if (!window.location.pathname.startsWith('/login')) {
                window.location.href = '/login'
            }
        }
        return Promise.reject(err)
    }
)

export default {
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
    listFolders() {
        return api.get('/folders')
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
}

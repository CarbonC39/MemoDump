import { createRouter, createWebHashHistory, createWebHistory } from 'vue-router'
import LoginView from './views/LoginView.vue'
import MainView from './views/MainView.vue'

const routes = [
    { path: '/login', name: 'Login', component: LoginView },
    { path: '/', name: 'Main', component: MainView },
]

// Wails WebView needs hash history; normal browsers use clean history
const isWails = typeof window !== 'undefined' && typeof window.go !== 'undefined'

const router = createRouter({
    history: isWails ? createWebHashHistory() : createWebHistory(),
    routes,
})

export default router

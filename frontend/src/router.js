import { createRouter, createWebHashHistory } from 'vue-router'
import LoginView from './views/LoginView.vue'
import MainView from './views/MainView.vue'

const routes = [
    { path: '/login', name: 'Login', component: LoginView },
    { path: '/', name: 'Main', component: MainView },
]

const router = createRouter({
    history: createWebHashHistory(),
    routes,
})

export default router

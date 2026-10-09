import { ref, reactive, onMounted, onBeforeUnmount } from 'vue'
import { useRouter } from 'vue-router'
import apiClient from '../api'
import { isWailsApp, isLocalBuild } from './runtime'

export function useAppInit() {
  const router = useRouter()

  const wailsDataDir = ref('')
  const serverNoAuth = ref(false)

  async function initWails() {
    if (!isWailsApp) return
    try {
      wailsDataDir.value = await window.go.main.App.GetDataDir()
    } catch (_) {}
  }

  async function changeDataDir() {
    if (!isWailsApp) return
    const changed = await window.go.main.App.ChangeDataDir()
    if (changed) {
      wailsDataDir.value = await window.go.main.App.GetDataDir()
    }
  }

  // Sidebar state
  const mobileSidebar = ref(false)
  const openSections = reactive({ search: false, all: false, storage: false })

  let keepaliveInterval = null

  function toggleSection(section) {
    openSections[section] = !openSections[section]
  }

  async function doLogout() {
    await apiClient.logout()
    router.push('/login')
  }

  onMounted(async () => {
    initWails()
    try { serverNoAuth.value = (await apiClient.config()).data.noAuth } catch (_) {}
    // Ping every 15 minutes to keep session alive while app is open
    keepaliveInterval = setInterval(() => {
      apiClient.ping().catch(() => {})
    }, 15 * 60 * 1000)
  })

  onBeforeUnmount(() => {
    if (keepaliveInterval) clearInterval(keepaliveInterval)
  })

  return { isWailsApp, isLocalBuild, wailsDataDir, serverNoAuth, mobileSidebar, openSections, toggleSection, initWails, changeDataDir, doLogout }
}

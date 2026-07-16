import { createApp } from 'vue'
import App from './App.vue'
import router from './router'
import { useI18n } from './i18n'
import 'material-icons/iconfont/outlined.css'
import './style.css'

// Initialize locale before mounting
const { setLocale } = useI18n()
let lang = 'en'
try {
  const raw = localStorage.getItem('memodump_settings')
  if (raw) {
    const settings = JSON.parse(raw)
    if (settings.language) lang = settings.language
  } else {
    // First visit: detect from browser
    const browserLang = navigator.language
    if (browserLang === 'zh-CN' || browserLang.startsWith('zh')) {
      lang = 'zh-CN'
    }
  }
} catch (_) {}
setLocale(lang)

const app = createApp(App)
app.use(router)
app.mount('#app')

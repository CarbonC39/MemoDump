import { createApp } from 'vue'
import App from './App.vue'
import router from './router'
import { useI18n } from './i18n'
import { initTheme } from './composables/useTheme.js'
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

// Application settings and user custom CSS live in <style> elements (not inline)
// so that user CSS can override app-set variables by source order. Target order
// in <head>:  #app-settings < <link /custom.css> < #app-custom  =>
// at equal specificity, later source order wins, so the cascade is:
// app-custom (in-app editor) > custom.css (server --css seed) > app-settings.
// #app-settings is therefore inserted BEFORE the static custom.css link;
// #app-custom is appended last.
// Created BEFORE app.mount: SettingsPanel mounts at init (v-show-toggled) and
// its onMounted -> applySettings() writes into #app-settings; the element must
// already exist or first-load application silently no-ops.
function ensureStyle(id, before) {
  let el = document.getElementById(id)
  if (!el) {
    el = document.createElement('style'); el.id = id
    if (before) document.head.insertBefore(el, before)
    else document.head.appendChild(el)
  }
  return el
}
const customCssLink = document.querySelector('link[rel="stylesheet"][href="/custom.css"]')
ensureStyle('app-settings', customCssLink)
ensureStyle('app-custom')
document.body.dataset.build = (typeof window.go !== 'undefined') ? 'desktop' : 'server'

// Apply persisted theme (FOUC prevention handled in index.html inline script)
initTheme()

app.mount('#app')

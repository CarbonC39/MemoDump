import { ref } from 'vue'
import en from './en.json'
import zhCN from './zh-CN.json'

const messages = { en, 'zh-CN': zhCN }

const locale = ref('en')

function t(key) {
  const keys = key.split('.')
  let result = messages[locale.value]
  for (const k of keys) {
    if (result == null || typeof result !== 'object') break
    result = result[k]
  }
  if (typeof result === 'string') return result
  // Fallback to English
  let enResult = messages['en']
  for (const k of keys) {
    if (enResult == null || typeof enResult !== 'object') break
    enResult = enResult[k]
  }
  if (typeof enResult === 'string') return enResult
  return key
}

function setLocale(loc) {
  locale.value = loc
  try {
    const raw = localStorage.getItem('memodump_settings')
    const settings = raw ? JSON.parse(raw) : {}
    settings.language = loc
    localStorage.setItem('memodump_settings', JSON.stringify(settings))
  } catch (_) {}
  document.documentElement.lang = loc
}

function useI18n() {
  return { t, locale, setLocale }
}

export { useI18n }

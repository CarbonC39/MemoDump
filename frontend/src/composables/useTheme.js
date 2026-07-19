// frontend/src/composables/useTheme.js
import { ref, computed, onBeforeUnmount } from 'vue'

const STORAGE_KEY = 'memodump_theme'

// Read persisted preference
function readStored() {
  try {
    const v = localStorage.getItem(STORAGE_KEY)
    if (v === 'dark' || v === 'light' || v === 'system') return v
  } catch (_) {}
  return 'system'
}

// Apply data-theme attribute to <html>
function applyDOM(theme) {
  if (theme === 'dark') {
    document.documentElement.setAttribute('data-theme', 'dark')
  } else if (theme === 'light') {
    document.documentElement.setAttribute('data-theme', 'light')
  } else {
    // system: follow OS preference
    document.documentElement.removeAttribute('data-theme')
    if (window.matchMedia('(prefers-color-scheme: dark)').matches) {
      document.documentElement.setAttribute('data-theme', 'dark')
    }
  }
  // Update PWA theme-color meta tag
  try {
    const meta = document.querySelector('meta[name="theme-color"]')
    if (meta) {
      const isDark = document.documentElement.getAttribute('data-theme') === 'dark'
      meta.content = isDark ? '#242233' : '#6495ED'
    }
  } catch (_) {}
}

// Standalone init — call once in main.js before mount (no Vue lifecycle needed)
export function initTheme() {
  const stored = readStored()
  theme.value = stored
  applyDOM(stored)
  // System listener managed globally — no cleanup needed for app lifetime
  if (stored === 'system') {
    const mql = window.matchMedia('(prefers-color-scheme: dark)')
    mql.addEventListener('change', () => {
      // Re-check stored value in case user changed it since init
      if (readStored() === 'system') applyDOM('system')
    })
  }
}

// Shared reactive state — module-level so all useTheme() callers see the same value.
const theme = ref(readStored())

export function useTheme() {
  let mql = null

  function setTheme(t) {
    theme.value = t
    try { localStorage.setItem(STORAGE_KEY, t) } catch (_) {}
    applyDOM(t)
    // Manage system listener
    if (t === 'system') {
      if (!mql) {
        mql = window.matchMedia('(prefers-color-scheme: dark)')
        mql.addEventListener('change', onSystemChange)
      }
    } else {
      if (mql) {
        mql.removeEventListener('change', onSystemChange)
        mql = null
      }
    }
  }

  function onSystemChange(e) {
    if (theme.value === 'system') applyDOM('system')
  }

  function initTheme() {
    const stored = readStored()
    applyDOM(stored)
    if (stored === 'system') {
      mql = window.matchMedia('(prefers-color-scheme: dark)')
      mql.addEventListener('change', onSystemChange)
    }
  }

  const themeIcon = computed(() => {
    // Show current state: moon when dark, sun when light
    if (theme.value === 'dark') return 'dark_mode'
    if (theme.value === 'light') return 'light_mode'
    // system mode: DOM is the source of truth (may change without setTheme)
    const isDark = document.documentElement.getAttribute('data-theme') === 'dark'
    return isDark ? 'dark_mode' : 'light_mode'
  })

  onBeforeUnmount(() => {
    if (mql) mql.removeEventListener('change', onSystemChange)
  })

  return { theme, setTheme, initTheme, themeIcon }
}

<template>
  <div class="settings-page">
    <div class="settings-header">
      <h2>{{ t('settings.title') }}</h2>
      <button class="btn btn-icon btn-ghost" @click="$emit('close')">
        <span class="material-icons-outlined">close</span>
      </button>
    </div>

    <div class="settings-body">
      <!-- Preview card -->
      <div class="preview-card">
        <div class="preview-card-header">{{ t('settings.preview') }}</div>
        <div class="preview-section">
          <div class="preview-label">{{ t('settings.previewProportional') }}</div>
          <div class="preview-sample preview-proportional">
            <p>The quick brown fox jumps over the lazy dog.</p>
            <p><strong>Bold text</strong> <em>italic text</em></p>
            <p>简体中文示例文字 繁體中文範例</p>
          </div>
        </div>
        <div class="preview-section">
          <div class="preview-label">{{ t('settings.previewMonospace') }}</div>
          <div class="preview-sample preview-monospace">
            function hello() {<br/>
            &nbsp;&nbsp;return "Hello, world!";<br/>
            }
          </div>
        </div>
      </div>

      <hr class="settings-divider" />

      <!-- Interface section -->
      <div class="settings-section">
        <div class="settings-section-label">{{ t('settings.language') }}</div>
        <hr class="settings-section-line" />

        <div class="setting-row">
          <span class="setting-row-label">{{ t('settings.language') }}</span>
          <select class="input input-select" :value="locale" @change="setLocale($event.target.value)">
            <option value="en">English</option>
            <option value="zh-CN">简体中文</option>
          </select>
        </div>

        <div class="setting-row">
          <span class="setting-row-label">{{ t('settings.appFontSize') }}</span>
          <div class="number-input-group">
            <input
              type="number"
              class="input input-number"
              v-model.number="local.appFontSize"
              :min="10" :max="24" :step="1"
              @blur="clamp('appFontSize', 10, 24)"
              @change="onSettingChange"
            />
            <span class="unit">px</span>
          </div>
        </div>
      </div>

      <hr class="settings-divider" />

      <!-- Editor section -->
      <div class="settings-section">
        <div class="settings-section-label">{{ t('settings.editorSection') }}</div>
        <hr class="settings-section-line" />

        <div class="setting-row">
          <span class="setting-row-label">{{ t('settings.editorWysiwygFontSize') }}</span>
          <div class="number-input-group">
            <input
              type="number"
              class="input input-number"
              v-model.number="local.editorWysiwygFontSize"
              :min="12" :max="32" :step="1"
              @blur="clamp('editorWysiwygFontSize', 12, 32)"
              @change="onSettingChange"
            />
            <span class="unit">px</span>
          </div>
        </div>

        <div class="setting-row">
          <span class="setting-row-label">{{ t('settings.editorRawFontSize') }}</span>
          <div class="number-input-group">
            <input
              type="number"
              class="input input-number"
              v-model.number="local.editorRawFontSize"
              :min="12" :max="32" :step="1"
              @blur="clamp('editorRawFontSize', 12, 32)"
              @change="onSettingChange"
            />
            <span class="unit">px</span>
          </div>
        </div>

        <div class="setting-row">
          <span class="setting-row-label">{{ t('settings.monospaceFont') }}</span>
          <select
            v-if="!customFields['editorMonospace']"
            class="input input-select"
            :value="local.editorMonospace"
            @change="onFontSelect('editorMonospace', $event.target.value)"
          >
            <option v-for="f in installedFonts" :key="f" :value="f">{{ f }}</option>
            <option value="__custom__">Custom...</option>
          </select>
          <div v-else class="custom-font-row">
            <input
              type="text"
              class="input"
              v-model="customValues['editorMonospace']"
              @input="onCustomInput('editorMonospace')"
            />
            <button class="btn btn-ghost btn-sm" @click="customFields['editorMonospace'] = false">&#8617;</button>
          </div>
        </div>
      </div>

      <hr class="settings-divider" />

      <!-- Advanced Typography (collapsed) -->
      <details class="settings-advanced">
        <summary>
          <span class="material-icons-outlined">chevron_right</span>
          {{ t('settings.advancedTypography') }}
        </summary>
        <div class="advanced-body">
          <!-- Writing System Selector -->
          <div class="setting-group">
            <label class="setting-label">{{ t('settings.fontsFor') }}</label>
            <select class="input" v-model="selectedSystem">
              <option value="latin">Latin</option>
              <option value="sc">简体中文</option>
              <option value="tcHK">繁體中文（香港）</option>
              <option value="tcTW">繁體中文（臺灣）</option>
            </select>
          </div>

          <!-- Proportional toggle -->
          <div class="setting-group">
            <label class="setting-label">{{ t('settings.proportional') }}</label>
            <div class="radio-row">
              <label class="radio-label">
                <input
                  type="radio"
                  value="sans-serif"
                  :checked="currentFonts.proportional === 'sans-serif'"
                  @change="setProportional('sans-serif')"
                />
                {{ t('settings.sansSerif') }}
              </label>
              <label class="radio-label">
                <input
                  type="radio"
                  value="serif"
                  :checked="currentFonts.proportional === 'serif'"
                  @change="setProportional('serif')"
                />
                {{ t('settings.serif') }}
              </label>
            </div>
          </div>

          <!-- Sans-serif font dropdown -->
          <div class="setting-group">
            <label class="setting-label">{{ t('settings.sansSerifFont') }}</label>
            <select
              v-if="!customFields[sansSerifKey]"
              class="input"
              :value="currentFonts.sansSerif"
              @change="onFontSelect(sansSerifKey, $event.target.value)"
            >
              <option v-for="f in installedFonts" :key="f" :value="f">{{ f }}</option>
              <option value="__custom__">Custom...</option>
            </select>
            <div v-else class="custom-font-row">
              <input
                type="text"
                class="input"
                v-model="customValues[sansSerifKey]"
                @input="onCustomInput(sansSerifKey)"
              />
              <button class="btn btn-ghost btn-sm" @click="customFields[sansSerifKey] = false">&#8617;</button>
            </div>
          </div>

          <!-- Serif font dropdown -->
          <div class="setting-group">
            <label class="setting-label">{{ t('settings.serifFont') }}</label>
            <select
              v-if="!customFields[serifKey]"
              class="input"
              :value="currentFonts.serif"
              @change="onFontSelect(serifKey, $event.target.value)"
            >
              <option v-for="f in installedFonts" :key="f" :value="f">{{ f }}</option>
              <option value="__custom__">Custom...</option>
            </select>
            <div v-else class="custom-font-row">
              <input
                type="text"
                class="input"
                v-model="customValues[serifKey]"
                @input="onCustomInput(serifKey)"
              />
              <button class="btn btn-ghost btn-sm" @click="customFields[serifKey] = false">&#8617;</button>
            </div>
          </div>
        </div>
      </details>

      <hr class="settings-divider" />

      <!-- Reset -->
      <button class="btn btn-ghost reset-btn" @click="resetToDefaults">
        <span class="material-icons-outlined">restart_alt</span>
        {{ t('settings.resetToDefaults') }}
      </button>
    </div>
  </div>
</template>

<script setup>
import { ref, reactive, computed, onMounted } from 'vue'
import { useI18n } from '../i18n'
const { t, locale, setLocale } = useI18n()

const emit = defineEmits(['close'])

// Must NOT define props.visible — visibility is controlled by parent v-show.

// --- Fonts to detect ---
const FONTS_TO_DETECT = [
  'Arial', 'Helvetica', 'Helvetica Neue', 'Inter', 'Segoe UI', 'Roboto', 'Open Sans',
  'Verdana', 'Tahoma', 'Georgia', 'Times New Roman', 'Palatino', 'Garamond',
  'Cambria', 'Merriweather', 'Roboto Mono', 'Consolas', 'Menlo', 'Monaco',
  'Courier New', 'Source Code Pro', 'Fira Code', 'JetBrains Mono', 'Cascadia Code',
  'SF Mono', 'Inconsolata',
  'PingFang SC', 'PingFang TC', 'PingFang HK',
  'Microsoft YaHei', 'Microsoft JhengHei',
  'Noto Sans CJK SC', 'Noto Sans CJK TC', 'Noto Sans CJK HK',
  'Noto Serif CJK SC', 'Noto Serif CJK TC', 'Noto Serif CJK HK',
  'Noto Sans Mono CJK SC', 'Noto Sans Mono CJK TC', 'Noto Sans Mono CJK HK',
  'Hiragino Sans GB', 'Hiragino Sans CNS',
  'Heiti SC', 'Heiti TC', 'STHeiti',
  'Songti SC', 'Songti TC', 'STSong',
  'SimSun', 'SimHei', 'KaiTi',
]

// --- Defaults ---
const DEFAULTS = Object.freeze({
  appFontSize: 14,
  editorWysiwygFontSize: 16,
  editorRawFontSize: 14,
  editorMonospace: 'Consolas',
  editorFonts: {
    latin:    { proportional: 'sans-serif', serif: 'Georgia',            sansSerif: 'Arial' },
    sc:       { proportional: 'sans-serif', serif: 'Noto Serif CJK SC', sansSerif: 'PingFang SC' },
    tcHK:     { proportional: 'sans-serif', serif: 'Noto Serif CJK HK', sansSerif: 'PingFang HK' },
    tcTW:     { proportional: 'sans-serif', serif: 'Noto Serif CJK TC', sansSerif: 'PingFang TC' },
  },
})

// --- Load settings from localStorage ---
function loadSettings() {
  try {
    const raw = localStorage.getItem('memodump_settings')
    if (raw) {
      const parsed = JSON.parse(raw)
      return deepMerge(DEFAULTS, parsed)
    }
  } catch (e) { console.warn('Failed to load font settings:', e) }
  return JSON.parse(JSON.stringify(DEFAULTS))
}

function deepMerge(defaults, overrides) {
  const result = {}
  for (const key of Object.keys(defaults)) {
    if (overrides[key] === undefined) {
      result[key] = defaults[key]
    } else if (typeof defaults[key] === 'object' && !Array.isArray(defaults[key])) {
      result[key] = deepMerge(defaults[key], overrides[key])
    } else {
      result[key] = overrides[key]
    }
  }
  return result
}

// --- Reactive local state ---
const local = reactive(loadSettings())

// --- Font detection ---
const installedFonts = ref([])
function detectFonts() {
  const detected = new Set()
  for (const font of FONTS_TO_DETECT) {
    try {
      if (document.fonts.check('12px "' + font + '"')) {
        detected.add(font)
      }
    } catch (_) {}
  }
  installedFonts.value = [...detected].sort()
}
onMounted(() => {
  detectFonts()
  // Apply CSS variables on initial load so settings take effect before any user interaction
  applySettings()
})

// --- Custom font mode tracking ---
const customFields = reactive({})
const customValues = reactive({})

// --- Font value helpers ---
function setFontValue(fieldKey, value) {
  if (fieldKey === 'editorMonospace') {
    local.editorMonospace = value
  } else {
    const [system, prop] = fieldKey.split('.')
    local.editorFonts[system][prop] = value
  }
  onSettingChange()
}

function getFontValue(fieldKey) {
  if (fieldKey === 'editorMonospace') return local.editorMonospace
  const [system, prop] = fieldKey.split('.')
  return local.editorFonts[system][prop]
}

function onFontSelect(fieldKey, val) {
  if (val === '__custom__') {
    customFields[fieldKey] = true
    customValues[fieldKey] = getFontValue(fieldKey)
  } else {
    customFields[fieldKey] = false
    setFontValue(fieldKey, val)
  }
}

function onCustomInput(fieldKey) {
  setFontValue(fieldKey, customValues[fieldKey])
}

// --- Apply CSS variables to :root ---
// Moved from MainView.vue — SettingsPanel owns the full settings lifecycle.
function applySettings() {
  try {
    const raw = localStorage.getItem('memodump_settings')
    if (!raw) return
    const s = JSON.parse(raw)
    const root = document.documentElement

    root.style.setProperty('--app-zoom', ((s.appFontSize || 14) / 14).toFixed(2))
    root.style.setProperty('--editor-wysiwyg-font-size', (s.editorWysiwygFontSize || 16) + 'px')
    root.style.setProperty('--editor-raw-font-size', (s.editorRawFontSize || 14) + 'px')

    if (s.editorFonts) {
      const proportionalParts = []
      for (const key of ['latin', 'sc', 'tcHK', 'tcTW']) {
        const fs = s.editorFonts[key]
        if (!fs) continue
        const fontName = fs.proportional === 'serif' ? fs.serif : fs.sansSerif
        if (fontName) proportionalParts.push(fontName.includes(' ') ? `"${fontName}"` : fontName)
      }
      proportionalParts.push('sans-serif')
      root.style.setProperty('--editor-font-proportional', proportionalParts.join(', '))
    }

    if (s.editorMonospace) {
      const name = s.editorMonospace.includes(' ') ? `"${s.editorMonospace}"` : s.editorMonospace
      root.style.setProperty('--editor-font-monospace', `${name}, monospace`)
    }
  } catch (e) { console.warn('Failed to apply font settings:', e) }
}

// --- Persist to localStorage ---
function saveSettings() {
  try {
    const raw = localStorage.getItem('memodump_settings')
    const existing = raw ? JSON.parse(raw) : {}
    localStorage.setItem('memodump_settings', JSON.stringify({ ...local, language: existing.language }))
  } catch (_) {}
}

// Called on every number/select change
function onSettingChange() {
  saveSettings()
  applySettings()
}

// --- Number input clamp on blur ---
function clamp(field, min, max) {
  if (local[field] < min) local[field] = min
  if (local[field] > max) local[field] = max
  saveSettings()
  applySettings()
}

// --- Writing system selection (advanced typography) ---
const selectedSystem = ref('latin')

const currentFonts = computed(() => {
  return local.editorFonts[selectedSystem.value]
})

const sansSerifKey = computed(() => selectedSystem.value + '.sansSerif')
const serifKey = computed(() => selectedSystem.value + '.serif')

function setProportional(val) {
  local.editorFonts[selectedSystem.value].proportional = val
  onSettingChange()
}

// --- Reset ---
function resetToDefaults() {
  const fresh = JSON.parse(JSON.stringify(DEFAULTS))
  for (const key of Object.keys(customFields)) {
    delete customFields[key]
  }
  for (const key of Object.keys(customValues)) {
    delete customValues[key]
  }
  Object.assign(local, fresh)
  onSettingChange()
}
</script>

<style scoped>
.settings-page {
  height: 100%;
  overflow-y: auto;
  background: var(--bg);
}

.settings-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 16px 24px;
  border-bottom: 1px solid var(--border);
  background: var(--bg-card);
  position: sticky;
  top: 0;
  z-index: 1;
}

.settings-header h2 {
  font-size: 18px;
  font-weight: 700;
}

.settings-body {
  max-width: 600px;
  margin: 0 auto;
  padding: 24px;
}

/* ---- Preview card ---- */
.preview-card {
  background: var(--bg-card);
  border: 1px solid var(--border);
  border-radius: var(--radius-lg);
  padding: 16px 20px;
}

.preview-card-header {
  font-size: 12px;
  font-weight: 600;
  color: var(--text-muted);
  text-transform: uppercase;
  letter-spacing: 0.5px;
  margin-bottom: 12px;
}

.preview-section {
  margin-bottom: 12px;
}
.preview-section:last-child { margin-bottom: 0; }

.preview-label {
  font-size: 11px;
  color: var(--text-muted);
  margin-bottom: 6px;
}

.preview-proportional {
  font-family: var(--editor-font-proportional);
  font-size: var(--editor-wysiwyg-font-size);
  line-height: 1.75;
  color: var(--text);
  background: var(--bg);
  border-radius: var(--radius);
  padding: 12px 16px;
}

.preview-proportional p { margin: 0.3em 0; }

.preview-monospace {
  font-family: var(--editor-font-monospace);
  font-size: var(--editor-raw-font-size);
  line-height: 1.7;
  color: var(--text);
  background: var(--bg);
  border-radius: var(--radius);
  padding: 12px 16px;
  white-space: pre-wrap;
}

/* ---- Dividers ---- */
.settings-divider {
  border: none;
  border-top: 1px solid var(--border-light);
  margin: 20px 0;
}

/* ---- Section headers ---- */
.settings-section-label {
  font-size: 16px;
  font-weight: 600;
  color: var(--text);
}

.settings-section-line {
  border: none;
  border-top: 1px solid var(--border-light);
  margin: 8px 0 14px;
}

/* ---- Setting rows ---- */
.setting-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 14px;
}
.setting-row:last-child { margin-bottom: 0; }

.setting-row-label {
  font-size: 13px;
  font-weight: 500;
  color: var(--text-secondary);
  flex-shrink: 0;
}

/* ---- Number input ---- */
.number-input-group {
  display: flex;
  align-items: center;
  gap: 4px;
}

.input-number {
  width: 64px;
  padding: 6px 8px;
  text-align: center;
  font-size: 13px;
}
/* Keep native spinner visible */
.input-number::-webkit-inner-spin-button,
.input-number::-webkit-outer-spin-button { opacity: 1; }

.unit {
  font-size: 13px;
  color: var(--text-secondary);
}

/* ---- Select ---- */
.input-select {
  width: 180px;
  font-size: 13px;
  padding: 6px 8px;
}

/* ---- Advanced typography ---- */
.settings-advanced {
  padding: 0;
}
.settings-advanced > summary {
  display: flex;
  align-items: center;
  gap: 4px;
  font-size: 13px;
  font-weight: 500;
  color: var(--text-secondary);
  cursor: pointer;
  list-style: none;
  padding: 2px 0;
}
.settings-advanced > summary::-webkit-details-marker { display: none; }
.settings-advanced > summary .material-icons-outlined {
  font-size: 18px;
  transition: transform 0.15s;
}
.settings-advanced[open] > summary .material-icons-outlined {
  transform: rotate(90deg);
}

.advanced-body {
  margin-top: 14px;
  padding: 14px;
  border: 1px solid var(--border-light);
  border-radius: var(--radius);
  background: var(--bg-card);
}

.advanced-body .setting-group {
  margin-bottom: 12px;
}
.advanced-body .setting-group:last-child { margin-bottom: 0; }

.advanced-body .setting-label {
  display: block;
  font-size: 12px;
  font-weight: 600;
  color: var(--text-secondary);
  margin-bottom: 4px;
  text-transform: uppercase;
  letter-spacing: 0.5px;
}

.advanced-body .input {
  font-size: 13px;
  padding: 6px 8px;
}

.radio-row {
  display: flex;
  gap: 16px;
}

.radio-label {
  display: flex;
  align-items: center;
  gap: 4px;
  font-size: 13px;
  color: var(--text);
  cursor: pointer;
}

.radio-label input[type="radio"] {
  accent-color: var(--primary);
}

.custom-font-row {
  display: flex;
  gap: 6px;
  align-items: center;
}
.custom-font-row .input { flex: 1; }
.custom-font-row .btn-sm {
  padding: 4px 8px;
  font-size: 14px;
  line-height: 1;
  flex-shrink: 0;
}

/* ---- Reset button ---- */
.reset-btn {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 6px;
  width: 100%;
  font-size: 13px;
  color: var(--text-muted);
  padding: 10px;
  border-radius: var(--radius);
  transition: color 0.15s, background 0.15s;
}
.reset-btn:hover {
  color: var(--danger);
  background: var(--danger-light);
}
.reset-btn .material-icons-outlined { font-size: 16px; }
</style>

<template>
  <Teleport to="body">
    <div class="settings-overlay" v-if="visible" ref="panelEl" tabindex="-1" @click.self="$emit('close')" @keydown.escape="$emit('close')">
      <div class="settings-panel">
        <div class="settings-header">
          <span class="material-icons-outlined">settings</span>
          <h3>{{ t('settings.title') }}</h3>
          <button class="btn btn-icon btn-ghost" @click="$emit('close')">
            <span class="material-icons-outlined">close</span>
          </button>
        </div>

        <div class="settings-body">
          <!-- Language -->
          <div class="setting-group">
            <label class="setting-label">{{ t('settings.language') }}</label>
            <select class="input" :value="locale" @change="setLocale($event.target.value)">
              <option value="en">English</option>
              <option value="zh-CN">简体中文</option>
            </select>
          </div>

          <!-- App Font Size -->
          <div class="setting-group">
            <label class="setting-label">{{ t('settings.appFontSize') }}</label>
            <div class="slider-row">
              <input
                type="range"
                min="10" max="24" step="1"
                :value="local.appFontSize"
                @input="setAppFontSize(Number($event.target.value))"
              />
              <span class="slider-value">{{ local.appFontSize }}px</span>
            </div>
          </div>

          <!-- WYSIWYG Font Size -->
          <div class="setting-group">
            <label class="setting-label">{{ t('settings.editorWysiwygFontSize') }}</label>
            <div class="slider-row">
              <input
                type="range"
                min="12" max="32" step="1"
                :value="local.editorWysiwygFontSize"
                @input="setWysiwygFontSize(Number($event.target.value))"
              />
              <span class="slider-value">{{ local.editorWysiwygFontSize }}px</span>
            </div>
          </div>

          <!-- Raw Editor Font Size -->
          <div class="setting-group">
            <label class="setting-label">{{ t('settings.editorRawFontSize') }}</label>
            <div class="slider-row">
              <input
                type="range"
                min="12" max="32" step="1"
                :value="local.editorRawFontSize"
                @input="setRawFontSize(Number($event.target.value))"
              />
              <span class="slider-value">{{ local.editorRawFontSize }}px</span>
            </div>
          </div>

          <hr class="settings-divider" />

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

          <hr class="settings-divider" />

          <!-- Global Monospace -->
          <div class="setting-group">
            <label class="setting-label">{{ t('settings.monospaceFont') }}</label>
            <select
              v-if="!customFields['editorMonospace']"
              class="input"
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

        <div class="settings-footer">
          <button class="btn btn-ghost reset-btn" @click="resetToDefaults">
            <span class="material-icons-outlined">restart_alt</span>
            {{ t('settings.resetToDefaults') }}
          </button>
        </div>
      </div>
    </div>
  </Teleport>
</template>

<script setup>
import { ref, reactive, computed, watch, onMounted } from 'vue'
import { useI18n } from '../i18n'
const { t, locale, setLocale } = useI18n()

const emit = defineEmits(['close', 'changed'])

const props = defineProps({
  visible: { type: Boolean, default: false },
})

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
      // Deep-merge with defaults to fill any missing keys from older versions
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
onMounted(() => detectFonts())

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
  saveSettings()
  emit('changed', { ...local })
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

// --- Panel ref for Escape key focus ---
const panelEl = ref(null)

watch(() => props.visible, (v) => {
  if (v) {
    setTimeout(() => panelEl.value?.focus(), 100)
  }
})

// --- Persist to localStorage ---
function saveSettings() {
  try {
    const raw = localStorage.getItem('memodump_settings')
    const existing = raw ? JSON.parse(raw) : {}
    localStorage.setItem('memodump_settings', JSON.stringify({ ...local, language: existing.language }))
  } catch (_) {}
}

// --- Change handlers ---
function setAppFontSize(val) {
  local.appFontSize = val
  saveSettings()
  emit('changed', { ...local })
}

function setWysiwygFontSize(val) {
  local.editorWysiwygFontSize = val
  saveSettings()
  emit('changed', { ...local })
}

function setRawFontSize(val) {
  local.editorRawFontSize = val
  saveSettings()
  emit('changed', { ...local })
}

const selectedSystem = ref('latin')

const currentFonts = computed(() => {
  return local.editorFonts[selectedSystem.value]
})

const sansSerifKey = computed(() => selectedSystem.value + '.sansSerif')
const serifKey = computed(() => selectedSystem.value + '.serif')

function setProportional(val) {
  local.editorFonts[selectedSystem.value].proportional = val
  saveSettings()
  emit('changed', { ...local })
}

function resetToDefaults() {
  const fresh = JSON.parse(JSON.stringify(DEFAULTS))
  // Clear custom mode tracking
  for (const key of Object.keys(customFields)) {
    delete customFields[key]
  }
  for (const key of Object.keys(customValues)) {
    delete customValues[key]
  }
  Object.assign(local, fresh)
  saveSettings()
  emit('changed', { ...local })
}
</script>

<style scoped>
.settings-overlay {
  position: fixed;
  inset: 0;
  z-index: 200;
  background: rgba(0, 0, 0, 0.2);
  display: flex;
}

.settings-panel {
  width: 300px;
  height: 100%;
  background: var(--bg-card);
  display: flex;
  flex-direction: column;
  box-shadow: 2px 0 16px rgba(0, 0, 0, 0.08);
  animation: slideIn 0.2s ease;
}

@keyframes slideIn {
  from { transform: translateX(-100%); }
  to   { transform: translateX(0); }
}

.settings-header {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 14px 16px;
  border-bottom: 1px solid var(--border);
  flex-shrink: 0;
}

.settings-header h3 {
  font-size: 15px;
  font-weight: 700;
  flex: 1;
}

.settings-header .material-icons-outlined {
  font-size: 20px;
  color: var(--text-secondary);
}

.settings-body {
  flex: 1;
  overflow-y: auto;
  padding: 16px;
}

.setting-group {
  margin-bottom: 14px;
}

.setting-label {
  display: block;
  font-size: 12px;
  font-weight: 600;
  color: var(--text-secondary);
  margin-bottom: 6px;
  text-transform: uppercase;
  letter-spacing: 0.5px;
}

.slider-row {
  display: flex;
  align-items: center;
  gap: 10px;
}

.slider-row input[type="range"] {
  flex: 1;
  accent-color: var(--primary);
  height: 4px;
}

.slider-value {
  font-size: 13px;
  font-weight: 600;
  color: var(--text);
  min-width: 36px;
  text-align: right;
}

.settings-divider {
  border: none;
  border-top: 1px solid var(--border-light);
  margin: 16px 0;
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

.settings-body .input {
  font-size: 13px;
  padding: 8px 10px;
}

.custom-font-row {
  display: flex;
  gap: 6px;
  align-items: center;
}

.custom-font-row .input {
  flex: 1;
}

.custom-font-row .btn-sm {
  padding: 4px 8px;
  font-size: 14px;
  line-height: 1;
  flex-shrink: 0;
}

.settings-footer {
  padding: 12px 16px;
  border-top: 1px solid var(--border);
  flex-shrink: 0;
}

.reset-btn {
  width: 100%;
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 6px;
  font-size: 13px;
  color: var(--text-muted);
  padding: 8px;
  border-radius: var(--radius);
  transition: color 0.15s, background 0.15s;
}

.reset-btn:hover {
  color: var(--danger);
  background: var(--danger-light);
}

.reset-btn .material-icons-outlined {
  font-size: 16px;
}
</style>

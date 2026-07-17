<template>
  <div class="settings-page">
    <div class="settings-body">
      <!-- Preview card -->
      <div class="preview-card">
        <div class="preview-card-header">
          <span>{{ t('settings.preview') }}</span>
          <button class="btn btn-icon btn-ghost preview-toggle" @click="previewMode = previewMode === 'wysiwyg' ? 'raw' : 'wysiwyg'" :title="previewMode === 'wysiwyg' ? t('editor.switchToRaw') : t('editor.switchToRich')">
            <span class="material-icons-outlined" style="font-size:16px">{{ previewMode === 'wysiwyg' ? 'code' : 'visibility' }}</span>
          </button>
        </div>

        <!-- WYSIWYG mode: uses editor-native .milkdown .editor class for font/size/line-height -->
        <div v-if="previewMode === 'wysiwyg'" class="preview-editor milkdown">
          <div class="editor">
            <template v-for="(line, i) in previewLines" :key="i">
              <p v-if="line.type === 'text'">{{ line.content }}</p>
              <p v-else-if="line.type === 'strong'"><strong>{{ line.content }}</strong></p>
              <p v-else-if="line.type === 'em'"><em>{{ line.content }}</em></p>
              <p v-else-if="line.type === 'code'"><code>{{ line.content }}</code></p>
              <h3 v-else-if="line.type === 'h3'">{{ line.content }}</h3>
            </template>
          </div>
        </div>

        <!-- Raw mode: uses editor-native raw styling -->
        <div v-else class="preview-raw">
          function hello() {<br/>
          &nbsp;&nbsp;return "Hello, world!";<br/>
          }<br/>
          const msg = "Plain text"
        </div>
      </div>

      <hr class="settings-divider" />

      <!-- Interface section -->
      <div class="settings-section">
        <div class="settings-section-label">{{ t('settings.language') }}</div>

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
          <span class="setting-row-label">{{ t('settings.fontPreset') }}</span>
          <select class="input input-select" v-model="local.editorProportional.mode" @change="onSettingChange">
            <option value="system">{{ t('settings.fontPresetSystem') }}</option>
            <option value="serif">{{ t('settings.fontPresetSerif') }}</option>
            <option value="sans">{{ t('settings.fontPresetSans') }}</option>
            <option value="custom">{{ t('settings.custom') }}</option>
          </select>
        </div>
        <div class="setting-row" v-if="local.editorProportional.mode === 'custom'">
          <span class="setting-row-label">{{ t('settings.fontCustomStack') }}</span>
          <input type="text" class="input input-select" v-model="local.editorProportional.custom"
                 :placeholder="t('settings.fontCustomStackHint')" @input="onSettingChange" />
        </div>

        <div class="setting-row">
          <span class="setting-row-label">{{ t('settings.monospaceFont') }}</span>
          <select class="input input-select" v-model="local.editorMonospace.mode" @change="onSettingChange">
            <option value="preset">{{ t('settings.fontPresetMono') }}</option>
            <option value="custom">{{ t('settings.custom') }}</option>
          </select>
        </div>
        <div class="setting-row" v-if="local.editorMonospace.mode === 'custom'">
          <span class="setting-row-label">{{ t('settings.fontCustomName') }}</span>
          <input type="text" class="input input-select" v-model="local.editorMonospace.custom"
                 :placeholder="'Consolas'" @input="onSettingChange" />
        </div>
      </div>

      <hr class="settings-divider" />

      <!-- Custom CSS -->
      <div class="settings-section">
        <div class="settings-section-label">{{ t('settings.customCss') }}</div>
        <textarea
          class="input custom-css-input"
          v-model="local.customCss"
          :placeholder="t('settings.customCssHint')"
          spellcheck="false"
          @input="onCustomCssInput"
        ></textarea>
      </div>

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

const previewMode = ref('wysiwyg')

// Preview sample text: tied to UI language.
const PREVIEW_SAMPLES = {
  latin: {
    paragraphs: [
      'The quick brown fox jumps over the lazy dog.',
      ['strong', 'Bold text'], ['em', 'italic text'],
      ['code', 'Inline code sample'],
      'Jazz, jive, and waltz — every dance tells a story.',
    ],
    heading: 'Sample heading',
  },
  sc: {
    paragraphs: [
      '敏捷的棕色狐狸跳过了懒狗。',
      ['strong', '粗体文字'], ['em', '斜体文字'],
      ['code', '内联代码示例'],
      '中文排版注重字间距与行高的和谐。',
    ],
    heading: '示例标题',
  },
}

const activePreviewLang = computed(() => locale.value === 'zh-CN' ? 'sc' : 'latin')

const previewLines = computed(() => {
  const base = PREVIEW_SAMPLES[activePreviewLang.value]
  if (!base) return [{ type: 'text', content: 'The quick brown fox jumps over the lazy dog.' }]
  const lines = []
  for (const item of base.paragraphs) {
    if (typeof item === 'string') {
      lines.push({ type: 'text', content: item })
    } else if (Array.isArray(item)) {
      lines.push({ type: item[0], content: item[1] })
    }
  }
  lines.push({ type: 'h3', content: base.heading })
  return lines
})

// --- CSS font stacks keyed by preset name ---
const FONT_STACKS = {
  system: "-apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, 'Helvetica Neue', Arial, 'PingFang SC', 'Microsoft YaHei', sans-serif",
  serif: "Georgia, 'Times New Roman', 'Noto Serif CJK SC', 'Songti SC', 'SimSun', serif",
  sans: "'Helvetica Neue', Arial, 'PingFang SC', 'Microsoft YaHei', sans-serif",
  mono: "'JetBrains Mono', 'Fira Code', 'SF Mono', Consolas, Menlo, monospace",
}

// --- Defaults ---
const DEFAULTS = Object.freeze({
  appFontSize: 14,
  editorWysiwygFontSize: 16,
  editorRawFontSize: 14,
  editorProportional: { mode: 'system', custom: '' },
  editorMonospace: { mode: 'preset', custom: '' },
  customCss: '',
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

onMounted(() => {
  // Apply CSS variables on initial load so settings take effect before any user interaction
  applySettings()
  applyCustomCss()
})

// --- Apply CSS variables to #app-settings <style> ---
// Batched into one :root { … } block written to #app-settings.textContent so
// user CSS in #app-custom can override app-set variables by source order.
function applySettings() {
  try {
    const raw = localStorage.getItem('memodump_settings')
    const s = raw ? JSON.parse(raw) : {}
    const decls = []
    decls.push(`--app-zoom: ${((s.appFontSize || 14) / 14).toFixed(2)}`)
    decls.push(`--editor-wysiwyg-font-size: ${(s.editorWysiwygFontSize || 16)}px`)
    decls.push(`--editor-raw-font-size: ${(s.editorRawFontSize || 14)}px`)

    const ep = s.editorProportional
    if (ep) {
      const stack = (ep.mode === 'custom' && ep.custom) ? ep.custom : (FONT_STACKS[ep.mode] || FONT_STACKS.system)
      decls.push(`--editor-font-proportional: ${stack}`)
    }
    const em = s.editorMonospace
    if (em) {
      const stack = (em.mode === 'custom' && em.custom) ? `${em.custom}, monospace` : FONT_STACKS.mono
      decls.push(`--editor-font-monospace: ${stack}`)
    }
    const el = document.getElementById('app-settings')
    if (el) el.textContent = `:root{\n${decls.join(';\n')};\n}`
  } catch (e) { console.warn('Failed to apply settings:', e) }
}

// --- Apply user custom CSS to #app-custom <style> ---
function applyCustomCss() {
  const el = document.getElementById('app-custom')
  if (el) el.textContent = local.customCss || ''
}

// Debounced save + apply for custom CSS textarea
let cssTimer = null
function onCustomCssInput() {
  if (cssTimer) clearTimeout(cssTimer)
  cssTimer = setTimeout(() => {
    saveSettings()
    applyCustomCss()
  }, 300)
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

// --- Reset ---
function resetToDefaults() {
  Object.assign(local, JSON.parse(JSON.stringify(DEFAULTS)))
  onSettingChange()
}
</script>

<style scoped>
.settings-page {
  height: 100%;
  overflow-y: auto;
  background: var(--bg);
}

.settings-body {
  max-width: 600px;
  margin: 0 auto;
  padding: 24px;
}

/* ---- Preview card (matches waterfall card style) ---- */
.preview-card {
  background: #FFFFFF;
  border: 1px solid rgba(0, 0, 0, 0.04);
  border-radius: 14px;
  padding: 16px 18px;
  box-shadow: 0 4px 14px rgba(0, 0, 0, 0.03);
}

.preview-card-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  font-size: 12px;
  font-weight: 600;
  color: var(--text-muted);
  text-transform: uppercase;
  letter-spacing: 0.5px;
  margin-bottom: 12px;
}

.preview-toggle {
  width: 24px;
  height: 24px;
  color: var(--text-muted);
}
.preview-toggle:hover {
  color: var(--primary-dark);
}

/* WYSIWYG preview — uses global .milkdown .editor styles via CSS cascade */
.preview-editor.milkdown .editor {
  /* Inherits font-family/size/line-height/color from global style.css */
  padding: 0;
  outline: none;
  background: transparent;
}

/* Raw preview — matches raw editor styling */
.preview-raw {
  font-family: var(--editor-font-monospace);
  font-size: var(--editor-raw-font-size);
  line-height: 1.7;
  color: var(--text);
  white-space: pre-wrap;
}

/* ---- Dividers ---- */
.settings-divider {
  border: none;
  border-top: 1px solid var(--border);
  margin: 20px 0;
}

/* ---- Section headers ---- */
.settings-section-label {
  font-size: 16px;
  font-weight: 600;
  color: var(--waterfall-title);
  margin-bottom: 14px;
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

/* ---- Custom CSS textarea ---- */
.custom-css-input {
  width: 100%;
  min-height: 120px;
  font-family: var(--editor-font-monospace);
  font-size: 12px;
  resize: vertical;
  padding: 8px;
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

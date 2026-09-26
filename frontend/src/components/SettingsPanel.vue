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

      <!-- Appearance section -->
      <div class="settings-section">
        <h3>{{ t('settings.appearance') }}</h3>

        <div class="setting-row">
          <span class="setting-row-label">{{ t('settings.theme') }}</span>
          <div class="theme-toggle-group">
            <button
              class="theme-option-btn"
              :class="{ active: theme === 'light' }"
              @click="setTheme('light')"
            >
              <span class="material-icons-outlined">light_mode</span>
              {{ t('settings.themeLight') }}
            </button>
            <button
              class="theme-option-btn"
              :class="{ active: theme === 'dark' }"
              @click="setTheme('dark')"
            >
              <span class="material-icons-outlined">dark_mode</span>
              {{ t('settings.themeDark') }}
            </button>
            <button
              class="theme-option-btn"
              :class="{ active: theme === 'system' }"
              @click="setTheme('system')"
            >
              <span class="material-icons-outlined">settings_brightness</span>
              {{ t('settings.themeSystem') }}
            </button>
          </div>
        </div>

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
        <h3>{{ t('settings.editorSection') }}</h3>

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
          <span class="setting-row-label">{{ t('settings.fontCustomStack') }}</span>
          <input type="text" class="input input-select" v-model="local.editorMonospace.custom"
                 :placeholder="t('settings.fontCustomStackHint')" @input="onSettingChange" />
        </div>
      </div>

      <hr class="settings-divider" />

      <!-- Image hosting -->
      <div class="settings-section">
        <div class="settings-section-header-row">
          <button
            type="button"
            class="settings-section-title-btn"
            :aria-expanded="imageSectionOpen"
            @click="imageSectionOpen = !imageSectionOpen"
          >
            <h3>{{ t('settings.imageSection') }}</h3>
          </button>
          <InfoTooltip class="image-heading-tip" :text="t('settings.imagePrivacyWarning')" :label="t('settings.imagePrivacyWarning')" />
          <button
            type="button"
            class="settings-section-header"
            :aria-expanded="imageSectionOpen"
            @click="imageSectionOpen = !imageSectionOpen"
          >
            <span class="image-mode-summary">{{ imageModeSummary }}</span>
            <span class="material-icons-outlined settings-caret" :class="{ open: imageSectionOpen }">expand_more</span>
          </button>
        </div>

        <div v-show="imageSectionOpen" class="settings-image-body">
          <div v-if="!isLocalImageBuild && !imageSettings.editable" class="setting-row">
            <span class="setting-row-label">{{ t('settings.imageReadOnlySource') }}</span>
          </div>

          <div class="setting-row">
            <span class="setting-row-label">{{ t('settings.imageProvider') }}</span>
            <select
              class="input input-select"
              :value="imageDraft.provider"
              :disabled="!isLocalImageBuild && !imageSettings.editable"
              @change="imageDraft.provider = $event.target.value"
            >
              <option v-if="isLocalImageBuild" value="off">{{ t('settings.imageProviderOff') }}</option>
              <option v-else value="local">{{ t('settings.imageProviderLocal') }}</option>
              <option value="s3">{{ t('settings.imageProviderS3') }}</option>
            </select>
          </div>

          <template v-if="imageDraft.provider === 's3'">
            <div class="setting-row">
              <span class="setting-row-label">Endpoint</span>
              <input type="text" class="input input-select" v-model.trim="imageDraft.endpoint"
                     :disabled="!isLocalImageBuild && !imageSettings.editable" />
            </div>
            <div class="setting-row">
              <span class="setting-row-label">Region</span>
              <input type="text" class="input input-select" v-model.trim="imageDraft.region"
                     :disabled="!isLocalImageBuild && !imageSettings.editable"
                     placeholder="us-east-1" />
            </div>
            <div class="setting-row">
              <span class="setting-row-label">Bucket</span>
              <input type="text" class="input input-select" v-model.trim="imageDraft.bucket"
                     :disabled="!isLocalImageBuild && !imageSettings.editable" />
            </div>
            <div class="setting-row">
              <span class="setting-row-label">Prefix</span>
              <input type="text" class="input input-select" v-model.trim="imageDraft.prefix"
                     :disabled="!isLocalImageBuild && !imageSettings.editable" />
            </div>
            <div class="setting-row">
              <span class="setting-row-label label-with-tip">
                Public URL
                <InfoTooltip :text="t('settings.imagePublicReadHelp')" :label="t('settings.imagePublicReadHelp')" />
              </span>
              <input type="text" class="input input-select" v-model.trim="imageDraft.publicBaseUrl"
                     :disabled="!isLocalImageBuild && !imageSettings.editable"
                     placeholder="https://cdn.example.com/images" />
            </div>
            <div class="setting-row">
              <span class="setting-row-label label-with-tip">
                Access Key
                <InfoTooltip v-if="isLocalImageBuild" :text="t('settings.imageLocalStorageWarning')" :label="t('settings.imageLocalStorageWarning')" />
              </span>
              <span class="secret-input">
                <input :type="showSecrets ? 'text' : 'password'" class="input input-select" v-model.trim="imageDraft.accessKey"
                       :disabled="!isLocalImageBuild && !imageSettings.editable" />
                <button type="button" class="secret-toggle" :disabled="!isLocalImageBuild && !imageSettings.editable"
                        @click="showSecrets = !showSecrets"
                        :aria-label="showSecrets ? t('settings.secretHide') : t('settings.secretShow')">
                  <span class="material-icons-outlined">{{ showSecrets ? 'visibility_off' : 'visibility' }}</span>
                </button>
              </span>
            </div>
            <div class="setting-row">
              <span class="setting-row-label">Secret Key</span>
              <span class="secret-input">
                <input :type="showSecrets ? 'text' : 'password'" class="input input-select" v-model.trim="imageDraft.secretKey"
                       :placeholder="imageSettings.configured ? t('settings.imageSecretUnchanged') : ''"
                       :disabled="!isLocalImageBuild && !imageSettings.editable" />
                <button type="button" class="secret-toggle" :disabled="!isLocalImageBuild && !imageSettings.editable"
                        @click="showSecrets = !showSecrets"
                        :aria-label="showSecrets ? t('settings.secretHide') : t('settings.secretShow')">
                  <span class="material-icons-outlined">{{ showSecrets ? 'visibility_off' : 'visibility' }}</span>
                </button>
              </span>
            </div>
            <div class="setting-row">
              <span class="setting-row-label">{{ t('settings.imageForcePathStyle') }}</span>
              <input type="checkbox" class="input-checkbox" v-model="imageDraft.forcePathStyle"
                     :disabled="!isLocalImageBuild && !imageSettings.editable" />
            </div>
          </template>
          <div class="setting-row" v-if="!isLocalImageBuild">
            <span class="setting-row-label">{{ t('settings.imageCleanup') }}</span>
              <input
                type="checkbox"
                class="input-checkbox"
                :checked="imageDraft.cleanupEnabled"
                :disabled="!imageSettings.editable"
                @change="onCleanupToggle"
              />
            </div>
            <div v-if="cleanupConfirmOpen" class="cleanup-confirm">
              <p class="cleanup-confirm-text">
                {{ t('settings.imageCleanupWarning') }}
                <strong>{{ t('settings.imageCleanupWarningRisk') }}</strong>
                {{ t('settings.imageCleanupWarningDedicated') }}
              </p>
              <div class="image-actions">
                <button class="btn btn-sm btn-primary" @click="confirmCleanupEnable">{{ t('settings.imageCleanupConfirm') }}</button>
                <button class="btn btn-sm btn-outline" @click="cleanupConfirmOpen = false">{{ t('modals.cancel') }}</button>
              </div>
            </div>
            <p v-if="cleanupSaveMessage" class="cleanup-save-status" :class="{ 'cleanup-save-error': cleanupSaveError }">
              {{ cleanupSaveMessage }}
            </p>
            <template v-if="imageDraft.provider === 's3'">
              <div class="setting-row">
                <span class="setting-row-label"></span>
                <div class="image-actions">
                  <button class="btn btn-sm btn-outline" :disabled="imageBusy" @click="onTestImageConnection">
                    <span v-if="imageBusy === 'test'" class="spinner spinner-sm" aria-hidden="true"></span>
                    <span v-else class="material-icons-outlined image-btn-icon" aria-hidden="true">wifi_tethering</span>
                    {{ imageBusy === 'test' ? t('settings.imageTesting') : t('settings.imageTestConnection') }}
                  </button>
                  <button class="btn btn-sm btn-primary" :disabled="imageBusy" @click="onSaveImageConfig">
                    <span v-if="imageBusy === 'save'" class="spinner spinner-sm" aria-hidden="true"></span>
                    {{ imageBusy === 'save' ? t('settings.imageSaving') : t('settings.imageSave') }}
                  </button>
                </div>
              </div>
              <div v-if="imageBusy === 'test'" class="image-status image-status-loading" role="status">
                <span class="spinner" aria-hidden="true"></span>
                <span>{{ t('settings.imageTesting') }}</span>
              </div>
              <div v-else-if="imageFormMessage" class="image-status" :class="imageFormError ? 'image-status-error' : 'image-status-ok'" role="status">
                <span class="material-icons-outlined image-status-icon" aria-hidden="true">{{ imageFormError ? 'error' : 'check_circle' }}</span>
                <span>{{ imageFormMessage }}</span>
              </div>
              <details class="cors-template">
                <summary>
                  <span class="material-icons-outlined cors-template-icon" aria-hidden="true">dns</span>
                  {{ t('settings.imageCorsTemplate') }}
                  <span class="cors-template-note">{{ t('settings.imageCorsHint') }}</span>
                </summary>
                <pre class="cors-template-code">{{ corsTemplate }}</pre>
                <p v-if="!isLocalImageBuild" class="cors-template-warning">{{ t('settings.imageCorsNotNeeded') }}</p>
              </details>
            </template>
        </div>
      </div>

      <hr v-if="cloudSyncAvailable()" class="settings-divider" />

      <!-- Cloud sync (experimental) -->
      <SyncPanel />

      <hr class="settings-divider" />

      <!-- Custom CSS -->
      <div class="settings-section">
        <h3>{{ t('settings.customCss') }}</h3>
        <textarea
          class="input custom-css-input"
          v-model="local.customCss"
          :placeholder="t('settings.customCssHint')"
          spellcheck="false"
          @input="onCustomCssInput"
        ></textarea>
        <div class="css-actions">
          <button class="btn btn-sm btn-outline" @click="applyCustomCssNow">
            {{ t('editor.save') }}
          </button>
        </div>
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
import { ref, reactive, computed, onMounted, watch } from 'vue'
import { useI18n } from '../i18n'
const { t, locale, setLocale } = useI18n()

import { useTheme } from '../composables/useTheme.js'
const { theme, setTheme } = useTheme()
import {
  getImageSettings,
  saveImageConfig,
  testImageConnection,
  isLocalBuild as isLocalImageBuild,
} from '../composables/useImageSettings'
import InfoTooltip from './InfoTooltip.vue'
import SyncPanel from './SyncPanel.vue'
import { cloudSyncAvailable } from '../composables/runtime'

const emit = defineEmits(['close'])

// Must NOT define props.visible — visibility is controlled by parent v-show.

const previewMode = ref('wysiwyg')

// ---- image hosting section ----
const imageSettings = getImageSettings()
const imageSectionOpen = ref(false)
const imageBusy = ref(null)
const imageFormMessage = ref('')
const imageFormError = ref(false)
const imageDraft = reactive({
  provider: imageSettings.provider,
  endpoint: imageSettings.endpoint,
  region: imageSettings.region,
  bucket: imageSettings.bucket,
  prefix: imageSettings.prefix,
  publicBaseUrl: imageSettings.publicBaseUrl,
  accessKey: imageSettings.accessKey,
  secretKey: imageSettings.secretKey,
  forcePathStyle: imageSettings.forcePathStyle,
  cleanupEnabled: imageSettings.cleanupEnabled,
})
const cleanupConfirmOpen = ref(false)
const cleanupSaveMessage = ref('')
const cleanupSaveError = ref(false)
const showSecrets = ref(false)

const imageModeSummary = computed(() => {
  if (imageSettings.provider === 's3') {
    return imageSettings.bucket ? `S3: ${imageSettings.bucket}` : t('settings.imageProviderS3')
  }
  return isLocalImageBuild ? t('settings.imageProviderOff') : t('settings.imageProviderLocal')
})

// Bucket CORS reference for browser-direct uploads (pure-frontend build). The
// web/Wails builds upload through the server proxy, so this only matters there.
const corsTemplate = `[
  {
    "AllowedOrigins": ["<app origin>"],
    "AllowedMethods": ["PUT", "POST", "GET", "HEAD"],
    "AllowedHeaders": ["Content-Type", "x-amz-*"],
    "ExposeHeaders": ["ETag"],
    "MaxAgeSeconds": 3000
  }
]`

function syncImageDraft() {
  imageDraft.provider = imageSettings.provider
  imageDraft.endpoint = imageSettings.endpoint
  imageDraft.region = imageSettings.region
  imageDraft.bucket = imageSettings.bucket
  imageDraft.prefix = imageSettings.prefix
  imageDraft.publicBaseUrl = imageSettings.publicBaseUrl
  imageDraft.accessKey = imageSettings.accessKey
  imageDraft.secretKey = imageSettings.secretKey
  imageDraft.forcePathStyle = imageSettings.forcePathStyle
  imageDraft.cleanupEnabled = imageSettings.cleanupEnabled
}

// MainView initializes image settings after child setup. Keep the editable
// draft in sync with that async hydration and with successful saves.
watch(imageSettings, syncImageDraft, { deep: true })

function onCleanupToggle() {
  if (imageDraft.cleanupEnabled) {
    // Turning off is immediate; turning on requires the inline confirm.
    imageDraft.cleanupEnabled = false
    cleanupConfirmOpen.value = false
    // Local mode has no explicit save — the toggle persists itself.
    if (imageDraft.provider !== 's3') saveCleanupConfig(false)
  } else {
    cleanupConfirmOpen.value = true
  }
}

function confirmCleanupEnable() {
  imageDraft.cleanupEnabled = true
  cleanupConfirmOpen.value = false
  if (imageDraft.provider !== 's3') saveCleanupConfig(true)
}

// saveCleanupConfig persists the cleanup setting on its own (local mode has no
// Save button). In S3 mode the toggle is part of the form and saved with the
// "保存配置" button instead.
async function saveCleanupConfig(enabled) {
  cleanupSaveMessage.value = ''
  cleanupSaveError.value = false
  try {
    await saveImageConfig({
      provider: imageDraft.provider,
      endpoint: imageDraft.endpoint,
      region: imageDraft.region,
      bucket: imageDraft.bucket,
      prefix: imageDraft.prefix,
      publicBaseUrl: imageDraft.publicBaseUrl,
      accessKey: imageDraft.accessKey,
      secretKey: imageDraft.secretKey,
      forcePathStyle: imageDraft.forcePathStyle,
      cleanup: { enabled },
    })
    imageDraft.cleanupEnabled = enabled
    cleanupSaveMessage.value = t('settings.imageSaveOk')
  } catch (e) {
    cleanupSaveError.value = true
    cleanupSaveMessage.value = e?.response?.data?.error?.message || t('settings.imageSaveFail')
  }
}

async function onSaveImageConfig() {
  imageBusy.value = 'save'
  imageFormMessage.value = ''
  imageFormError.value = false
  try {
    await saveImageConfig({
      provider: imageDraft.provider,
      endpoint: imageDraft.endpoint,
      region: imageDraft.region,
      bucket: imageDraft.bucket,
      prefix: imageDraft.prefix,
      publicBaseUrl: imageDraft.publicBaseUrl,
      accessKey: imageDraft.accessKey,
      secretKey: imageDraft.secretKey,
      forcePathStyle: imageDraft.forcePathStyle,
      cleanup: { enabled: imageDraft.cleanupEnabled },
    })
    imageFormMessage.value = t('settings.imageSaveOk')
  } catch (e) {
    imageFormMessage.value = e?.response?.data?.error?.message || e?.response?.data?.error || t('settings.imageSaveFail')
    imageFormError.value = true
  } finally {
    imageBusy.value = null
  }
}

async function onTestImageConnection() {
  imageBusy.value = 'test'
  imageFormMessage.value = ''
  imageFormError.value = false
  try {
    const result = await testImageConnection({
      provider: imageDraft.provider,
      endpoint: imageDraft.endpoint,
      region: imageDraft.region,
      bucket: imageDraft.bucket,
      prefix: imageDraft.prefix,
      publicBaseUrl: imageDraft.publicBaseUrl,
      accessKey: imageDraft.accessKey,
      secretKey: imageDraft.secretKey,
      forcePathStyle: imageDraft.forcePathStyle,
    })
    imageFormMessage.value = result.warnings?.length
      ? `${t('settings.imageTestOk')} (${result.warnings.join('; ')})`
      : t('settings.imageTestOk')
  } catch (e) {
    imageFormMessage.value = e?.response?.data?.error?.message || e?.message || t('settings.imageTestFail')
    imageFormError.value = true
  } finally {
    imageBusy.value = null
  }
}

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
      let stack
      if (em.mode === 'custom' && em.custom) {
        const trimmed = em.custom.trim()
        // Accept full font-family; only append , monospace if no generic fallback yet
        stack = /,\s*(serif|sans-serif|monospace|cursive|fantasy|system-ui|ui-sans-serif|ui-monospace)\s*$/i.test(trimmed)
          ? trimmed : trimmed + ', monospace'
      } else {
        stack = FONT_STACKS.mono
      }
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

// Debounced save to localStorage for custom CSS (never auto-apply)
let cssTimer = null
function onCustomCssInput() {
  if (cssTimer) clearTimeout(cssTimer)
  cssTimer = setTimeout(() => {
    saveSettings()
  }, 300)
}

// Explicit apply button — only then inject into #app-custom
function applyCustomCssNow() {
  applyCustomCss()
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
  scrollbar-gutter: stable;
  background: var(--bg);
}

.settings-body {
  max-width: 600px;
  margin: 0 auto;
  padding: 24px;
}

/* ---- Preview card (matches waterfall card style) ---- */
.preview-card {
  background: var(--bg-card);
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

/* ---- Image hosting section ---- */
.settings-section-header-row {
  display: flex;
  align-items: center;
  gap: 8px;
}
.settings-section-title-btn {
  flex-shrink: 0;
  display: flex;
  align-items: center;
  background: none;
  border: none;
  padding: 0;
  cursor: pointer;
  text-align: left;
}
.settings-section-title-btn h3 { margin: 0; }
.settings-section-header {
  flex: 1;
  display: flex;
  align-items: center;
  gap: 10px;
  background: none;
  border: none;
  padding: 0;
  cursor: pointer;
  text-align: left;
}
.image-heading-tip { flex-shrink: 0; }
.image-mode-summary {
  flex: 1;
  color: var(--text-muted);
  font-size: 12px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.settings-caret {
  color: var(--text-muted);
  font-size: 18px;
  transition: transform 0.15s ease;
}
.settings-caret.open { transform: rotate(180deg); }
.settings-image-body { padding-top: 14px; }
.image-actions {
  display: flex;
  gap: 8px;
}
.label-with-tip {
  display: inline-flex;
  align-items: center;
  gap: 4px;
}
.cleanup-confirm {
  margin-top: 8px;
  padding: 10px 12px;
  background: var(--primary-bg);
  border: 1px solid var(--border);
  border-radius: var(--radius);
}
.cleanup-confirm-text {
  font-size: 12px;
  line-height: 1.6;
  color: var(--text-secondary);
  margin-bottom: 8px;
}
.cleanup-confirm-text strong {
  color: var(--text);
}
.cleanup-save-status {
  margin: 6px 0 0;
  font-size: 12px;
  line-height: 1.5;
  color: var(--text-muted);
}
.cleanup-save-status.cleanup-save-error { color: var(--danger); }

/* Custom-styled checkbox (used for forcePathStyle + cleanup toggle) */
.input-checkbox {
  appearance: none;
  -webkit-appearance: none;
  width: 18px;
  height: 18px;
  flex-shrink: 0;
  border: 1.5px solid var(--border);
  border-radius: 5px;
  background: var(--bg-card);
  cursor: pointer;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  transition: border-color 0.15s, background 0.15s;
}
.input-checkbox:hover:not(:disabled) { border-color: var(--primary); }
.input-checkbox:checked {
  background: var(--primary);
  border-color: var(--primary);
}
.input-checkbox:checked::after {
  content: '';
  width: 9px;
  height: 5px;
  border-left: 2px solid #fff;
  border-bottom: 2px solid #fff;
  transform: rotate(-45deg) translateY(-1px);
}
.input-checkbox:disabled { opacity: 0.5; cursor: default; }
.image-status {
  display: flex;
  align-items: flex-start;
  gap: 6px;
  margin-top: 8px;
  padding: 8px 10px;
  border-radius: var(--radius);
  font-size: 12px;
  line-height: 1.5;
  background: var(--bg-sidebar);
  color: var(--text-secondary);
}
.image-status-loading { color: var(--text-muted); }
.image-status-icon {
  flex-shrink: 0;
  font-size: 16px;
}
.image-status-ok { background: rgba(34, 197, 94, 0.1); }
.image-status-ok .image-status-icon { color: var(--success); }
.image-status-error { background: var(--danger-light); }
.image-status-error .image-status-icon { color: var(--danger); }

.spinner {
  display: inline-block;
  width: 12px;
  height: 12px;
  border: 2px solid currentColor;
  border-top-color: transparent;
  border-radius: 50%;
  animation: spin 0.7s linear infinite;
}
.spinner-sm { width: 10px; height: 10px; }
@keyframes spin { to { transform: rotate(360deg); } }

.image-btn-icon { font-size: 16px; }

.cors-template {
  margin-top: 8px;
  font-size: 12px;
  line-height: 1.6;
  color: var(--text-secondary);
}
.cors-template summary {
  display: flex;
  align-items: center;
  gap: 6px;
  cursor: pointer;
  list-style: none;
  font-weight: 500;
  color: var(--text-secondary);
}
.cors-template summary::-webkit-details-marker { display: none; }
.cors-template-icon { font-size: 16px; }
.cors-template-note { color: var(--text-muted); font-weight: 400; }
.cors-template-code {
  margin: 8px 0 0;
  padding: 8px 10px;
  background: var(--bg-sidebar);
  border: 1px solid var(--border);
  border-radius: var(--radius);
  font-family: var(--editor-font-monospace);
  font-size: 11px;
  line-height: 1.5;
  overflow-x: auto;
  white-space: pre;
}
.cors-template-warning { margin: 6px 0 0; color: var(--text-muted); }

.secret-input {
  position: relative;
  display: inline-flex;
  align-items: center;
}
.secret-input .input { padding-right: 30px; }
.secret-toggle {
  position: absolute;
  right: 4px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 22px;
  height: 22px;
  border-radius: 50%;
  color: var(--text-muted);
  background: transparent;
}
.secret-toggle:hover:not(:disabled) {
  color: var(--primary-dark);
  background: var(--primary-bg);
}
.secret-toggle:disabled { cursor: default; opacity: 0.5; }
.secret-toggle .material-icons-outlined { font-size: 16px; }

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

/* ---- Custom CSS textarea ---- */
.custom-css-input {
  width: 100%;
  min-height: 120px;
  font-family: var(--editor-font-monospace);
  font-size: 12px;
  resize: vertical;
  padding: 8px;
}

.css-actions {
  display: flex;
  justify-content: flex-end;
  margin-top: 8px;
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

/* ---- Theme segmented control ---- */
.theme-toggle-group {
  display: flex;
  gap: 0;
  border-radius: 8px;
  overflow: hidden;
  border: 1px solid var(--border);
}

.theme-option-btn {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  padding: 5px 12px;
  font-size: 12px;
  font-weight: 500;
  border: none;
  background: transparent;
  color: var(--text-secondary);
  cursor: pointer;
  transition: background 0.12s, color 0.12s;
  border-right: 1px solid var(--border);
}

.theme-option-btn:last-child { border-right: none; }

.theme-option-btn .material-icons-outlined {
  font-size: 14px;
}

.theme-option-btn.active {
  background: var(--primary);
  color: var(--on-accent);
}

.theme-option-btn:not(.active):hover {
  background: var(--primary-bg);
}
</style>

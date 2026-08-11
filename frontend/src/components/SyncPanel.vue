<template>
  <div v-if="cloudSyncAvailable()" class="settings-section">
    <div class="settings-section-header-row">
      <button
        type="button"
        class="settings-section-title-btn"
        :aria-expanded="open"
        @click="open = !open"
      >
        <h3>{{ t('settings.syncSection') }}</h3>
      </button>
      <span v-if="state.connected" class="sync-badge">{{ t('settings.syncConnected') }}</span>
      <button
        type="button"
        class="settings-section-header"
        :aria-expanded="open"
        @click="open = !open"
      >
        <span class="material-icons-outlined settings-caret" :class="{ open }">expand_more</span>
      </button>
    </div>

    <div v-show="open" class="settings-sync-body">
      <div v-if="state.connectionError" class="setting-row">
        <span class="setting-row-label error-text">{{ t('settings.syncConnectionError') }}: {{ state.connectionError }}</span>
      </div>
      <div v-if="state.identityError" class="setting-row">
        <span class="setting-row-label error-text">{{ t('settings.syncIdentityError') }}: {{ state.identityError }}</span>
      </div>
      <div v-if="state.noE2EE" class="setting-row">
        <span class="setting-row-label sync-warning">{{ t('settings.syncNoE2EE') }}</span>
      </div>
      <div v-if="state.experimental" class="setting-row">
        <span class="setting-row-label">{{ t('settings.syncExperimental') }}</span>
      </div>
      <div v-if="state.connected && state.autoEnabled" class="setting-row">
        <span class="setting-row-label">{{ t('settings.syncAutoNote') }}</span>
      </div>
      <div v-if="state.connected && state.syncRunning" class="setting-row">
        <span class="setting-row-label">{{ t('settings.syncRunningLabel') }}</span>
      </div>
      <div v-if="state.connected && state.nextRun" class="setting-row">
        <span class="setting-row-label">{{ t('settings.syncNextRun') }}: {{ formatTime(state.nextRun) }}</span>
      </div>
      <div v-if="state.connected && state.autoPaused" class="setting-row">
        <span class="setting-row-label sync-warning">{{ t('settings.syncPausedLabel') }}<template v-if="state.pauseReason"> — {{ state.pauseReason }}</template></span>
      </div>

      <!-- Browser-owned sync (Pure frontend/PWA): S3 configuration. The form is
           independent of image settings; secrets live in this browser. -->
      <div v-if="isLocalBuild && !state.connected" class="sync-config">
        <div class="setting-row">
          <span class="setting-row-label">{{ t('settings.syncConfigTitle') }}</span>
        </div>
        <div class="setting-row">
          <span class="setting-row-label">Endpoint</span>
          <input type="text" class="input input-select" v-model.trim="cfg.endpoint" placeholder="https://s3.example.com" />
        </div>
        <div class="setting-row">
          <span class="setting-row-label">Region</span>
          <input type="text" class="input input-select" v-model.trim="cfg.region" placeholder="us-east-1" />
        </div>
        <div class="setting-row">
          <span class="setting-row-label">Bucket</span>
          <input type="text" class="input input-select" v-model.trim="cfg.bucket" />
        </div>
        <div class="setting-row">
          <span class="setting-row-label">Prefix</span>
          <input type="text" class="input input-select" v-model.trim="cfg.prefix" />
        </div>
        <div class="setting-row">
          <span class="setting-row-label">Access Key</span>
          <span class="secret-input">
            <input :type="showSecrets ? 'text' : 'password'" class="input input-select" v-model.trim="cfg.accessKey" />
            <button type="button" class="secret-toggle" @click="showSecrets = !showSecrets"
                    :aria-label="showSecrets ? t('settings.secretHide') : t('settings.secretShow')">
              <span class="material-icons-outlined">{{ showSecrets ? 'visibility_off' : 'visibility' }}</span>
            </button>
          </span>
        </div>
        <div class="setting-row">
          <span class="setting-row-label">Secret Key</span>
          <span class="secret-input">
            <input :type="showSecrets ? 'text' : 'password'" class="input input-select" v-model.trim="cfg.secretKey" />
            <button type="button" class="secret-toggle" @click="showSecrets = !showSecrets"
                    :aria-label="showSecrets ? t('settings.secretHide') : t('settings.secretShow')">
              <span class="material-icons-outlined">{{ showSecrets ? 'visibility_off' : 'visibility' }}</span>
            </button>
          </span>
        </div>
        <div class="setting-row">
          <span class="setting-row-label">{{ t('settings.syncConfigForcePathStyle') }}</span>
          <input type="checkbox" class="input-checkbox" v-model="cfg.forcePathStyle" />
        </div>
        <div class="setting-row">
          <span class="setting-row-label sync-warning">{{ t('settings.syncCredentialWarning') }}</span>
        </div>
        <div class="setting-row">
          <span class="setting-row-label">{{ t('settings.syncDeletesPropagate') }}</span>
        </div>
        <details class="cors-template">
          <summary>
            <span class="material-icons-outlined cors-template-icon" aria-hidden="true">dns</span>
            {{ t('settings.syncCorsTemplate') }}
          </summary>
          <pre class="cors-template-code">{{ syncCorsTemplate }}</pre>
          <p class="cors-template-warning">{{ t('settings.syncCorsHint') }}</p>
        </details>
        <div class="sync-actions">
          <button type="button" class="btn btn-sm btn-outline" :disabled="configBusy" @click="onTestConfig">
            {{ configBusy ? t('settings.syncTesting') : t('settings.syncConfigTest') }}
          </button>
          <button type="button" class="btn btn-sm btn-primary" :disabled="configBusy" @click="onSaveConfig">
            {{ t('settings.syncConfigSave') }}
          </button>
        </div>
        <div v-if="configMessage" class="setting-row">
          <span :class="configError ? 'error-text' : 'ok-text'">{{ configMessage }}</span>
        </div>
      </div>

      <div class="sync-actions">
        <button type="button" class="btn btn-sm" :disabled="state.busy || state.enabled" @click="onEnable">
          {{ t('settings.syncEnable') }}
        </button>
        <button type="button" class="btn btn-sm" :disabled="state.busy || !state.enabled" @click="onRun">
          {{ t('settings.syncRun') }}
        </button>
        <button type="button" class="btn btn-sm" :disabled="state.busy || !state.enabled" @click="onDisable">
          {{ t('settings.syncDisable') }}
        </button>
        <button type="button" class="btn btn-sm btn-outline" :disabled="state.busy" @click="onTest">
          {{ t('settings.syncTest') }}
        </button>
        <button
          v-if="state.connection || state.connectionError || state.identityError"
          type="button"
          class="btn btn-sm btn-outline"
          :disabled="state.busy"
          @click="onReset"
        >
          {{ t('settings.syncReset') }}
        </button>
      </div>

      <div v-if="state.lastRun" class="sync-status">
        <div>{{ t('settings.syncLastRun') }}: {{ state.lastRun.Synced ? t('settings.syncOk') : t('settings.syncFailed') }}</div>
        <div v-if="state.lastRun.LastError">{{ t('settings.syncError') }}: {{ state.lastRun.LastError }}</div>
        <div v-if="state.lastCompleted">{{ t('settings.syncLastCompleted') }}: {{ formatTime(state.lastCompleted) }}</div>
        <div>{{ t('settings.syncConflicts') }}: {{ state.lastRun.Conflicts || 0 }}</div>
        <div>{{ t('settings.syncRecoveryCount') }}: {{ state.recovery.length }}</div>
      </div>

      <div v-if="state.recovery.length" class="sync-recovery">
        <div class="setting-row-label">{{ t('settings.syncRecoveryCopies') }}</div>
        <ul class="sync-recovery-list">
          <li v-for="(copy, i) in state.recovery" :key="copy.syncId + copy.stateHash">
            <span>{{ copy.path || copy.syncId }} ({{ copy.size }} B)</span>
            <button type="button" class="btn btn-sm btn-outline" :disabled="state.busy" @click="onRestore(i)">
              {{ t('settings.syncRestore') }}
            </button>
          </li>
        </ul>
      </div>
      <div v-else-if="state.recoveryError" class="setting-row">
        <span class="setting-row-label error-text">{{ t('settings.syncRecoveryError') }}: {{ state.recoveryError }}</span>
      </div>

      <div v-if="syncMessage" class="setting-row">
        <span :class="syncError ? 'error-text' : 'ok-text'">{{ syncMessage }}</span>
      </div>
    </div>
  </div>
</template>

<script setup>
import { onMounted, reactive, ref } from 'vue'
import { useI18n } from '../i18n'
import { cloudSyncAvailable, isLocalBuild } from '../composables/runtime'
import { initSyncConfig, getSyncConfigState, saveConfig, testConfig } from '../composables/useSyncConfig'
import {
  initSyncSettings,
  getSyncSettings,
  enableSync,
  runSync,
  disableSync,
  resetSync,
  testSync,
  restoreRecovery,
} from '../composables/useSyncSettings'

const { t } = useI18n()
const state = getSyncSettings()
const open = ref(false)
const syncMessage = ref('')
const syncError = ref(false)
const showSecrets = ref(false)

// Browser-owned note-sync configuration (local build only).
const cfg = getSyncConfigState()
const configBusy = ref(false)
const configMessage = ref('')
const configError = ref(false)
const syncCorsTemplate = `[
  {
    "AllowedOrigins": ["<app origin>"],
    "AllowedMethods": ["PUT", "GET", "HEAD", "DELETE"],
    "AllowedHeaders": ["Authorization", "Content-Type", "x-amz-*", "If-Match", "If-None-Match"],
    "ExposeHeaders": ["ETag"],
    "MaxAgeSeconds": 3000
  }
]`

onMounted(() => {
  initSyncSettings()
  initSyncConfig()
})

function formatTime(value) {
  const d = new Date(value)
  return Number.isNaN(d.getTime()) ? String(value) : d.toLocaleString()
}

async function onSaveConfig() {
  configMessage.value = ''
  configBusy.value = true
  try {
    await saveConfig()
    configMessage.value = t('settings.syncConfigSaved')
    configError.value = false
  } catch (e) {
    configMessage.value = e?.response?.data?.error || t('settings.syncFailed')
    configError.value = true
  } finally {
    configBusy.value = false
  }
}

async function onTestConfig() {
  configMessage.value = ''
  configBusy.value = true
  try {
    await testConfig()
    configMessage.value = t('settings.syncConfigTestOk')
    configError.value = false
  } catch (e) {
    configMessage.value = e?.response?.data?.error || t('settings.syncTestFailed')
    configError.value = true
  } finally {
    configBusy.value = false
  }
}

async function onEnable() {
  syncMessage.value = ''
  try {
    await enableSync()
    syncMessage.value = t('settings.syncEnabledOk')
    syncError.value = false
  } catch (e) {
    syncMessage.value = e?.response?.data?.error || t('settings.syncFailed')
    syncError.value = true
  }
}

async function onRun() {
  syncMessage.value = ''
  try {
    await runSync()
    syncMessage.value = state.lastRun?.Synced ? t('settings.syncRunOk') : t('settings.syncRunFailed')
    syncError.value = !state.lastRun?.Synced
  } catch (e) {
    syncMessage.value = e?.response?.data?.error || t('settings.syncFailed')
    syncError.value = true
  }
}

async function onDisable() {
  syncMessage.value = ''
  try {
    await disableSync()
    syncMessage.value = t('settings.syncDisabledOk')
    syncError.value = false
  } catch (e) {
    syncMessage.value = e?.response?.data?.error || t('settings.syncFailed')
    syncError.value = true
  }
}

async function onTest() {
  syncMessage.value = ''
  try {
    await testSync()
    syncMessage.value = t('settings.syncTestOk')
    syncError.value = false
  } catch (e) {
    syncMessage.value = e?.response?.data?.error || t('settings.syncTestFailed')
    syncError.value = true
  }
}

async function onReset() {
  if (!window.confirm(t('settings.syncResetConfirm'))) return
  syncMessage.value = ''
  try {
    await resetSync()
    syncMessage.value = t('settings.syncResetOk')
    syncError.value = false
  } catch (e) {
    syncMessage.value = e?.response?.data?.error || t('settings.syncResetFailed')
    syncError.value = true
  }
}

async function onRestore(index) {
  const copy = state.recovery[index]
  syncMessage.value = ''
  try {
    await restoreRecovery(copy.syncId, copy.stateHash)
    syncMessage.value = t('settings.syncRestoredOk')
    syncError.value = false
  } catch (e) {
    syncMessage.value = e?.response?.data?.error || t('settings.syncFailed')
    syncError.value = true
  }
}
</script>

<style scoped>
.settings-section {
  padding: 18px 0;
  border-bottom: 1px solid var(--border-light);
}
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
.settings-caret {
  color: var(--text-muted);
  font-size: 18px;
  transition: transform 0.15s ease;
}
.settings-caret.open { transform: rotate(180deg); }
.sync-badge {
  flex-shrink: 0;
  font-size: 11px;
  padding: 2px 8px;
  border-radius: 10px;
  background: var(--primary-bg);
  color: var(--primary-dark);
}
.settings-sync-body { padding-top: 14px; }
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
.sync-warning { color: var(--danger); }
.error-text { color: var(--danger); }
.ok-text { color: var(--success); }
.sync-actions {
  display: flex;
  gap: 8px;
  flex-wrap: wrap;
  margin-top: 4px;
}
.sync-status {
  margin-top: 14px;
  font-size: 12px;
  color: var(--text-muted);
  display: flex;
  flex-direction: column;
  gap: 2px;
}
.sync-recovery {
  margin-top: 14px;
}
.sync-recovery-list {
  list-style: none;
  margin: 6px 0 0;
  padding: 0;
  display: flex;
  flex-direction: column;
  gap: 6px;
}
.sync-recovery-list li {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  font-size: 12px;
  color: var(--text-secondary);
}
.sync-config { margin-top: 14px; }
.input-checkbox {
  appearance: none;
  -webkit-appearance: none;
  width: 18px;
  height: 18px;
  flex-shrink: 0;
  border: 1.5px solid var(--border);
  border-radius: 5px;
  background: var(--bg-card);
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
.cors-template {
  margin: 8px 0 14px;
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
}
.cors-template-warning { margin: 6px 0 0; color: var(--text-muted); }
</style>

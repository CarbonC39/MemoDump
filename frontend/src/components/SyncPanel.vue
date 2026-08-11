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
      <InfoTooltip class="sync-heading-tip" :text="t('settings.syncSafety')" :label="t('settings.syncSafety')" />
      <button
        type="button"
        class="settings-section-header"
        :aria-label="t('settings.syncSection')"
        :aria-expanded="open"
        @click="open = !open"
      >
        <span class="sync-mode-summary">{{ headerSummary }}</span>
        <span class="material-icons-outlined settings-caret" :class="{ open }">expand_more</span>
      </button>
    </div>

    <div v-show="open" class="settings-sync-body">
      <div v-if="state.connectionError || state.identityError" class="sync-status sync-status-error" role="alert">
        <span class="material-icons-outlined sync-status-icon" aria-hidden="true">error</span>
        <span>{{ state.connectionError || state.identityError }}</span>
      </div>

      <template v-if="state.connected">
        <div class="setting-row sync-status-row">
          <span class="setting-row-label">{{ t('settings.syncStatus') }}</span>
          <span class="sync-state-copy">
            <strong>{{ connectedTitle }}</strong>
            <span>{{ connectedDetail }}</span>
            <span v-if="state.autoPaused && state.pauseReason" class="sync-state-warning">{{ state.pauseReason }}</span>
            <span v-if="state.lastRun?.LastError" class="sync-state-warning">{{ state.lastRun.LastError }}</span>
          </span>
        </div>

        <div v-if="state.lastRun?.Conflicts" class="setting-row">
          <span class="setting-row-label">{{ t('settings.syncConflicts') }}</span>
          <span class="sync-field-value">{{ state.lastRun.Conflicts }}</span>
        </div>

        <div class="setting-row">
          <span class="setting-row-label"></span>
          <div class="sync-actions">
            <button
              type="button"
              class="btn btn-sm btn-primary"
              data-testid="sync-run"
              :disabled="actionBusy"
              @click="onRun"
            >
              {{ state.syncRunning ? t('settings.syncRunningLabel') : t('settings.syncRun') }}
            </button>
            <button
              type="button"
              class="btn btn-sm btn-outline"
              data-testid="sync-disconnect"
              :disabled="actionBusy"
              @click="onDisconnect"
            >
              {{ t('settings.syncDisconnect') }}
            </button>
          </div>
        </div>
      </template>

      <template v-else>
        <div v-if="state.connection" class="sync-status">
          <span class="material-icons-outlined sync-status-icon" aria-hidden="true">cloud_off</span>
          <span>{{ t('settings.syncDisconnected') }}</span>
        </div>

        <div class="sync-config">
          <div v-if="!cfg.editable" class="setting-row">
            <span class="setting-row-label">{{ t('settings.syncConfigReadOnly') }}</span>
          </div>

          <label class="setting-row sync-field">
            <span class="setting-row-label">Endpoint</span>
            <input v-model.trim="cfg.endpoint" type="text" class="input input-select" placeholder="https://s3.example.com" :disabled="!cfg.editable" />
          </label>
          <label class="setting-row sync-field">
            <span class="setting-row-label">Bucket</span>
            <input v-model.trim="cfg.bucket" type="text" class="input input-select" :disabled="!cfg.editable" />
          </label>
          <label class="setting-row sync-field">
            <span class="setting-row-label label-with-tip">
              Access Key
              <InfoTooltip
                :text="isLocalBuild ? t('settings.syncCredentialWarning') : t('settings.syncServerCredentialWarning')"
                :label="isLocalBuild ? t('settings.syncCredentialWarning') : t('settings.syncServerCredentialWarning')"
              />
            </span>
            <span class="secret-input">
              <input v-model.trim="cfg.accessKey" :type="showSecrets ? 'text' : 'password'" class="input input-select" :disabled="!cfg.editable" />
              <button
                type="button"
                class="secret-toggle"
                :disabled="!cfg.editable"
                :aria-label="showSecrets ? t('settings.secretHide') : t('settings.secretShow')"
                @click="showSecrets = !showSecrets"
              >
                <span class="material-icons-outlined">{{ showSecrets ? 'visibility_off' : 'visibility' }}</span>
              </button>
            </span>
          </label>
          <label class="setting-row sync-field">
            <span class="setting-row-label">Secret Key</span>
            <span class="secret-input">
              <input
                v-model.trim="cfg.secretKey"
                :type="showSecrets ? 'text' : 'password'"
                class="input input-select"
                :placeholder="cfg.configured ? t('settings.syncSecretUnchanged') : ''"
                :disabled="!cfg.editable"
              />
              <button
                type="button"
                class="secret-toggle"
                :disabled="!cfg.editable"
                :aria-label="showSecrets ? t('settings.secretHide') : t('settings.secretShow')"
                @click="showSecrets = !showSecrets"
              >
                <span class="material-icons-outlined">{{ showSecrets ? 'visibility_off' : 'visibility' }}</span>
              </button>
            </span>
          </label>

          <details class="sync-advanced">
            <summary>
              <span class="material-icons-outlined sync-advanced-icon" aria-hidden="true">tune</span>
              <span>{{ t('settings.syncAdvanced') }}</span>
              <span class="material-icons-outlined details-caret" aria-hidden="true">expand_more</span>
            </summary>
            <div class="sync-advanced-body">
              <label class="setting-row sync-field">
                <span class="setting-row-label">Region</span>
                <input v-model.trim="cfg.region" type="text" class="input input-select" placeholder="us-east-1" :disabled="!cfg.editable" />
              </label>
              <label class="setting-row sync-field">
                <span class="setting-row-label">Prefix</span>
                <input v-model.trim="cfg.prefix" type="text" class="input input-select" :disabled="!cfg.editable" />
              </label>
              <label class="setting-row">
                <span class="setting-row-label">{{ t('settings.syncConfigForcePathStyle') }}</span>
                <input v-model="cfg.forcePathStyle" type="checkbox" class="input-checkbox" :disabled="!cfg.editable" />
              </label>
            </div>
          </details>

          <details v-if="isLocalBuild" class="cors-template">
            <summary>
              <span class="material-icons-outlined cors-template-icon" aria-hidden="true">dns</span>
              <span>{{ t('settings.syncCorsTemplate') }}</span>
              <span class="material-icons-outlined details-caret" aria-hidden="true">expand_more</span>
            </summary>
            <pre class="cors-template-code">{{ syncCorsTemplate }}</pre>
            <p class="cors-template-warning">{{ t('settings.syncCorsHint') }}</p>
          </details>

          <div class="setting-row sync-action-row">
            <span class="setting-row-label"></span>
            <div class="sync-actions">
              <button
                type="button"
                class="btn btn-sm btn-primary"
                data-testid="sync-connect"
                :disabled="actionBusy || !connectReady"
                @click="onConnect"
              >
                {{ actionBusy ? t('settings.syncConnecting') : state.connection ? t('settings.syncReconnect') : t('settings.syncConnect') }}
              </button>
            </div>
          </div>
        </div>

        <button
          v-if="state.connection || state.connectionError || state.identityError"
          type="button"
          class="sync-text-action"
          :disabled="actionBusy"
          @click="onReset"
        >
          {{ t('settings.syncForget') }}
        </button>
      </template>

      <div v-if="state.recovery.length" class="sync-recovery">
        <div class="sync-recovery-heading">
          <strong>{{ t('settings.syncRecoveryCopies') }}</strong>
          <span>{{ state.recovery.length }}</span>
        </div>
        <ul class="sync-recovery-list">
          <li v-for="(copy, i) in state.recovery" :key="copy.syncId + copy.stateHash">
            <span>{{ copy.path || copy.syncId }} ({{ copy.size }} B)</span>
            <button type="button" class="btn btn-sm btn-outline" :disabled="state.busy" @click="onRestore(i)">
              {{ t('settings.syncRestore') }}
            </button>
          </li>
        </ul>
      </div>
      <div v-else-if="state.recoveryError" class="sync-status sync-status-error">
        <span class="material-icons-outlined sync-status-icon" aria-hidden="true">error</span>
        <span>{{ t('settings.syncRecoveryError') }}: {{ state.recoveryError }}</span>
      </div>

      <div v-if="syncMessage" class="sync-status" :class="syncError ? 'sync-status-error' : 'sync-status-ok'" aria-live="polite">
        <span class="material-icons-outlined sync-status-icon" aria-hidden="true">{{ syncError ? 'error' : 'check_circle' }}</span>
        <span>{{ syncMessage }}</span>
      </div>
    </div>
  </div>
</template>

<script setup>
import { computed, onMounted, ref } from 'vue'
import { useI18n } from '../i18n'
import InfoTooltip from './InfoTooltip.vue'
import { cloudSyncAvailable, isLocalBuild } from '../composables/runtime'
import { initSyncConfig, getSyncConfigState, saveConfig } from '../composables/useSyncConfig'
import {
  initSyncSettings,
  getSyncSettings,
  enableSync,
  runSync,
  disableSync,
  resetSync,
  restoreRecovery,
} from '../composables/useSyncSettings'

const { t } = useI18n()
const state = getSyncSettings()
const cfg = getSyncConfigState()
const open = ref(false)
const syncMessage = ref('')
const syncError = ref(false)
const showSecrets = ref(false)
const configBusy = ref(false)

const syncCorsTemplate = `[
  {
    "AllowedOrigins": ["<app origin>"],
    "AllowedMethods": ["PUT", "GET", "HEAD", "DELETE"],
    "AllowedHeaders": ["Authorization", "Content-Type", "x-amz-*", "If-Match", "If-None-Match"],
    "ExposeHeaders": ["ETag", "Retry-After"],
    "MaxAgeSeconds": 3000
  }
]`

const actionBusy = computed(() => configBusy.value || state.busy)
const connectReady = computed(() => {
  if (!cfg.editable) return cfg.configured
  return Boolean(cfg.endpoint && cfg.bucket && cfg.accessKey && (cfg.secretKey || cfg.configured))
})
const connectedTitle = computed(() => {
  if (state.autoPaused) return t('settings.syncAttention')
  if (state.syncRunning) return t('settings.syncRunningLabel')
  return t('settings.syncConnected')
})
const headerSummary = computed(() => {
  if (state.connected) return connectedTitle.value
  if (state.connection) return t('settings.syncDisconnectedShort')
  return t('settings.syncNotConnected')
})
const connectedDetail = computed(() => {
  if (state.syncRunning) return t('settings.syncSyncingDetail')
  if (state.lastCompleted) return `${t('settings.syncLastSynced')}: ${formatTime(state.lastCompleted)}`
  return t('settings.syncConnectedHelp')
})

onMounted(() => {
  initSyncSettings()
  initSyncConfig()
})

function formatTime(value) {
  const d = new Date(value)
  return Number.isNaN(d.getTime()) ? String(value) : d.toLocaleString()
}

function setMessage(message, error = false) {
  syncMessage.value = message
  syncError.value = error
}

function errorMessage(error, fallback) {
  return error?.response?.data?.error || fallback
}

async function onConnect() {
  setMessage('')
  configBusy.value = true
  try {
    if (cfg.editable) await saveConfig()
    await enableSync()
    setMessage(t('settings.syncConnectedOk'))
  } catch (error) {
    setMessage(errorMessage(error, t('settings.syncFailed')), true)
  } finally {
    configBusy.value = false
  }
}

async function onRun() {
  setMessage('')
  try {
    await runSync()
    setMessage(
      state.lastRun?.Synced ? t('settings.syncRunOk') : t('settings.syncRunFailed'),
      !state.lastRun?.Synced,
    )
  } catch (error) {
    setMessage(errorMessage(error, t('settings.syncFailed')), true)
  }
}

async function onDisconnect() {
  setMessage('')
  try {
    await disableSync()
    setMessage(t('settings.syncDisconnectedOk'))
  } catch (error) {
    setMessage(errorMessage(error, t('settings.syncFailed')), true)
  }
}

async function onReset() {
  if (!window.confirm(t('settings.syncResetConfirm'))) return
  setMessage('')
  try {
    await resetSync()
    setMessage(t('settings.syncResetOk'))
  } catch (error) {
    setMessage(errorMessage(error, t('settings.syncResetFailed')), true)
  }
}

async function onRestore(index) {
  const copy = state.recovery[index]
  setMessage('')
  try {
    await restoreRecovery(copy.syncId, copy.stateHash)
    setMessage(t('settings.syncRestoredOk'))
  } catch (error) {
    setMessage(errorMessage(error, t('settings.syncFailed')), true)
  }
}
</script>

<style scoped>
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
.sync-heading-tip { flex-shrink: 0; }
.sync-mode-summary {
  flex: 1;
  overflow: hidden;
  color: var(--text-muted);
  font-size: 12px;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.settings-caret {
  color: var(--text-muted);
  font-size: 18px;
  transition: transform 0.15s ease;
}
.settings-caret.open { transform: rotate(180deg); }
.settings-sync-body { padding-top: 14px; }
.setting-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 14px;
}
.setting-row:last-child { margin-bottom: 0; }
.setting-row-label {
  color: var(--text-secondary);
  font-size: 13px;
  font-weight: 500;
  flex-shrink: 0;
}
.label-with-tip {
  display: inline-flex;
  align-items: center;
  gap: 4px;
}
.sync-status {
  display: flex;
  align-items: flex-start;
  gap: 6px;
  margin: 8px 0 14px;
  padding: 8px 10px;
  border-radius: var(--radius);
  background: var(--bg-sidebar);
  color: var(--text-secondary);
  font-size: 12px;
  line-height: 1.5;
}
.sync-status-icon { flex-shrink: 0; font-size: 16px; }
.sync-status-ok { background: rgba(34, 197, 94, 0.1); }
.sync-status-ok .sync-status-icon { color: var(--success); }
.sync-status-error { background: var(--danger-light); }
.sync-status-error .sync-status-icon { color: var(--danger); }
.sync-status-row { align-items: flex-start; }
.sync-state-copy {
  display: flex;
  min-width: 0;
  flex-direction: column;
  gap: 3px;
  align-items: flex-end;
  color: var(--text-muted);
  font-size: 12px;
  line-height: 1.45;
  text-align: right;
}
.sync-state-copy strong {
  color: var(--text-primary);
  font-size: 13px;
}
.sync-state-warning { color: var(--danger); overflow-wrap: anywhere; }
.sync-field-value {
  color: var(--text-muted);
  font-size: 13px;
}
.sync-config { margin: 0; }
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
.secret-toggle:hover:not(:disabled) { color: var(--primary-dark); background: var(--primary-bg); }
.secret-toggle:disabled { cursor: default; opacity: 0.5; }
.secret-toggle .material-icons-outlined { font-size: 16px; }
.sync-advanced {
  margin: 0 0 14px;
  color: var(--text-secondary);
  font-size: 12px;
  line-height: 1.6;
}
.sync-advanced summary {
  display: flex;
  align-items: center;
  gap: 6px;
  cursor: pointer;
  list-style: none;
  color: var(--text-secondary);
  font-weight: 500;
}
.sync-advanced summary::-webkit-details-marker { display: none; }
.sync-advanced-icon { font-size: 16px; }
.details-caret {
  margin-left: auto;
  color: var(--text-muted);
  font-size: 18px;
  transition: transform 0.15s ease;
}
.sync-advanced[open] .details-caret,
.cors-template[open] .details-caret { transform: rotate(180deg); }
.sync-advanced-body { padding-top: 12px; }
.input-checkbox {
  appearance: none;
  -webkit-appearance: none;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 18px;
  height: 18px;
  flex-shrink: 0;
  border: 1.5px solid var(--border);
  border-radius: 5px;
  background: var(--bg-card);
  cursor: pointer;
}
.input-checkbox:hover:not(:disabled) { border-color: var(--primary); }
.input-checkbox:checked { border-color: var(--primary); background: var(--primary); }
.input-checkbox:checked::after {
  content: '';
  width: 8px;
  height: 4px;
  border-left: 2px solid #fff;
  border-bottom: 2px solid #fff;
  transform: rotate(-45deg) translateY(-1px);
}
.input-checkbox:disabled { opacity: 0.5; cursor: default; }
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
  white-space: pre;
}
.cors-template-warning { margin: 6px 0 0; color: var(--text-muted); }
.sync-actions {
  display: flex;
  gap: 8px;
}
.sync-action-row { margin-top: 2px; }
.sync-text-action {
  margin: 0 0 14px;
  padding: 2px 0;
  border: 0;
  color: var(--text-muted);
  background: transparent;
  font-size: 12px;
  text-decoration: underline;
  text-underline-offset: 2px;
}
.sync-text-action:hover:not(:disabled) { color: var(--text-secondary); }
.sync-text-action:disabled { opacity: 0.5; }
.sync-recovery {
  margin-top: 16px;
  padding-top: 14px;
  border-top: 1px solid var(--border);
}
.sync-recovery-heading {
  display: flex;
  align-items: center;
  justify-content: space-between;
  color: var(--text-secondary);
  font-size: 12px;
}
.sync-recovery-list {
  display: flex;
  flex-direction: column;
  gap: 7px;
  margin: 8px 0 0;
  padding: 0;
  list-style: none;
}
.sync-recovery-list li {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  color: var(--text-secondary);
  font-size: 12px;
}

@media (max-width: 560px) {
  .sync-actions { flex-wrap: wrap; }
  .sync-state-copy { max-width: 70%; }
}
</style>

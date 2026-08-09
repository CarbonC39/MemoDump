<template>
  <div class="settings-section">
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
      <div v-if="state.noE2EE" class="setting-row">
        <span class="setting-row-label sync-warning">{{ t('settings.syncNoE2EE') }}</span>
      </div>
      <div v-if="state.experimental" class="setting-row">
        <span class="setting-row-label">{{ t('settings.syncExperimental') }}</span>
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
      </div>

      <div v-if="state.lastRun" class="sync-status">
        <div>{{ t('settings.syncLastRun') }}: {{ state.lastRun.Synced ? t('settings.syncOk') : t('settings.syncFailed') }}</div>
        <div v-if="state.lastRun.LastError">{{ t('settings.syncError') }}: {{ state.lastRun.LastError }}</div>
        <div>{{ t('settings.syncConflicts') }}: {{ state.lastRun.Conflicts || 0 }}</div>
        <div>{{ t('settings.syncRecoveryCount') }}: {{ state.recovery.length }}</div>
      </div>

      <div v-if="syncMessage" class="setting-row">
        <span :class="syncError ? 'error-text' : 'ok-text'">{{ syncMessage }}</span>
      </div>
    </div>
  </div>
</template>

<script setup>
import { onMounted, ref } from 'vue'
import { useI18n } from '../i18n'
import {
  initSyncSettings,
  getSyncSettings,
  enableSync,
  runSync,
  disableSync,
  testSync,
} from '../composables/useSyncSettings'

const { t } = useI18n()
const state = getSyncSettings()
const open = ref(false)
const syncMessage = ref('')
const syncError = ref(false)

onMounted(() => initSyncSettings())

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
</script>

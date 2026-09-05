<template>
  <div v-if="promptVisible" class="modal-overlay" @click.self="$emit('cancel-prompt')">
    <div class="prompt-modal">
      <h2 class="dialog-title">{{ promptTitle }}</h2>
      <input
        ref="promptInput"
        class="input"
        :value="promptValue"
        :placeholder="promptTitle"
        @input="$emit('update:prompt-value', $event.target.value)"
        @keydown.enter="$emit('submit-prompt')"
      />
      <div class="prompt-actions">
        <button class="btn btn-ghost" @click="$emit('cancel-prompt')">{{ t('modals.cancel') }}</button>
        <button class="btn btn-primary" @click="$emit('submit-prompt')">{{ t('modals.confirm') }}</button>
      </div>
    </div>
  </div>

  <div v-if="confirmDialog.visible" class="modal-overlay" @click.self="$emit('cancel-confirm')">
    <div class="prompt-modal">
      <h2 class="dialog-title">{{ confirmDialog.title }}</h2>
      <p v-if="confirmDialog.message" class="confirm-message">{{ confirmDialog.message }}</p>
      <div class="prompt-actions">
        <button class="btn btn-ghost" @click="$emit('cancel-confirm')">{{ t('modals.cancel') }}</button>
        <button
          class="btn"
          :class="confirmDialog.danger ? 'btn-danger' : 'btn-primary'"
          @click="$emit('accept-confirm')"
        >
          {{ confirmDialog.okLabel }}
        </button>
      </div>
    </div>
  </div>

  <div v-if="copyDialog.visible" class="modal-overlay" @click.self="$emit('close-copy')">
    <div class="prompt-modal">
      <h2 class="dialog-title">{{ t('modals.copyText') }}</h2>
      <p class="copy-instruction">{{ t('modals.copyInstruction') }}</p>
      <textarea
        ref="copyTextarea"
        class="input copy-textarea"
        :value="copyDialog.content"
        readonly
        @focus="$event.target.setSelectionRange(0, $event.target.value.length)"
      />
      <div class="prompt-actions">
        <button class="btn btn-ghost" @click="$emit('close-copy')">{{ t('modals.close') }}</button>
        <button class="btn btn-primary" @click="$emit('copy', copyTextarea)">{{ t('modals.copy') }}</button>
      </div>
    </div>
  </div>
</template>

<script setup>
import { nextTick, ref, watch } from 'vue'
import { useI18n } from '../i18n'

const props = defineProps({
  promptVisible: { type: Boolean, default: false },
  promptTitle: { type: String, default: '' },
  promptValue: { type: String, default: '' },
  confirmDialog: { type: Object, required: true },
  copyDialog: { type: Object, required: true },
})
defineEmits([
  'update:prompt-value',
  'submit-prompt',
  'cancel-prompt',
  'accept-confirm',
  'cancel-confirm',
  'close-copy',
  'copy',
])

const { t } = useI18n()
const promptInput = ref(null)
const copyTextarea = ref(null)

watch(() => props.promptVisible, (visible) => {
  if (visible) nextTick(() => promptInput.value?.focus())
})
</script>

<style scoped>
.modal-overlay {
  position: fixed;
  z-index: 999;
  inset: 0;
  display: flex;
  align-items: center;
  justify-content: center;
  background: rgba(0, 0, 0, 0.3);
}
.prompt-modal {
  width: 340px;
  max-width: 90%;
  padding: 24px;
  background: var(--bg-card);
  border-radius: var(--radius-lg);
  box-shadow: var(--shadow-md);
}
.dialog-title { margin: 0 0 16px; }
.prompt-actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  margin-top: 20px;
}
.confirm-message {
  margin-top: -6px;
  color: var(--text-secondary);
  font-size: 14px;
  line-height: 1.5;
}
.btn-danger {
  color: var(--on-accent);
  background: var(--danger);
}
.btn-danger:hover {
  background: var(--danger);
  filter: brightness(0.92);
}
.copy-instruction {
  margin-bottom: 10px;
  color: var(--text-secondary);
  font-size: 13px;
}
.copy-textarea {
  box-sizing: border-box;
  width: 100%;
  height: 180px;
  resize: vertical;
  font-family: monospace;
  font-size: 13px;
}

@media (max-width: 768px) {
  .input { font-size: 16px !important; }
}
</style>

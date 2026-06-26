<template>
  <div class="login-page">
    <div class="login-card">
      <div class="login-header">
        <div class="login-logo">
          <img src="/favicon.ico" width="64" height="64" alt="Logo" style="border-radius: 10px;" />
        </div>
        <h1>{{ t('login.brand') }}</h1>
      </div>
      <form @submit.prevent="doLogin" class="login-form">
        <div v-if="error" class="login-error">{{ error }}</div>
        <div class="form-group">
          <label>{{ t('login.account') }}</label>
          <input v-model="user" type="text" class="input" :placeholder="t('login.username')" autofocus />
        </div>
        <div class="form-group">
          <label>{{ t('login.password') }}</label>
          <input v-model="pass" type="password" class="input" :placeholder="t('login.password')" />
        </div>
        <button type="submit" class="btn btn-primary w-full" :disabled="loading" style="width:100%">
          {{ loading ? t('login.signingIn') : t('login.signIn') }}
        </button>
      </form>
    </div>
  </div>
</template>

<script setup>
import { ref } from 'vue'
import { useRouter, useRoute } from 'vue-router'
import { useI18n } from '../i18n'
import api from '../api'

const { t } = useI18n()
const router = useRouter()
const route = useRoute()
const user = ref('')
const pass = ref('')
const error = ref('')
const loading = ref(false)

function getSafeRedirect(target) {
  if (!target || typeof target !== 'string') return '/'
  if (target.startsWith('/') && !target.startsWith('//')) {
    return target
  }

  return '/'
}

async function doLogin() {
  if (!user.value || !pass.value) {
    error.value = t('login.enterCredentials')
    return
  }

  loading.value = true
  error.value = ''

  try {
    await api.login(user.value, pass.value)
    const redirectPath = getSafeRedirect(route.query.redirect)
    router.push(redirectPath)
  } catch (e) {
    error.value = e.response?.data?.error || t('login.loginFailed')
  } finally {
    loading.value = false
  }
}
</script>

<style scoped>
.login-page {
  height: 100%;
  display: flex;
  align-items: center;
  justify-content: center;
  background: linear-gradient(135deg, #EDF2FC 0%, #F8FAFD 100%);
}
.login-card {
  width: 100%;
  max-width: 380px;
  background: #fff;
  border-radius: var(--radius-lg);
  padding: 40px 32px;
  box-shadow: var(--shadow-md);
}
.login-header {
  text-align: center;
  margin-bottom: 32px;
}
.login-logo {
  margin-bottom: 16px;
}
.login-header h1 {
  font-size: 24px;
  font-weight: 700;
  color: var(--text);
  margin-bottom: 4px;
}
.login-header p {
  font-size: 14px;
  color: var(--text-muted);
}
.login-form {
  display: flex;
  flex-direction: column;
  gap: 16px;
}
.form-group {
  display: flex;
  flex-direction: column;
  gap: 6px;
}
.form-group label {
  font-size: 13px;
  font-weight: 500;
  color: var(--text-secondary);
}
.login-error {
  background: var(--danger-light);
  color: var(--danger);
  padding: 10px 14px;
  border-radius: var(--radius);
  font-size: 13px;
}
</style>

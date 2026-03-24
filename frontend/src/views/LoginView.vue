<template>
  <div class="login-page">
    <div class="login-card">
      <div class="login-header">
        <div class="login-logo">
          <img src="/favicon.ico" width="64" height="64" alt="Logo" style="border-radius: 10px;" />
        </div>
        <h1>MemoDump</h1>
      </div>
      <form @submit.prevent="doLogin" class="login-form">
        <div v-if="error" class="login-error">{{ error }}</div>
        <div class="form-group">
          <label>Account</label>
          <input v-model="user" type="text" class="input" placeholder="Username" autofocus />
        </div>
        <div class="form-group">
          <label>Password</label>
          <input v-model="pass" type="password" class="input" placeholder="Password" />
        </div>
        <button type="submit" class="btn btn-primary w-full" :disabled="loading" style="width:100%">
          {{ loading ? 'Signing in...' : 'Sign In' }}
        </button>
      </form>
    </div>
  </div>
</template>

<script setup>
import { ref } from 'vue'
import { useRouter } from 'vue-router'
import api from '../api'

const router = useRouter()
const user = ref('')
const pass = ref('')
const error = ref('')
const loading = ref(false)

async function doLogin() {
  if (!user.value || !pass.value) {
    error.value = 'Please enter account and password'
    return
  }
  loading.value = true
  error.value = ''
  try {
    await api.login(user.value, pass.value)
    router.push('/')
  } catch (e) {
    error.value = e.response?.data?.error || 'Login failed'
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

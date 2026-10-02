<script setup lang="ts">
import { reactive, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'

import { ApiError, validateEmail } from '@yggauth/shared'

import { useAdminStore } from '../stores/admin'

const store = useAdminStore()
const route = useRoute()
const router = useRouter()

const form = reactive({ email: '', password: '' })
const error = ref('')
const submitting = ref(false)

async function onSubmit(): Promise<void> {
  error.value = validateEmail(form.email).message || (form.password ? '' : '请输入密码')
  if (error.value) {
    return
  }

  submitting.value = true
  try {
    await store.api.post('/api/auth/login', { email: form.email, password: form.password })
    await store.load()

    const target = typeof route.query.redirect === 'string' ? route.query.redirect : '/dashboard'
    // 只接受站内相对路径:直接把 ?redirect= 喂给 router 就是开放重定向。
    await router.push(target.startsWith('/') && !target.startsWith('//') ? target : '/dashboard')
  } catch (err) {
    error.value = err instanceof ApiError ? err.message : '登录失败'
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <div class="auth-shell">
    <div class="auth-card">
      <h1 class="auth-title" style="color: #3b6ea5">YggAuth</h1>
      <p class="auth-subtitle">管理后台</p>

      <a-form layout="vertical" @submit.prevent="onSubmit">
        <a-alert v-if="error" type="error" :message="error" show-icon style="margin-bottom: 16px" />
        <a-form-item label="邮箱">
          <a-input v-model:value="form.email" type="email" autocomplete="username" />
        </a-form-item>
        <a-form-item label="密码">
          <a-input-password v-model:value="form.password" autocomplete="current-password" />
        </a-form-item>
        <a-button type="primary" :loading="submitting" html-type="submit" block>登录</a-button>
      </a-form>
    </div>
  </div>
</template>

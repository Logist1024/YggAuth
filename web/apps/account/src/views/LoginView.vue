<script setup lang="ts">
import { computed, reactive, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'

import { ApiError, validateEmail } from '@yggauth/shared'

import { useSessionStore } from '../stores/session'

const store = useSessionStore()
const route = useRoute()
const router = useRouter()

const form = reactive({ email: '', password: '' })
const errors = reactive({ email: '', password: '' })
const submitting = ref(false)
const banner = ref('')

const canRegister = computed(() => store.policy.registration_mode !== 'closed')

async function onSubmit(): Promise<void> {
  errors.email = validateEmail(form.email).message
  errors.password = form.password ? '' : '请输入密码'
  banner.value = ''
  if (errors.email || errors.password) {
    return
  }

  submitting.value = true
  try {
    await store.api.post('/api/auth/login', { email: form.email, password: form.password })
    await store.loadAccount()

    // redirect 只接受站内相对路径。
    // 直接把 ?redirect= 原样喂给 router 会变成开放重定向 ——
    // 攻击者发一条 /login?redirect=https://evil.example 的链接,
    // 用户登录后就被送到钓鱼站。
    const target = typeof route.query.redirect === 'string' ? route.query.redirect : '/'
    await router.push(isInternalPath(target) ? target : '/')
  } catch (err) {
    banner.value = err instanceof ApiError ? err.message : '登录失败,请稍后再试'
  } finally {
    submitting.value = false
  }
}

function isInternalPath(path: string): boolean {
  return path.startsWith('/') && !path.startsWith('//')
}
</script>

<template>
  <a-form layout="vertical" @submit.prevent="onSubmit">
    <a-alert v-if="banner" type="error" :message="banner" show-icon style="margin-bottom: 16px" />

    <a-form-item label="邮箱" :validate-status="errors.email ? 'error' : ''" :help="errors.email">
      <a-input v-model:value="form.email" type="email" autocomplete="username" placeholder="you@example.com" />
    </a-form-item>

    <a-form-item label="密码" :validate-status="errors.password ? 'error' : ''" :help="errors.password">
      <a-input-password v-model:value="form.password" autocomplete="current-password" />
    </a-form-item>

    <div class="form-actions">
      <a-button type="primary" :loading="submitting" html-type="submit" block>登录</a-button>
    </div>

    <div style="margin-top: 16px; display: flex; justify-content: space-between">
      <RouterLink to="/forgot-password">忘记密码?</RouterLink>
      <RouterLink v-if="canRegister" to="/register">注册账号</RouterLink>
    </div>
  </a-form>
</template>

<script setup lang="ts">
import { computed, reactive, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'

import { errorBanner, validateEmail, type ErrorBanner } from '@yggauth/shared'

import { useAdminStore } from '../stores/admin'

const store = useAdminStore()
const route = useRoute()
const router = useRouter()

const form = reactive({ email: '', password: '' })
const banner = ref<ErrorBanner | null>(null)
const submitting = ref(false)

/**
 * 详情行:先给可操作的建议,再给错误码。
 *
 * 没有内容时返回 undefined,让 a-alert 干脆不渲染详情区 ——
 * 传空串会留下一个空白的描述块,看起来像样式坏了。
 */
const bannerDetail = computed(() => {
  if (!banner.value) {
    return undefined
  }
  const lines = [banner.value.hint, banner.value.trace].filter(Boolean)
  return lines.length > 0 ? lines.join('\n') : undefined
})

async function onSubmit(): Promise<void> {
  // 本地校验失败也走同一套展示结构,否则页面上会出现两种长相不同的错误。
  const local = validateEmail(form.email).message || (form.password ? '' : '请输入密码')
  if (local) {
    banner.value = { title: local, hint: '', trace: '' }
    return
  }

  submitting.value = true
  banner.value = null
  try {
    await store.api.post('/api/auth/login', { email: form.email, password: form.password })
    await store.load()

    const target = typeof route.query.redirect === 'string' ? route.query.redirect : '/dashboard'
    // 只接受站内相对路径:直接把 ?redirect= 喂给 router 就是开放重定向。
    await router.push(target.startsWith('/') && !target.startsWith('//') ? target : '/dashboard')
  } catch (err) {
    banner.value = errorBanner(err, '登录失败,请稍后再试')
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
        <a-alert
          v-if="banner"
          class="error-alert"
          type="error"
          show-icon
          style="margin-bottom: 16px"
          :message="banner.title"
          :description="bannerDetail"
        />
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

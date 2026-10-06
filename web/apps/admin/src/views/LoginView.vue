<script setup lang="ts">
import { reactive, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'

import { errorBanner, validateEmail, type ErrorBanner } from '@yggauth/shared'

import { useAdminStore } from '../stores/admin'
import ErrorAlert from '../components/ErrorAlert.vue'

const store = useAdminStore()
const route = useRoute()
const router = useRouter()

const form = reactive({ email: '', password: '' })
const banner = ref<ErrorBanner | null>(null)
const submitting = ref(false)

async function onSubmit(): Promise<void> {
  // 防重入:按钮的 loading 挡得住点击,挡不住回车连发 —— 登录多打一次还会给限流计数加一笔。
  if (submitting.value) return
  // 本地校验失败也走同一套展示结构,否则页面上会出现两种长相不同的错误。
  const local = validateEmail(form.email).message || (form.password ? '' : '请输入密码')
  if (local) {
    banner.value = { title: local, hint: '', trace: '' }
    return
  }

  submitting.value = true
  banner.value = null
  // 一开写就作废旧提示:已经在重新登录了,再挂一句「登录状态已过期」
  // 只会干扰对下一次结果的判断。
  store.clearNotice()
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
        <!--
          跨页提示:会话过期时后台会把人送回这一页。
          用 info 而不是 error —— 登录本身还没失败,这只是在解释
          「你为什么又站回了登录页」。
        -->
        <a-alert
          v-if="store.notice"
          type="info"
          show-icon
          closable
          style="margin-bottom: 16px"
          :message="store.notice"
          @close="store.clearNotice()"
        />
        <ErrorAlert :banner="banner" />
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

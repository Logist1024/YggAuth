<script setup lang="ts">
import { computed, reactive, ref } from 'vue'
import { useRouter } from 'vue-router'

import {
  ApiError,
  validateEmail,
  validatePassword,
  validateUsername,
  type ValidationResult,
} from '@yggauth/shared'

import { useSessionStore } from '../stores/session'

const store = useSessionStore()
const router = useRouter()

const form = reactive({ username: '', email: '', password: '', confirm: '' })
const errors = reactive({ username: '', email: '', password: '', confirm: '' })
const submitting = ref(false)
const banner = ref('')
const done = ref('')

const registrationClosed = computed(() => store.policy.registration_mode === 'closed')

function check(): boolean {
  errors.username = validateUsername(form.username, store.policy).message
  errors.email = validateEmail(form.email).message
  errors.password = validatePassword(form.password, store.policy).message
  errors.confirm = form.password === form.confirm ? '' : '两次输入的密码不一致'
  return !errors.username && !errors.email && !errors.password && !errors.confirm
}

async function onSubmit(): Promise<void> {
  banner.value = ''
  if (!check()) {
    return
  }

  submitting.value = true
  try {
    const data = await store.api.post<{ verify_url?: string }>('/api/auth/register', {
      username: form.username,
      email: form.email,
      password: form.password,
    })
    if (store.policy.require_email_verification) {
      // 验证链接只在开发/控制台投递场景里出现在响应里。
      // 生产环境由邮件送达,这里不给用户一个点不开的按钮。
      done.value = data.verify_url ?? ''
    } else {
      await router.push({ name: 'login' })
    }
  } catch (err) {
    banner.value = err instanceof ApiError ? err.message : '注册失败,请稍后再试'
  } finally {
    submitting.value = false
  }
}

void (0 as unknown as ValidationResult)
</script>

<template>
  <a-result v-if="registrationClosed" status="warning" title="注册已关闭" sub-title="当前部署不接受自助注册,请联系管理员邀请。" />

  <a-result
    v-else-if="done"
    status="success"
    title="注册成功"
    sub-title="请前往邮箱完成验证后再登录。"
  >
    <template #extra>
      <a-button type="primary" @click="router.push({ name: 'login' })">去登录</a-button>
    </template>
  </a-result>

  <a-form v-else layout="vertical" @submit.prevent="onSubmit">
    <a-alert v-if="banner" type="error" :message="banner" show-icon style="margin-bottom: 16px" />

    <a-form-item label="用户名" :validate-status="errors.username ? 'error' : ''" :help="errors.username">
      <a-input v-model:value="form.username" autocomplete="username" placeholder="字母、数字与下划线" />
    </a-form-item>

    <a-form-item label="邮箱" :validate-status="errors.email ? 'error' : ''" :help="errors.email">
      <a-input v-model:value="form.email" type="email" autocomplete="email" />
    </a-form-item>

    <a-form-item
      label="密码"
      :validate-status="errors.password ? 'error' : ''"
      :help="errors.password || `至少 ${store.policy.password_min_length} 位`"
    >
      <a-input-password v-model:value="form.password" autocomplete="new-password" />
    </a-form-item>

    <a-form-item label="确认密码" :validate-status="errors.confirm ? 'error' : ''" :help="errors.confirm">
      <a-input-password v-model:value="form.confirm" autocomplete="new-password" />
    </a-form-item>

    <div class="form-actions">
      <a-button type="primary" :loading="submitting" html-type="submit" block>注册</a-button>
    </div>

    <div style="margin-top: 16px; text-align: center">
      已有账号?<RouterLink to="/login">去登录</RouterLink>
    </div>
  </a-form>
</template>

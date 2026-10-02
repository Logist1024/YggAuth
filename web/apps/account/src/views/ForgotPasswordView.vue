<script setup lang="ts">
import { reactive, ref } from 'vue'

import { ApiError, validateEmail } from '@yggauth/shared'

import { useSessionStore } from '../stores/session'

const store = useSessionStore()
const form = reactive({ email: '' })
const error = ref('')
const submitting = ref(false)
const done = ref(false)

async function onSubmit(): Promise<void> {
  error.value = validateEmail(form.email).message
  if (error.value) {
    return
  }

  submitting.value = true
  try {
    await store.api.post('/api/auth/password/forgot', { email: form.email })
    done.value = true
  } catch (err) {
    error.value = err instanceof ApiError ? err.message : '请求失败,请稍后再试'
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <a-result v-if="done" status="success" title="请查收邮件">
    <template #subTitle>
      如果这个邮箱在本站注册过,你会在几分钟内收到一封重置邮件。
    </template>
    <template #extra>
      <RouterLink to="/login">返回登录</RouterLink>
    </template>
  </a-result>

  <a-form v-else layout="vertical" @submit.prevent="onSubmit">
    <p class="muted" style="margin-bottom: 16px">
      填写注册邮箱,我们会发一封包含重置链接的邮件。
    </p>
    <a-form-item label="邮箱" :validate-status="error ? 'error' : ''" :help="error">
      <a-input v-model:value="form.email" type="email" autocomplete="email" />
    </a-form-item>
    <div class="form-actions">
      <a-button type="primary" :loading="submitting" html-type="submit" block>发送重置邮件</a-button>
    </div>
    <div style="margin-top: 16px; text-align: center">
      <RouterLink to="/login">返回登录</RouterLink>
    </div>
  </a-form>
</template>

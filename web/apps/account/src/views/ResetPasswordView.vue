<script setup lang="ts">
import { computed, reactive, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'

import { ApiError, validatePassword } from '@yggauth/shared'

import { useSessionStore } from '../stores/session'

const store = useSessionStore()
const route = useRoute()
const router = useRouter()

const token = computed(() => (typeof route.query.token === 'string' ? route.query.token : ''))
const form = reactive({ password: '', confirm: '' })
const errors = reactive({ password: '', confirm: '' })
const submitting = ref(false)
const banner = ref('')
const done = ref(false)

async function onSubmit(): Promise<void> {
  errors.password = validatePassword(form.password, store.policy).message
  errors.confirm = form.password === form.confirm ? '' : '两次输入的密码不一致'
  banner.value = ''
  if (errors.password || errors.confirm) {
    return
  }

  submitting.value = true
  try {
    await store.api.post('/api/auth/password/reset', { token: token.value, password: form.password })
    done.value = true
  } catch (err) {
    banner.value = err instanceof ApiError ? err.message : '重置失败,请重新申请邮件'
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <a-result v-if="done" status="success" title="密码已重置" sub-title="请用新密码登录。">
    <template #extra>
      <a-button type="primary" @click="router.push({ name: 'login' })">去登录</a-button>
    </template>
  </a-result>

  <a-form v-else layout="vertical" @submit.prevent="onSubmit">
    <a-alert v-if="!token" type="warning" message="链接缺少重置令牌" show-icon style="margin-bottom: 16px" />
    <a-alert v-if="banner" type="error" :message="banner" show-icon style="margin-bottom: 16px" />

    <a-form-item
      label="新密码"
      :validate-status="errors.password ? 'error' : ''"
      :help="errors.password || `至少 ${store.policy.password_min_length} 位`"
    >
      <a-input-password v-model:value="form.password" autocomplete="new-password" />
    </a-form-item>

    <a-form-item label="确认新密码" :validate-status="errors.confirm ? 'error' : ''" :help="errors.confirm">
      <a-input-password v-model:value="form.confirm" autocomplete="new-password" />
    </a-form-item>

    <div class="form-actions">
      <a-button type="primary" :loading="submitting" html-type="submit" :disabled="!token" block>
        重置密码
      </a-button>
    </div>
  </a-form>
</template>

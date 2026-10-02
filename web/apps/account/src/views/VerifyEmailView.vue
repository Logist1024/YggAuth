<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRoute } from 'vue-router'

import { ApiError } from '@yggauth/shared'

import { useSessionStore } from '../stores/session'

const store = useSessionStore()
const route = useRoute()

const token = computed(() => (typeof route.query.token === 'string' ? route.query.token : ''))
const state = ref<'idle' | 'loading' | 'ok' | 'error'>('idle')
const message = ref('')

onMounted(async () => {
  if (!token.value) {
    state.value = 'error'
    message.value = '链接缺少验证令牌'
    return
  }

  state.value = 'loading'
  try {
    await store.api.post('/api/auth/email/verify', { token: token.value })
    state.value = 'ok'
  } catch (err) {
    state.value = 'error'
    message.value = err instanceof ApiError ? err.message : '验证失败'
  }
})
</script>

<template>
  <a-spin v-if="state === 'loading'" style="display: block; text-align: center" />

  <a-result
    v-else-if="state === 'ok'"
    status="success"
    title="邮箱验证成功"
    sub-title="现在可以登录了。"
  >
    <template #extra>
      <RouterLink to="/login">去登录</RouterLink>
    </template>
  </a-result>

  <a-result v-else status="error" title="验证失败" :sub-title="message">
    <template #extra>
      <RouterLink to="/login">返回登录</RouterLink>
    </template>
  </a-result>
</template>

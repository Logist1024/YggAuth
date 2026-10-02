<script setup lang="ts">
// 未登录页面的外壳:居中一张卡片。
import { computed } from 'vue'

import { useSessionStore } from '../stores/session'

const store = useSessionStore()
const theme = computed(() => store.theme)
</script>

<template>
  <div class="auth-shell">
    <div class="auth-card">
      <h1 class="auth-title" :style="{ color: theme.primary }">{{ theme.title }}</h1>
      <p class="auth-subtitle">{{ theme.subtitle }}</p>
      <RouterView />
    </div>
  </div>
</template>

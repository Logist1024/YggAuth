<script setup lang="ts">
// 已登录页面的外壳:顶部导航 + 内容区。
import { computed } from 'vue'
import { useRouter } from 'vue-router'

import { useSessionStore } from '../stores/session'

const store = useSessionStore()
const router = useRouter()
const theme = computed(() => store.theme)

async function onLogout(): Promise<void> {
  await store.logout()
  await router.push({ name: 'login' })
}
</script>

<template>
  <a-layout style="min-height: 100vh">
    <a-layout-header
      style="background: #fff; display: flex; align-items: center; gap: 24px; padding: 0 24px"
    >
      <span :style="{ color: theme.primary, fontWeight: 600, fontSize: 18 }">{{ theme.title }}</span>
      <a-menu mode="horizontal" :selected-keys="[$route.name as string]">
        <a-menu-item key="overview"><RouterLink to="/">账号概览</RouterLink></a-menu-item>
        <a-menu-item key="security"><RouterLink to="/security">安全设置</RouterLink></a-menu-item>
        <a-menu-item key="skin"><RouterLink to="/skin">皮肤管理</RouterLink></a-menu-item>
        <a-menu-item key="sessions"><RouterLink to="/sessions">登录设备</RouterLink></a-menu-item>
      </a-menu>
      <span style="flex: 1" />
      <span class="muted">{{ store.account?.username }}</span>
      <a-button type="link" @click="onLogout">退出</a-button>
    </a-layout-header>
    <a-layout-content>
      <RouterView />
    </a-layout-content>
  </a-layout>
</template>

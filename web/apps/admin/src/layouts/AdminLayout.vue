<script setup lang="ts">
// 后台外壳:侧边菜单 + 内容区。
//
// 菜单来自后端并已按权限过滤,这里不再重复过滤一次 ——
// 两处各写一遍规则,总有一处会漏更新。
import { computed } from 'vue'
import { useRoute, useRouter } from 'vue-router'

import { useAdminStore } from '../stores/admin'

const store = useAdminStore()
const route = useRoute()
const router = useRouter()

const selectedKeys = computed(() => [String(route.name)])

async function onLogout(): Promise<void> {
  await store.logout()
  await router.push({ name: 'login' })
}
</script>

<template>
  <a-layout style="min-height: 100vh">
    <a-layout-sider theme="light" width="220">
      <div style="padding: 16px; font-weight: 600; color: #3b6ea5; font-size: 16px">
        YggAuth 管理后台
      </div>
      <a-menu mode="inline" :selected-keys="selectedKeys">
        <a-menu-item v-for="item in store.visibleMenu" :key="item.key">
          <RouterLink :to="item.path ?? '/'">{{ item.title }}</RouterLink>
        </a-menu-item>
      </a-menu>
    </a-layout-sider>

    <a-layout>
      <a-layout-header style="background: #fff; display: flex; align-items: center; padding: 0 24px">
        <span style="font-weight: 600">{{ route.meta.title ?? '管理后台' }}</span>
        <span style="flex: 1" />
        <span class="muted" style="margin-right: 12px">{{ store.account?.username }}</span>
        <a-button type="link" @click="onLogout">退出</a-button>
      </a-layout-header>
      <a-layout-content>
        <RouterView />
      </a-layout-content>
    </a-layout>
  </a-layout>
</template>

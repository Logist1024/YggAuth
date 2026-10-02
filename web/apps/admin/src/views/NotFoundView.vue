<script setup lang="ts">
// 后台没有「未登录也能访问」的页面,所以 404 也要求会话。
// 守卫里对 not-found 跳过了登录检查,这里自己再兜一次。
import { onMounted } from 'vue'
import { useRouter } from 'vue-router'

import { useAdminStore } from '../stores/admin'

const store = useAdminStore()
const router = useRouter()

onMounted(async () => {
  if (!store.account) {
    await store.load()
  }
  if (!store.account) {
    await router.replace({ name: 'login' })
  }
})
</script>

<template>
  <div class="auth-shell">
    <a-result status="404" title="页面不存在" sub-title="你访问的地址没有对应的页面。">
      <template #extra>
        <RouterLink to="/dashboard">返回仪表盘</RouterLink>
      </template>
    </a-result>
  </div>
</template>

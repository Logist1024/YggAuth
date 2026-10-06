<script setup lang="ts">
// 未登录页面的外壳:居中一张卡片。
//
// 除了品牌与标题,这里还负责回答一个新用户最先冒出来的问题:
// 「这是个什么账号?注册了能干什么?」
// 只放一个表单的登录页,用户看不出自己该不该注册。
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
      <p class="auth-purpose">一个账号,同时登录内部业务系统与 Minecraft 服务器。</p>
      <!-- 过渡样式见 styles.css:登录 ↔ 注册 ↔ 找回密码来回跳时给点反馈 -->
      <RouterView v-slot="{ Component }">
        <Transition name="route">
          <component :is="Component" />
        </Transition>
      </RouterView>
    </div>
  </div>
</template>

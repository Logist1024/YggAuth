<script setup lang="ts">
// 已登录页面的外壳:顶部导航 + 内容区。
import { computed, ref } from 'vue'
import { useRouter } from 'vue-router'

import { errorBanner, type ErrorBanner } from '@yggauth/shared'

import { useSessionStore } from '../stores/session'

const store = useSessionStore()
const router = useRouter()
const theme = computed(() => store.theme)

const resendState = ref<'idle' | 'sending' | 'sent'>('idle')
const resendErr = ref<ErrorBanner | null>(null)

/**
 * 重发验证邮件。
 *
 * 端点在 requireAuth 组里 —— 这个提示条正好只在已登录且
 * email_verified 为假时出现(最常见的是刚换过邮箱)。注册后
 * 未验证的用户登录会被拦下,那条路径用不上这个接口。
 *
 * 后端无论是否真的发出邮件都返回成功(防枚举),所以这里
 * 只能报「已发送」,不能替后端判断是否真的有邮件在路上。
 */
async function resendVerification(): Promise<void> {
  resendErr.value = null
  resendState.value = 'sending'
  try {
    await store.api.post('/api/auth/email/resend', {})
    resendState.value = 'sent'
  } catch (err) {
    resendErr.value = errorBanner(err, '发送失败,请稍后再试')
    resendState.value = 'idle'
  }
}

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
        <a-menu-item key="audit"><RouterLink to="/audit">操作记录</RouterLink></a-menu-item>
      </a-menu>
      <span style="flex: 1" />
      <span class="muted">{{ store.account?.username }}</span>
      <a-button type="link" @click="onLogout">退出</a-button>
    </a-layout-header>

    <a-layout-content>
      <!-- 邮箱未验证:藏在「安全设置」里没人找得到,顶栏提示才拦得住 -->
      <div
        v-if="store.account && !store.account.email_verified"
        style="padding: 0 24px; background: #fffbe6; border-bottom: 1px solid #ffe58f"
      >
        <a-alert
          v-if="resendState === 'sent'"
          type="success"
          show-icon
          banner
          message="验证邮件已发送,请查收邮箱(没收到可以稍后再发一次)。"
          style="margin: 8px 0"
        />
        <a-alert
          v-else
          type="warning"
          show-icon
          banner
          style="margin: 8px 0"
        >
          <template #message>
            <a-space>
              <span>邮箱尚未验证,部分功能会受限。</span>
              <a-button
                size="small"
                type="link"
                :loading="resendState === 'sending'"
                @click="resendVerification"
              >
                重发验证邮件
              </a-button>
              <!-- 行内位只放标题:错误码与建议是横幅的第二、三行,
                   塞进这条 warning 里只会把提示挤成一团。 -->
              <span v-if="resendErr" class="error-text">{{ resendErr.title }}</span>
            </a-space>
          </template>
        </a-alert>
      </div>

      <!-- 过渡只包住内容区:顶栏是常驻的,跟着一起淡入会整屏闪一下 -->
      <RouterView v-slot="{ Component }">
        <Transition name="route">
          <component :is="Component" />
        </Transition>
      </RouterView>
    </a-layout-content>
  </a-layout>
</template>

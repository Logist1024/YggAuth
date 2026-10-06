<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRoute } from 'vue-router'

import { ApiError, ErrCode, errorBanner, bannerMessage, type ErrorBanner } from '@yggauth/shared'

import { useSessionStore } from '../stores/session'

const store = useSessionStore()
const route = useRoute()

const token = computed(() => (typeof route.query.token === 'string' ? route.query.token : ''))
const state = ref<'idle' | 'loading' | 'ok' | 'error'>('idle')
const message = ref<ErrorBanner | null>(null)
/**
 * 验证链接是**一次性**的:重复打开、或成功后刷新页面,第二次请求
 * 必然拿到「令牌无效」。这时说「请重新获取」是句没人做得到的话 ——
 * 待验证账号登不进去,重发接口又要求已登录。该说的是先去登录试试,
 * 真没验证过再找管理员。
 */
const staleLink = ref(false)

onMounted(async () => {
  if (!token.value) {
    state.value = 'error'
    message.value = bannerMessage('链接缺少验证令牌')
    return
  }

  state.value = 'loading'
  try {
    await store.api.post('/api/auth/email/verify', { token: token.value })
    state.value = 'ok'
  } catch (err) {
    state.value = 'error'
    message.value = errorBanner(err, '验证失败')
    staleLink.value = err instanceof ApiError && err.code === ErrCode.INVALID_TOKEN
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

  <!--
    结果页不能只报一句「验证失败」:用户此刻最想知道的是「然后怎么办」,
    所以把可操作的 hint 也放进来。槽位名是 subTitle(与 a-result 源码一致),
    写成 sub-title 会静默失效、只剩一个空副标题。
  -->
  <a-result v-else status="error" title="验证失败">
    <template #subTitle>
      <div>{{ message?.title }}</div>
      <div v-if="staleLink" style="margin-top: 8px">
        这个链接已经用过了(重复打开或刷新都会走到这里)。先去登录;如果仍提示邮箱尚未验证,请联系管理员处理。
      </div>
      <div v-else-if="message?.hint" style="margin-top: 8px">{{ message.hint }}</div>
    </template>
    <template #extra>
      <RouterLink to="/login">返回登录</RouterLink>
    </template>
  </a-result>
</template>

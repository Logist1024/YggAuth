<script setup lang="ts">
import { reactive, ref } from 'vue'

import { useRouter } from 'vue-router'

import { validatePassword, errorBanner, type ErrorBanner } from '@yggauth/shared'

import { useSessionStore } from '../stores/session'
import ErrorAlert from '../components/ErrorAlert.vue'

const store = useSessionStore()
const router = useRouter()

const pwd = reactive({ current: '', next: '', confirm: '' })
const errors = reactive({ current: '', next: '', confirm: '' })
const saving = ref(false)
const banner = ref<ErrorBanner | null>(null)

/**
 * 首登强制改密的目标页。
 *
 * 与「安全设置」里的改密表单是两件事:那一页是可选的自助操作,
 * 这一页是**闸门** —— 旗标置位期间,后端对白名单之外的请求一律回 20013,
 * 前端也只放行本页。所以它没有「跳过」,也没有通往别处的按钮:
 * 跳过一个根本走不过去的闸门,只会变成一次令人困惑的往返。
 */
async function onSubmit(): Promise<void> {
  // 防重入:成功后会吊销全部会话,重复提交只会连着清几次登录态、记几条审计。
  if (saving.value) return
  errors.current = pwd.current ? '' : '请输入当前密码'
  errors.next = validatePassword(pwd.next, store.policy).message
  errors.confirm = pwd.next === pwd.confirm ? '' : '两次输入的密码不一致'
  banner.value = null
  if (errors.current || errors.next || errors.confirm) {
    return
  }

  saving.value = true
  try {
    // 与安全设置同一个接口:PATCH /api/account/password。
    // 它在后端的放行名单里,首登强制改密期间也必须放行,否则用户
    // 将被自己锁在闸门这一侧 —— 这正是这个页面存在的理由。
    await store.api.patch('/api/account/password', {
      old_password: pwd.current,
      new_password: pwd.next,
    })
    pwd.current = ''
    pwd.next = ''
    pwd.confirm = ''
    // 改密后端会吊销全部会话并清 cookie,本地也必须跟着清:
    // 留着一个「看起来还登录着、实际每个请求都 401」的页面更糟。
    store.setAccount(null)
    store.setNotice({ type: 'success', text: '密码已修改,请用新密码重新登录。' })
    await router.push({ name: 'login' })
  } catch (err) {
    banner.value = errorBanner(err, '修改失败')
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <div class="page">
    <h2>修改密码</h2>

    <a-alert
      type="warning"
      show-icon
      message="首次登录必须先设置新密码"
      description="为了账号安全,设置新密码之前无法使用站内其他功能。设置完成后需要用新密码重新登录一次。"
      style="margin-bottom: 16px"
    />

    <a-card>
      <ErrorAlert :banner="banner" />

      <!-- 回车即提交:与登录页、安全设置保持同一交互 -->
      <a-form layout="vertical" @submit.prevent="onSubmit">
        <a-form-item label="当前密码" :validate-status="errors.current ? 'error' : ''" :help="errors.current">
          <a-input-password v-model:value="pwd.current" autocomplete="current-password" />
        </a-form-item>
        <a-form-item
          label="新密码"
          :validate-status="errors.next ? 'error' : ''"
          :help="errors.next || `至少 ${store.policy.password_min_length} 位`"
        >
          <a-input-password v-model:value="pwd.next" autocomplete="new-password" />
        </a-form-item>
        <a-form-item label="确认新密码" :validate-status="errors.confirm ? 'error' : ''" :help="errors.confirm">
          <a-input-password v-model:value="pwd.confirm" autocomplete="new-password" />
        </a-form-item>
        <div class="form-actions">
          <a-button type="primary" :loading="saving" html-type="submit" block>设置新密码</a-button>
        </div>
      </a-form>
    </a-card>
  </div>
</template>

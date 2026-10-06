<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'

import { useRouter } from 'vue-router'

import {
  errorBanner,
  FALLBACK_POLICY,
  validatePassword,
  type AccountPolicy,
  type ErrorBanner,
} from '@yggauth/shared'

import { useAdminStore } from '../stores/admin'
import ErrorAlert from '../components/ErrorAlert.vue'

const store = useAdminStore()
const router = useRouter()

const pwd = reactive({ current: '', next: '', confirm: '' })
const errors = reactive({ current: '', next: '', confirm: '' })
const saving = ref(false)
const banner = ref<ErrorBanner | null>(null)
/**
 * 密码策略。
 *
 * 拉不到就用与后端默认一致的兜底值 —— 拿不到规则**不该**把改密页卡死,
 * 用户照常填一个够长的密码即可;规则本身仍由后端最终裁决。
 */
const policy = ref<AccountPolicy>({ ...FALLBACK_POLICY })

onMounted(async () => {
  try {
    policy.value = { ...FALLBACK_POLICY, ...(await store.api.get<AccountPolicy>('/api/auth/policy')) }
  } catch {
    // 留在兜底值上,页面照常可用
  }
})

/**
 * 后台的首登强制改密页。
 *
 * 与账号站那一页是同一件事:旗标置位期间,后端对 /api/auth/* 与
 * /api/account* 之外的请求一律回 20013 —— 后台自己的 /api/admin/* 全在里面。
 * 所以这一页没有「跳过」,改完就必须重新登录。
 */
async function onSubmit(): Promise<void> {
  // 防重入:改密会吊销全部会话,重复提交只会连着清几次登录态、记几条审计。
  if (saving.value) return
  errors.current = pwd.current ? '' : '请输入当前密码'
  errors.next = validatePassword(pwd.next, policy.value).message
  errors.confirm = pwd.next === pwd.confirm ? '' : '两次输入的密码不一致'
  banner.value = null
  if (errors.current || errors.next || errors.confirm) {
    return
  }

  saving.value = true
  try {
    // 与账号站同一个接口,也在后端的放行名单里。
    await store.api.patch('/api/account/password', {
      old_password: pwd.current,
      new_password: pwd.next,
    })
    // 后端已吊销全部会话并清了 cookie,本地只做纯本地的清理 ——
    // 再发一次 /api/auth/logout 只会拿回一个 401。
    store.clearSession()
    store.notice = '密码已修改,请用新密码重新登录。'
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
      description="设置新密码之前,后台的管理功能均不可用。设置完成后需要用新密码重新登录一次。"
      style="margin-bottom: 16px"
    />

    <a-card>
      <ErrorAlert :banner="banner" />

      <!-- 回车即提交:与登录页保持同一交互 -->
      <a-form layout="vertical" @submit.prevent="onSubmit">
        <a-form-item label="当前密码" :validate-status="errors.current ? 'error' : ''" :help="errors.current">
          <a-input-password v-model:value="pwd.current" autocomplete="current-password" />
        </a-form-item>
        <a-form-item
          label="新密码"
          :validate-status="errors.next ? 'error' : ''"
          :help="errors.next || `至少 ${policy.password_min_length} 位`"
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

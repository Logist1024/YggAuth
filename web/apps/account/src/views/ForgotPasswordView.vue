<script setup lang="ts">
import { reactive, ref } from 'vue'

import { errorBanner, type ErrorBanner, validateEmail } from '@yggauth/shared'

import { useSessionStore } from '../stores/session'
import ErrorAlert from '../components/ErrorAlert.vue'

const store = useSessionStore()
const form = reactive({ email: '' })
/**
 * 邮箱字段的校验提示,就地挂在表单项下。
 *
 * 曾与请求错误共用同一个变量 —— 于是服务器 500 会被渲染成
 * 「邮箱」这项的错误,看上去像地址填错了。
 */
const fieldError = ref('')
/** 请求失败。单独一条横幅,因为它与输入内容无关。 */
const banner = ref<ErrorBanner | null>(null)
const submitting = ref(false)
const done = ref(false)

async function onSubmit(): Promise<void> {
  // 防重入:按钮的 loading 挡得住点击,挡不住回车连发 —— 重复提交会连发几封重置邮件。
  if (submitting.value) return
  banner.value = null
  fieldError.value = validateEmail(form.email).message
  if (fieldError.value) {
    return
  }

  submitting.value = true
  try {
    await store.api.post('/api/auth/password/forgot', { email: form.email })
    done.value = true
  } catch (err) {
    banner.value = errorBanner(err, '请求失败,请稍后再试')
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <a-result v-if="done" status="success" title="请查收邮件">
    <template #subTitle>
      如果这个邮箱在本站注册过,你会在几分钟内收到一封重置邮件。
    </template>
    <template #extra>
      <RouterLink to="/login">返回登录</RouterLink>
    </template>
  </a-result>

  <a-form v-else layout="vertical" @submit.prevent="onSubmit">
    <p class="muted" style="margin-bottom: 16px">
      填写注册邮箱,我们会发一封包含重置链接的邮件。
    </p>
    <ErrorAlert :banner="banner" />
    <a-form-item label="邮箱" :validate-status="fieldError ? 'error' : ''" :help="fieldError">
      <a-input v-model:value="form.email" type="email" autocomplete="email" />
    </a-form-item>
    <div class="form-actions">
      <a-button type="primary" :loading="submitting" html-type="submit" block>发送重置邮件</a-button>
    </div>
    <div style="margin-top: 16px; text-align: center">
      <RouterLink to="/login">返回登录</RouterLink>
    </div>
  </a-form>
</template>

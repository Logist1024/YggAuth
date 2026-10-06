<script setup lang="ts">
import { computed, reactive, ref } from 'vue'
import { useRouter } from 'vue-router'

import { validateEmail, validatePassword, validateUsername, errorBanner, type ErrorBanner } from '@yggauth/shared'

import { useSessionStore } from '../stores/session'
import ErrorAlert from '../components/ErrorAlert.vue'

const store = useSessionStore()
const router = useRouter()

const form = reactive({ username: '', email: '', password: '', confirm: '', inviteCode: '' })
const errors = reactive({ username: '', email: '', password: '', confirm: '', inviteCode: '' })
const submitting = ref(false)
const banner = ref<ErrorBanner | null>(null)
/** 注册请求已成功。与 verifyUrl 分开,后端不回链接时也停在这一页。 */
const done = ref(false)
const verifyUrl = ref('')

/**
 * 注册成功页上说什么 —— 由响应本身决定,前端不猜部署环境。
 *
 * 后端在注册这一刻就发验证邮件(internal/identity/account/service.go 的 Register),
 * 但**是否把链接一并回给调用方**取决于这封信投不投得出去:
 * - 带 verify_url:这个部署的邮件到不了收件人(console 传输),
 *   链接是用户唯一能走通的路,直接摆出来;
 * - 不带:邮件会真的寄到邮箱里(传输为 smtp),令牌只走邮件 ——
 *   同一份链接再在页面上展示一遍,等于把「证明你拥有这个邮箱」变成摆设。
 *
 * 早先这里写「请查收邮件」是假话(那时后端压根不发信),兜底文案是
 * 「联系管理员」;现在两个分支各自都是真的,不用再兜那种底。
 */
const successHint = computed(() =>
  verifyUrl.value
    ? '账号已创建,还差邮箱验证这一步。直接打开下面的链接即可完成验证。'
    : `账号已创建。验证邮件已发送到 ${form.email},请查收并点击其中的链接完成验证后再登录。`,
)

const registrationClosed = computed(() => store.policy.registration_mode === 'closed')
/**
 * 邀请码只在 invite_only 模式下出现。
 *
 * 后端 registerRequest 一直收 invite_code,前端此前却没有任何地方
 * 填它 —— 于是 invite_only 部署注册时永远撞在「邀请码必填」上,
 * 而页面上连个输入框都没有。
 */
const inviteRequired = computed(() => store.policy.registration_mode === 'invite_only')

function check(): boolean {
  errors.username = validateUsername(form.username, store.policy).message
  errors.email = validateEmail(form.email).message
  errors.password = validatePassword(form.password, store.policy).message
  errors.confirm = form.password === form.confirm ? '' : '两次输入的密码不一致'
  errors.inviteCode = inviteRequired.value && !form.inviteCode.trim() ? '请输入邀请码' : ''
  return (
    !errors.username &&
    !errors.email &&
    !errors.password &&
    !errors.confirm &&
    !errors.inviteCode
  )
}

async function onSubmit(): Promise<void> {
  // 防重入:按钮的 loading 挡得住点击,挡不住回车连发 —— 重复注册会再发一封验证邮件。
  if (submitting.value) return
  banner.value = null
  if (!check()) {
    return
  }

  submitting.value = true
  try {
    const data = await store.api.post<{ verify_url?: string }>('/api/auth/register', {
      username: form.username,
      email: form.email,
      password: form.password,
      // open 模式下传空串即可,不给后端一个「有码但没填」的歧义
      invite_code: form.inviteCode.trim(),
    })
    if (store.policy.require_email_verification) {
      done.value = true
      verifyUrl.value = data.verify_url ?? ''
    } else {
      await router.push({ name: 'login' })
    }
  } catch (err) {
    banner.value = errorBanner(err, '注册失败,请稍后再试')
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <a-result v-if="registrationClosed" status="warning" title="注册已关闭" sub-title="当前部署不接受自助注册,请联系管理员邀请。" />

  <a-result v-else-if="done" status="success" title="注册成功" :sub-title="successHint">
    <template #extra>
      <!-- 整站同源,直接跳转即可;打开新标签反而会留下一个停在原地的旧页面 -->
      <a-button v-if="verifyUrl" type="primary" :href="verifyUrl">打开验证链接</a-button>
      <a-button :type="verifyUrl ? 'default' : 'primary'" @click="router.push({ name: 'login' })">
        去登录
      </a-button>
    </template>
  </a-result>

  <a-form v-else layout="vertical" @submit.prevent="onSubmit">
    <ErrorAlert :banner="banner" />

    <a-form-item label="用户名" :validate-status="errors.username ? 'error' : ''" :help="errors.username">
      <a-input v-model:value="form.username" autocomplete="username" placeholder="字母、数字与下划线" />
    </a-form-item>

    <a-form-item label="邮箱" :validate-status="errors.email ? 'error' : ''" :help="errors.email">
      <a-input v-model:value="form.email" type="email" autocomplete="email" />
    </a-form-item>

    <a-form-item
      label="密码"
      :validate-status="errors.password ? 'error' : ''"
      :help="errors.password || `至少 ${store.policy.password_min_length} 位`"
    >
      <a-input-password v-model:value="form.password" autocomplete="new-password" />
    </a-form-item>

    <a-form-item label="确认密码" :validate-status="errors.confirm ? 'error' : ''" :help="errors.confirm">
      <a-input-password v-model:value="form.confirm" autocomplete="new-password" />
    </a-form-item>

    <a-form-item
      v-if="inviteRequired"
      label="邀请码"
      :validate-status="errors.inviteCode ? 'error' : ''"
      :help="errors.inviteCode || '当前部署仅接受持有邀请码的注册'"
    >
      <a-input v-model:value="form.inviteCode" placeholder="例如 ABCD-2345" />
    </a-form-item>

    <div class="form-actions">
      <a-button type="primary" :loading="submitting" html-type="submit" block>注册</a-button>
    </div>

    <div style="margin-top: 16px; text-align: center">
      已有账号?<RouterLink to="/login">去登录</RouterLink>
    </div>
  </a-form>
</template>

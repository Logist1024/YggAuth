<script setup lang="ts">
import { computed, reactive, ref } from 'vue'

import { useRouter } from 'vue-router'

import { validateEmail, validatePassword, validateUsername, errorBanner, type ErrorBanner } from '@yggauth/shared'

import { useSessionStore } from '../stores/session'
import ErrorAlert from '../components/ErrorAlert.vue'

const store = useSessionStore()
const router = useRouter()

const profile = reactive({ username: '', email: '' })
const profileErrors = reactive({ username: '', email: '' })
const profileSaved = ref('')
const profileErr = ref<ErrorBanner | null>(null)
const savingProfile = ref(false)

/**
 * 邮箱是不是真的被改了。
 *
 * 用 computed 而不是存布尔值:用户把地址改回去,提示要跟着消失;
 * 保存成功后 store.account 会被刷新成新地址,这个值自然回到 false,
 * 于是下方的成功提示接管,不会同时挂着两条。
 */
const emailChanged = computed(
  () => profile.email.trim().toLowerCase() !== (store.account?.email ?? '').trim().toLowerCase(),
)

/** 只在邮箱真被改动时才提醒后果 —— 只改用户名的用户不该被警告打扰。 */
const emailChangeHint = computed(() =>
  emailChanged.value
    ? '改邮箱需要重新验证:保存后会给新邮箱发一封验证邮件,并结束当前登录;完成验证后才能再次登录。'
    : '',
)

const pwd = reactive({ current: '', next: '', confirm: '' })
const pwdErrors = reactive({ current: '', next: '', confirm: '' })
const pwdErr = ref<ErrorBanner | null>(null)
const savingPwd = ref(false)

function initProfile(): void {
  profile.username = store.account?.username ?? ''
  profile.email = store.account?.email ?? ''
}
initProfile()

async function saveProfile(): Promise<void> {
  // 防重入:按钮的 loading 挡得住点击,挡不住回车连发。
  if (savingProfile.value) return
  profileErrors.username = validateUsername(profile.username, store.policy).message
  profileErrors.email = validateEmail(profile.email).message
  profileErr.value = null
  profileSaved.value = ''
  if (profileErrors.username || profileErrors.email) {
    return
  }

  savingProfile.value = true
  // 在请求发出之前记下「邮箱改没改」:成功路径里我们会把自己送回登录页,
  // 事后再判断就永远分不出这两种结局。
  const changedEmail = emailChanged.value
  try {
    await store.api.patch('/api/account/', {
      username: profile.username,
      email: profile.email,
    })

    if (changedEmail) {
      // 改邮箱会把状态打回「待验证」,后端从这一刻起拒绝该账号的
      // 每个请求(403 账号当前不可用)。接着调 loadAccount 只会抛错,
      // 把一次成功的保存演成红色的「保存失败」—— 所以像改密那样,
      // 主动交代清楚再把人送回登录页,而不是等请求一个个撞墙。
      store.setAccount(null)
      store.setNotice({ type: 'success', text: '邮箱已更换。验证邮件已发往新邮箱,完成验证后即可重新登录。' })
      await router.push({ name: 'login' })
      return
    }

    // 只改用户名时账号仍可读,刷新一次让概览页与本页同步。
    await store.loadAccount()
    profileSaved.value = '已保存。'
  } catch (err) {
    profileErr.value = errorBanner(err, '保存失败')
  } finally {
    savingProfile.value = false
  }
}

async function changePassword(): Promise<void> {
  // 防重入:改密会把全部设备踢下线,重复提交只会连着清几次登录态、记几条审计。
  if (savingPwd.value) return
  pwdErrors.current = pwd.current ? '' : '请输入当前密码'
  pwdErrors.next = validatePassword(pwd.next, store.policy).message
  pwdErrors.confirm = pwd.next === pwd.confirm ? '' : '两次输入的密码不一致'
  pwdErr.value = null
  if (pwdErrors.current || pwdErrors.next || pwdErrors.confirm) {
    return
  }

  savingPwd.value = true
  try {
    // 后端是 PATCH /api/account/password,字段名是 old_password / new_password。
    // 改密成功后后端会吊销全部会话并清掉 cookie,本地会话也要清掉,
    // 否则用户会停留在一个「看起来还登录着、实际每个请求都 401」的页面。
    await store.api.patch('/api/account/password', {
      old_password: pwd.current,
      new_password: pwd.next,
    })
    pwd.current = ''
    pwd.next = ''
    pwd.confirm = ''
    store.setAccount(null)
    // 提示跟着走到登录页:那边只看到「被登出了」,
    // 不给一句「密码已改、请用新密码登录」,用户会以为被踢出了。
    store.setNotice({ type: 'success', text: '密码已修改,全部设备已退出,请用新密码重新登录。' })
    await router.push({ name: 'login' })
  } catch (err) {
    pwdErr.value = errorBanner(err, '修改失败')
  } finally {
    savingPwd.value = false
  }
}
</script>

<template>
  <div class="page">
    <h2>安全设置</h2>
    <p class="muted" style="margin-bottom: 16px">
      在这里修改用户名、邮箱与密码。需要让某台设备退出登录时,去「登录设备」处理。
    </p>

    <a-card title="账号资料" style="margin-bottom: 16px">
      <ErrorAlert :banner="profileErr" />
      <a-alert v-if="profileSaved" type="success" :message="profileSaved" show-icon style="margin-bottom: 16px" />

      <!--
        两块表单都走「回车即提交」:登录/注册那边本来就是 html-type=submit,
        安全设置却只认点按钮 —— 打完字按回车是所有人的肌肉记忆。
        a-form 内部拿到原生提交后会 preventDefault 并 emit('submit'),
        接住它就等于「回车 = 保存」;不接的话回车敲下去毫无反应。
      -->
      <a-form layout="vertical" @submit.prevent="saveProfile">
        <a-form-item label="用户名" :validate-status="profileErrors.username ? 'error' : ''" :help="profileErrors.username">
          <a-input v-model:value="profile.username" autocomplete="username" />
        </a-form-item>
        <a-form-item
          label="邮箱"
          :validate-status="profileErrors.email ? 'error' : ''"
          :help="profileErrors.email || emailChangeHint"
        >
          <a-input v-model:value="profile.email" type="email" autocomplete="email" />
        </a-form-item>
        <a-button type="primary" :loading="savingProfile" html-type="submit">保存</a-button>
      </a-form>
    </a-card>

    <a-card title="修改密码">
      <ErrorAlert :banner="pwdErr" />

      <a-form layout="vertical" @submit.prevent="changePassword">
        <a-form-item label="当前密码" :validate-status="pwdErrors.current ? 'error' : ''" :help="pwdErrors.current">
          <a-input-password v-model:value="pwd.current" autocomplete="current-password" />
        </a-form-item>
        <a-form-item
          label="新密码"
          :validate-status="pwdErrors.next ? 'error' : ''"
          :help="pwdErrors.next || `至少 ${store.policy.password_min_length} 位`"
        >
          <a-input-password v-model:value="pwd.next" autocomplete="new-password" />
        </a-form-item>
        <a-form-item label="确认新密码" :validate-status="pwdErrors.confirm ? 'error' : ''" :help="pwdErrors.confirm">
          <a-input-password v-model:value="pwd.confirm" autocomplete="new-password" />
        </a-form-item>
        <a-button type="primary" :loading="savingPwd" html-type="submit">修改密码</a-button>
      </a-form>
    </a-card>
  </div>
</template>

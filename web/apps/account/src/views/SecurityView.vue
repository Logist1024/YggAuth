<script setup lang="ts">
import { reactive, ref } from 'vue'

import { ApiError, validateEmail, validatePassword, validateUsername } from '@yggauth/shared'

import { useSessionStore } from '../stores/session'

const store = useSessionStore()

const profile = reactive({ username: '', email: '' })
const profileErrors = reactive({ username: '', email: '' })
const profileSaved = ref('')
const profileErr = ref('')
const savingProfile = ref(false)

const pwd = reactive({ current: '', next: '', confirm: '' })
const pwdErrors = reactive({ current: '', next: '', confirm: '' })
const pwdSaved = ref('')
const pwdErr = ref('')
const savingPwd = ref(false)

function initProfile(): void {
  profile.username = store.account?.username ?? ''
  profile.email = store.account?.email ?? ''
}
initProfile()

async function saveProfile(): Promise<void> {
  profileErrors.username = validateUsername(profile.username, store.policy).message
  profileErrors.email = validateEmail(profile.email).message
  profileErr.value = ''
  profileSaved.value = ''
  if (profileErrors.username || profileErrors.email) {
    return
  }

  savingProfile.value = true
  try {
    const data = await store.api.patch<{ account: typeof store.account }>('/api/account/', {
      username: profile.username,
      email: profile.email,
    })
    store.setAccount(data.account)
    profileSaved.value = '已保存'
  } catch (err) {
    profileErr.value = err instanceof ApiError ? err.message : '保存失败'
  } finally {
    savingProfile.value = false
  }
}

async function changePassword(): Promise<void> {
  pwdErrors.current = pwd.current ? '' : '请输入当前密码'
  pwdErrors.next = validatePassword(pwd.next, store.policy).message
  pwdErrors.confirm = pwd.next === pwd.confirm ? '' : '两次输入的密码不一致'
  pwdErr.value = ''
  pwdSaved.value = ''
  if (pwdErrors.current || pwdErrors.next || pwdErrors.confirm) {
    return
  }

  savingPwd.value = true
  try {
    await store.api.post('/api/account/password', {
      current_password: pwd.current,
      new_password: pwd.next,
    })
    pwdSaved.value = '密码已修改,其它设备的会话已全部失效'
    pwd.current = ''
    pwd.next = ''
    pwd.confirm = ''
  } catch (err) {
    pwdErr.value = err instanceof ApiError ? err.message : '修改失败'
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
      <a-alert v-if="profileErr" type="error" :message="profileErr" show-icon style="margin-bottom: 16px" />
      <a-alert v-if="profileSaved" type="success" :message="profileSaved" show-icon style="margin-bottom: 16px" />

      <a-form layout="vertical">
        <a-form-item label="用户名" :validate-status="profileErrors.username ? 'error' : ''" :help="profileErrors.username">
          <a-input v-model:value="profile.username" />
        </a-form-item>
        <a-form-item label="邮箱" :validate-status="profileErrors.email ? 'error' : ''" :help="profileErrors.email">
          <a-input v-model:value="profile.email" type="email" />
        </a-form-item>
        <a-button type="primary" :loading="savingProfile" @click="saveProfile">保存</a-button>
      </a-form>
    </a-card>

    <a-card title="修改密码">
      <a-alert v-if="pwdErr" type="error" :message="pwdErr" show-icon style="margin-bottom: 16px" />
      <a-alert v-if="pwdSaved" type="success" :message="pwdSaved" show-icon style="margin-bottom: 16px" />

      <a-form layout="vertical">
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
        <a-button type="primary" :loading="savingPwd" @click="changePassword">修改密码</a-button>
      </a-form>
    </a-card>
  </div>
</template>

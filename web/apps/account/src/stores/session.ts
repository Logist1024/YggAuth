/**
 * 账号站的全局状态。
 *
 * 只放三样东西:会话、品牌主题、后端下发的校验规则。
 * 其余状态都留在各页面自己的组件里 —— 全局 store 越长,
 * 「A 页面改了 B 页面���的数据」这种问题越难查。
 */
import { defineStore } from 'pinia'
import { computed, ref } from 'vue'

import {
  ApiClient,
  ApiError,
  BRAND_THEMES,
  FALLBACK_POLICY,
  parseClientId,
  type AccountPolicy,
  type ClientId,
} from '@yggauth/shared'

export interface SessionAccount {
  id: string
  username: string
  email: string
  status: string
  email_verified: boolean
  mc_login_enabled: boolean
  created_at: string
}

export const useSessionStore = defineStore('session', () => {
  const clientId = ref<ClientId>(parseClientId(new URLSearchParams(location.search).get('client_id')))
  const account = ref<SessionAccount | null>(null)
  const loading = ref(false)
  /** 后端下发的校验规则;拉取失败时用兜底值。 */
  const policy = ref<AccountPolicy>({ ...FALLBACK_POLICY })

  const api = new ApiClient({
    // 401 时先试一次匿名刷新;失败就清空会话。
    // 不自动跳转:跳转由路由守卫统一处理,免得页面里到处写 location.href。
    onUnauthorized: async () => false,
  })

  const theme = computed(() => BRAND_THEMES[clientId.value])
  const isAuthenticated = computed(() => account.value !== null)

  /** 读取后端策略。规则只有一份来源:后端。 */
  async function loadPolicy(): Promise<void> {
    try {
      const data = await api.get<AccountPolicy>('/api/auth/policy')
      policy.value = { ...FALLBACK_POLICY, ...data }
    } catch (err) {
      // 拉不到就用兜底值继续,而不是把注册页卡死。
      // 兜底值与后端默认值一致,最坏情况是提示文案不准确,
      // 但用户仍然能注册成功 —— 这比整页不可用好得多。
      if (!(err instanceof ApiError)) {
        throw err
      }
    }
  }

  /** 拉取当前账号;未登录返回 null 而不是抛错。 */
  async function loadAccount(): Promise<SessionAccount | null> {
    loading.value = true
    try {
      const data = await api.get<{ account: SessionAccount }>('/api/account/')
      account.value = data.account
      return data.account
    } catch (err) {
      if (err instanceof ApiError && err.requiresLogin) {
        account.value = null
        return null
      }
      throw err
    } finally {
      loading.value = false
    }
  }

  function setAccount(next: SessionAccount | null): void {
    account.value = next
  }

  async function logout(): Promise<void> {
    try {
      await api.post('/api/auth/logout')
    } finally {
      // 无论后端返回什么,本地都要清干净:
      // 留着「看起来还登录着」的状态比登出失败更糟 ——
      // 用户会以为还在登录,其实每个请求都在 401。
      account.value = null
    }
  }

  return {
    clientId,
    account,
    loading,
    policy,
    api,
    theme,
    isAuthenticated,
    loadPolicy,
    loadAccount,
    setAccount,
    logout,
  }
})
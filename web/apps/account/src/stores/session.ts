/**
 * 账号站的全局状态。
 *
 * 只放四样东西:会话、品牌主题、后端下发的校验规则、跨页提示。
 * 其余状态都留在各页面自己的组件里 —— 全局 store 越长,
 * 「A 页面改了 B 页面里的数据」这种问题越难查。
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

/**
 * 后端 GET /api/account/ 返回的原始形态。
 *
 * MC 登录开关的字段名是 login_enabled(见 internal/identity/handler.go 的
 * toAccountView),前端内部统一叫 mc_login_enabled —— 在这里做一次映射,
 * 而不是让每个页面各自兼容。后端改名前端只改这一处。
 */
interface RawSessionAccount {
  id: string
  username: string
  email: string
  status: string
  email_verified: boolean
  login_enabled?: boolean
  mc_login_enabled?: boolean
  created_at: string
}

function normalizeAccount(raw: RawSessionAccount): SessionAccount {
  return {
    id: raw.id,
    username: raw.username,
    email: raw.email,
    status: raw.status,
    email_verified: raw.email_verified,
    mc_login_enabled: raw.mc_login_enabled ?? raw.login_enabled ?? false,
    created_at: raw.created_at,
  }
}

/**
 * 跨页的一次性提示,登录页展示一次就清掉。
 *
 * 用来回答「我为什么会站在登录页上」:改密、会话过期都会把人
 * 送回这里,而登录页本身没做错什么 —— 不给一句话,用户看到的
 * 就是「我莫名被踢出来了」,接着就会怀疑是不是号被盗了。
 */
export interface Notice {
  type: 'success' | 'info'
  text: string
}

export const useSessionStore = defineStore('session', () => {
  const clientId = ref<ClientId>(parseClientId(new URLSearchParams(location.search).get('client_id')))
  const account = ref<SessionAccount | null>(null)
  const loading = ref(false)
  /** 后端下发的校验规则;拉取失败时用兜底值。 */
  const policy = ref<AccountPolicy>({ ...FALLBACK_POLICY })
  const notice = ref<Notice | null>(null)
  /**
   * 会话要求先改密码(首登强制改密,后端 20013)。
   *
   * 真源在后端的凭据行,这里只是它的副本:登录后由 /api/account/ 带回,
   * 页面刷新也靠它恢复 —— 只认登录响应里那个字段的话,刷新一次就没了,
   * 而后端照样每个请求都拦,用户看到的就是「点什么都是红错」。
   */
  const mustChangePassword = ref(false)

  const api = new ApiClient({
    // 401 时先给一次恢复机会。账号站的会话是 HttpOnly cookie,
    // 前端没有可拿去刷新的凭据,所以恢复不了、直接返回 false ——
    // 跳转由路由守卫统一处理,免得页面里到处写 location.href。
    //
    // 只在「本以为还登录着」时才提示过期:刚打开站点、本来就没
    // 登录的人,给他一句「已过期」是凭空捏造事实。
    onUnauthorized: async () => {
      if (account.value) {
        notice.value = { type: 'info', text: '登录状态已过期,请重新登录。' }
      }
      return false
    },
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
      const data = await api.get<{ account: RawSessionAccount; must_change_password?: boolean }>('/api/account/')
      const normalized = normalizeAccount(data.account)
      account.value = normalized
      mustChangePassword.value = data.must_change_password === true
      return normalized
    } catch (err) {
      if (err instanceof ApiError && err.requiresLogin) {
        account.value = null
        mustChangePassword.value = false
        return null
      }
      throw err
    } finally {
      loading.value = false
    }
  }

  function setAccount(next: SessionAccount | null): void {
    account.value = next
    // 登出与「改密码后被动登出」都走这条:旗标跟着会话一起清,
    // 否则改完密码再登录,会因为一个早就不该存在的旗标被送回改密页。
    if (next === null) {
      mustChangePassword.value = false
    }
  }

  /** 记一条跨页提示;登录页展示后调 clearNotice 清掉。 */
  function setNotice(next: Notice): void {
    notice.value = next
  }

  function clearNotice(): void {
    notice.value = null
  }

  async function logout(): Promise<void> {
    try {
      await api.post('/api/auth/logout')
    } finally {
      // 无论后端返回什么,本地都要清干净:
      // 留着「看起来还登录着」的状态比登出失败更糟 ——
      // 用户会以为还在登录,其实每个请求都在 401。
      account.value = null
      mustChangePassword.value = false
      // 主动退出不需要解释,也不该把早先那句「已过期」带过去。
      notice.value = null
    }
  }

  return {
    clientId,
    account,
    loading,
    policy,
    notice,
    mustChangePassword,
    api,
    theme,
    isAuthenticated,
    loadPolicy,
    loadAccount,
    setAccount,
    setNotice,
    clearNotice,
    logout,
  }
})
/**
 * 管理后台的会话与权限。
 *
 * 关键点:菜单**由后端下发**,前端只按权限点过滤。
 * 把菜单写死在前端意味着「加一个功能要改两个地方」,
 * 而漏改的那个地方会以「管理员看不到新功能」的形式出现 ——
 * 没人会把它当成 bug。
 */
import { defineStore } from 'pinia'
import { computed, ref } from 'vue'

import { ApiClient, ApiError, can } from '@yggauth/shared'

export interface AdminAccount {
  id: string
  username: string
  email: string
  status: string
  email_verified: boolean
  mc_login_enabled: boolean
  created_at: string
}

export interface MenuItem {
  key: string
  title: string
  path?: string
  icon?: string
  /** 需要的权限点;留空表示所有登录管理员可见。 */
  permission?: string
  /** 侧边栏分组标题。留空表示不分组,由界面置顶。 */
  group?: string
  children?: MenuItem[]
}

export const useAdminStore = defineStore('admin', () => {
  const account = ref<AdminAccount | null>(null)
  const permissions = ref<string[]>([])
  const menu = ref<MenuItem[]>([])
  const loading = ref(false)
  /**
   * 登录页顶部的一次性提示。
   *
   * 会话过期时后台会把人送回登录页 —— 不给一句话,管理员看到的
   * 就是「点着点着就被踢出来了」。只在「本以为还登录着」时才写,
   * 否则刚打开站点、本来就没登录的人也会看到一句「已过期」。
   */
  const notice = ref<string | null>(null)
  /**
   * 会话要求先改密码(首登强制改密,后端 20013)。
   *
   * 置位期间后台的 `/api/admin/*` 全部会被后端挡下,所以 load()
   * 会**先**问一个放行名单里的接口拿到这个旗标,再决定要不要去拉
   * 权限与菜单 —— 顺序反了,首屏就是一屏红错而不是改密页。
   */
  const mustChangePassword = ref(false)

  const api = new ApiClient({
    // 与账号站一致:会话是 HttpOnly cookie,前端没有可拿去刷新的
    // 凭据,恢复不了就返回 false,跳转交给路由守卫。
    onUnauthorized: async () => {
      if (account.value) {
        notice.value = '登录状态已过期,请重新登录。'
      }
      return false
    },
  })

  const isAuthenticated = computed(() => account.value !== null)

  /** 当前主体是否持有某权限点。与 Go 侧 rbac.Can 语义一致。 */
  function has(permission: string): boolean {
    return can(permissions.value, permission)
  }

  /** 可见菜单:后端下发什么就显示什么,再按本地权限点过滤。 */
  const visibleMenu = computed(() => {
    const out: MenuItem[] = []
    for (const item of menu.value) {
      if (item.children && item.children.length > 0) {
        const children = item.children.filter(
          (c) => !c.permission || can(permissions.value, c.permission),
        )
        if (children.length > 0) {
          out.push({ ...item, children })
        }
        continue
      }
      if (!item.permission || can(permissions.value, item.permission)) {
        out.push(item)
      }
    }
    return out
  })

  /**
   * 拉取当前管理员的账号、权限与菜单。
   *
   * 三者一次取齐:分开取会让首屏出现「菜单闪一下再变化」,
   * 而那正好是「这个管理员到底能不能看这一项」最容易被误解的时刻。
   */
  async function load(): Promise<void> {
    loading.value = true
    try {
      // 第一步只问 /api/auth/session:它在首登强制改密的放行名单里。
      // 直接并进下面那次 Promise.all 的话,旗标一置位这两个请求就会
      // 撞上 20013,整次 load 以红错告终 —— 而正确结果只是「去改密页」。
      const session = await api.get<{
        // 与 /api/admin/me 的 account 同形(后端只给这三个字段),
        // 所以类型仍按 AdminAccount 走,这里不再另造一份。
        account: AdminAccount
        permissions: string[]
        must_change_password?: boolean
      }>('/api/auth/session')
      mustChangePassword.value = session.must_change_password === true
      account.value = session.account
      if (mustChangePassword.value) {
        // 改密之前不拉权限与菜单:它们都在 /api/admin/* 下,后端会挡;
        // 改密成功会重新走一遍 load,到时候再取。
        permissions.value = []
        menu.value = []
        return
      }

      // 一次取齐:账号、权限、菜单。分三次取会让首屏出现
      // 「菜单闪一下再变化」,而那正好是管理员判断
      // 「我到底能不能看这一项」最容易被误导的时刻。
      const [me, menuRes] = await Promise.all([
        api.get<{
          account: AdminAccount
          permissions: string[]
        }>('/api/admin/me'),
        api.get<{ menus: MenuItem[] }>('/api/admin/menus'),
      ])
      account.value = me.account
      permissions.value = me.permissions ?? []
      menu.value = menuRes.menus ?? []
    } catch (err) {
      if (err instanceof ApiError && err.requiresLogin) {
        account.value = null
        return
      }
      throw err
    } finally {
      loading.value = false
    }
  }

  /**
   * 本地清会话。
   *
   * 改密码之后后端已经吊销了全部会话并清了 cookie,这里再发一次
   * /api/auth/logout 只会拿回 401 —— 所以「退出」与「改密后的被动
   * 下线」共用这个纯本地动作,真正的登出请求只在 logout() 里发。
   */
  function clearSession(): void {
    account.value = null
    permissions.value = []
    menu.value = []
    mustChangePassword.value = false
  }

  async function logout(): Promise<void> {
    try {
      await api.post('/api/auth/logout')
    } finally {
      clearSession()
      // 主动退出不需要解释,也别把早先那句「已过期」带过去。
      notice.value = null
    }
  }

  function clearNotice(): void {
    notice.value = null
  }

  return {
    account,
    permissions,
    menu,
    loading,
    notice,
    mustChangePassword,
    api,
    isAuthenticated,
    visibleMenu,
    has,
    load,
    clearSession,
    clearNotice,
    logout,
  }
})
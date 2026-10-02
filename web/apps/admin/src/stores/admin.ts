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
  children?: MenuItem[]
}

export const useAdminStore = defineStore('admin', () => {
  const account = ref<AdminAccount | null>(null)
  const permissions = ref<string[]>([])
  const menu = ref<MenuItem[]>([])
  const loading = ref(false)

  const api = new ApiClient({
    onUnauthorized: async () => false,
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

  async function logout(): Promise<void> {
    try {
      await api.post('/api/auth/logout')
    } finally {
      account.value = null
      permissions.value = []
      menu.value = []
    }
  }

  return {
    account,
    permissions,
    menu,
    loading,
    api,
    isAuthenticated,
    visibleMenu,
    has,
    load,
    logout,
  }
})
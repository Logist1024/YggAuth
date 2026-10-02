/**
 * 管理后台路由。
 *
 * 每条受保护的路由都带 meta.permission。全局守卫会用它拦下
 * **直接输 URL** 的访问 —— 只隐藏菜单是不够的:
 * 菜单是给人看的,URL 是给机器用的,而管理员既是人也是机器。
 *
 * 这不是安全边界。后端的 httpx.RequirePermission 才是。
 * 前端这道只是让越权访问表现为「友好地跳回首页」,
 * 而不是「页面打开了然后每个请求都 403」。
 */
import { createRouter, createWebHistory, type RouteRecordRaw } from 'vue-router'

import { useAdminStore } from './stores/admin'

declare module 'vue-router' {
  interface RouteMeta {
    /** 访问该路由所需的权限点。 */
    permission?: string
    /** 是否需要登录。 */
    requireAuth?: boolean
    title?: string
  }
}

const routes: RouteRecordRaw[] = [
  { path: '/login', name: 'login', component: () => import('./views/LoginView.vue') },
  {
    path: '/',
    component: () => import('./layouts/AdminLayout.vue'),
    meta: { requireAuth: true },
    children: [
      {
        path: 'dashboard',
        name: 'dashboard',
        component: () => import('./views/DashboardView.vue'),
        meta: { title: '仪表盘' },
      },
      {
        path: 'accounts',
        name: 'accounts',
        component: () => import('./views/AccountsView.vue'),
        meta: { permission: 'account:read', title: '账号管理' },
      },
      {
        path: 'roles',
        name: 'roles',
        component: () => import('./views/RolesView.vue'),
        meta: { permission: 'rbac:read', title: '角色权限' },
      },
      {
        path: 'invitations',
        name: 'invitations',
        component: () => import('./views/InvitationsView.vue'),
        meta: { permission: 'rbac:write', title: '邀请管理' },
      },
      {
        path: 'audit',
        name: 'audit',
        component: () => import('./views/AuditView.vue'),
        meta: { permission: 'audit:read', title: '审计日志' },
      },
      {
        path: 'clients',
        name: 'clients',
        component: () => import('./views/ClientsView.vue'),
        meta: { permission: 'oidc:client:read', title: 'OIDC 客户端' },
      },
      {
        path: 'keys',
        name: 'keys',
        component: () => import('./views/SigningKeysView.vue'),
        meta: { permission: 'oidc:client:read', title: '签名密钥' },
      },
      {
        path: 'mc/profiles',
        name: 'mc-profiles',
        component: () => import('./views/MCProfilesView.vue'),
        meta: { permission: 'minecraft:profile:read', title: '玩家档案' },
      },
      {
        path: 'mc/textures',
        name: 'mc-textures',
        component: () => import('./views/MCTexturesView.vue'),
        meta: { permission: 'minecraft:texture:read', title: '材质库' },
      },
      {
        path: 'settings',
        name: 'settings',
        component: () => import('./views/SettingsView.vue'),
        meta: { permission: 'setting:read', title: '应用配置' },
      },
    ],
  },
  { path: '/:pathMatch(.*)*', name: 'not-found', component: () => import('./views/NotFoundView.vue') },
]

export const router = createRouter({
  history: createWebHistory(),
  routes,
})

router.beforeEach(async (to) => {
  // 登录页本身不需要会话。
  if (!to.meta.requireAuth && to.name !== 'not-found') {
    return true
  }

  const store = useAdminStore()
  if (!store.account) {
    await store.load()
  }
  if (!store.account) {
    return { name: 'login', query: { redirect: to.fullPath } }
  }

  const required = to.meta.permission
  if (required && !store.has(required)) {
    // 跳到仪表盘而不是 403 页:管理员多半是点错了链接,
    // 给一个「回到首页」比给一堵墙有用。
    return { name: 'dashboard' }
  }

  if (to.meta.title) {
    document.title = `${to.meta.title} · YggAuth 管理后台`
  }
  return true
})
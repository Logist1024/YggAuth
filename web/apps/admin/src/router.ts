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

import { loadPublicConfig, publicConfig } from '@yggauth/shared'

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
  // 登录页单独成路由(不在 AdminLayout 里),所以它自己带 title ——
  // 不带的话标签页会一直停在 index.html 的默认标题。
  { path: '/login', name: 'login', meta: { title: '登录' }, component: () => import('./views/LoginView.vue') },
  {
    path: '/',
    component: () => import('./layouts/AdminLayout.vue'),
    meta: { requireAuth: true },
    children: [
      // /admin/ 空路径直接进仪表盘:没有这条,访问根路径会落到空布局白屏。
      { path: '', redirect: { name: 'dashboard' } },
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
      // 首登强制改密的目标页。**不带 permission** —— 它是给每一个
      // 被要求改密的人的,而那会儿权限与菜单都还没拉(去拉也会被挡)。
      {
        path: 'password/change',
        name: 'password-change',
        component: () => import('./views/ChangePasswordView.vue'),
        meta: { title: '修改密码' },
      },
    ],
  },
  {
    path: '/:pathMatch(.*)*',
    name: 'not-found',
    meta: { title: '页面不存在' },
    component: () => import('./views/NotFoundView.vue'),
  },
]

export const router = createRouter({
  // 必须与 Vite 的 base、以及 Go 侧的挂载前缀三者一致。
  // 少了这一项,浏览器地址栏是 /admin/dashboard 而路由按 /dashboard 匹配,
  // 结果是每条路由都落到 not-found。
  history: createWebHistory('/admin'),
  routes,
  /**
   * 导航后滚到哪。
   *
   * 少了它,在长列表(审计日志、账号管理)翻页或切页时,
   * 浏览器会把新页面停在原来的滚动位置 —— 用户看到的是
   * 「点了没反应」或者直接是页面中段。前进/后退则还原原位,
   * 否则「退回上一页」还得重新往下翻。
   */
  scrollBehavior(_to, _from, savedPosition) {
    return savedPosition ?? { top: 0 }
  },
})

router.beforeEach(async (to) => {
  // 站点名称是后台可改的设置:首次导航拉一次公开配置(之后命中缓存)。
  await loadPublicConfig()

  // 先设标签页标题:下面有提前 return(未登录跳转),
  // 放在末尾会让登录页停在 index.html 的默认标题。
  // 站点名来自**设置**而不是写死的字符串 —— 否则「站点名称」这项设置对 <title> 无效。
  if (to.meta.title) {
    document.title = `${to.meta.title} · ${publicConfig().site_name} 管理后台`
  }

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

  // 首登强制改密:旗标置位时,除改密页本身外一律送去改密页。
  //
  // 必须排在权限判断之前:改密期间 permissions 是空的,不先拦这一下,
  // 管理员会先收到一句「你没有查看 XX 的权限」,而真正的原因是
  // 他还没改密码。
  if (store.mustChangePassword && to.name !== 'password-change') {
    return { name: 'password-change', query: { redirect: to.fullPath } }
  }

  const required = to.meta.permission
  if (required && !store.has(required)) {
    // 跳到仪表盘而不是 403 页:管理员多半是点错了链接,
    // 给一个「回到首页」比给一堵墙有用。
    //
    // 但不能一声不吭 —— 静默跳转在用户眼里就是「点了菜单没反应」,
    // 于是再点一次、再点一次。原因写进 store.notice,由 AdminLayout
    // 在内容区顶部渲染(与登录页那条同源),用户点 × 关掉即可。
    store.notice = `你没有查看「${to.meta.title ?? required}」的权限,已回到仪表盘。`
    return { name: 'dashboard' }
  }

  return true
})
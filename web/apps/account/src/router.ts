/**
 * 账号站的路由。
 *
 * meta.requireAuth 的页面由全局守卫保护。守卫的判断依据是
 * 「会话里有没有账号」,而不是某个本地布尔值 —— 后者一旦被
 * 组件写错,就会出现「明明已登录却被踢回登录页」或反过来。
 */
import { createRouter, createWebHistory, type RouteRecordRaw } from 'vue-router'

declare module 'vue-router' {
  interface RouteMeta {
    /** 浏览器标签页与页面标题条使用,缺了它会一直显示默认标题。 */
    title?: string
    /** 是否需要登录。 */
    requireAuth?: boolean
  }
}

import { ApiError, ErrCode } from '@yggauth/shared'

import { useSessionStore } from './stores/session'
import AuthLayout from './layouts/AuthLayout.vue'
import AppLayout from './layouts/AppLayout.vue'

const routes: RouteRecordRaw[] = [
  {
    path: '/login',
    component: AuthLayout,
    children: [{ path: '', name: 'login', meta: { title: '登录' }, component: () => import('./views/LoginView.vue') }],
  },
  {
    path: '/register',
    component: AuthLayout,
    children: [{ path: '', name: 'register', meta: { title: '注册账号' }, component: () => import('./views/RegisterView.vue') }],
  },
  {
    path: '/forgot-password',
    component: AuthLayout,
    children: [
      { path: '', name: 'forgot-password', meta: { title: '找回密码' }, component: () => import('./views/ForgotPasswordView.vue') },
    ],
  },
  {
    path: '/reset-password',
    component: AuthLayout,
    children: [
      { path: '', name: 'reset-password', meta: { title: '重置密码' }, component: () => import('./views/ResetPasswordView.vue') },
    ],
  },
  {
    path: '/verify-email',
    component: AuthLayout,
    children: [
      { path: '', name: 'verify-email', meta: { title: '验证邮箱' }, component: () => import('./views/VerifyEmailView.vue') },
    ],
  },
  {
    path: '/',
    component: AppLayout,
    meta: { requireAuth: true },
    children: [
      { path: '', name: 'overview', meta: { title: '账号概览' }, component: () => import('./views/OverviewView.vue') },
      { path: 'security', name: 'security', meta: { title: '安全设置' }, component: () => import('./views/SecurityView.vue') },
      { path: 'skin', name: 'skin', meta: { title: '皮肤管理' }, component: () => import('./views/SkinView.vue') },
      { path: 'sessions', name: 'sessions', meta: { title: '登录设备' }, component: () => import('./views/SessionsView.vue') },
      { path: 'audit', name: 'audit', meta: { title: '操作记录' }, component: () => import('./views/AuditView.vue') },
    ],
  },
  {
    path: '/:pathMatch(.*)*',
    name: 'not-found', meta: { title: '页面不存在' },
    component: () => import('./views/NotFoundView.vue'),
  },
]

export const router = createRouter({
  history: createWebHistory(),
  routes,
  /**
   * 导航后滚到哪。
   *
   * 少了它,在长页面(操作记录翻到第 5 页)点顶栏菜单换页时,
   * 浏览器会把新页面也停在原来的滚动位置 —— 用户看到的是
   * 「点了没反应」或者直接是页面中段。前进/后退则还原原位,
   * 否则「退回上一页」还得重新往下翻。
   */
  scrollBehavior(_to, _from, savedPosition) {
    return savedPosition ?? { top: 0 }
  },
})

router.beforeEach(async (to) => {
  // 先设标签页标题:下面有两处提前 return(未登录跳转、无需登录的公开页),
  // 放在末尾会让登录页、验证页、404 一直停在 index.html 的默认标题。
  if (to.meta.title) {
    document.title = `${to.meta.title} · YggAuth`
  }

  const store = useSessionStore()

  if (!store.policy) {
    await store.loadPolicy()
  }

  if (!to.meta.requireAuth) {
    return true
  }

  // 只在「还不知道自己是否登录」时才去问一次后端。
  // 每次导航都问会让每个页面切换都多一个往返。
  if (!store.account) {
    try {
      await store.loadAccount()
    } catch (err) {
      // 账号当前不可用(邮箱待验证、被停用)时后端回 403,
      // loadAccount 只把「会话没了」当成未登录,这种错误会抛出来。
      // 沿着它继续走,用户得到的是一屏报错;而「改完邮箱按浏览器后退
      // 回安全设置」就正好落在这里 —— 直接送回登录页并说明原因。
      if (err instanceof ApiError && err.code === ErrCode.ACCOUNT_DISABLED) {
        store.setNotice({ type: 'info', text: '账号暂时不可登录:请先完成邮箱验证,或联系管理员。' })
        return { name: 'login', query: { redirect: to.fullPath } }
      }
      throw err
    }
  }

  if (!store.account) {
    return { name: 'login', query: { redirect: to.fullPath } }
  }
  return true
})
/**
 * 账号站的路由。
 *
 * meta.requireAuth 的页面由全局守卫保护。守卫的判断依据是
 * 「会话里有没有账号」,而不是某个本地布尔值 —— 后者一旦被
 * 组件写错,就会出现「明明已登录却被踢回登录页」或反过来。
 */
import { createRouter, createWebHistory, type RouteRecordRaw } from 'vue-router'

import { useSessionStore } from './stores/session'
import AuthLayout from './layouts/AuthLayout.vue'
import AppLayout from './layouts/AppLayout.vue'

const routes: RouteRecordRaw[] = [
  {
    path: '/login',
    component: AuthLayout,
    children: [{ path: '', name: 'login', component: () => import('./views/LoginView.vue') }],
  },
  {
    path: '/register',
    component: AuthLayout,
    children: [{ path: '', name: 'register', component: () => import('./views/RegisterView.vue') }],
  },
  {
    path: '/forgot-password',
    component: AuthLayout,
    children: [
      { path: '', name: 'forgot-password', component: () => import('./views/ForgotPasswordView.vue') },
    ],
  },
  {
    path: '/reset-password',
    component: AuthLayout,
    children: [
      { path: '', name: 'reset-password', component: () => import('./views/ResetPasswordView.vue') },
    ],
  },
  {
    path: '/verify-email',
    component: AuthLayout,
    children: [
      { path: '', name: 'verify-email', component: () => import('./views/VerifyEmailView.vue') },
    ],
  },
  {
    path: '/',
    component: AppLayout,
    meta: { requireAuth: true },
    children: [
      { path: '', name: 'overview', component: () => import('./views/OverviewView.vue') },
      { path: 'security', name: 'security', component: () => import('./views/SecurityView.vue') },
      { path: 'skin', name: 'skin', component: () => import('./views/SkinView.vue') },
      { path: 'sessions', name: 'sessions', component: () => import('./views/SessionsView.vue') },
    ],
  },
  {
    path: '/:pathMatch(.*)*',
    name: 'not-found',
    component: () => import('./views/NotFoundView.vue'),
  },
]

export const router = createRouter({
  history: createWebHistory(),
  routes,
})

router.beforeEach(async (to) => {
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
    await store.loadAccount()
  }

  if (!store.account) {
    return { name: 'login', query: { redirect: to.fullPath } }
  }
  return true
})
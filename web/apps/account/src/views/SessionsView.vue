<script setup lang="ts">
import { onMounted, ref } from 'vue'

import { ApiError } from '@yggauth/shared'

import { useSessionStore } from '../stores/session'

interface SessionItem {
  id: string
  ip: string
  user_agent: string
  created_at: string
  last_seen_at: string
  current: boolean
}

const store = useSessionStore()
const items = ref<SessionItem[]>([])
const loading = ref(false)
const banner = ref('')

onMounted(load)

async function load(): Promise<void> {
  loading.value = true
  try {
    // 键名是 sessions,不是 items —— 读错键不会报错,
    // 只会安静地渲染成一张空表,看起来像「没有登录设备」。
    const data = await store.api.get<{ sessions: SessionItem[] }>('/api/account/sessions')
    items.value = data.sessions ?? []
  } catch (err) {
    banner.value = err instanceof ApiError ? err.message : '查询失败'
  } finally {
    loading.value = false
  }
}

async function revoke(id: string): Promise<void> {
  banner.value = ''
  try {
    await store.api.delete(`/api/account/sessions/${id}`)
    await load()
  } catch (err) {
    banner.value = err instanceof ApiError ? err.message : '吊销失败'
  }
}

async function logoutAll(): Promise<void> {
  banner.value = ''
  try {
    await store.api.post('/api/auth/logout-all')
    await store.logout()
    location.href = '/login'
  } catch (err) {
    banner.value = err instanceof ApiError ? err.message : '操作失败'
  }
}
</script>

<template>
  <div class="page">
    <h2>登录设备</h2>
    <p class="muted" style="margin-bottom: 16px">
      这里列出所有保持登录状态的设备。发现不认识的设备时,让它退出并顺手改一次密码。
    </p>
    <a-alert v-if="banner" type="error" :message="banner" show-icon style="margin-bottom: 16px" />

    <a-spin :spinning="loading">
      <a-list bordered :data-source="items">
        <template #renderItem="{ item }">
          <a-list-item>
            <a-list-item-meta>
              <template #title>
                {{ item.user_agent || '未知设备' }}
                <a-tag v-if="item.current" color="green">当前设备</a-tag>
              </template>
              <template #description>
                {{ item.ip }} · 最近活动 {{ new Date(item.last_seen_at).toLocaleString() }}
              </template>
            </a-list-item-meta>
            <template #actions>
              <a-popconfirm title="确定要在这台设备上退出登录?" @confirm="revoke(item.id)">
                <a-button v-if="!item.current" danger size="small">退出</a-button>
              </a-popconfirm>
            </template>
          </a-list-item>
        </template>
        <template #emptyText>
          <a-empty>
            <template #description>
              <div>没有查询到登录设备</div>
              <div class="muted" style="margin-top: 4px">
                正常情况下至少应包含当前这一台,刷新试试。
              </div>
            </template>
          </a-empty>
        </template>
      </a-list>
    </a-spin>

    <a-popconfirm title="将退出所有设备(包括当前设备),确定?" @confirm="logoutAll">
      <a-button danger style="margin-top: 16px">退出全部设备</a-button>
    </a-popconfirm>
  </div>
</template>

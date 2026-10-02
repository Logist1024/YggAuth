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
    const data = await store.api.get<{ items: SessionItem[] }>('/api/account/sessions')
    items.value = data.items
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
      </a-list>
    </a-spin>

    <a-popconfirm title="将退出所有设备(包括当前设备),确定?" @confirm="logoutAll">
      <a-button danger style="margin-top: 16px">退出全部设备</a-button>
    </a-popconfirm>
  </div>
</template>

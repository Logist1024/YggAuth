<script setup lang="ts">
import { onMounted, ref } from 'vue'

import { ApiError } from '@yggauth/shared'

import { useAdminStore } from '../stores/admin'

interface SettingItem {
  key: string
  value: string
  description: string
  updated_at: string
}

const store = useAdminStore()
const items = ref<SettingItem[]>([])
const drafts = ref<Record<string, string>>({})
const loading = ref(false)
const banner = ref('')
const saved = ref('')

onMounted(load)

async function load(): Promise<void> {
  loading.value = true
  try {
    const data = await store.api.get<{ settings: SettingItem[] }>('/api/admin/settings')
    items.value = data.settings
    drafts.value = Object.fromEntries(data.settings.map((s) => [s.key, s.value]))
  } catch (err) {
    banner.value = err instanceof ApiError ? err.message : '查询失败'
  } finally {
    loading.value = false
  }
}

async function save(key: string): Promise<void> {
  banner.value = ''
  saved.value = ''
  try {
    await store.api.patch('/api/admin/settings', { key, value: drafts.value[key] })
    saved.value = `${key} 已保存`
    await load()
  } catch (err) {
    banner.value = err instanceof ApiError ? err.message : '保存失败'
  }
}
</script>

<template>
  <div class="page">
    <a-alert v-if="banner" type="error" :message="banner" show-icon style="margin-bottom: 16px" />
    <a-alert v-if="saved" type="success" :message="saved" show-icon style="margin-bottom: 16px" />

    <a-spin :spinning="loading">
      <a-card v-for="item in items" :key="item.key" :title="item.key" style="margin-bottom: 16px">
        <template v-if="item.description">
          <p class="muted">{{ item.description }}</p>
        </template>
        <a-space style="width: 100%">
          <a-input v-model:value="drafts[item.key]" />
          <a-button type="primary" @click="save(item.key)">保存</a-button>
        </a-space>
        <p class="muted" style="margin-top: 8px">
          最后更新:{{ item.updated_at ? new Date(item.updated_at).toLocaleString() : '-' }}
        </p>
      </a-card>
    </a-spin>
  </div>
</template>

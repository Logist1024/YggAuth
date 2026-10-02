<script setup lang="ts">
import { onMounted, ref } from 'vue'

import { ApiError } from '@yggauth/shared'

import { useAdminStore } from '../stores/admin'

interface MCProfile {
  id: string
  uuid: string
  name: string
  created_at: string
}

const store = useAdminStore()
const items = ref<MCProfile[]>([])
const loading = ref(false)
const banner = ref('')
const search = ref('')

onMounted(load)

async function load(): Promise<void> {
  loading.value = true
  banner.value = ''
  try {
    const params = search.value ? `?search=${encodeURIComponent(search.value)}` : ''
    // 后端目前没有 MC 档案列表端点;没有就如实提示,
    // 而不是让页面白着或者在前端假装有数据。
    const data = await store.api
      .get<{ items: MCProfile[] }>(`/api/admin/mc/profiles${params}`)
      .catch((err: unknown) => {
        if (err instanceof ApiError) {
          banner.value = err.message
        }
        return { items: [] as MCProfile[] }
      })
    items.value = data.items
  } finally {
    loading.value = false
  }
}
</script>

<template>
  <div class="page">
    <a-alert v-if="banner" type="warning" :message="banner" show-icon style="margin-bottom: 16px" />

    <a-card title="玩家档案">
      <a-space style="margin-bottom: 16px">
        <a-input-search
          v-model:value="search"
          placeholder="按玩家名或 UUID 搜索"
          style="width: 300px"
          @search="load"
        />
      </a-space>

      <a-spin :spinning="loading">
        <a-table :data-source="items" row-key="uuid" :pagination="{ pageSize: 20 }">
          <a-table-column key="name" title="玩家名" />
          <a-table-column key="uuid" title="UUID">
            <template #default="{ record }"><code>{{ record.uuid }}</code></template>
          </a-table-column>
          <a-table-column key="created_at" title="创建时间">
            <template #default="{ record }">{{ new Date(record.created_at).toLocaleString() }}</template>
          </a-table-column>
          <a-table-column key="avatar" title="头像">
            <template #default="{ record }">
              <img :src="`/mc/avatar/${record.uuid}?size=32`" :alt="record.name" width="32" height="32" />
            </template>
          </a-table-column>
        </a-table>
      </a-spin>
    </a-card>
  </div>
</template>

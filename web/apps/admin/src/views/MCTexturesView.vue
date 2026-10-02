<script setup lang="ts">
import { onMounted, ref } from 'vue'

import { ApiError } from '@yggauth/shared'

import { useAdminStore } from '../stores/admin'

interface TextureRow {
  hash: string
  type: string
  size: number
  ref_count: number
  last_accessed_at: string
}

const store = useAdminStore()
const items = ref<TextureRow[]>([])
const loading = ref(false)
const banner = ref('')

onMounted(load)

async function load(): Promise<void> {
  loading.value = true
  banner.value = ''
  try {
    const data = await store.api
      .get<{ items: TextureRow[] }>('/api/admin/mc/textures')
      .catch((err: unknown) => {
        if (err instanceof ApiError) {
          banner.value = err.message
        }
        return { items: [] as TextureRow[] }
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

    <a-card title="材质库">
      <a-spin :spinning="loading">
        <a-table :data-source="items" row-key="hash" :pagination="{ pageSize: 20 }">
          <a-table-column key="preview" title="预览">
            <template #default="{ record }">
              <img :src="`/mc/textures/${record.hash}`" width="32" height="32" :alt="record.type" />
            </template>
          </a-table-column>
          <a-table-column key="type" title="类型" />
          <a-table-column key="hash" title="哈希">
            <template #default="{ record }"><code>{{ record.hash.slice(0, 16) }}…</code></template>
          </a-table-column>
          <a-table-column key="size" title="大小">
            <template #default="{ record }">{{ (record.size / 1024).toFixed(1) }} KB</template>
          </a-table-column>
          <a-table-column key="ref_count" title="引用数" />
          <a-table-column key="last_accessed_at" title="最近访问">
            <template #default="{ record }">{{ new Date(record.last_accessed_at).toLocaleString() }}</template>
          </a-table-column>
        </a-table>
      </a-spin>
    </a-card>
  </div>
</template>

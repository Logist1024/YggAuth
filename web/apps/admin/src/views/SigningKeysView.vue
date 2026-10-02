<script setup lang="ts">
import { onMounted, ref } from 'vue'

import { ApiError } from '@yggauth/shared'

import { useAdminStore } from '../stores/admin'

interface SigningKey {
  kid: string
  algo: string
  status: string
  created_at: string
  retired_at: string | null
}

const store = useAdminStore()
const items = ref<SigningKey[]>([])
const loading = ref(false)
const banner = ref('')
const rotating = ref(false)

onMounted(load)

async function load(): Promise<void> {
  loading.value = true
  try {
    const data = await store.api.get<{ items: SigningKey[] }>('/api/admin/signing-keys')
    items.value = data.items
  } catch (err) {
    banner.value = err instanceof ApiError ? err.message : '查询失败'
  } finally {
    loading.value = false
  }
}

async function rotate(): Promise<void> {
  banner.value = ''
  rotating.value = true
  try {
    const data = await store.api.post<{ kid: string }>('/api/admin/signing-keys/rotate', {})
    banner.value = ''
    await load()
    void data
  } catch (err) {
    banner.value = err instanceof ApiError ? err.message : '轮换失败'
  } finally {
    rotating.value = false
  }
}
</script>

<template>
  <div class="page">
    <a-alert v-if="banner" type="error" :message="banner" show-icon style="margin-bottom: 16px" />
    <a-alert
      type="info"
      show-icon
      style="margin-bottom: 16px"
      message="轮换只影响新签发的令牌。已发出的令牌仍用旧密钥验签,所以旧密钥不会被删除。"
    />

    <a-card
      title="签名密钥"
>
      <template #extra>
        <a-button type="primary" :loading="rotating" @click="rotate">轮换密钥</a-button>
      </template>
      <a-spin :spinning="loading">
        <a-table :data-source="items" row-key="kid" :pagination="false">
          <a-table-column key="kid" title="kid" />
          <a-table-column key="algo" title="算法" />
          <a-table-column key="status" title="状态">
            <template #default="{ record }">
              <a-tag :color="record.status === 'active' ? 'green' : 'default'">{{ record.status }}</a-tag>
            </template>
          </a-table-column>
          <a-table-column key="created_at" title="创建时间">
            <template #default="{ record }">{{ new Date(record.created_at).toLocaleString() }}</template>
          </a-table-column>
          <a-table-column key="retired_at" title="退役时间">
            <template #default="{ record }">
              {{ record.retired_at ? new Date(record.retired_at).toLocaleString() : '-' }}
            </template>
          </a-table-column>
        </a-table>
      </a-spin>
    </a-card>
  </div>
</template>

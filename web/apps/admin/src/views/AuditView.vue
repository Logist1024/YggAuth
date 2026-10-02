<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'

import { ApiError } from '@yggauth/shared'

import { useAdminStore } from '../stores/admin'

interface AuditEvent {
  id: string
  actor_type: string
  actor_id: string
  action: string
  target_type: string
  target_id: string
  outcome: string
  ip: string
  created_at: string
}

const store = useAdminStore()
const items = ref<AuditEvent[]>([])
const total = ref(0)
const loading = ref(false)
const banner = ref('')

const query = reactive({ limit: 50, offset: 0, action: '', actor_type: '', outcome: '' })

onMounted(load)

async function load(): Promise<void> {
  loading.value = true
  banner.value = ''
  try {
    const params = new URLSearchParams({
      limit: String(query.limit),
      offset: String(query.offset),
    })
    for (const [key, value] of Object.entries(query)) {
      if (key !== 'limit' && key !== 'offset' && value) {
        params.set(key, value)
      }
    }
    const data = await store.api.get<{ events: AuditEvent[]; total: number }>(
      `/api/admin/audit?${params.toString()}`,
    )
    items.value = data.events
    total.value = data.total
  } catch (err) {
    banner.value = err instanceof ApiError ? err.message : '查询失败'
  } finally {
    loading.value = false
  }
}

function searchNow(): void {
  query.offset = 0
  void load()
}

function exportCSV(): void {
  // 导出走后端接口而不是前端拼 CSV:
  // 前端导出必然受分页限制,而审计日志的价值恰恰在「全量」。
  const params = new URLSearchParams({ limit: '10000' })
  for (const [key, value] of Object.entries(query)) {
    if (key !== 'limit' && key !== 'offset' && value) {
      params.set(key, value)
    }
  }
  window.location.href = `/api/admin/audit/export?${params.toString()}`
}
</script>

<template>
  <div class="page">
    <a-alert v-if="banner" type="error" :message="banner" show-icon style="margin-bottom: 16px" />

    <a-card>
      <a-space style="margin-bottom: 16px" wrap>
        <a-input v-model:value="query.action" placeholder="动作,如 account.login" style="width: 200px" />
        <a-select v-model:value="query.outcome" placeholder="结果" allow-clear style="width: 140px"
          :options="[
            { label: '成功', value: 'success' },
            { label: '失败', value: 'failure' },
          ]" />
        <a-button type="primary" @click="searchNow">筛选</a-button>
        <a-button @click="exportCSV">导出 CSV</a-button>
      </a-space>

      <a-spin :spinning="loading">
        <a-table :data-source="items" row-key="id" :pagination="false">
          <a-table-column key="created_at" title="时间">
            <template #default="{ record }">{{ new Date(record.created_at).toLocaleString() }}</template>
          </a-table-column>
          <a-table-column key="actor" title="主体">
            <template #default="{ record }">{{ record.actor_type }}:{{ record.actor_id || '-' }}</template>
          </a-table-column>
          <a-table-column key="action" title="动作" />
          <a-table-column key="target" title="目标">
            <template #default="{ record }">{{ record.target_type }}:{{ record.target_id }}</template>
          </a-table-column>
          <a-table-column key="outcome" title="结果">
            <template #default="{ record }">
              <a-tag :color="record.outcome === 'success' ? 'green' : 'red'">{{ record.outcome }}</a-tag>
            </template>
          </a-table-column>
          <a-table-column key="ip" title="IP" />
        </a-table>
      </a-spin>

      <a-pagination
        :current="query.offset / query.limit + 1"
        :page-size="query.limit"
        :total="total"
        show-size-changer
        style="margin-top: 16px; text-align: right"
        @change="(p: number, size: number) => { query.limit = size; query.offset = (p - 1) * size; load() }"
      />
    </a-card>
  </div>
</template>

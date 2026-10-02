<script setup lang="ts">
import { onMounted, ref } from 'vue'

import { ApiError } from '@yggauth/shared'

import { useAdminStore } from '../stores/admin'

interface Stats {
  accounts_total: number
  accounts_recent: number
  roles_total: number
  permissions_total: number
  audit_events_24h: number
}

const store = useAdminStore()
const stats = ref<Stats | null>(null)
const error = ref('')

onMounted(async () => {
  try {
    stats.value = await store.api.get<Stats>('/api/admin/dashboard')
  } catch (err) {
    error.value = err instanceof ApiError ? err.message : '查询失败'
  }
})
</script>

<template>
  <div class="page">
    <a-alert v-if="error" type="error" :message="error" show-icon style="margin-bottom: 16px" />

    <a-row v-if="stats" :gutter="16">
      <a-col :span="6">
        <a-card title="账号总数"><a-statistic :value="stats.accounts_total" /></a-card>
      </a-col>
      <a-col :span="6">
        <a-card title="最近注册"><a-statistic :value="stats.accounts_recent" /></a-card>
      </a-col>
      <a-col :span="6">
        <a-card title="角色数量"><a-statistic :value="stats.roles_total" /></a-card>
      </a-col>
      <a-col :span="6">
        <a-card title="24 小时审计事件"><a-statistic :value="stats.audit_events_24h" /></a-card>
      </a-col>
    </a-row>
  </div>
</template>

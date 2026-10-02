<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'

import { ApiError } from '@yggauth/shared'

import { useAdminStore } from '../stores/admin'

interface AccountRow {
  id: string
  username: string
  email: string
  status: string
  email_verified: boolean
  mc_login_enabled: boolean
  created_at: string
}

const store = useAdminStore()
const items = ref<AccountRow[]>([])
const total = ref(0)
const page = ref(1)
const pageSize = ref(20)
const search = ref('')
const loading = ref(false)
const banner = ref('')

const query = reactive({ limit: 20, offset: 0, search: '' })

onMounted(load)

async function load(): Promise<void> {
  loading.value = true
  banner.value = ''
  try {
    const params = new URLSearchParams({
      limit: String(query.limit),
      offset: String(query.offset),
    })
    if (query.search) {
      params.set('search', query.search)
    }
    const data = await store.api.get<{ accounts: AccountRow[]; total: number }>(
      `/api/admin/accounts?${params.toString()}`,
    )
    items.value = data.accounts
    total.value = data.total
  } catch (err) {
    banner.value = err instanceof ApiError ? err.message : '查询失败'
  } finally {
    loading.value = false
  }
}

function searchNow(): void {
  query.search = search.value
  query.offset = 0
  page.value = 1
  void load()
}

function changePage(p: number, size: number): void {
  page.value = p
  pageSize.value = size
  query.limit = size
  query.offset = (p - 1) * size
  void load()
}

async function update(id: string, patch: Record<string, unknown>): Promise<void> {
  banner.value = ''
  try {
    await store.api.patch(`/api/admin/accounts/${id}`, patch)
    await load()
  } catch (err) {
    banner.value = err instanceof ApiError ? err.message : '操作失败'
  }
}
</script>

<template>
  <div class="page">
    <a-alert v-if="banner" type="error" :message="banner" show-icon style="margin-bottom: 16px" />

    <a-card>
      <a-space style="margin-bottom: 16px">
        <a-input-search
          v-model:value="search"
          placeholder="按用户名或邮箱搜索"
          style="width: 280px"
          @search="searchNow"
        />
        <a-button type="primary" @click="searchNow">搜索</a-button>
      </a-space>

      <a-spin :spinning="loading">
        <a-table :data-source="items" :pagination="false" row-key="id">
          <a-table-column key="username" title="用户名" />
          <a-table-column key="email" title="邮箱" />
          <a-table-column key="status" title="状态">
            <template #default="{ record }">
              <a-tag :color="record.status === 'active' ? 'green' : 'orange'">{{ record.status }}</a-tag>
            </template>
          </a-table-column>
          <a-table-column key="email_verified" title="邮箱验证">
            <template #default="{ record }">
              <a-tag :color="record.email_verified ? 'green' : 'default'">
                {{ record.email_verified ? '已验证' : '未验证' }}
              </a-tag>
            </template>
          </a-table-column>
          <a-table-column key="mc" title="MC 登录">
            <template #default="{ record }">
              <a-switch
                :checked="record.mc_login_enabled"
                @change="(v: boolean) => update(record.id, { mc_login_enabled: v })"
              />
            </template>
          </a-table-column>
          <a-table-column key="actions" title="操作">
            <template #default="{ record }">
              <a-popconfirm
                title="确定要停用这个账号吗?"
                @confirm="update(record.id, { status: 'disabled' })"
              >
                <a-button v-if="record.status !== 'disabled'" danger size="small">停用</a-button>
              </a-popconfirm>
              <a-popconfirm
                title="确定要恢复这个账号吗?"
                @confirm="update(record.id, { status: 'active' })"
              >
                <a-button v-if="record.status === 'disabled'" size="small">恢复</a-button>
              </a-popconfirm>
            </template>
          </a-table-column>
        </a-table>
      </a-spin>

      <a-pagination
        :current="page"
        :page-size="pageSize"
        :total="total"
        show-size-changer
        style="margin-top: 16px; text-align: right"
        @change="changePage"
      />
    </a-card>
  </div>
</template>

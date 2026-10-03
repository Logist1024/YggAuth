<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'

import { ApiError } from '@yggauth/shared'

import EmptyState from '../components/EmptyState.vue'
import { useAdminStore } from '../stores/admin'

interface AccountRow {
  id: string
  username: string
  email: string
  status: string
  email_verified: boolean
  /** 后端字段名就是 login_enabled(见 internal/admin/handler.go 的账号列表)。 */
  login_enabled: boolean
  created_at: string
}

/** 账号状态的显示名。后端存的是枚举值,不该直接甩给用户看。 */
const STATUS_LABEL: Record<string, string> = {
  active: '正常',
  disabled: '已停用',
  pending_verification: '待验证邮箱',
  locked: '已锁定',
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
    <p class="muted" style="margin-bottom: 16px">
      这里列出全部注册账号。可以按用户名或邮箱搜索,停用后该账号将无法登录。
    </p>

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
        <!--
          data-index 不能省。
          a-table-column 只写 key 而不写 data-index、又不给默认插槽时,
          Ant Design Vue 会渲染出一个空单元格 —— 不报错、不警告,
          表格看上去就像「数据没查出来」。
        -->
        <a-table :data-source="items" :pagination="false" row-key="id">
          <template #emptyText>
            <EmptyState
              :description="query.search ? '没有匹配的账号' : '还没有任何账号'"
              :hint="
                query.search
                  ? '换个用户名或邮箱关键词试试。'
                  : '用户注册后会自动出现在这里;也可以生成邀请码定向邀请。'
              "
            >
              <RouterLink v-if="!query.search" to="/invitations">
                <a-button type="primary">去生成邀请码</a-button>
              </RouterLink>
            </EmptyState>
          </template>
          <a-table-column key="username" data-index="username" title="用户名" />
          <a-table-column key="email" data-index="email" title="邮箱" />
          <a-table-column key="status" title="状态">
            <template #default="{ record }">
              <a-tag :color="record.status === 'active' ? 'green' : 'orange'">
                {{ STATUS_LABEL[record.status] ?? record.status }}
              </a-tag>
            </template>
          </a-table-column>
          <a-table-column key="email_verified" title="邮箱验证">
            <template #default="{ record }">
              <a-tag :color="record.email_verified ? 'green' : 'default'">
                {{ record.email_verified ? '已验证' : '未验证' }}
              </a-tag>
            </template>
          </a-table-column>
          <!--
            MC 登录是只读的:后端只有「账号本人凭 MC 令牌切换自己的开关」
            (/mc/account/login-enabled),管理端的 PATCH /api/admin/accounts/:id
            只接受 status 与 email。做成开关会变成一个点了没反应、
            也不报错的控件 —— 比只读展示更糟。
          -->
          <a-table-column key="mc" title="MC 登录">
            <template #default="{ record }">
              <a-tag :color="record.login_enabled ? 'green' : 'default'">
                {{ record.login_enabled ? '已开启' : '已关闭' }}
              </a-tag>
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

<script setup lang="ts">
import { onMounted, ref } from 'vue'

import { ApiError } from '@yggauth/shared'

import EmptyState from '../components/EmptyState.vue'
import { useAdminStore } from '../stores/admin'

interface Invitation {
  id: string
  /** 为空表示不限定受邀人。 */
  email: string | null
  code: string
  max_uses: number
  used_count: number
  created_at: string
  expires_at: string
  revoked_at: string | null
  created_by: string | null
}

const store = useAdminStore()
const items = ref<Invitation[]>([])
const banner = ref('')
const loading = ref(false)
const email = ref('')
const submitting = ref(false)
const createdLink = ref('')

onMounted(load)

async function load(): Promise<void> {
  loading.value = true
  try {
    const data = await store.api.get<{ invitations: Invitation[] }>('/api/admin/invitations')
    items.value = data.invitations ?? []
  } catch (err) {
    banner.value = err instanceof ApiError ? err.message : '查询失败'
  } finally {
    loading.value = false
  }
}

/**
 * 邀请码状态是**推导**出来的,不是接口里的字段。
 *
 * 后端只返回 expires_at / revoked_at / used_count / max_uses 四个事实,
 * 没有 status 字段 —— 之前前端读 record.status 永远是 undefined,
 * 整列都是空的。
 */
function statusOf(inv: Invitation): { label: string; color: string } {
  if (inv.revoked_at) {
    return { label: '已撤销', color: 'default' }
  }
  if (new Date(inv.expires_at).getTime() < Date.now()) {
    return { label: '已过期', color: 'default' }
  }
  if (inv.used_count >= inv.max_uses) {
    return { label: '已用尽', color: 'default' }
  }
  return { label: '可用', color: 'blue' }
}

async function create(): Promise<void> {
  banner.value = ''
  submitting.value = true
  try {
    // 响应是创建出来的邀请码本身,直接挂在 data 下,没有再套一层 invitation。
    // 邮箱留空时后端生成不限受邀人的通用码,可用次数与有效期也都有默认值。
    const data = await store.api.post<{ code: string }>('/api/admin/invitations', {
      email: email.value,
    })
    createdLink.value = data.code
    email.value = ''
    await load()
  } catch (err) {
    banner.value = err instanceof ApiError ? err.message : '创建失败'
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <div class="page">
    <p class="muted" style="margin-bottom: 16px">
      邀请码用于在关闭开放注册时定向邀请用户。邮箱留空表示不限定受邀人,默认 7 天内有效、可使用 1 次。
    </p>

    <a-alert v-if="banner" type="error" :message="banner" show-icon style="margin-bottom: 16px" />
    <a-alert
      v-if="createdLink"
      type="success"
      show-icon
      style="margin-bottom: 16px"
      :message="`邀请码:${createdLink}`"
    />

    <a-card title="创建邀请" style="margin-bottom: 16px">
      <a-space>
        <a-input v-model:value="email" type="email" placeholder="受邀人邮箱(可留空)" style="width: 320px" />
        <a-button type="primary" :loading="submitting" @click="create">生成邀请</a-button>
      </a-space>
    </a-card>

    <a-card title="邀请列表">
      <a-spin :spinning="loading">
        <a-table :data-source="items" row-key="id" :pagination="{ pageSize: 20 }">
          <template #emptyText>
            <EmptyState
              description="还没有邀请码"
              hint="在上方填写受邀人邮箱后生成一个,把得到的邀请码发给对方即可。"
            />
          </template>
          <a-table-column key="email" title="邮箱" :width="240">
            <template #default="{ record }">
              <span v-if="record.email">{{ record.email }}</span>
              <span v-else class="muted">不限</span>
            </template>
          </a-table-column>
          <a-table-column key="code" data-index="code" title="邀请码" />
          <a-table-column key="uses" title="使用情况" :width="110">
            <template #default="{ record }">
              {{ record.used_count }} / {{ record.max_uses }}
            </template>
          </a-table-column>
          <a-table-column key="status" title="状态" :width="100">
            <template #default="{ record }">
              <a-tag :color="statusOf(record).color">{{ statusOf(record).label }}</a-tag>
            </template>
          </a-table-column>
          <a-table-column key="expires_at" title="过期时间" :width="180">
            <template #default="{ record }">{{ new Date(record.expires_at).toLocaleString() }}</template>
          </a-table-column>
        </a-table>
      </a-spin>
    </a-card>
  </div>
</template>

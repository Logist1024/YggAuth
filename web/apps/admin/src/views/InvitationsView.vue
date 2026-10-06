<script setup lang="ts">
import { onMounted, ref } from 'vue'

import { formatTime, errorBanner, backToTop, type ErrorBanner } from '@yggauth/shared'

import EmptyState from '../components/EmptyState.vue'
import CopyableText from '../components/CopyableText.vue'
import { useAdminStore } from '../stores/admin'
import ErrorAlert from '../components/ErrorAlert.vue'

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
const banner = ref<ErrorBanner | null>(null)
const loading = ref(false)
const submitting = ref(false)

/** 创建表单。默认值与后端 CreateInvitation 的兜底一致。 */
const draft = ref({ email: '', maxUses: 1, days: 7, code: '' })
const createdCode = ref('')

onMounted(load)

async function load(): Promise<void> {
  loading.value = true
  banner.value = null
  try {
    const data = await store.api.get<{ invitations: Invitation[] }>('/api/admin/invitations')
    items.value = data.invitations ?? []
  } catch (err) {
    banner.value = errorBanner(err, '查询失败')
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
  // 防重入:回车连发会绕过按钮的 loading,一次弹窗能生成两个邀请码。
  if (submitting.value) return
  banner.value = null
  submitting.value = true
  try {
    // 响应是创建出来的邀请码本身,直接挂在 data 下,没有再套一层 invitation。
    // email/code 留空时后端分别生成「不限受邀人」与自动生成的码。
    const body: Record<string, unknown> = {
      max_uses: draft.value.maxUses,
      days: draft.value.days,
    }
    if (draft.value.email.trim()) body.email = draft.value.email.trim()
    if (draft.value.code.trim()) body.code = draft.value.code.trim()

    const data = await store.api.post<{ code: string }>('/api/admin/invitations', body)
    createdCode.value = data.code
    draft.value = { email: '', maxUses: 1, days: 7, code: '' }
    await load()
  } catch (err) {
    banner.value = errorBanner(err, '创建失败')
  } finally {
    submitting.value = false
  }
}

async function revoke(inv: Invitation): Promise<void> {
  banner.value = null
  try {
    // 后端对重复撤销是幂等的,所以这里不必先判断 revoked_at
    await store.api.post(`/api/admin/invitations/${inv.id}/revoke`, {})
    await load()
  } catch (err) {
    banner.value = errorBanner(err, '撤销失败')
  }
}
</script>

<template>
  <div class="page">
    <p class="muted" style="margin-bottom: 16px">
      邀请码用于在关闭开放注册时定向邀请用户。邮箱留空表示不限定受邀人,默认 7 天内有效、可使用 1 次。
    </p>

    <ErrorAlert :banner="banner" />

    <a-alert v-if="createdCode" type="success" show-icon closable style="margin-bottom: 16px">
      <template #message>邀请码已生成,把它发给受邀人即可(下面的列表里也能再找到它)</template>
      <template #description>
        <CopyableText :value="createdCode" label="邀请码" />
      </template>
    </a-alert>

    <a-card title="创建邀请" style="margin-bottom: 16px">
      <!-- 回车即提交:与登录页那套 html-type=submit 保持一致,不必特意去点按钮 -->
      <a-form layout="vertical" @submit.prevent="create">
        <a-space wrap>
          <a-form-item label="受邀人邮箱(可留空)" style="width: 300px">
            <a-input v-model:value="draft.email" type="email" placeholder="留空表示不限定受邀人" />
          </a-form-item>
          <a-form-item label="可用次数" style="width: 140px">
            <a-input-number v-model:value="draft.maxUses" :min="1" :max="999" style="width: 100%" />
          </a-form-item>
          <a-form-item label="有效天数" style="width: 140px">
            <a-input-number v-model:value="draft.days" :min="1" :max="365" style="width: 100%" />
          </a-form-item>
          <a-form-item label="自定义邀请码(可留空)" style="width: 240px">
            <a-input v-model:value="draft.code" placeholder="留空则自动生成" />
          </a-form-item>
        </a-space>
        <a-button type="primary" :loading="submitting" html-type="submit">生成邀请码</a-button>
      </a-form>
    </a-card>

    <a-card title="邀请列表">
      <a-spin :spinning="loading">
        <!-- 换页是一次整屏数据替换:回顶,否则视线停在分页器旁,看到的是新页的末尾 -->
        <a-table
          :scroll="{ x: 1200 }"
          :data-source="items"
          row-key="id"
          :pagination="{ pageSize: 20 }"
          @change="() => backToTop()"
        >
          <template #emptyText>
            <EmptyState
              description="还没有邀请码"
              hint="在上方填写受邀人邮箱后生成一个,把得到的邀请码发给对方即可。"
            />
          </template>
          <a-table-column key="email" title="邮箱" :width="220">
            <template #default="{ record }">
              <span v-if="record.email">{{ record.email }}</span>
              <span v-else class="muted">不限</span>
            </template>
          </a-table-column>
          <a-table-column key="code" title="邀请码" :width="200">
            <template #default="{ record }">
              <CopyableText :value="record.code" />
            </template>
          </a-table-column>
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
          <a-table-column key="expires_at" title="过期时间" :width="170">
            <template #default="{ record }">{{ formatTime(record.expires_at) }}</template>
          </a-table-column>
          <a-table-column key="actions" title="操作" :width="90">
            <template #default="{ record }">
              <a-popconfirm
                v-if="!record.revoked_at"
                title="撤销后该码立即不可再用于注册,确定?"
                @confirm="revoke(record)"
              >
                <a-button danger size="small">撤销</a-button>
              </a-popconfirm>
              <span v-else class="muted">—</span>
            </template>
          </a-table-column>
        </a-table>
      </a-spin>
    </a-card>
  </div>
</template>

<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'

import { ApiError } from '@yggauth/shared'

import EmptyState from '../components/EmptyState.vue'
import { useAdminStore } from '../stores/admin'

/** 字段名对齐 internal/identity/audit.Entry 的 json tag。 */
interface AuditEvent {
  id: number
  occurred_at: string
  actor: string
  /** 后端解析出来的用户名。解析不到(如 system)时为空。 */
  actor_name?: string
  action: string
  target_type: string
  target_id: string
  /** 目标为账号时后端解析出来的用户名。 */
  target_name?: string
  outcome: string
  ip: string
  user_agent: string
}

/** 结果的显示名。后端存的是枚举值。 */
const OUTCOME_LABEL: Record<string, string> = {
  success: '成功',
  failure: '失败',
}

/**
 * 格式化时间,并对脏数据兜底。
 *
 * 直接 new Date(x).toLocaleString() 在字段名对不上时会渲染成
 * 一屏「Invalid Date」—— 看起来像时间坏了,实际是契约漂移。
 * 显示一个破折号比显示 12 行 Invalid Date 更容易看出问题所在。
 */
function formatTime(value: string): string {
  const t = new Date(value)
  return Number.isNaN(t.getTime()) ? '—' : t.toLocaleString()
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
        // Object.entries 的值类型是 string | number(query 里 limit/offset 是数字),
        // TS 无法通过 key 判断收窄。运行到这里 value 必然已是字符串,
        // 但 String() 让这件事不依赖推断 —— 它对字符串是恒等变换。
        params.set(key, String(value))
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
      // 同 load():值类型是 string | number,显式转字符串。
      params.set(key, String(value))
    }
  }
  window.location.href = `/api/admin/audit/export?${params.toString()}`
}
</script>

<template>
  <div class="page">
    <p class="muted" style="margin-bottom: 16px">
      这里记录登录、改密、权限变更等操作。可按动作与结果筛选,或导出 CSV 存档。
    </p>

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
          <template #emptyText>
            <EmptyState
              :description="
                query.action || query.outcome ? '没有符合筛选条件的记录' : '还没有任何操作记录'
              "
              :hint="
                query.action || query.outcome
                  ? '放宽或清空筛选条件试试。'
                  : '登录、改密、权限变更等操作产生后会自动出现在这里。'
              "
            />
          </template>
          <a-table-column key="occurred_at" title="时间" :width="180">
            <template #default="{ record }">{{ formatTime(record.occurred_at) }}</template>
          </a-table-column>
          <a-table-column key="actor" title="主体" :width="180">
            <template #default="{ record }">
              <!-- 显示用户名,原始串留在 tooltip 里 —— 排查时仍需确认到底是哪个主体。 -->
              <a-tooltip v-if="record.actor_name" :title="record.actor">
                <span>{{ record.actor_name }}</span>
              </a-tooltip>
              <span v-else class="muted">{{ record.actor || '—' }}</span>
            </template>
          </a-table-column>
          <a-table-column key="action" data-index="action" title="动作" />
          <a-table-column key="target" title="目标" :width="180">
            <template #default="{ record }">
              <a-tooltip v-if="record.target_name" :title="`${record.target_type}:${record.target_id}`">
                <span>{{ record.target_name }}</span>
              </a-tooltip>
              <span v-else>{{ record.target_type ? `${record.target_type}:${record.target_id}` : '—' }}</span>
            </template>
          </a-table-column>
          <a-table-column key="outcome" title="结果" :width="100">
            <template #default="{ record }">
              <a-tag :color="record.outcome === 'success' ? 'green' : 'red'">
                {{ OUTCOME_LABEL[record.outcome] ?? record.outcome }}
              </a-tag>
            </template>
          </a-table-column>
          <a-table-column key="ip" data-index="ip" title="IP" :width="150" />
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

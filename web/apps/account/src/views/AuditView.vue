<script setup lang="ts">
// 我的操作记录:登录、改密、换邮箱等发生在自己账号上的事。
//
// 数据来自 GET /api/account/audit —— 后端把 account_id 钉死在登录态上,
// 这里无论如何都查不到别人的记录。
import { onMounted, reactive, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'

import { formatTime, errorBanner, backToTop, showTotal, readPage, readPageSize, readText, AUDIT_ACTION_OPTIONS, ACTION_LABEL, type ErrorBanner } from '@yggauth/shared'

import EmptyState from '../components/EmptyState.vue'
import { useSessionStore } from '../stores/session'
import ErrorAlert from '../components/ErrorAlert.vue'

/** 字段名对齐 internal/identity/audit.Entry 的 json tag。 */
interface AuditEvent {
  id: number
  occurred_at: string
  action: string
  target_type: string
  target_id: string
  outcome: string
  ip: string
  user_agent: string
}

const OUTCOME_LABEL: Record<string, string> = {
  success: '成功',
  failure: '失败',
}

// 动作的中文译名改从 shared/audit.ts 取(ACTION_LABEL):此前这里自抄一份,
// 收了两个后端早已不写的动作、又漏了一半真会出现的,同两张表各说各话。

const store = useSessionStore()
const items = ref<AuditEvent[]>([])
const total = ref(0)
const loading = ref(false)
const banner = ref<ErrorBanner | null>(null)

/**
 * 输入框里的草稿。改了它不等于查了 —— 必须点「筛选」才发请求。
 */
const query = reactive({ limit: 20, offset: 0, action: '', outcome: '' })

/**
 * 已提交给后端的筛选条件。
 *
 * 分开存是为了让空态文案说真话:用户在输入框里敲了两个字还没按
 * 回车时,空表若显示「没有符合筛选条件的记录」,就等于替用户宣布
 * 一个他还没执行过的查询失败了。
 */
const applied = reactive({ action: '', outcome: '' })

const route = useRoute()
const router = useRouter()

// 筛选条件与页码放进地址栏:刷新不丢,同一屏记录也能直接发链接。
for (const k of ['action', 'outcome'] as const) {
  const v = readText(route.query[k]).trim()
  applied[k] = v
  query[k] = v
}
query.limit = readPageSize(route.query.size)
query.offset = (readPage(route.query.page) - 1) * query.limit

/** 把已生效条件与页码写进地址栏,空条件不进链接。 */
function syncUrl(): void {
  const next: Record<string, string> = {}
  if (applied.action) next.action = applied.action
  if (applied.outcome) next.outcome = applied.outcome
  const p = query.offset / query.limit + 1
  if (p > 1) next.page = String(p)
  if (query.limit !== 20) next.size = String(query.limit)
  void router.replace({ query: next })
}

onMounted(load)

async function load(): Promise<void> {
  syncUrl()
  loading.value = true
  banner.value = null
  try {
    const params = new URLSearchParams({
      limit: String(query.limit),
      offset: String(query.offset),
    })
    if (applied.action) params.set('action', applied.action)
    if (applied.outcome) params.set('outcome', applied.outcome)

    const data = await store.api.get<{ events: AuditEvent[]; total: number }>(
      `/api/account/audit?${params.toString()}`,
    )
    items.value = data.events
    total.value = data.total
  } catch (err) {
    banner.value = errorBanner(err, '查询失败')
  } finally {
    loading.value = false
  }
}

function searchNow(): void {
  applied.action = query.action.trim()
  applied.outcome = query.outcome
  query.offset = 0
  // 先回顶再请求:等待期间视线已经在新数据要出现的位置上,
  // 而不是停在底部的分页器旁边,盯着上一页的尾巴。
  backToTop()
  void load()
}

const filtered = (): boolean => Boolean(applied.action || applied.outcome)
</script>

<template>
  <div class="page">
    <h2>操作记录</h2>
    <p class="muted" style="margin-bottom: 16px">
      这里只记录发生在我这个账号上的操作。怀疑账号被人动过时,先从这里看起。
    </p>

    <ErrorAlert :banner="banner" />

    <a-card>
      <a-space style="margin-bottom: 16px" wrap>
        <!--
          操作改成下拉(带搜索):后端按 `action LIKE` 精确匹配,拼错一个字母就是 0 条,
          而空态只说「没有符合筛选条件的记录」,不告诉你是自己拼错了。
          选完即查,与右边的「结果」下拉同一种交互;清单不是白名单(见 shared/audit.ts)。
        -->
        <a-select
          v-model:value="query.action"
          placeholder="按操作筛选"
          style="width: 240px"
          allow-clear
          show-search
          option-filter-prop="label"
          :options="AUDIT_ACTION_OPTIONS"
          @change="searchNow"
        />
        <!--
          下拉只有两项,选完即查比再点一次「筛选」少一步 ——
          文字筛选仍要按回车/点按钮,那边每次敲键都发请求不可接受。
        -->
        <a-select
          v-model:value="query.outcome"
          placeholder="结果"
          allow-clear
          style="width: 140px"
          :options="[
            { label: '成功', value: 'success' },
            { label: '失败', value: 'failure' },
          ]"
          @change="searchNow"
        />
        <a-button type="primary" @click="searchNow">筛选</a-button>
      </a-space>

      <a-spin :spinning="loading">
        <a-table :scroll="{ x: 1200 }" :data-source="items" row-key="id" :pagination="false">
          <template #emptyText>
            <EmptyState
              :description="filtered() ? '没有符合筛选条件的记录' : '还没有任何操作记录'"
              :hint="
                filtered()
                  ? '放宽或清空筛选条件试试。'
                  : '登录、修改密码、更换邮箱等操作产生后会自动出现在这里。'
              "
            />
          </template>
          <a-table-column key="occurred_at" title="时间" :width="170">
            <template #default="{ record }">{{ formatTime(record.occurred_at) }}</template>
          </a-table-column>
          <a-table-column key="action" title="操作" :width="180">
            <template #default="{ record }">
              <a-tooltip :title="record.action">
                <span>{{ ACTION_LABEL[record.action] ?? record.action }}</span>
              </a-tooltip>
            </template>
          </a-table-column>
          <a-table-column key="outcome" title="结果" :width="90">
            <template #default="{ record }">
              <a-tag :color="record.outcome === 'success' ? 'green' : 'red'">
                {{ OUTCOME_LABEL[record.outcome] ?? record.outcome }}
              </a-tag>
            </template>
          </a-table-column>
          <a-table-column key="target" title="对象" :width="200">
            <template #default="{ record }">
              <span v-if="record.target_type && record.target_id" class="muted">
                {{ record.target_type }}<template v-if="record.target_id">:{{ record.target_id }}</template>
              </span>
              <span v-else class="muted">—</span>
            </template>
          </a-table-column>
          <a-table-column key="ip" data-index="ip" title="IP" :width="140" />
          <a-table-column key="user_agent" title="设备">
            <template #default="{ record }">
              <a-tooltip v-if="record.user_agent" :title="record.user_agent">
                <span class="muted">{{ record.user_agent }}</span>
              </a-tooltip>
              <span v-else class="muted">—</span>
            </template>
          </a-table-column>
        </a-table>
      </a-spin>

      <a-pagination
        :current="query.offset / query.limit + 1"
        :page-size="query.limit"
        :total="total"
        :show-total="showTotal"
        show-size-changer
        style="margin-top: 16px; text-align: right"
        @change="
          (p: number, size: number) => {
            query.limit = size
            query.offset = (p - 1) * size
            backToTop()
            load()
          }
        "
      />
    </a-card>
  </div>
</template>

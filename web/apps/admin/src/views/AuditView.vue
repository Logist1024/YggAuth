<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'

import { formatTime, errorBanner, backToTop, showTotal, readPage, readPageSize, readText, AUDIT_ACTION_OPTIONS, type ErrorBanner } from '@yggauth/shared'

import EmptyState from '../components/EmptyState.vue'
import { useAdminStore } from '../stores/admin'
import ErrorAlert from '../components/ErrorAlert.vue'

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

const store = useAdminStore()
const items = ref<AuditEvent[]>([])
const total = ref(0)
const loading = ref(false)
const banner = ref<ErrorBanner | null>(null)
const exporting = ref(false)

/**
 * 筛选项**严格对应** auditFilter 支持的键。
 *
 * 之前表单里有个 actor_type,后端根本不读 —— 用户填了它、点了筛选,
 * 查询照旧返回全量,看起来就是「筛选不生效」。
 *
 * from/to 由 <input type="datetime-local"> 提供,值形如
 * 2024-05-01T08:30,不是后端要求的 RFC3339,所以查询前要转换。
 */
const query = reactive({
  limit: 50,
  offset: 0,
  action: '',
  outcome: '',
  account_id: '',
  target_type: '',
  target_id: '',
  from: '',
  to: '',
})

/**
 * 已提交给后端的筛选条件(不含分页)。
 *
 * 输入框改了不等于查了 —— 必须点「筛选」才发请求。把两者分开,
 * 是为了三件事都说得通:空态文案只反映**真的执行过**的条件、
 * 「导出 CSV」导出的是**眼前这张表**而不是敲了一半的草稿、
 * 翻页也不会被没提交的输入悄悄改掉查询。
 */
const applied = reactive({
  action: '',
  outcome: '',
  account_id: '',
  target_type: '',
  target_id: '',
  from: '',
  to: '',
})

type FilterKey = keyof typeof applied
const FILTER_KEYS: FilterKey[] = ['action', 'outcome', 'account_id', 'target_type', 'target_id', 'from', 'to']

const route = useRoute()
const router = useRouter()

// 地址栏承载「筛好了哪一屏」:刷新不丢条件,链接发给同事打开就是同一屏。
// 恢复的是**已生效**的条件,同时把草稿也填上 —— 否则表格按条件筛着,
// 输入框却是空的,谁也说不清这一屏是怎么筛出来的。
for (const k of FILTER_KEYS) {
  const v = readText(route.query[k]).trim()
  applied[k] = v
  query[k] = v
}
query.limit = readPageSize(route.query.size, 50)
query.offset = (readPage(route.query.page) - 1) * query.limit

/** 把已生效条件与页码写进地址栏,空条件不进链接。 */
function syncUrl(): void {
  const next: Record<string, string> = {}
  for (const k of FILTER_KEYS) {
    if (applied[k]) next[k] = applied[k]
  }
  const p = query.offset / query.limit + 1
  if (p > 1) next.page = String(p)
  if (query.limit !== 50) next.size = String(query.limit)
  void router.replace({ query: next })
}

/** 该组条件里有没有非空项。 */
function anySet(state: Record<FilterKey, string>): boolean {
  return FILTER_KEYS.some((k) => Boolean(state[k]))
}

/** 输入框里有东西,或表格正按某个条件显示 —— 两者任一都要能一键清掉。 */
const hasFilter = (): boolean => anySet(query) || anySet(applied)

/** datetime-local 的值 → 后端可解析的 RFC3339(不带小数秒)。 */
function toRFC3339(value: string): string {
  if (!value) return ''
  const d = new Date(value)
  if (Number.isNaN(d.getTime())) return ''
  // toISOString 带毫秒,而 Go 的 time.Parse(time.RFC3339) 不接受小数秒
  return `${d.toISOString().slice(0, 19)}Z`
}

/** 列表与导出共用同一份参数,避免两处各写一遍后悄悄漂移。 */
function filterParams(): URLSearchParams {
  const p = new URLSearchParams({
    limit: String(query.limit),
    offset: String(query.offset),
  })
  if (applied.action) p.set('action', applied.action)
  if (applied.outcome) p.set('outcome', applied.outcome)
  if (applied.account_id) p.set('account_id', applied.account_id.trim())
  if (applied.target_type) p.set('target_type', applied.target_type.trim())
  if (applied.target_id) p.set('target_id', applied.target_id.trim())
  const from = toRFC3339(applied.from)
  if (from) p.set('from', from)
  const to = toRFC3339(applied.to)
  if (to) p.set('to', to)
  return p
}

/** 表格正按这些条件显示。空态文案看的是它,不是输入框里的草稿。 */
const hasApplied = (): boolean => anySet(applied)

onMounted(load)

async function load(): Promise<void> {
  syncUrl()
  loading.value = true
  banner.value = null
  try {
    const data = await store.api.get<{ events: AuditEvent[]; total: number }>(
      `/api/admin/audit?${filterParams().toString()}`,
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
  for (const k of FILTER_KEYS) {
    // 下拉清空时 ant-design-vue 会 emit change(undefined),v-model 于是把
    // query 里的字段写成 undefined —— 这里直接 .trim() 会当场抛错,
    // 表现就是点了 × 什么都没发生、地址栏的参数原样留着(冒烟里断言过)。
    const raw = query[k]
    // 两端空格吃掉:「account.login 」对不上任何动作,而用户多半只是多按了空格。
    applied[k] = (typeof raw === 'string' ? raw : '').trim()
  }
  query.offset = 0
  // 先回顶再请求:等待期间视线已经在新数据要出现的位置上,
  // 而不是停在底部的分页器旁边,盯着上一页的尾巴。
  backToTop()
  void load()
}

/**
 * 清空全部筛选并立即刷新。
 *
 * 之前这个按钮只写清了 action/outcome 两个字段,却还附带一次查询 ——
 * account_id、时间范围留着,看起来像「点了没反应」。
 * 草稿与已生效条件都要清:只清一边,表格刷新完条件又冒出来。
 */
function clearFilters(): void {
  for (const k of FILTER_KEYS) {
    query[k] = ''
    applied[k] = ''
  }
  query.offset = 0
  backToTop()
  void load()
}

/** 从 Content-Disposition 里取回后端起的文件名。 */
function filenameFromDisposition(header: string | null): string | null {
  if (!header) return null
  const m = /filename="([^"]+)"/.exec(header)
  return m ? m[1] : null
}

/**
 * 导出 CSV。
 *
 * 走 fetch 而不是 window.location.href 直跳:直跳会丢掉
 * XHR/fetch 才会带的自定义头,且站点跨子域分站时(session cookie
 * 是按子域发的),导航请求一旦没带上凭据就是一次没有任何提示的 401。
 * 拿到 blob 再触发下载,失败时用户能看到具体原因。
 */
async function exportCSV(): Promise<void> {
  exporting.value = true
  banner.value = null
  try {
    const p = filterParams()
    // 全量导出:忽略分页
    p.set('limit', '10000')
    p.delete('offset')

    const res = await fetch(`/api/admin/audit/export?${p.toString()}`, {
      credentials: 'include',
    })
    if (!res.ok) {
      // 后端的错误体是 {code, message, data} 信封,优先展示 message
      let message = `导出失败(HTTP ${res.status})`
      try {
        const body = (await res.json()) as { message?: string }
        if (body?.message) message = body.message
      } catch {
        // 非 JSON 响应(如网关返回的 HTML),保留上面的默认文案
      }
      throw new Error(message)
    }

    const blob = await res.blob()
    const name = filenameFromDisposition(res.headers.get('Content-Disposition')) ?? 'audit.csv'
    const url = URL.createObjectURL(blob)
    const link = document.createElement('a')
    link.href = url
    link.download = name
    document.body.appendChild(link)
    link.click()
    link.remove()
    // 立刻释放会让部分浏览器来不及读取,所以放在点击之后
    URL.revokeObjectURL(url)
  } catch (err) {
    // errorBanner 同样兜住非 ApiError 的 Error(如 URL.createObjectURL 抛的)
    banner.value = errorBanner(err, '导出失败')
  } finally {
    exporting.value = false
  }
}
</script>

<template>
  <div class="page">
    <p class="muted" style="margin-bottom: 16px">
      这里记录登录、改密、权限变更等操作。可按动作、结果、主体与时间段筛选,或导出 CSV 存档。
    </p>

    <ErrorAlert :banner="banner" />

    <a-card>
      <a-space style="margin-bottom: 16px" wrap>
        <!--
          动作改成下拉(带搜索):后端 SQL 按 `action LIKE` 精确匹配,
          敲错一个字母就是 0 条,而空态只说「没有符合筛选条件的记录」,不告诉你是自己拼错了。
          选完即查,与右边的「结果」下拉同一种交互;清单不是白名单,后端新增动作时补一条即可(见 shared/audit.ts)。
        -->
        <a-select
          v-model:value="query.action"
          placeholder="按动作筛选"
          style="width: 260px"
          allow-clear
          show-search
          option-filter-prop="label"
          :options="AUDIT_ACTION_OPTIONS"
          @change="searchNow"
        />
        <!-- 选完即查:下拉只有两项,再点一次「筛选」纯属多余 -->
        <a-select
          v-model:value="query.outcome"
          placeholder="结果"
          allow-clear
          style="width: 130px"
          :options="[
            { label: '成功', value: 'success' },
            { label: '失败', value: 'failure' },
          ]"
          @change="searchNow"
        />
        <a-input
          v-model:value="query.target_type"
          placeholder="目标类型,如 account"
          style="width: 170px"
          allow-clear
          @press-enter="searchNow"
        />
        <!--
          后端的 target_id 过滤一直都在,此前只是前端没给入口 ——
          「目标」列明明显示着 `类型:ID`,却没有任何地方能把它贴进来筛。
        -->
        <a-input
          v-model:value="query.target_id"
          placeholder="目标 ID,如账号 ID"
          style="width: 220px"
          allow-clear
          @press-enter="searchNow"
        />
        <a-input
          v-model:value="query.account_id"
          placeholder="主体账号 ID"
          style="width: 280px"
          allow-clear
          @press-enter="searchNow"
        />
        <a-input
          v-model:value="query.from"
          type="datetime-local"
          style="width: 210px"
          title="起始时间"
        />
        <a-input
          v-model:value="query.to"
          type="datetime-local"
          style="width: 210px"
          title="截止时间"
        />
        <a-button type="primary" @click="searchNow">筛选</a-button>
        <a-button v-if="hasFilter()" @click="clearFilters">清空</a-button>
        <a-button :loading="exporting" @click="exportCSV">导出 CSV</a-button>
      </a-space>

      <a-spin :spinning="loading">
        <a-table :scroll="{ x: 1200 }" :data-source="items" row-key="id" :pagination="false">
          <template #emptyText>
            <EmptyState
              :description="hasApplied() ? '没有符合筛选条件的记录' : '还没有任何操作记录'"
              :hint="
                hasApplied()
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

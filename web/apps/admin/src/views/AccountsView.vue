<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'

import { formatTime, errorBanner, bannerMessage, backToTop, showTotal, readPage, readPageSize, readText, type ErrorBanner } from '@yggauth/shared'

import EmptyState from '../components/EmptyState.vue'
import CopyableText from '../components/CopyableText.vue'
import { useAdminStore } from '../stores/admin'
import ErrorAlert from '../components/ErrorAlert.vue'

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
const banner = ref<ErrorBanner | null>(null)

const query = reactive({ limit: 20, offset: 0, search: '' })

const route = useRoute()
const router = useRouter()

// 列表状态放进地址栏:刷新不丢,「查到这一屏」的链接也能直接发给同事。
// 写回用 replace 而不是 push —— 翻十页不该在浏览器历史里堆十条记录,
// 后退键仍然该是「离开这一页」。地址栏只留与默认值不同的项,链接才短。
// 两端空格吃掉:「tester 」查不到任何人,而用户多半只是多按了一下空格。
search.value = readText(route.query.q).trim()
page.value = readPage(route.query.page)
pageSize.value = readPageSize(route.query.size)
query.search = search.value
query.limit = pageSize.value
query.offset = (page.value - 1) * query.limit

/** 把当前列表状态写进地址栏。 */
function syncUrl(): void {
  const next: Record<string, string> = {}
  if (query.search) next.q = query.search
  if (page.value > 1) next.page = String(page.value)
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
    if (query.search) {
      params.set('search', query.search)
    }
    const data = await store.api.get<{ accounts: AccountRow[]; total: number }>(
      `/api/admin/accounts?${params.toString()}`,
    )
    items.value = data.accounts
    total.value = data.total
  } catch (err) {
    banner.value = errorBanner(err, '查询失败')
  } finally {
    loading.value = false
  }
}

function searchNow(): void {
  // 与读地址栏同样吃掉两端空格:搜索框里的「  zhang 」应当命中 zhang。
  query.search = search.value.trim()
  query.offset = 0
  page.value = 1
  // 换了一屏数据,视线却还停在底部的分页器旁 —— 先回顶再查。
  backToTop()
  void load()
}

function changePage(p: number, size: number): void {
  page.value = p
  pageSize.value = size
  query.limit = size
  query.offset = (p - 1) * size
  backToTop()
  void load()
}

async function update(id: string, patch: Record<string, unknown>): Promise<void> {
  banner.value = null
  try {
    await store.api.patch(`/api/admin/accounts/${id}`, patch)
    await load()
  } catch (err) {
    banner.value = errorBanner(err, '操作失败')
  }
}

// ---------------- 详情抽屉:改邮箱 + 角色授予/撤销

interface RoleSummary {
  id: string
  code: string
  name: string
}

const detailOpen = ref(false)
const detail = ref<AccountRow | null>(null)
const detailRoles = ref<RoleSummary[]>([])
/** 可授予的全部角色;拉不到时为空,角色区降级为只读。 */
const rolesPool = ref<RoleSummary[]>([])
const detailLoading = ref(false)
const detailBanner = ref<ErrorBanner | null>(null)
const detailSuccess = ref('')
const emailDraft = ref('')
const grantRoleID = ref<string>()
const acting = ref(false)

async function openDetail(row: AccountRow): Promise<void> {
  detail.value = row
  emailDraft.value = row.email
  detailOpen.value = true
  detailBanner.value = null
  detailSuccess.value = ''
  grantRoleID.value = undefined
  detailLoading.value = true
  try {
    const [mine, pool] = await Promise.all([
      store.api.get<{ roles: RoleSummary[] }>(`/api/admin/accounts/${row.id}/roles`),
      // 角色池需要 rbac:read。没有该权限的管理员只是不能授予新角色,
      // 已有角色仍要照常显示,所以这里不让它拖垮整个抽屉。
      store.api
        .get<{ roles: RoleSummary[] }>('/api/admin/roles')
        .catch(() => ({ roles: [] as RoleSummary[] })),
    ])
    detailRoles.value = mine.roles
    rolesPool.value = pool.roles
  } catch (err) {
    detailBanner.value = errorBanner(err, '查询失败')
  } finally {
    detailLoading.value = false
  }
}

/** 还没被授予的角色 —— 避免下拉里出现已经勾着的项。 */
const grantable = (): RoleSummary[] => {
  const held = new Set(detailRoles.value.map((r) => r.id))
  return rolesPool.value.filter((r) => !held.has(r.id))
}

async function saveEmail(): Promise<void> {
  // 防重入:回车连发会绕过按钮的 loading,一次改动会记两条审计。
  if (acting.value) return
  if (!detail.value) return
  const next = emailDraft.value.trim()
  if (!next) {
    detailBanner.value = bannerMessage('邮箱不能为空')
    return
  }
  if (next === detail.value.email) {
    detailBanner.value = bannerMessage('邮箱没有变化')
    return
  }
  acting.value = true
  detailBanner.value = null
  detailSuccess.value = ''
  try {
    // 后端改邮箱后会把该账号的全部登录态吊销,并重新要求验证邮箱
    await store.api.patch(`/api/admin/accounts/${detail.value.id}`, { email: next })
    detail.value.email = next
    detailSuccess.value = '邮箱已更新。该账号已退出全部设备,并需要重新验证邮箱。'
    await load()
  } catch (err) {
    detailBanner.value = errorBanner(err, '保存失败')
  } finally {
    acting.value = false
  }
}

async function grant(): Promise<void> {
  if (!detail.value || !grantRoleID.value) return
  const grantedID = grantRoleID.value
  acting.value = true
  detailBanner.value = null
  detailSuccess.value = ''
  try {
    await store.api.post('/api/admin/roles/grant', {
      account_id: detail.value.id,
      role_id: grantedID,
    })
    const granted = rolesPool.value.find((r) => r.id === grantedID)
    grantRoleID.value = undefined
    const mine = await store.api.get<{ roles: RoleSummary[] }>(
      `/api/admin/accounts/${detail.value.id}/roles`,
    )
    detailRoles.value = mine.roles
    detailSuccess.value = `已授予「${granted?.name ?? ''}」`
  } catch (err) {
    detailBanner.value = errorBanner(err, '授予失败')
  } finally {
    acting.value = false
  }
}

async function revoke(role: RoleSummary): Promise<void> {
  if (!detail.value) return
  acting.value = true
  detailBanner.value = null
  detailSuccess.value = ''
  try {
    await store.api.post('/api/admin/roles/revoke', {
      account_id: detail.value.id,
      role_id: role.id,
    })
    detailRoles.value = detailRoles.value.filter((r) => r.id !== role.id)
    detailSuccess.value = `已移除角色「${role.name}」`
  } catch (err) {
    detailBanner.value = errorBanner(err, '移除失败')
  } finally {
    acting.value = false
  }
}
</script>

<template>
  <div class="page">
    <p class="muted" style="margin-bottom: 16px">
      这里列出全部注册账号。可以按用户名或邮箱搜索,停用后该账号将无法登录。
    </p>

    <ErrorAlert :banner="banner" />

    <a-card>
      <a-space style="margin-bottom: 16px">
        <!--
          与筛选下拉一样给个 ×:搜索词多半是敲错了再改,现在只能全选删。
          清空只清输入框,不自动查询 —— 文字筛选的口径是回车/点按钮
          (每次敲键都发请求不可接受),见 docs/frontend.md「列表页通则」。
        -->
        <a-input-search
          v-model:value="search"
          placeholder="按用户名或邮箱搜索"
          style="width: 280px"
          allow-clear
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
        <a-table :scroll="{ x: 1200 }" :data-source="items" :pagination="false" row-key="id">
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
          <a-table-column key="actions" title="操作" :width="220">
            <template #default="{ record }">
              <a-space>
                <a-button size="small" @click="openDetail(record)">详情</a-button>
                <!--
                  标题要说清后果,别只问「确定吗」:中间件是**每个请求**都重新查一次
                  账号状态(internal/transport/auth.go SessionAuth),所以停用不只是
                  「以后登不进来」,正在用的登录态也会立刻全部失效 —— 这是停用前
                  就该知道的事,不是点了才发现的意外。
                -->
                <a-popconfirm
                  title="停用后该账号马上无法登录,现有登录态也会立刻失效,确定?"
                  @confirm="update(record.id, { status: 'disabled' })"
                >
                  <a-button v-if="record.status !== 'disabled'" danger size="small">停用</a-button>
                </a-popconfirm>
                <a-popconfirm
                  title="恢复后该账号可以重新登录,确定?"
                  @confirm="update(record.id, { status: 'active' })"
                >
                  <a-button v-if="record.status === 'disabled'" size="small">恢复</a-button>
                </a-popconfirm>
              </a-space>
            </template>
          </a-table-column>
        </a-table>
      </a-spin>

      <a-pagination
        :current="page"
        :page-size="pageSize"
        :total="total"
        :show-total="showTotal"
        show-size-changer
        style="margin-top: 16px; text-align: right"
        @change="changePage"
      />
    </a-card>

    <a-drawer
      v-model:open="detailOpen"
      :title="detail ? `${detail.username} 的账号详情` : '账号详情'"
      :width="480"
    >
      <a-spin :spinning="detailLoading">
        <a-alert
          v-if="detailBanner"
          type="error"
          :message="detailBanner"
          show-icon
          style="margin-bottom: 16px"
        />
        <a-alert
          v-if="detailSuccess"
          type="success"
          :message="detailSuccess"
          show-icon
          style="margin-bottom: 16px"
        />

        <template v-if="detail">
          <a-descriptions :column="1" bordered size="small" style="margin-bottom: 8px">
            <a-descriptions-item label="用户名">{{ detail.username }}</a-descriptions-item>
            <a-descriptions-item label="状态">
              <a-tag :color="detail.status === 'active' ? 'green' : 'orange'">
                {{ STATUS_LABEL[detail.status] ?? detail.status }}
              </a-tag>
            </a-descriptions-item>
            <a-descriptions-item label="邮箱验证">
              <a-tag :color="detail.email_verified ? 'green' : 'default'">
                {{ detail.email_verified ? '已验证' : '未验证' }}
              </a-tag>
            </a-descriptions-item>
            <a-descriptions-item label="注册时间">
              {{ formatTime(detail.created_at) }}
            </a-descriptions-item>
            <a-descriptions-item label="账号 ID">
              <!-- 账号 ID 是筛选审计、贴给同事的常用输入,手抄 36 位 UUID 是自找麻烦。 -->
              <CopyableText :value="detail.id" label="账号 ID" />
            </a-descriptions-item>
          </a-descriptions>

          <a-divider orientation="left">修改邮箱</a-divider>
          <!--
            回车即提交:antd 的 a-form 会把原生提交拦下来、只 emit 一个 'submit',
            没人接的话回车敲在输入框里毫无反应。接住它,回车就与点「保存邮箱」等价。
          -->
          <a-form layout="vertical" @submit.prevent="saveEmail">
            <a-form-item>
              <a-input v-model:value="emailDraft" type="email" autocomplete="email" placeholder="new@example.com" />
            </a-form-item>
            <a-alert
              type="info"
              show-icon
              message="保存后该账号会退出全部设备,并需要验证新邮箱。"
              style="margin-bottom: 12px"
            />
            <a-button type="primary" :loading="acting" html-type="submit">保存邮箱</a-button>
          </a-form>

          <a-divider orientation="left">角色</a-divider>
          <a-spin :spinning="acting">
            <div v-if="detailRoles.length === 0" class="muted" style="margin-bottom: 12px">
              还没有授予任何角色,该账号只有普通用户的权限。
            </div>

            <div v-for="role in detailRoles" :key="role.id" class="role-row">
              <span>
                <strong>{{ role.name }}</strong>
                <span class="muted"> · {{ role.code }}</span>
              </span>
              <!-- 权限变更即刻生效,误点会让人当场失去访问,所以确认一次 -->
              <a-popconfirm
                title="移除后该角色的权限立即失效,确定?"
                placement="left"
                @confirm="revoke(role)"
              >
                <a-button size="small" danger>移除</a-button>
              </a-popconfirm>
            </div>

            <a-space style="margin-top: 16px" align="start">
              <a-select
                v-model:value="grantRoleID"
                :options="grantable().map((r) => ({ label: `${r.name} (${r.code})`, value: r.id }))"
                placeholder="选择要授予的角色"
                style="width: 260px"
                allow-clear
              />
              <a-button type="primary" :disabled="!grantRoleID" :loading="acting" @click="grant">
                授予
              </a-button>
            </a-space>

            <div v-if="rolesPool.length === 0" class="muted" style="margin-top: 12px">
              未加载到可授予的角色列表 —— 当前管理员可能没有角色管理权限,
              这里只能查看与移除。
            </div>
          </a-spin>
        </template>
      </a-spin>
    </a-drawer>
  </div>
</template>

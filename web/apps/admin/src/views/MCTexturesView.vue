<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'

import { formatTime, errorBanner, backToTop, showTotal, readPage, readPageSize, readText, type ErrorBanner } from '@yggauth/shared'

import EmptyState from '../components/EmptyState.vue'
import { useAdminStore } from '../stores/admin'
import ErrorAlert from '../components/ErrorAlert.vue'

interface Texture {
  id: string
  hash: string
  type: 'skin' | 'cape'
  size: number
  width: number
  height: number
  ref_count: number
  created_at: string
}

const store = useAdminStore()
const items = ref<Texture[]>([])
const total = ref(0)
const page = ref(1)
const pageSize = ref(20)
/** 空串表示不过滤。 */
const kind = ref<'all' | 'skin' | 'cape'>('all')
const loading = ref(false)
const banner = ref<ErrorBanner | null>(null)

const query = reactive({ limit: 20, offset: 0, kind: '' })

const route = useRoute()
const router = useRouter()

// 类型筛选与页码放进地址栏:刷新不丢,「只看披风」这样的链接也能直接分享。
const routeKind = readText(route.query.kind)
kind.value = routeKind === 'skin' || routeKind === 'cape' ? routeKind : 'all'
page.value = readPage(route.query.page)
pageSize.value = readPageSize(route.query.size)
query.kind = kind.value === 'all' ? '' : kind.value
query.limit = pageSize.value
query.offset = (page.value - 1) * query.limit

/** 把当前列表状态写进地址栏。 */
function syncUrl(): void {
  const next: Record<string, string> = {}
  if (kind.value !== 'all') next.kind = kind.value
  if (page.value > 1) next.page = String(page.value)
  if (query.limit !== 20) next.size = String(query.limit)
  void router.replace({ query: next })
}

const KIND_LABEL: Record<string, string> = { skin: '皮肤', cape: '披风' }

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
    if (query.kind) {
      params.set('kind', query.kind)
    }
    const data = await store.api.get<{ textures: Texture[]; total: number }>(
      `/api/admin/mc/textures?${params.toString()}`,
    )
    items.value = data.textures
    total.value = data.total
  } catch (err) {
    banner.value = errorBanner(err, '查询失败')
  } finally {
    loading.value = false
  }
}

function filter(next: 'all' | 'skin' | 'cape'): void {
  kind.value = next
  query.kind = next === 'all' ? '' : next
  query.offset = 0
  page.value = 1
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

/** 表里给的是字节数,看 KB 比看 2097152 直观得多。 */
function humanSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`
  return `${(bytes / 1024 / 1024).toFixed(2)} MB`
}

</script>

<template>
  <div class="page">
    <p class="muted" style="margin-bottom: 16px">
      玩家上传过的皮肤与披风,按内容哈希去重,同一份文件只存一份。
      「引用数」是正在使用这份文件的档案数量,为 0 说明已经没人用了。
    </p>

    <ErrorAlert :banner="banner" />

    <a-card>
      <a-space style="margin-bottom: 16px" align="center">
        <span class="muted">类型:</span>
        <a-radio-group
          v-model:value="kind"
          button-style="solid"
          @change="filter(kind)"
        >
          <a-radio-button value="all">全部</a-radio-button>
          <a-radio-button value="skin">皮肤</a-radio-button>
          <a-radio-button value="cape">披风</a-radio-button>
        </a-radio-group>
      </a-space>

      <a-spin :spinning="loading">
        <a-table :scroll="{ x: 980 }" :data-source="items" :pagination="false" row-key="id">
          <template #emptyText>
            <EmptyState
              :description="query.kind ? '没有这类材质' : '材质库还是空的'"
              :hint="
                query.kind
                  ? '换个类型看看,或者切回「全部」。'
                  : '玩家在账号站的「皮肤管理」里上传皮肤或披风后,文件会出现在这里。'
              "
            />
          </template>
          <a-table-column key="type" data-index="type" title="类型" :width="90">
            <template #default="{ record }">
              <a-tag :color="record.type === 'skin' ? 'blue' : 'orange'">
                {{ KIND_LABEL[record.type] ?? record.type }}
              </a-tag>
            </template>
          </a-table-column>
          <a-table-column key="size" title="尺寸" :width="110">
            <template #default="{ record }">{{ record.width }} × {{ record.height }}</template>
          </a-table-column>
          <a-table-column key="bytes" title="大小" :width="110">
            <template #default="{ record }">{{ humanSize(record.size) }}</template>
          </a-table-column>
          <a-table-column key="ref_count" data-index="ref_count" title="引用数" :width="90" />
          <a-table-column key="created_at" data-index="created_at" title="上传时间" :width="170">
            <template #default="{ record }">{{ formatTime(record.created_at) }}</template>
          </a-table-column>
          <a-table-column key="hash" data-index="hash" title="内容哈希">
            <template #default="{ record }">
              <code style="word-break: break-all">{{ record.hash }}</code>
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
  </div>
</template>

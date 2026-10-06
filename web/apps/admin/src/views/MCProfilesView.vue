<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'

import { formatTime, errorBanner, backToTop, showTotal, readPage, readPageSize, readText, type ErrorBanner } from '@yggauth/shared'

import EmptyState from '../components/EmptyState.vue'
import { useAdminStore } from '../stores/admin'
import ErrorAlert from '../components/ErrorAlert.vue'

interface Profile {
  id: string
  /** 玩家在 Minecraft 侧看到的 UUID,与账号 ID 不是一回事。 */
  uuid: string
  current_name: string
  created_at: string
  has_skin: boolean
  has_cape: boolean
}

const store = useAdminStore()
const items = ref<Profile[]>([])
const total = ref(0)
const page = ref(1)
const pageSize = ref(20)
const search = ref('')
const loading = ref(false)
const banner = ref<ErrorBanner | null>(null)

// 与账号列表同一套参数,分页与搜索的交互完全一致。
const query = reactive({ limit: 20, offset: 0, search: '' })

const route = useRoute()
const router = useRouter()

// 搜索词与页码放进地址栏:刷新不丢,链接也能直接分享(与账号列表同一套做法)。
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
    const data = await store.api.get<{ profiles: Profile[]; total: number }>(
      `/api/admin/mc/profiles?${params.toString()}`,
    )
    items.value = data.profiles
    total.value = data.total
  } catch (err) {
    banner.value = errorBanner(err, '查询失败')
  } finally {
    loading.value = false
  }
}

function searchNow(): void {
  query.search = search.value.trim()
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
</script>

<template>
  <div class="page">
    <p class="muted" style="margin-bottom: 16px">
      玩家第一次在游戏里用本服务登录时会自动建档,一个账号对应一份档案。
      改名由玩家本人在账号站的「皮肤管理」里完成,这里只做查询。
    </p>

    <ErrorAlert :banner="banner" />

    <a-card>
      <a-space style="margin-bottom: 16px">
        <!-- 与账号管理的搜索框保持同一种交互:敲错了能一键清掉,不用全选删。 -->
        <a-input-search
          v-model:value="search"
          placeholder="按玩家名搜索"
          style="width: 280px"
          allow-clear
          @search="searchNow"
        />
        <a-button type="primary" @click="searchNow">搜索</a-button>
      </a-space>

      <a-spin :spinning="loading">
        <a-table :scroll="{ x: 900 }" :data-source="items" :pagination="false" row-key="id">
          <template #emptyText>
            <EmptyState
              :description="query.search ? '没有匹配的玩家档案' : '还没有玩家档案'"
              :hint="
                query.search
                  ? '换个玩家名关键词试试;搜索是子串匹配,不用写全名。'
                  : '还没有人在游戏里用本服务登录过。'
              "
            />
          </template>
          <a-table-column key="current_name" data-index="current_name" title="玩家名" />
          <a-table-column key="uuid" data-index="uuid" title="UUID" :width="300">
            <template #default="{ record }">
              <code>{{ record.uuid }}</code>
            </template>
          </a-table-column>
          <a-table-column key="has_skin" title="皮肤">
            <template #default="{ record }">
              <a-tag :color="record.has_skin ? 'green' : 'default'">
                {{ record.has_skin ? '已上传' : '未上传' }}
              </a-tag>
            </template>
          </a-table-column>
          <a-table-column key="has_cape" title="披风">
            <template #default="{ record }">
              <a-tag :color="record.has_cape ? 'green' : 'default'">
                {{ record.has_cape ? '已上传' : '未上传' }}
              </a-tag>
            </template>
          </a-table-column>
          <a-table-column key="created_at" data-index="created_at" title="建档时间">
            <template #default="{ record }">{{ formatTime(record.created_at) }}</template>
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

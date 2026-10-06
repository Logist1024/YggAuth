<script setup lang="ts">
import { onMounted, ref } from 'vue'

import { formatTime, errorBanner, type ErrorBanner } from '@yggauth/shared'

import { useAdminStore } from '../stores/admin'
import EmptyState from '../components/EmptyState.vue'
import ErrorAlert from '../components/ErrorAlert.vue'

interface SigningKey {
  kid: string
  algo: string
  status: string
  created_at: string
  retired_at: string | null
}

const store = useAdminStore()
const items = ref<SigningKey[]>([])
const loading = ref(false)
const banner = ref<ErrorBanner | null>(null)
const rotating = ref(false)
/**
 * 最近一次轮换出的 kid。
 *
 * 轮换的全部可见结果就是列表多一行 —— 表格一长、新行还在最上面,
 * 用户很容易以为按钮没点动,于是再点一次,平白多换一把密钥。
 */
const rotated = ref('')

/** 状态列的显示名。后端存的是枚举值,不该直接甩给用户看。 */
const STATUS_LABEL: Record<string, string> = {
  active: '生效中',
  retired: '已退役',
}

onMounted(load)

async function load(): Promise<void> {
  loading.value = true
  try {
    const data = await store.api.get<{ items: SigningKey[] }>('/api/admin/signing-keys')
    items.value = data.items
  } catch (err) {
    banner.value = errorBanner(err, '查询失败')
  } finally {
    loading.value = false
  }
}

async function rotate(): Promise<void> {
  banner.value = null
  rotating.value = true
  try {
    const data = await store.api.post<{ kid: string }>('/api/admin/signing-keys/rotate', {})
    rotated.value = data.kid
    await load()
  } catch (err) {
    banner.value = errorBanner(err, '轮换失败')
  } finally {
    rotating.value = false
  }
}
</script>

<template>
  <div class="page">
    <ErrorAlert :banner="banner" />
    <!-- 轮换成功的回执:新 kid 是接入方要抄的值,顺手给出来 -->
    <a-alert
      v-if="rotated"
      type="success"
      show-icon
      closable
      style="margin-bottom: 16px"
      :message="`轮换完成,新密钥的 kid 是 ${rotated}。`"
      description="它只影响此后新签发的令牌;依赖固定 kid 的接入方需要同步更新。"
      @close="rotated = ''"
    />
    <a-alert
      type="info"
      show-icon
      style="margin-bottom: 16px"
      message="轮换只影响新签发的令牌。已发出的令牌仍用旧密钥验签,所以旧密钥不会被删除。"
    />

    <a-card title="签名密钥">
      <template #extra>
        <!--
          轮换是全局动作:之后新签发的令牌都改用新 kid,
          依赖固定 kid 的接入方会突然验签失败。一次误点的
          代价太大,必须让人明确确认。
        -->
        <a-popconfirm
          title="轮换后新签发的令牌改用新 kid,依赖固定 kid 的接入方需要同步更新。确定?"
          @confirm="rotate"
        >
          <a-button type="primary" :loading="rotating">轮换密钥</a-button>
        </a-popconfirm>
      </template>
      <a-spin :spinning="loading">
        <a-table :scroll="{ x: 1200 }" :data-source="items" row-key="kid" :pagination="false">
          <template #emptyText>
            <EmptyState
              description="还没有签名密钥"
              hint="第一次需要签发令牌或下发 JWKS 时会自动创建,也可以直接轮换生成一把。"
            />
          </template>
          <!-- data-index 不能省,同 AccountsView:只写 key 会渲染出空单元格 -->
          <a-table-column key="kid" data-index="kid" title="kid" />
          <a-table-column key="algo" data-index="algo" title="算法" />
          <a-table-column key="status" title="状态">
            <template #default="{ record }">
              <a-tag :color="record.status === 'active' ? 'green' : 'default'">
                {{ STATUS_LABEL[record.status] ?? record.status }}
              </a-tag>
            </template>
          </a-table-column>
          <a-table-column key="created_at" title="创建时间">
            <template #default="{ record }">{{ formatTime(record.created_at) }}</template>
          </a-table-column>
          <a-table-column key="retired_at" title="退役时间">
            <template #default="{ record }">
              {{ record.retired_at ? formatTime(record.retired_at) : '—' }}
            </template>
          </a-table-column>
        </a-table>
      </a-spin>
    </a-card>
  </div>
</template>

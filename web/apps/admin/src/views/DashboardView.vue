<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'

import { ApiError } from '@yggauth/shared'

import { useAdminStore } from '../stores/admin'

interface Stats {
  accounts_total: number
  /** 近 7 天新增账号数。 */
  accounts_recent: number
  roles_total: number
  permissions_total: number
  audit_events_24h: number
  oidc_clients_total: number
  mc_servers_total: number
}

interface Card {
  title: string
  value: number
  /** 这个数字是什么意思。光有数字的仪表盘等于没有仪表盘。 */
  hint: string
}

interface Todo {
  title: string
  detail: string
  to: string
  action: string
}

const store = useAdminStore()
const stats = ref<Stats | null>(null)
const error = ref('')

/**
 * 卡片排成两列而不是四列。
 *
 * `.page` 的 max-width 是 960px,四列时每张卡内宽只有约 180px,
 * 一行说明文字必然折成「…新增 1 / 个」这种带孤字的换行。
 * 两列换来的是能写清楚每个数字的含义 —— 那才是这几张卡的价值所在。
 */
const cards = computed<Card[]>(() => {
  const s = stats.value
  if (!s) {
    return []
  }
  return [
    {
      title: '账号总数',
      value: s.accounts_total,
      hint: `全部注册账号,其中近 7 天新增 ${s.accounts_recent} 个。`,
    },
    {
      title: '接入应用',
      value: s.oidc_clients_total,
      hint: '已登记的业务系统(OIDC 客户端)。接入后业务系统才能用本服务登录。',
    },
    {
      title: 'MC 服务器',
      value: s.mc_servers_total,
      hint: '已登记的 Minecraft 服务器。服务器首次通过本服务校验时会自动登记。',
    },
    {
      title: '近 24 小时审计事件',
      value: s.audit_events_24h,
      hint: '登录、改密、权限变更等操作记录,可在「安全与审计」里检索。',
    },
  ]
})

/**
 * 下一步只列**确实还没做、而且有地方可做**的项。
 *
 * 两条规矩:计数不为 0 的不列(已完成的检查项会变成背景噪音,
 * 用户第二次就不再看了);没有落地页的不列(一个点不动的待办
 * 比不显示更让人困惑)。
 */
const todos = computed<Todo[]>(() => {
  const s = stats.value
  if (!s) {
    return []
  }
  const out: Todo[] = []
  if (s.accounts_total === 0) {
    out.push({
      title: '还没有任何账号',
      detail: '注册入口当前是开放的,用户可自行注册;也可以生成邀请码定向邀请。',
      to: '/invitations',
      action: '生成邀请码',
    })
  }
  if (s.oidc_clients_total === 0) {
    out.push({
      title: '还没有接入业务系统',
      detail: '创建 OIDC 客户端之后,业务系统才能用本服务登录。',
      to: '/clients',
      action: '创建客户端',
    })
  }
  return out
})

onMounted(async () => {
  try {
    stats.value = await store.api.get<Stats>('/api/admin/dashboard')
  } catch (err) {
    error.value = err instanceof ApiError ? err.message : '查询失败'
  }
})
</script>

<template>
  <div class="page">
    <a-alert v-if="error" type="error" :message="error" show-icon style="margin-bottom: 16px" />

    <p class="muted" style="margin-bottom: 16px">
      这里是当前部署的运行概览。下面几项如果显示为 0,页面底部会给出下一步该做什么。
    </p>

    <a-row v-if="stats" :gutter="16">
      <a-col v-for="c in cards" :key="c.title" :span="12">
        <a-card style="margin-bottom: 16px">
          <a-statistic :title="c.title" :value="c.value" />
          <p class="muted" style="margin: 8px 0 0">{{ c.hint }}</p>
        </a-card>
      </a-col>
    </a-row>

    <a-card v-if="stats" title="下一步">
      <template v-if="todos.length > 0">
        <div v-for="t in todos" :key="t.title" class="todo">
          <div>
            <div style="font-weight: 500">{{ t.title }}</div>
            <div class="muted">{{ t.detail }}</div>
          </div>
          <RouterLink :to="t.to">
            <a-button type="primary">{{ t.action }}</a-button>
          </RouterLink>
        </div>
      </template>
      <a-empty v-else description="基础配置已完成。日常操作可以在左侧对应页面进行。" />
    </a-card>
  </div>
</template>

<style scoped>
.todo {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  padding: 12px 0;
  border-bottom: 1px solid #f0f0f0;
}

.todo:last-child {
  border-bottom: none;
}
</style>

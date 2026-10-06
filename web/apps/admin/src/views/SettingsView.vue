<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'

import { errorBanner, bannerMessage, type ErrorBanner } from '@yggauth/shared'

import { useAdminStore } from '../stores/admin'
import ErrorAlert from '../components/ErrorAlert.vue'

/**
 * 配置项的展示元数据。
 *
 * 后端 GET /api/admin/settings 返回的是**键 → 值**的映射(见
 * internal/admin/handler.go 的 GetSettings),不带标签、说明,也不带类型。
 * 但输入控件的形态、以及提交时该用哪种 JSON 类型,都必须由类型决定 ——
 * 把 900 当字符串发回去,后端按整数读就会失败。所以这张表是必需的。
 *
 * 下表覆盖 db/migrations/00004_init_app.sql 里 INSERT 的**全部** 12 个键。
 * 那次迁移是权威清单:后台只是覆盖值,不会再新增键。
 *
 * 未登记的键不会消失:按**值的实际类型**推断控件(见 inferType),
 * 只是没有中文标签。
 */
interface SettingMeta {
  label: string
  description: string
  type: 'number' | 'boolean' | 'enum' | 'text'
  /** type 为 enum 时的可选项。 */
  options?: { label: string; value: string }[]
}

const META: Record<string, SettingMeta> = {
  'login.max_failed_attempts': {
    label: '登录失败次数上限',
    description: '连续失败达到该次数后锁定账号。',
    type: 'number',
  },
  'login.lock_seconds': {
    label: '登录锁定时长(秒)',
    description: '触发锁定后账号不可登录的时长。',
    type: 'number',
  },
  'password.min_length': {
    label: '密码最小长度',
    description: '注册与改密时的最小字符数。',
    type: 'number',
  },
  'password.max_length': {
    label: '密码最大长度',
    description: '上限过大等于变相允许超长密码拖慢哈希计算。',
    type: 'number',
  },
  'password.reject_common': {
    label: '拒绝常见弱口令',
    description: '开启后拒绝内置弱口令表中的密码。',
    type: 'boolean',
  },
  'registration.mode': {
    label: '注册模式',
    description: 'open 允许任何人注册;invite_only 需要邀请码;closed 关闭注册入口。',
    type: 'enum',
    options: [
      { label: '开放注册', value: 'open' },
      { label: '需要邀请码', value: 'invite_only' },
      { label: '关闭注册', value: 'closed' },
    ],
  },
  'registration.mc_login_default': {
    label: '新账号默认开放 MC 登录',
    description: '新注册账号的 MC 登录开关初始值。',
    type: 'boolean',
  },
  'session.idle_ttl_hours': {
    label: '会话空闲有效期(小时)',
    description: '超过该时长未活动,登录态失效。',
    type: 'number',
  },
  'session.max_ttl_hours': {
    label: '会话最长有效期(小时)',
    description: '无论是否活跃,超过该时长都必须重新登录。',
    type: 'number',
  },
  'mail.verify_cooldown_seconds': {
    label: '重发验证邮件冷却(秒)',
    description: '两次重发验证邮件之间的最小间隔。',
    type: 'number',
  },
  'mail.verify_daily_limit': {
    label: '验证邮件每日上限',
    description: '同一账号每天最多收到的验证邮件数。',
    type: 'number',
  },
  'mc.name_retention_days': {
    label: 'MC 旧用户名保留天数',
    description: '改名后旧用户名被保留、禁止他人注册的天数。',
    type: 'number',
  },
}

const BOOL_OPTIONS = [
  { label: '开启', value: 'true' },
  { label: '关闭', value: 'false' },
]

const store = useAdminStore()
/** 后端原样返回的配置值,用于判断草稿是否真的改动过。 */
const values = ref<Record<string, unknown>>({})
/** 编辑草稿。统一用字符串承载,提交时按类型转换。 */
const drafts = ref<Record<string, string>>({})
const loading = ref(false)
const banner = ref<ErrorBanner | null>(null)
const saved = ref('')
/** 正在保存的那一行的键。一行一个表单,所以按行记,而不是一个全局布尔。 */
const savingKey = ref('')

/**
 * 未登记的键按值类型推断控件。
 *
 * 此前回落是写死的 'number':一个手工插入的布尔键会得到数字输入框,
 * 而字符串键会 —— 因为模板的 v-else 兜底是 enum —— 渲染成一个
 * **没有选项**的空下拉,用户根本无从修改。两处都属于「保存不出来的设置」。
 */
function inferType(value: unknown): SettingMeta['type'] {
  if (typeof value === 'boolean') return 'boolean'
  if (typeof value === 'number') return 'number'
  return 'text'
}

const rows = computed(() =>
  Object.keys(values.value)
    .sort()
    .map((key) => {
      const meta = META[key]
      return {
        key,
        raw: values.value[key],
        meta: meta ?? {
          label: key,
          description: '',
          type: inferType(values.value[key]),
        },
      }
    }),
)

onMounted(load)

async function load(): Promise<void> {
  loading.value = true
  try {
    const data = await store.api.get<{ settings: Record<string, unknown> }>('/api/admin/settings')
    values.value = data.settings ?? {}
    drafts.value = Object.fromEntries(
      Object.entries(values.value).map(([k, v]) => [k, String(v)]),
    )
  } catch (err) {
    banner.value = errorBanner(err, '查询失败')
  } finally {
    loading.value = false
  }
}

/** 草稿与后端值是否不一致。用来决定保存按钮能不能点。 */
function dirty(key: string): boolean {
  return drafts.value[key] !== String(values.value[key])
}

/** 按元数据里的类型把草稿转成真正的 JSON 值。 */
function coerce(key: string, type: SettingMeta['type']): unknown {
  const raw = drafts.value[key] ?? ''
  if (type === 'boolean') {
    return raw === 'true'
  }
  if (type === 'number') {
    // 进入这里前已被 save 的校验拦过,Number 一定有界
    return Number(raw)
  }
  return raw
}

/** 数字字段的错误文案;空串表示可以提交。 */
function numberError(raw: string): string {
  if (raw.trim() === '') return '不能为空'
  if (!Number.isFinite(Number(raw))) return '必须是数字'
  return ''
}

async function save(key: string, type: SettingMeta['type']): Promise<void> {
  // 防重入:按钮的 loading 挡得住点击,挡不住回车连发 —— 同一条配置会被
  // PATCH 两次,在审计日志里留下两条一模一样的「修改」。
  if (savingKey.value === key) return
  savingKey.value = key
  banner.value = null
  saved.value = ''

  const label = META[key]?.label ?? key
  if (type === 'number') {
    const err = numberError(drafts.value[key] ?? '')
    if (err) {
      banner.value = bannerMessage(`${label}${err}`)
      savingKey.value = ''
      return
    }
  }

  try {
    // 请求体是「配置键 → 值」的映射,不是 { key, value }。
    // 发成后者的话,后端会把字面量 "key" 和 "value" 当成两个配置名写进表里 ——
    // 不报错,但表里多出两条垃圾记录。
    await store.api.patch('/api/admin/settings', { [key]: coerce(key, type) })
    saved.value = `${label} 已保存`
    await load()
  } catch (err) {
    banner.value = errorBanner(err, '保存失败')
  } finally {
    savingKey.value = ''
  }
}
</script>

<template>
  <div class="page">
    <p class="muted" style="margin-bottom: 16px">
      这些配置覆盖代码里的默认值,保存后立即生效。修改会被记入审计日志。
    </p>

    <ErrorAlert :banner="banner" />
    <a-alert v-if="saved" type="success" :message="saved" show-icon style="margin-bottom: 16px" />

    <a-spin :spinning="loading">
      <a-empty v-if="rows.length === 0" description="没有可配置项" />

      <a-card
        v-for="row in rows"
        :key="row.key"
        :title="row.meta.label"
        style="margin-bottom: 16px"
      >
        <p v-if="row.meta.description" class="muted" style="margin-bottom: 12px">
          {{ row.meta.description }}
        </p>

        <!--
          一行一个表单:主按钮是 html-type="submit",回车即保存,
          与其余页面的写法一致(见 docs/07-frontend.md「表单提交」)。
          没改动时保存按钮是禁用的 —— 浏览器对唯一的禁用提交按钮不触发
          隐式提交,所以空回车不会打出一条「不能为空」的横幅。
        -->
        <a-form @submit.prevent="save(row.key, row.meta.type)">
          <a-space>
            <a-input
              v-if="row.meta.type === 'number'"
              v-model:value="drafts[row.key]"
              type="number"
              style="width: 200px"
            />
            <a-select
              v-else-if="row.meta.type === 'boolean'"
              v-model:value="drafts[row.key]"
              :options="BOOL_OPTIONS"
              style="width: 200px"
            />
            <a-select
              v-else-if="row.meta.type === 'enum'"
              v-model:value="drafts[row.key]"
              :options="row.meta.options ?? []"
              style="width: 200px"
            />
            <!--
              兜底必须是可输入的文本框。落到空的 a-select 上时,
              用户面对一个没有任何选项、也点不开的下拉 —— 这项设置就等于改不了。
            -->
            <a-input v-else v-model:value="drafts[row.key]" style="width: 200px" />
            <a-button type="primary" html-type="submit" :loading="savingKey === row.key" :disabled="!dirty(row.key)">
              保存
            </a-button>
          </a-space>
        </a-form>

        <p class="muted" style="margin-top: 8px">配置键:{{ row.key }}</p>
      </a-card>
    </a-spin>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'

import { errorBanner, bannerMessage, type ErrorBanner } from '@yggauth/shared'

import { useAdminStore } from '../stores/admin'
import ErrorAlert from '../components/ErrorAlert.vue'

/**
 * 配置键的定义 —— 由后端 `GET /api/admin/settings` 的 `schema` 下发。
 *
 * 这里**不再手抄一张元数据表**:前端原先有一张 META,列了迁移 00004 里的
 * 12 个键;后端加一个键,前端不改就显示不出来,改了标签也不会同步。
 * 规则只有一份真源(后端登记表 internal/config/keys.go),前端只负责渲染。
 */
interface SettingSchema {
  key: string
  type: 'int' | 'bool' | 'string' | 'enum' | 'secret'
  title: string
  min?: number
  max?: number
  enum?: { value: string; label: string }[]
  unit?: string
  hint?: string
}

interface Row {
  key: string
  schema: SettingSchema | null
  value: unknown
  /** 库里有、但后端登记表不认的键(历史遗留)。只读展示,不能保存。 */
  unregistered: boolean
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
const schema = ref<SettingSchema[]>([])
const loading = ref(false)
const banner = ref<ErrorBanner | null>(null)
const saved = ref('')
/** 正在保存的那一行的键。一行一个表单,所以按行记,而不是一个全局布尔。 */
const savingKey = ref('')

const rows = computed<Row[]>(() => {
  const known = new Set(schema.value.map((s) => s.key))
  const registered: Row[] = schema.value.map((s) => ({
    key: s.key,
    schema: s,
    // 库里没有这一行时用空串:否则 String(undefined) = "undefined"
    // 会让「没值」看起来像一个真实的值。
    value: values.value[s.key] ?? '',
    unregistered: false,
  }))
  const extras: Row[] = Object.keys(values.value)
    .filter((k) => !known.has(k))
    .sort()
    .map((k) => ({ key: k, schema: null, value: values.value[k], unregistered: true }))
  // 后端没返回值的登记键照样要显示(值缺失 = 用代码默认值),
  // 否则「新加的配置项」会整行消失,像没生效一样。
  return [...registered, ...extras]
})

/** 控件类型:登记键按 schema,未登记的键按值的实际类型推断。 */
function controlType(row: Row): 'int' | 'bool' | 'string' | 'enum' | 'secret' {
  if (row.schema) return row.schema.type
  if (typeof row.value === 'boolean') return 'bool'
  if (typeof row.value === 'number') return 'int'
  return 'string'
}

onMounted(load)

async function load(): Promise<void> {
  loading.value = true
  try {
    const data = await store.api.get<{
      settings: Record<string, unknown>
      schema?: SettingSchema[]
    }>('/api/admin/settings')
    values.value = data.settings ?? {}
    schema.value = data.schema ?? []
    drafts.value = Object.fromEntries(
      Object.entries(values.value).map(([k, v]) => [k, v == null ? '' : String(v)]),
    )
    // 敏感值的草稿从**空**开始:空 = 不修改。
    // 后端恒回显掩码,如果拿掩码当草稿值,用户不改也会被当成「改成一串星号」。
    for (const s of schema.value) {
      if (s.type === 'secret') drafts.value[s.key] = ''
    }
  } catch (err) {
    banner.value = errorBanner(err, '查询失败')
  } finally {
    loading.value = false
  }
}

/** 草稿与后端值是否不一致。用来决定保存按钮能不能点。 */
function dirty(row: Row): boolean {
  const draft = drafts.value[row.key] ?? ''
  if (controlType(row) === 'secret') return draft !== ''
  return draft !== String(row.value)
}

/** 整数字段的错误文案;空串表示可以提交。 */
function intError(raw: string, spec?: SettingSchema | null): string {
  const value = raw.trim()
  if (value === '') return '不能为空'
  const n = Number(value)
  if (!Number.isFinite(n) || !Number.isInteger(n)) return '必须是整数'
  if (spec?.min != null && n < spec.min) return `不能小于 ${spec.min}`
  if (spec?.max != null && n > spec.max) return `不能大于 ${spec.max}`
  return ''
}

/** 按类型把草稿转成真正的 JSON 值。 */
function coerce(row: Row): unknown {
  const type = controlType(row)
  const raw = drafts.value[row.key] ?? ''
  if (type === 'bool') return raw === 'true'
  if (type === 'int') return Number(raw)
  return raw
}

async function save(row: Row): Promise<void> {
  // 防重入:按钮的 loading 挡得住点击,挡不住回车连发 —— 同一条配置会被
  // PATCH 两次,在审计日志里留下两条一模一样的「修改」。
  if (savingKey.value === row.key) return
  if (row.unregistered) return
  savingKey.value = row.key
  banner.value = null
  saved.value = ''

  const type = controlType(row)
  const label = row.schema?.title ?? row.key
  if (type === 'int') {
    const err = intError(drafts.value[row.key] ?? '', row.schema)
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
    await store.api.patch('/api/admin/settings', { [row.key]: coerce(row) })
    saved.value = `${label} 已保存`
    await load()
  } catch (err) {
    banner.value = errorBanner(err, '保存失败')
  } finally {
    savingKey.value = ''
  }
}

// ---------------------------------------------------------------- 发件测试

interface TestMailResult {
  transport: string
  stage: 'connect' | 'tls' | 'auth' | 'send'
  ok: boolean
  error?: string
  note?: string
}

/** 阶段的中文名:后端只给机器值,给管理员看要换成能读懂的词。 */
const STAGE_LABELS: Record<string, string> = {
  connect: '连接服务器',
  tls: 'TLS 握手',
  auth: '账号认证',
  send: '投递邮件',
}

const testTo = ref('')
const testing = ref(false)
const testResult = ref<TestMailResult | null>(null)

/**
 * 发一封测试信(docs/configuration.md §7.4)。
 *
 * 没有它,「发件邮箱配错了」的唯一发现方式是等下一个注册用户 ——
 * 而错误精确到阶段,管理员才知道该改什么:认证失败是账号密码,
 * 连接失败多半是地址,握手失败通常是端口(465 要勾隐式 TLS)。
 * 用的是**已保存的**配置:拿草稿去测,会测出一个跟真实生效无关的结果。
 */
async function sendTestMail(): Promise<void> {
  if (testing.value) return
  testing.value = true
  testResult.value = null
  banner.value = null
  try {
    testResult.value = await store.api.post<TestMailResult>('/api/admin/settings/mail/test', {
      to: testTo.value.trim(),
    })
  } catch (err) {
    banner.value = errorBanner(err, '发送测试信失败')
  } finally {
    testing.value = false
  }
}
</script>

<template>
  <div class="page">
    <p class="muted" style="margin-bottom: 16px">
      这些配置覆盖 .env 里的种子值,保存后立即生效、无需重启。修改会被记入审计日志。
    </p>

    <ErrorAlert :banner="banner" />
    <a-alert v-if="saved" type="success" :message="saved" show-icon style="margin-bottom: 16px" />

    <a-spin :spinning="loading">
      <a-empty v-if="rows.length === 0" description="没有可配置项" />

      <a-card
        v-for="row in rows"
        :key="row.key"
        :title="row.schema?.title ?? row.key"
        style="margin-bottom: 16px"
      >
        <p v-if="row.schema?.hint" class="muted" style="margin-bottom: 12px">
          {{ row.schema.hint }}
        </p>

        <!--
          一行一个表单:主按钮是 html-type="submit",回车即保存,
          与其余页面的写法一致(见 docs/frontend.md「表单提交」)。
          没改动时保存按钮是禁用的 —— 浏览器对唯一的禁用提交按钮不触发
          隐式提交,所以空回车不会打出一条「不能为空」的横幅。
        -->
        <a-form @submit.prevent="save(row)">
          <a-space>
            <template v-if="controlType(row) === 'int'">
              <a-input
                v-model:value="drafts[row.key]"
                type="number"
                :min="row.schema?.min"
                :max="row.schema?.max"
                style="width: 200px"
              />
              <span v-if="row.schema?.unit" class="muted">{{ row.schema.unit }}</span>
            </template>

            <a-select
              v-else-if="controlType(row) === 'bool'"
              v-model:value="drafts[row.key]"
              :options="BOOL_OPTIONS"
              style="width: 200px"
            />

            <a-select
              v-else-if="controlType(row) === 'enum'"
              v-model:value="drafts[row.key]"
              :options="row.schema?.enum ?? []"
              style="width: 200px"
            />

            <!--
              敏感值:草稿从空开始,留空表示不修改。
              后端 GET 只回显掩码,所以这里永远不会出现真实密码。
            -->
            <a-input
              v-else-if="controlType(row) === 'secret'"
              v-model:value="drafts[row.key]"
              type="password"
              placeholder="留空表示不修改"
              style="width: 200px"
            />

            <a-input v-else v-model:value="drafts[row.key]" style="width: 200px" />

            <a-button
              v-if="!row.unregistered"
              type="primary"
              html-type="submit"
              :loading="savingKey === row.key"
              :disabled="!dirty(row)"
            >
              保存
            </a-button>
          </a-space>
        </a-form>

        <p v-if="row.unregistered" class="muted" style="margin-top: 8px">
          未登记的配置键(历史遗留),后台不接受它的修改;配置键:{{ row.key }}
        </p>
        <p v-else class="muted" style="margin-top: 8px">
          配置键:{{ row.key }}
          <template v-if="controlType(row) === 'secret'"> · 当前值不回显</template>
        </p>
      </a-card>
    </a-spin>

    <!--
      发件测试:配置项本身(schema 驱动)上面都渲染出来了,
      但「这些值合起来能不能真的发信」要发一封信才知道 ——
      放在列表末尾,先保存再测的顺序也自然成立。
    -->
    <a-card title="发件邮箱测试" style="margin-bottom: 16px">
      <p class="muted" style="margin-bottom: 12px">
        用已保存的发件配置发一封信。上面的 mail.* 改完先保存,再回来测。
      </p>
      <a-form @submit.prevent="sendTestMail">
        <a-space>
          <a-input
            v-model:value="testTo"
            placeholder="收件邮箱,如 you@example.com"
            style="width: 260px"
          />
          <a-button type="primary" html-type="submit" :loading="testing">发送测试信</a-button>
        </a-space>
      </a-form>

      <a-alert
        v-if="testResult"
        :type="testResult.ok ? 'success' : 'error'"
        show-icon
        style="margin-top: 12px"
        :message="
          testResult.ok
            ? '测试信已交给 SMTP,收件人通常几秒内收到'
            : `失败于「${STAGE_LABELS[testResult.stage] ?? testResult.stage}」阶段`
        "
        :description="testResult.ok ? (testResult.note ?? '') : testResult.error ?? ''"
      />
    </a-card>
  </div>
</template>

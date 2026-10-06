<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'

import { validateClientId, validateRedirectURI, errorBanner, backToTop, type ErrorBanner } from '@yggauth/shared'

import { useAdminStore } from '../stores/admin'
import CopyableText from '../components/CopyableText.vue'
import EmptyState from '../components/EmptyState.vue'
import ErrorAlert from '../components/ErrorAlert.vue'

interface ClientRow {
  id: string
  name: string
  redirect_uris: string[]
  grant_types: string[]
  scopes: string[]
  require_pkce: boolean
  public: boolean
  status: string
  created_at: string
}

const store = useAdminStore()
const items = ref<ClientRow[]>([])
const loading = ref(false)
const banner = ref<ErrorBanner | null>(null)
/** 刚生成的密钥。展示与标题分开存:密钥本身要交给复制组件,不能混进一句话里。 */
const secret = ref('')
const secretTitle = ref('')

const creating = ref(false)
const submitting = ref(false)
const formErrors = reactive({ client_id: '', redirect_uri: '' })
const form = reactive({
  client_id: '',
  name: '',
  redirect_uris: '',
  grant_types: ['authorization_code', 'refresh_token'],
  scopes: ['openid', 'profile', 'email'],
  require_pkce: true,
  public: false,
})

onMounted(load)

async function load(): Promise<void> {
  loading.value = true
  // 每次刷新都先清:否则上一次的报错会一直挂在新列表上方,
  // 看起来像「这次也没查出来」。
  banner.value = null
  try {
    const data = await store.api.get<{ items: ClientRow[] }>('/api/admin/clients')
    items.value = data.items
  } catch (err) {
    banner.value = errorBanner(err, '查询失败')
  } finally {
    loading.value = false
  }
}

function resetForm(): void {
  form.client_id = ''
  form.name = ''
  form.redirect_uris = ''
  formErrors.client_id = ''
  formErrors.redirect_uri = ''
}

/**
 * 弹窗的「确定」在 a-modal 的 footer 上,并不在 <a-form> 内部 ——
 * 它提交不了这个表单,回车也就发不出 submit。这里补上「输入框里回车 = 确定」,
 * 与点「创建」完全等价;`.exact` 放过带修饰键的组合,textarea 的回车留给换行。
 *
 * 事件要从模板里显式传进来:带修饰符的处理器会被编译成
 * `$event => submitOnEnter(create)` —— 那样只会造出一个闭包再丢掉,函数体永远不执行。
 */
function submitOnEnter(fn: () => Promise<void>, e: KeyboardEvent): void {
  if (submitting.value) return
  if ((e.target as HTMLElement | null)?.tagName === 'TEXTAREA') return
  e.preventDefault()
  void fn()
}

async function create(): Promise<void> {
  banner.value = null
  secret.value = ''
  secretTitle.value = ''

  formErrors.client_id = validateClientId(form.client_id).message
  const uris = form.redirect_uris
    .split(/[\n,]/)
    .map((u) => u.trim())
    .filter(Boolean)
  const bad = uris.map(validateRedirectURI).find((r) => !r.ok)
  formErrors.redirect_uri = bad?.message ?? (uris.length === 0 ? '至少需要一个回调地址' : '')
  if (formErrors.client_id || formErrors.redirect_uri) {
    return
  }

  submitting.value = true
  try {
    const data = await store.api.post<{ client_secret?: string }>('/api/admin/clients', {
      client_id: form.client_id,
      name: form.name || form.client_id,
      redirect_uris: uris,
      grant_types: form.grant_types,
      scopes: form.scopes,
      require_pkce: form.require_pkce,
      public: form.public,
    })

    // 密钥只在创建响应里出现一次,关掉弹窗就再也拿不到了。
    // 必须摆在一个能直接复制的地方,而不是指望用户手抄几十个字符。
    secret.value = data.client_secret ?? ''
    secretTitle.value = '客户端密钥已生成,只显示这一次,请立即保存'
    creating.value = false
    resetForm()
    await load()
  } catch (err) {
    banner.value = errorBanner(err, '创建失败')
  } finally {
    submitting.value = false
  }
}

async function rotate(client: ClientRow): Promise<void> {
  banner.value = null
  secret.value = ''
  secretTitle.value = ''
  try {
    const data = await store.api.post<{ client_secret: string }>(
      `/api/admin/clients/${encodeURIComponent(client.id)}/rotate-secret`,
      {},
    )
    secret.value = data.client_secret
    secretTitle.value = '新密钥已生成,旧密钥已立即失效,只显示这一次'
  } catch (err) {
    banner.value = errorBanner(err, '轮换失败')
  }
}

async function remove(client: ClientRow): Promise<void> {
  banner.value = null
  // 顺手收掉密钥条:它属于刚被删掉的客户端,留着只会让人复制一个已失效的串
  secret.value = ''
  secretTitle.value = ''
  try {
    await store.api.delete(`/api/admin/clients/${encodeURIComponent(client.id)}`)
    await load()
  } catch (err) {
    banner.value = errorBanner(err, '删除失败')
  }
}
</script>

<template>
  <div class="page">
    <p class="muted" style="margin-bottom: 16px">
      这里登记要接入本站的第三方应用(OAuth2/OIDC 客户端)。对方拿 client_id 与密钥换令牌;
      排查接入失败时,先在下方核对回调地址与 PKCE 配置是否和对方一致。
    </p>

    <ErrorAlert :banner="banner" />
    <a-alert v-if="secret" type="warning" show-icon style="margin-bottom: 16px">
      <template #message>{{ secretTitle }}</template>
      <template #description>
        <CopyableText :value="secret" label="密钥" :boxed="false" />
      </template>
    </a-alert>

    <a-card title="OIDC 客户端">
      <template #extra>
        <a-button type="primary" @click="creating = true">新建客户端</a-button>
      </template>
      <a-spin :spinning="loading">
        <!-- 换页是一次整屏数据替换:回顶,否则视线停在分页器旁,看到的是新页的末尾 -->
        <a-table
          :scroll="{ x: 1200 }"
          :data-source="items"
          row-key="id"
          :pagination="{ pageSize: 20 }"
          @change="() => backToTop()"
        >
          <template #emptyText>
            <EmptyState
              description="还没有登记客户端"
              hint="第三方应用要让用户用本站账号登录时,先在这里创建一个客户端,再把 client_id 与密钥交给对方接入。"
            />
          </template>
          <!-- data-index 不能省:只写 key 不写 data-index 又不给插槽时,
               Ant Design Vue 会渲染空单元格,看起来像数据没查出来。 -->
          <a-table-column key="id" data-index="id" title="客户端标识" />
          <a-table-column key="name" data-index="name" title="名称" />
          <a-table-column key="redirect_uris" title="回调地址">
            <template #default="{ record }">
              <div v-for="uri in record.redirect_uris" :key="uri"><code>{{ uri }}</code></div>
            </template>
          </a-table-column>
          <a-table-column key="pkce" title="PKCE">
            <template #default="{ record }">
              <a-tag :color="record.require_pkce ? 'green' : 'red'">
                {{ record.require_pkce ? '必需' : '不要求' }}
              </a-tag>
            </template>
          </a-table-column>
          <a-table-column key="public" title="类型">
            <template #default="{ record }">
              <a-tag>{{ record.public ? '公开客户端' : '机密客户端' }}</a-tag>
            </template>
          </a-table-column>
          <a-table-column key="actions" title="操作">
            <template #default="{ record }">
              <a-popconfirm
                title="轮换后旧密钥立即失效,正在使用旧密钥的客户端会全部失败。确定?"
                @confirm="rotate(record)"
              >
                <a-button size="small">轮换密钥</a-button>
              </a-popconfirm>
              <a-popconfirm title="删除客户端不可撤销,确定?" @confirm="remove(record)">
                <a-button danger size="small">删除</a-button>
              </a-popconfirm>
            </template>
          </a-table-column>
        </a-table>
      </a-spin>
    </a-card>

    <a-modal
      v-model:open="creating"
      title="新建客户端"
      :confirm-loading="submitting"
      :width="620"
      @ok="create"
    >
      <a-form layout="vertical" @submit.prevent="create" @keydown.enter.exact.prevent="submitOnEnter(create, $event)">
        <a-form-item
          label="客户端标识(client_id)"
          required
          :validate-status="formErrors.client_id ? 'error' : ''"
          :help="formErrors.client_id"
        >
          <a-input v-model:value="form.client_id" placeholder="my-app" />
        </a-form-item>

        <a-form-item label="显示名称">
          <a-input v-model:value="form.name" />
        </a-form-item>

        <a-form-item
          label="回调地址"
          required
          :validate-status="formErrors.redirect_uri ? 'error' : ''"
          :help="formErrors.redirect_uri || '每行一个。必须使用 HTTPS(仅本机允许 HTTP)'"
        >
          <a-textarea v-model:value="form.redirect_uris" :rows="3" />
        </a-form-item>

        <a-form-item label="授权类型">
          <a-select
            v-model:value="form.grant_types"
            mode="multiple"
            :options="[
              { label: 'authorization_code', value: 'authorization_code' },
              { label: 'refresh_token', value: 'refresh_token' },
              { label: 'client_credentials', value: 'client_credentials' },
              { label: 'urn:ietf:params:oauth:grant-type:device_code', value: 'urn:ietf:params:oauth:grant-type:device_code' },
            ]"
          />
        </a-form-item>

        <a-form-item label="作用域">
          <a-select
            v-model:value="form.scopes"
            mode="multiple"
            :options="[
              { label: 'openid', value: 'openid' },
              { label: 'profile', value: 'profile' },
              { label: 'email', value: 'email' },
              { label: 'offline_access', value: 'offline_access' },
            ]"
          />
        </a-form-item>

        <a-space>
          <a-form-item label="强制 PKCE">
            <a-switch v-model:checked="form.require_pkce" />
          </a-form-item>
          <a-form-item label="公开客户端(无密钥)">
            <a-switch v-model:checked="form.public" />
          </a-form-item>
        </a-space>
      </a-form>
    </a-modal>
  </div>
</template>

<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'

import { ApiError, validateClientId, validateRedirectURI } from '@yggauth/shared'

import { useAdminStore } from '../stores/admin'

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
const banner = ref('')
const secretBanner = ref('')

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
  try {
    const data = await store.api.get<{ items: ClientRow[] }>('/api/admin/clients')
    items.value = data.items
  } catch (err) {
    banner.value = err instanceof ApiError ? err.message : '查询失败'
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

async function create(): Promise<void> {
  banner.value = ''
  secretBanner.value = ''

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
    // 必须在用户看得见的地方说清楚,而不是默默丢进剪贴板。
    secretBanner.value = data.client_secret
      ? `客户端密钥(只显示这一次,请立即保存):${data.client_secret}`
      : ''
    creating.value = false
    resetForm()
    await load()
  } catch (err) {
    banner.value = err instanceof ApiError ? err.message : '创建失败'
  } finally {
    submitting.value = false
  }
}

async function rotate(client: ClientRow): Promise<void> {
  banner.value = ''
  secretBanner.value = ''
  try {
    const data = await store.api.post<{ client_secret: string }>(
      `/api/admin/clients/${encodeURIComponent(client.id)}/rotate-secret`,
      {},
    )
    secretBanner.value = `新密钥(旧密钥已立即失效,只显示这一次):${data.client_secret}`
  } catch (err) {
    banner.value = err instanceof ApiError ? err.message : '轮换失败'
  }
}

async function remove(client: ClientRow): Promise<void> {
  banner.value = ''
  try {
    await store.api.delete(`/api/admin/clients/${encodeURIComponent(client.id)}`)
    await load()
  } catch (err) {
    banner.value = err instanceof ApiError ? err.message : '删除失败'
  }
}
</script>

<template>
  <div class="page">
    <a-alert v-if="banner" type="error" :message="banner" show-icon style="margin-bottom: 16px" />
    <a-alert v-if="secretBanner" type="warning" :message="secretBanner" show-icon style="margin-bottom: 16px" />

    <a-card
      title="OIDC 客户端"
>
      <template #extra>
        <a-button type="primary" @click="creating = true">新建客户端</a-button>
      </template>
      <a-spin :spinning="loading">
        <a-table :data-source="items" row-key="id" :pagination="{ pageSize: 20 }">
          <a-table-column key="id" title="客户端标识" />
          <a-table-column key="name" title="名称" />
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
      <a-form layout="vertical">
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

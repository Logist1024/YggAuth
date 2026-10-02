<script setup lang="ts">
import { onMounted, ref } from 'vue'

import { ApiError } from '@yggauth/shared'

import { useAdminStore } from '../stores/admin'

interface Invitation {
  id: string
  email: string
  code: string
  status: string
  expires_at: string
  created_at: string
}

const store = useAdminStore()
const items = ref<Invitation[]>([])
const banner = ref('')
const loading = ref(false)
const email = ref('')
const submitting = ref(false)
const createdLink = ref('')

onMounted(load)

async function load(): Promise<void> {
  loading.value = true
  try {
    const data = await store.api.get<{ invitations: Invitation[] }>('/api/admin/invitations')
    items.value = data.invitations
  } catch (err) {
    banner.value = err instanceof ApiError ? err.message : '查询失败'
  } finally {
    loading.value = false
  }
}

async function create(): Promise<void> {
  banner.value = ''
  if (!email.value) {
    banner.value = '请填写邮箱'
    return
  }

  submitting.value = true
  try {
    const data = await store.api.post<{ invitation: Invitation }>('/api/admin/invitations', {
      email: email.value,
    })
    createdLink.value = data.invitation.code
    email.value = ''
    await load()
  } catch (err) {
    banner.value = err instanceof ApiError ? err.message : '创建失败'
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <div class="page">
    <a-alert v-if="banner" type="error" :message="banner" show-icon style="margin-bottom: 16px" />
    <a-alert
      v-if="createdLink"
      type="success"
      show-icon
      style="margin-bottom: 16px"
      :message="`邀请码:${createdLink}`"
    />

    <a-card title="创建邀请" style="margin-bottom: 16px">
      <a-space>
        <a-input v-model:value="email" type="email" placeholder="受邀人邮箱" style="width: 320px" />
        <a-button type="primary" :loading="submitting" @click="create">生成邀请</a-button>
      </a-space>
    </a-card>

    <a-card title="邀请列表">
      <a-spin :spinning="loading">
        <a-table :data-source="items" row-key="id" :pagination="{ pageSize: 20 }">
          <a-table-column key="email" title="邮箱" />
          <a-table-column key="code" title="邀请码" />
          <a-table-column key="status" title="状态">
            <template #default="{ record }">
              <a-tag :color="record.status === 'pending' ? 'blue' : 'default'">{{ record.status }}</a-tag>
            </template>
          </a-table-column>
          <a-table-column key="expires_at" title="过期时间">
            <template #default="{ record }">{{ new Date(record.expires_at).toLocaleString() }}</template>
          </a-table-column>
        </a-table>
      </a-spin>
    </a-card>
  </div>
</template>

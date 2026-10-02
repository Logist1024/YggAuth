<script setup lang="ts">
import { onMounted, ref } from 'vue'

import { ApiError } from '@yggauth/shared'

import { useAdminStore } from '../stores/admin'

interface RoleRow {
  id: string
  code: string
  name: string
  description: string
  members: number
  permissions: string[]
}

interface PermissionPoint {
  id: string
  code: string
  name: string
}

const store = useAdminStore()
const roles = ref<RoleRow[]>([])
const points = ref<PermissionPoint[]>([])
const banner = ref('')
const loading = ref(false)

const creating = ref(false)
const form = ref({ code: '', name: '', description: '', permissions: [] as string[] })
const submitting = ref(false)

onMounted(load)

async function load(): Promise<void> {
  loading.value = true
  try {
    const [r, p] = await Promise.all([
      store.api.get<{ roles: RoleRow[] }>('/api/admin/roles'),
      store.api.get<{ permissions: PermissionPoint[] }>('/api/admin/permission-points'),
    ])
    roles.value = r.roles
    points.value = p.permissions
  } catch (err) {
    banner.value = err instanceof ApiError ? err.message : '查询失败'
  } finally {
    loading.value = false
  }
}

async function create(): Promise<void> {
  banner.value = ''
  submitting.value = true
  try {
    await store.api.post('/api/admin/roles', { ...form.value })
    creating.value = false
    form.value = { code: '', name: '', description: '', permissions: [] }
    await load()
  } catch (err) {
    banner.value = err instanceof ApiError ? err.message : '创建失败'
  } finally {
    submitting.value = false
  }
}

async function remove(role: RoleRow): Promise<void> {
  banner.value = ''
  try {
    await store.api.delete(`/api/admin/roles/${role.id}`)
    await load()
  } catch (err) {
    banner.value = err instanceof ApiError ? err.message : '删除失败'
  }
}
</script>

<template>
  <div class="page">
    <a-alert v-if="banner" type="error" :message="banner" show-icon style="margin-bottom: 16px" />

    <a-card title="角色">
      <template #extra>
        <a-button type="primary" @click="creating = true">新建角色</a-button>
      </template>
      <a-spin :spinning="loading">
        <a-table :data-source="roles" row-key="id" :pagination="false">
          <a-table-column key="code" title="标识" />
          <a-table-column key="name" title="名称" />
          <a-table-column key="description" title="描述" />
          <a-table-column key="members" title="成员数" />
          <a-table-column key="perms" title="权限点">
            <template #default="{ record }">
              <a-tag v-for="p in record.permissions" :key="p" style="margin-right: 4px">{{ p }}</a-tag>
            </template>
          </a-table-column>
          <a-table-column key="actions" title="操作">
            <template #default="{ record }">
              <a-popconfirm title="删除角色不会自动回收已授予的权限,确定?" @confirm="remove(record)">
                <a-button danger size="small">删除</a-button>
              </a-popconfirm>
            </template>
          </a-table-column>
        </a-table>
      </a-spin>
    </a-card>

    <a-modal v-model:open="creating" title="新建角色" :confirm-loading="submitting" @ok="create">
      <a-form layout="vertical">
        <a-form-item label="标识(code)" required>
          <a-input v-model:value="form.code" placeholder="例如 support_agent" />
        </a-form-item>
        <a-form-item label="名称" required>
          <a-input v-model:value="form.name" />
        </a-form-item>
        <a-form-item label="描述">
          <a-textarea v-model:value="form.description" :rows="2" />
        </a-form-item>
        <a-form-item label="权限点">
          <a-select
            v-model:value="form.permissions"
            mode="multiple"
            :options="points.map((p) => ({ label: p.code, value: p.code }))"
            style="width: 100%"
          />
        </a-form-item>
      </a-form>
    </a-modal>
  </div>
</template>


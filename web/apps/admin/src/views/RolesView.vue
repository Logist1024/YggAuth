<script setup lang="ts">
import { onMounted, ref } from 'vue'

import { ApiError } from '@yggauth/shared'

import EmptyState from '../components/EmptyState.vue'
import { useAdminStore } from '../stores/admin'

interface RoleRow {
  id: string
  code: string
  name: string
  description: string
  /** 系统内置角色后端禁止删除(见 internal/identity/rbac/rbac.go 的 DeleteRole)。 */
  is_system: boolean
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
    <p class="muted" style="margin-bottom: 16px">
      角色是权限点的集合。系统内置的两个角色不可删除,需要更细的权限划分时新建一个。
    </p>

    <a-alert v-if="banner" type="error" :message="banner" show-icon style="margin-bottom: 16px" />

    <a-card title="角色">
      <template #extra>
        <a-button type="primary" @click="creating = true">新建角色</a-button>
      </template>
      <a-spin :spinning="loading">
        <!--
          列宽必须显式给。权限点列是变长的(内置角色有 17 个),不给宽度时
          表格会把其余列压到最小 —— 表现为列头被挤成竖排单字,整行读不了。
          标识/名称这类短字段也要给宽度,否则同样会被抢空间。
        -->
        <a-table :data-source="roles" row-key="id" :pagination="false">
          <template #emptyText>
            <EmptyState
              description="还没有任何角色"
              hint="正常部署下这里至少会有「平台管理员」与「普通用户」两个系统角色。"
            />
          </template>
          <a-table-column key="code" data-index="code" title="标识" :width="170" />
          <a-table-column key="name" data-index="name" title="名称" :width="150" />
          <a-table-column key="description" data-index="description" title="描述" />
          <a-table-column key="perm_count" title="权限点" :width="300">
            <template #default="{ record }">
              <span v-if="record.permissions.length === 0" class="muted">未授予</span>
              <a-tooltip v-else>
                <template #title>
                  <div v-for="p in record.permissions" :key="p">{{ p }}</div>
                </template>
                <a-tag
                  v-for="p in record.permissions.slice(0, 2)"
                  :key="p"
                  style="margin-right: 4px"
                >
                  {{ p }}
                </a-tag>
                <a-tag v-if="record.permissions.length > 2">
                  +{{ record.permissions.length - 2 }}
                </a-tag>
              </a-tooltip>
            </template>
          </a-table-column>
          <a-table-column key="actions" title="操作" :width="110">
            <template #default="{ record }">
              <a-tooltip v-if="record.is_system" title="系统内置角色不可删除">
                <a-button danger size="small" disabled>删除</a-button>
              </a-tooltip>
              <a-popconfirm
                v-else
                title="删除角色不会自动回收已授予的权限,确定?"
                @confirm="remove(record)"
              >
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


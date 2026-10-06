<script setup lang="ts">
import { onMounted, ref } from 'vue'

import { errorBanner, bannerMessage, type ErrorBanner } from '@yggauth/shared'

import EmptyState from '../components/EmptyState.vue'
import { useAdminStore } from '../stores/admin'
import ErrorAlert from '../components/ErrorAlert.vue'

interface RoleRow {
  id: string
  code: string
  name: string
  description: string
  /** 系统内置角色后端禁止删除(见 internal/identity/rbac/rbac.go 的 DeleteRole)。 */
  is_system: boolean
  permissions: string[]
}

/** 字段名对齐 internal/admin/handler.go 的 ListPermissions。 */
interface PermissionPoint {
  code: string
  /** 这个权限点到底放行什么。选权限时全靠它,否则只能靠猜。 */
  description: string
}

const store = useAdminStore()
const roles = ref<RoleRow[]>([])
const points = ref<PermissionPoint[]>([])
const banner = ref<ErrorBanner | null>(null)
const loading = ref(false)

const creating = ref(false)
const form = ref({ code: '', name: '', description: '', permissions: [] as string[] })
const submitting = ref(false)

/** 编辑态。code 不可改(后端 UpdateRole 不收它),所以编辑表单里只展示。 */
const editing = ref(false)
const editForm = ref({ id: '', code: '', name: '', description: '', permissions: [] as string[] })

/** 权限点下拉项:description 挂在 title 上,鼠标悬停即可看到用途。 */
const pointOptions = (): { label: string; value: string; title: string }[] =>
  points.value.map((p) => ({ label: p.code, value: p.code, title: p.description }))

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
    banner.value = errorBanner(err, '查询失败')
  } finally {
    loading.value = false
  }
}

/**
 * 弹窗的「确定」在 a-modal 的 footer 上,并不在 <a-form> 内部 ——
 * 它提交不了这个表单,回车也就发不出 submit。这里补上「输入框里回车 = 确定」,
 * 与点「确定」完全等价;`.exact` 放过带修饰键的组合,textarea 的回车留给换行。
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
  submitting.value = true
  try {
    await store.api.post('/api/admin/roles', { ...form.value })
    creating.value = false
    form.value = { code: '', name: '', description: '', permissions: [] }
    await load()
  } catch (err) {
    banner.value = errorBanner(err, '创建失败')
  } finally {
    submitting.value = false
  }
}

async function remove(role: RoleRow): Promise<void> {
  banner.value = null
  try {
    await store.api.delete(`/api/admin/roles/${role.id}`)
    await load()
  } catch (err) {
    banner.value = errorBanner(err, '删除失败')
  }
}

function openEdit(role: RoleRow): void {
  banner.value = null
  editForm.value = {
    id: role.id,
    code: role.code,
    name: role.name,
    description: role.description,
    // 复制一份:直接引用 role.permissions 会让多选框在点「取消」后
    // 也已经改了表格里的显示,看起来像没保存却变了。
    permissions: [...role.permissions],
  }
  editing.value = true
}

/**
 * 保存角色。
 *
 * permissions 始终提交:后端把「没传该字段」解释为不改权限、
 * 「传空数组」解释为清空。编辑表单里摆的就是当前权限的完整
 * 所见即所得视图,所以提交框里的值(含空)就是用户想要的结果。
 */
async function update(): Promise<void> {
  if (!editForm.value.name.trim()) {
    banner.value = bannerMessage('名称不能为空')
    return
  }
  banner.value = null
  submitting.value = true
  try {
    await store.api.patch(`/api/admin/roles/${editForm.value.id}`, {
      name: editForm.value.name,
      description: editForm.value.description,
      permissions: editForm.value.permissions,
    })
    editing.value = false
    await load()
  } catch (err) {
    banner.value = errorBanner(err, '保存失败')
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <div class="page">
    <p class="muted" style="margin-bottom: 16px">
      角色是权限点的集合。系统内置的两个角色不可删除,需要更细的权限划分时新建一个。
    </p>

    <ErrorAlert :banner="banner" />

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
        <a-table :scroll="{ x: 1200 }" :data-source="roles" row-key="id" :pagination="false">
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
          <a-table-column key="actions" title="操作" :width="170">
            <template #default="{ record }">
              <a-space>
                <a-button size="small" @click="openEdit(record)">编辑</a-button>
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
              </a-space>
            </template>
          </a-table-column>
        </a-table>
      </a-spin>
    </a-card>

    <a-modal v-model:open="creating" title="新建角色" :confirm-loading="submitting" @ok="create">
      <a-form layout="vertical" @submit.prevent="create" @keydown.enter.exact.prevent="submitOnEnter(create, $event)">
        <a-form-item label="标识(code)" required>
          <a-input v-model:value="form.code" placeholder="例如 support_agent" />
        </a-form-item>
        <a-form-item label="名称" required>
          <a-input v-model:value="form.name" />
        </a-form-item>
        <a-form-item label="描述">
          <a-textarea v-model:value="form.description" :rows="2" />
        </a-form-item>
        <a-form-item label="权限点" extra="按住 Ctrl/Cmd 可多选,悬停看每个权限点的用途">
          <a-select
            v-model:value="form.permissions"
            mode="multiple"
            :options="pointOptions()"
            placeholder="不选则创建一个不含权限的角色"
            style="width: 100%"
          />
        </a-form-item>
      </a-form>
    </a-modal>

    <a-modal
      v-model:open="editing"
      title="编辑角色"
      :confirm-loading="submitting"
      @ok="update"
    >
      <a-form layout="vertical" @submit.prevent="update" @keydown.enter.exact.prevent="submitOnEnter(update, $event)">
        <a-form-item label="标识(code)">
          <!-- 后端不支持改 code,置灰展示避免有人改了却不生效 -->
          <a-input :value="editForm.code" disabled />
        </a-form-item>
        <a-form-item label="名称" required>
          <a-input v-model:value="editForm.name" />
        </a-form-item>
        <a-form-item label="描述">
          <a-textarea v-model:value="editForm.description" :rows="2" />
        </a-form-item>
        <a-form-item label="权限点" extra="保存时以这里的勾选为准,清空即收回全部权限">
          <a-select
            v-model:value="editForm.permissions"
            mode="multiple"
            :options="pointOptions()"
            placeholder="未授予任何权限"
            style="width: 100%"
          />
        </a-form-item>
      </a-form>
    </a-modal>
  </div>
</template>


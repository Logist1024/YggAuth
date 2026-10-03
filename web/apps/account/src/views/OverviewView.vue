<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'

import { ApiError } from '@yggauth/shared'

import { useSessionStore } from '../stores/session'

/** 账号状态的显示名。后端存的是枚举值,不该直接甩给用户看。 */
const STATUS_LABEL: Record<string, string> = {
  active: '正常',
  disabled: '已停用',
  pending_verification: '待验证邮箱',
  locked: '已锁定',
}

/** 用途说明。新用户第一眼要能看懂「这个账号是干什么的」。 */
const CAPABILITIES = [
  {
    title: '登录内部业务系统',
    detail: '在业务系统的登录页选择用本服务登录,不必再单独注册一套账号。',
  },
  {
    title: '登录 Minecraft 服务器',
    detail: '在启动器里把认证服务器指向本服务,同一个账号即可进服。',
  },
  {
    title: '管理皮肤与披风',
    detail: '上传的材质会自动同步到游戏内,不需要手动替换文件。',
  },
]

const store = useSessionStore()
const mc = ref<{ id: string; name: string; login_enabled: boolean } | null>(null)
const mcError = ref('')
const loadingMC = ref(false)

const account = computed(() => store.account)

onMounted(async () => {
  loadingMC.value = true
  try {
    mc.value = await store.api.get('/api/account/mc/profile')
  } catch (err) {
    // 还没在游戏里登录过时没有档案,这是正常状态而不是错误。
    if (err instanceof ApiError && err.status === 404) {
      mcError.value = ''
    } else {
      mcError.value = err instanceof ApiError ? err.message : '查询失败'
    }
  } finally {
    loadingMC.value = false
  }
})
</script>

<template>
  <div class="page">
    <h2>账号概览</h2>

    <!--
      这张卡放在最上面,因为它回答的是新用户最先问的问题:
      「我有这个账号了,然后呢?」之前这一页直接进基本信息表格,
      用户看完只知道自己的邮箱,不知道这个账号能拿去干什么。
    -->
    <a-card title="这个账号能做什么" style="margin-bottom: 16px">
      <a-row :gutter="24">
        <a-col v-for="c in CAPABILITIES" :key="c.title" :span="8">
          <div class="capability-title">{{ c.title }}</div>
          <p class="muted">{{ c.detail }}</p>
        </a-col>
      </a-row>
    </a-card>

    <a-row :gutter="16">
      <a-col :span="14">
        <a-card title="基本信息">
          <a-descriptions bordered :column="1">
            <a-descriptions-item label="用户名">{{ account?.username }}</a-descriptions-item>
            <a-descriptions-item label="邮箱">{{ account?.email }}</a-descriptions-item>
            <a-descriptions-item label="状态">
              {{ account ? (STATUS_LABEL[account.status] ?? account.status) : '' }}
            </a-descriptions-item>
            <a-descriptions-item label="邮箱验证">
              <a-tag :color="account?.email_verified ? 'green' : 'orange'">
                {{ account?.email_verified ? '已验证' : '未验证' }}
              </a-tag>
            </a-descriptions-item>
            <a-descriptions-item label="注册时间">
              {{ account ? new Date(account.created_at).toLocaleString() : '' }}
            </a-descriptions-item>
          </a-descriptions>
          <div style="margin-top: 16px">
            <RouterLink to="/security">修改用户名、邮箱或密码 →</RouterLink>
          </div>
        </a-card>
      </a-col>

      <a-col :span="10">
        <a-card title="Minecraft 档案">
          <a-spin :spinning="loadingMC" />
          <a-alert v-if="mcError" type="error" :message="mcError" show-icon />

          <template v-if="!mcError">
            <a-descriptions v-if="mc" bordered :column="1">
              <a-descriptions-item label="玩家名">{{ mc.name }}</a-descriptions-item>
              <a-descriptions-item label="UUID">
                <code>{{ mc.id }}</code>
              </a-descriptions-item>
              <a-descriptions-item label="MC 登录">
                <a-tag :color="mc.login_enabled ? 'green' : 'default'">
                  {{ mc.login_enabled ? '已开启' : '已关闭' }}
                </a-tag>
              </a-descriptions-item>
            </a-descriptions>
            <a-empty v-else description="还没有绑定 Minecraft 档案。在游戏里用本服务登录一次即可自动创建。" />
          </template>

          <div style="margin-top: 16px">
            <RouterLink to="/skin">管理皮肤与披风 →</RouterLink>
          </div>
        </a-card>
      </a-col>
    </a-row>
  </div>
</template>

<style scoped>
.capability-title {
  font-weight: 500;
  margin-bottom: 4px;
}
</style>

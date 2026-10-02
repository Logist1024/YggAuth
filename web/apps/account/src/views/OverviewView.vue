<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'

import { ApiError } from '@yggauth/shared'

import { useSessionStore } from '../stores/session'

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

    <a-row :gutter="16">
      <a-col :span="14">
        <a-card title="基本信息">
          <a-descriptions bordered :column="1">
            <a-descriptions-item label="用户名">{{ account?.username }}</a-descriptions-item>
            <a-descriptions-item label="邮箱">{{ account?.email }}</a-descriptions-item>
            <a-descriptions-item label="状态">{{ account?.status }}</a-descriptions-item>
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

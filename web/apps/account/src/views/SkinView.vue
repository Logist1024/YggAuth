<script setup lang="ts">
import { onMounted, ref } from 'vue'

import { ApiError, validateMCName } from '@yggauth/shared'

import { useSessionStore } from '../stores/session'

interface TextureInfo {
  type: string
  hash: string
  size: number
  url: string
  width: number
  height: number
}

const store = useSessionStore()

const skin = ref<TextureInfo | null>(null)
const cape = ref<TextureInfo | null>(null)
const avatarUrl = ref('')
const mcName = ref('')
const nameError = ref('')
const banner = ref('')
const uploading = ref<'skin' | 'cape' | null>(null)
const saving = ref(false)

// 皮肤/披风的体积上限,与后端 MC_SKIN_MAX_SIZE 一致。
// 这里只是提前给个提示,真正的判定在后端。
const MAX_SIZE = 2 * 1024 * 1024

const ACCEPTED_SKIN = '皮肤需要 64×64(现代)或 64×32(旧版)PNG'
const ACCEPTED_CAPE = '披风需要 64×32 PNG'

async function load(): Promise<void> {
  banner.value = ''
  for (const [key, kind] of [
    ['skin', 'skin'],
    ['cape', 'cape'],
  ] as const) {
    try {
      const data = await store.api.get<TextureInfo>(`/api/account/mc/texture?type=${kind}`)
      if (key === 'skin') {
        skin.value = data
      } else {
        cape.value = data
      }
    } catch (err) {
      // 没上传过时后端返回 404,这是正常状态。
      if (err instanceof ApiError && err.status === 404) {
        if (key === 'skin') {
          skin.value = null
        } else {
          cape.value = null
        }
        continue
      }
      banner.value = err instanceof ApiError ? err.message : '查询失败'
    }
  }

  const profile = await store.api
    .get<{ id: string; name: string }>('/api/account/mc/profile')
    .catch(() => null)
  if (profile) {
    mcName.value = profile.name
    avatarUrl.value = `/mc/avatar/${profile.id}?size=128&_=${Date.now()}`
  }
}

onMounted(load)

async function onFile(kind: 'skin' | 'cape', event: Event): Promise<void> {
  const input = event.target as HTMLInputElement
  const file = input.files?.[0]
  if (!file) {
    return
  }

  banner.value = ''
  if (file.size > MAX_SIZE) {
    banner.value = `文件超过 ${MAX_SIZE / 1024 / 1024} MB 上限`
    input.value = ''
    return
  }

  uploading.value = kind
  try {
    await store.api.request(`/api/account/mc/texture?type=${kind}`, {
      method: 'PUT',
      raw: file,
      headers: { 'Content-Type': 'image/png' },
    })
    await load()
  } catch (err) {
    banner.value = err instanceof ApiError ? err.message : '上传失败'
  } finally {
    uploading.value = null
    input.value = ''
  }
}

async function remove(kind: 'skin' | 'cape'): Promise<void> {
  banner.value = ''
  try {
    await store.api.delete(`/api/account/mc/texture/${kind}`)
    await load()
  } catch (err) {
    banner.value = err instanceof ApiError ? err.message : '删除失败'
  }
}

async function rename(): Promise<void> {
  nameError.value = validateMCName(mcName.value).message
  banner.value = ''
  if (nameError.value) {
    return
  }

  saving.value = true
  try {
    await store.api.patch('/api/account/mc/name', { new_name: mcName.value })
    await load()
  } catch (err) {
    banner.value = err instanceof ApiError ? err.message : '改名失败'
  } finally {
    saving.value = false
  }
}

async function toggleLogin(): Promise<void> {
  banner.value = ''
  const enabled = store.account?.mc_login_enabled ?? false
  try {
    await store.api.post('/api/account/mc/login-enabled', { enabled: !enabled })
    const refreshed = await store.loadAccount()
    void refreshed
  } catch (err) {
    banner.value = err instanceof ApiError ? err.message : '设置失败'
  }
}
</script>

<template>
  <div class="page">
    <h2>皮肤管理</h2>
    <a-alert v-if="banner" type="error" :message="banner" show-icon style="margin-bottom: 16px" />

    <a-row :gutter="16">
      <a-col :span="10">
        <a-card title="预览">
          <div style="text-align: center">
            <a-image
              v-if="avatarUrl"
              :src="avatarUrl"
              :width="128"
              :height="128"
              style="image-rendering: pixelated; border-radius: 4px"
              alt="头像预览"
            />
            <a-empty v-else description="还没有 Minecraft 档案" />
          </div>
          <p class="muted" style="text-align: center; margin-top: 8px">
            头像在服务端异步渲染,换皮肤后可能需要几秒才更新。
          </p>
        </a-card>
      </a-col>

      <a-col :span="14">
        <a-card title="玩家名" style="margin-bottom: 16px">
          <a-space style="width: 100%">
            <a-input v-model:value="mcName" :status="nameError ? 'error' : ''" :disabled="!mcName" />
            <a-button type="primary" :loading="saving" :disabled="!mcName" @click="rename">改名</a-button>
          </a-space>
          <p v-if="nameError" class="muted" style="color: #cf1322">{{ nameError }}</p>
          <p v-else class="muted">改名后旧名会在保留期内禁止他人注册。</p>
        </a-card>

        <a-card title="皮肤" style="margin-bottom: 16px">
          <a-space direction="vertical" style="width: 100%">
            <a-space>
              <a-upload :show-upload-list="false" :before-upload="(e: Event) => onFile('skin', e)" accept="image/png">
                <a-button :loading="uploading === 'skin'">上传皮肤</a-button>
              </a-upload>
              <a-button v-if="skin" danger @click="remove('skin')">删除</a-button>
            </a-space>
            <span class="muted">{{ ACCEPTED_SKIN }}</span>
            <a-image v-if="skin" :src="skin.url" :width="128" :height="128" style="image-rendering: pixelated" />
          </a-space>
        </a-card>

        <a-card title="披风" style="margin-bottom: 16px">
          <a-space direction="vertical" style="width: 100%">
            <a-space>
              <a-upload :show-upload-list="false" :before-upload="(e: Event) => onFile('cape', e)" accept="image/png">
                <a-button :loading="uploading === 'cape'">上传披风</a-button>
              </a-upload>
              <a-button v-if="cape" danger @click="remove('cape')">删除</a-button>
            </a-space>
            <span class="muted">{{ ACCEPTED_CAPE }}</span>
          </a-space>
        </a-card>

        <a-card title="Minecraft 登录">
          <a-space>
            <a-switch
              :checked="store.account?.mc_login_enabled ?? false"
              @change="toggleLogin"
            />
            <span class="muted">
              关闭后,在游戏里用本服务登录会明确被拒;已有的 MC 令牌仍然有效直到过期。
            </span>
          </a-space>
        </a-card>
      </a-col>
    </a-row>
  </div>
</template>

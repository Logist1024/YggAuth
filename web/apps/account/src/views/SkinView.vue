<script setup lang="ts">
import { onMounted, ref } from 'vue'

import { ApiError, validateMCName, errorBanner, bannerMessage, type ErrorBanner } from '@yggauth/shared'

import { useSessionStore } from '../stores/session'
import EmptyState from '../components/EmptyState.vue'
import ErrorAlert from '../components/ErrorAlert.vue'

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
const banner = ref<ErrorBanner | null>(null)
const uploading = ref<'skin' | 'cape' | null>(null)
const saving = ref(false)
const toggling = ref(false)
/**
 * 首屏数据是否已取回。
 *
 * 档案存在与否决定「玩家名」那一栏显示表单还是空态 ——
 * 不等首屏结果就渲染的话,每个正常账号都会先闪一下空态。
 * 只在首次置位:上传、改名后的刷新不重置它,免得表单闪没。
 */
const ready = ref(false)

// 皮肤/披风的体积上限,与后端 MC_SKIN_MAX_SIZE 一致。
// 这里只是提前给个提示,真正的判定在后端。
const MAX_SIZE = 2 * 1024 * 1024

const ACCEPTED_SKIN = '皮肤需要 64×64(现代)或 64×32(旧版)PNG'
const ACCEPTED_CAPE = '披风需要 64×32 PNG'

async function load(): Promise<void> {
  banner.value = null
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
      banner.value = errorBanner(err, '查询失败')
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

onMounted(async () => {
  try {
    await load()
  } finally {
    // 取数失败也要置位:否则页面永远停在加载态,
    // 用户连错误横幅都看不到。
    ready.value = true
  }
})

async function uploadFile(kind: 'skin' | 'cape', file: File): Promise<void> {
  banner.value = null
  if (file.size > MAX_SIZE) {
    banner.value = bannerMessage(`文件超过 ${MAX_SIZE / 1024 / 1024} MB 上限`)
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
    banner.value = errorBanner(err, '上传失败')
  } finally {
    uploading.value = null
  }
}

/**
 * a-upload 的 before-upload 收到的第一个参数就是 File 本身,
 * 不是 DOM Event。之前按 Event 取 target.files,永远拿不到文件。
 *
 * 返回 false 阻止组件的自动上传 —— 上传由上面的 uploadFile 手动调接口。
 */
function beforeUpload(kind: 'skin' | 'cape', file: File): boolean {
  void uploadFile(kind, file)
  return false
}

async function remove(kind: 'skin' | 'cape'): Promise<void> {
  banner.value = null
  try {
    await store.api.delete(`/api/account/mc/texture/${kind}`)
    await load()
  } catch (err) {
    banner.value = errorBanner(err, '删除失败')
  }
}

async function rename(): Promise<void> {
  nameError.value = validateMCName(mcName.value).message
  banner.value = null
  if (nameError.value) {
    return
  }

  saving.value = true
  try {
    await store.api.patch('/api/account/mc/name', { new_name: mcName.value })
    await load()
  } catch (err) {
    banner.value = errorBanner(err, '改名失败')
  } finally {
    saving.value = false
  }
}

async function toggleLogin(): Promise<void> {
  banner.value = null
  const enabled = store.account?.mc_login_enabled ?? false
  // 请求期间锁住:开关点起来没有阻尼,连点两次会发两次方向相反的请求,
  // 最终状态取决于谁先到 —— 用户看到的与自己点的可能不一致。
  toggling.value = true
  try {
    await store.api.post('/api/account/mc/login-enabled', { enabled: !enabled })
    await store.loadAccount()
  } catch (err) {
    banner.value = errorBanner(err, '设置失败')
  } finally {
    toggling.value = false
  }
}
</script>

<template>
  <div class="page">
    <h2>皮肤管理</h2>
    <ErrorAlert :banner="banner" />

    <a-row :gutter="16">
      <a-col :span="10">
        <a-card title="预览">
          <div style="text-align: center">
            <a-spin v-if="!ready" />
            <a-image
              v-else-if="avatarUrl"
              :src="avatarUrl"
              :width="128"
              :height="128"
              style="image-rendering: pixelated; border-radius: 4px"
              alt="头像预览"
            />
            <a-empty v-else description="还没有 Minecraft 档案" />
          </div>
          <!-- 空态时不写「换皮肤几秒更新」:这会儿根本没有皮肤可换 -->
          <p v-if="avatarUrl" class="muted" style="text-align: center; margin-top: 8px">
            头像在服务端异步渲染,换皮肤后可能需要几秒才更新。
          </p>
        </a-card>
      </a-col>

      <a-col :span="14">
        <a-card title="玩家名" style="margin-bottom: 16px">
          <a-spin v-if="!ready" />
          <!-- 没有档案时给空态,而不是一张禁用的输入框:点不动的控件比空白更让人困惑 -->
          <EmptyState
            v-else-if="!mcName"
            description="还没有 Minecraft 档案"
            hint="在游戏里用本服务登录一次即可自动创建,之后就能在这里改名。"
          />
          <template v-else>
            <a-space style="width: 100%">
              <a-input v-model:value="mcName" :status="nameError ? 'error' : ''" />
              <a-button type="primary" :loading="saving" @click="rename">改名</a-button>
            </a-space>
            <p v-if="nameError" class="error-text" style="margin-top: 8px">{{ nameError }}</p>
            <p v-else class="muted" style="margin-top: 8px">改名后旧名会在保留期内禁止他人注册。</p>
          </template>
        </a-card>

        <a-card title="皮肤" style="margin-bottom: 16px">
          <a-space direction="vertical" style="width: 100%">
            <a-space>
              <a-upload :show-upload-list="false" :before-upload="(file: File) => beforeUpload('skin', file)" accept="image/png">
                <a-button :loading="uploading === 'skin'">上传皮肤</a-button>
              </a-upload>
              <a-popconfirm title="删除后需要重新上传,确定?" @confirm="remove('skin')">
                <a-button v-if="skin" danger>删除</a-button>
              </a-popconfirm>
            </a-space>
            <span class="muted">{{ ACCEPTED_SKIN }}</span>
            <a-image v-if="skin" :src="skin.url" :width="128" :height="128" style="image-rendering: pixelated" />
          </a-space>
        </a-card>

        <a-card title="披风" style="margin-bottom: 16px">
          <a-space direction="vertical" style="width: 100%">
            <a-space>
              <a-upload :show-upload-list="false" :before-upload="(file: File) => beforeUpload('cape', file)" accept="image/png">
                <a-button :loading="uploading === 'cape'">上传披风</a-button>
              </a-upload>
              <a-popconfirm title="删除后需要重新上传,确定?" @confirm="remove('cape')">
                <a-button v-if="cape" danger>删除</a-button>
              </a-popconfirm>
            </a-space>
            <span class="muted">{{ ACCEPTED_CAPE }}</span>
          </a-space>
        </a-card>

        <a-card title="Minecraft 登录">
          <a-space>
            <a-switch
              :checked="store.account?.mc_login_enabled ?? false"
              :disabled="toggling"
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

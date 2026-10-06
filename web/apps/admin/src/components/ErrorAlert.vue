<script setup lang="ts">
/**
 * 统一的错误横幅。
 *
 * 三段式:标题说发生了什么、正文给可操作建议、末尾是错误码 + HTTP 状态。
 * 末尾那串码是报障时唯一能让后端查到日志的线索 —— 它单独成行,
 * 不和正文挤在一起,才不会被读成正文的一部分。
 *
 * 两个 app 各留一份:两个 SPA 都是独立构建的 Vite 应用,
 * 没有共享的 .vue 资源目录(与 EmptyState 同样的处理)。
 * 纯逻辑放 @yggauth/shared,展示组件留在各自的 components 下。
 */
import { computed } from 'vue'

import type { ErrorBanner } from '@yggauth/shared'

const props = defineProps<{ banner: ErrorBanner | null }>()

const detail = computed(() => [props.banner?.hint, props.banner?.trace].filter(Boolean).join('\n'))
</script>

<template>
  <a-alert
    v-if="banner"
    class="error-alert"
    type="error"
    show-icon
    style="margin-bottom: 16px"
    :message="banner.title"
    :description="detail || undefined"
  />
</template>

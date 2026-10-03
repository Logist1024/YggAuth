<script setup lang="ts">
/**
 * 统一的空态。
 *
 * Ant Design Vue 的 a-empty 默认只显示「暂无数据」四个字 ——
 * 它既不解释为什么是空的,也不告诉用户怎么让它有内容。
 * 列表页最容易让人卡住的就是这一刻:满屏空白,分不清是
 * 「还没有数据」「筛选没匹配上」还是「加载失败了」。
 *
 * 刻意放在应用内部而不是 @yggauth/shared:那个包是纯 TS
 * (typecheck 走 tsc,不认识 .vue),放进去两个应用的构建与
 * 类型检查都要额外配置,收益不值这个复杂度。
 */
defineProps<{
  /** 一句话解释这里为什么是空的。 */
  description: string
  /** 补充说明:怎么让它有内容。 */
  hint?: string
}>()
</script>

<template>
  <a-empty>
    <template #description>
      <div>{{ description }}</div>
      <div v-if="hint" class="muted" style="margin-top: 4px">{{ hint }}</div>
    </template>
    <!-- 默认插槽留给行动按钮。 -->
    <slot />
  </a-empty>
</template>

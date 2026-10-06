<script setup lang="ts">
/**
 * 统一的空态。
 *
 * Ant Design Vue 的 a-empty 默认只显示「暂无数据」四个字 ——
 * 它既不解释为什么是空的,也不告诉用户怎么让它有内容。
 * 列表页最容易让人卡住的就是这一刻:满屏空白,分不清是
 * 「还没有数据」「筛选没匹配上」还是「加载失败了」。
 *
 * 与管理后台的同名组件是各自一份:两个应用是独立构建的,
 * 一个共享 .vue 组件要额外引一个 workspace 包与构建配置,
 * 收益不值这个复杂度。文案与结构保持一致即可。
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

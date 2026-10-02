<script setup lang="ts">
// 根组件只负责挂路由视图。
// 全局样式、主题色都在 index.html 与 main.ts 里定,不在这里做,
// 免得首屏先渲染一次默认样式再切主题(闪一下)。
</script>

<template>
  <RouterView />
</template>

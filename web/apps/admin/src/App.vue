<script setup lang="ts">
// 根组件只负责两件事:把 Ant Design Vue 的默认文案切成中文,挂路由视图。
//
// 不挂 locale 的话,空表会显示 "No data"、分页显示 "20 / page" ——
// 一个中文界面里混着英文默认文案,看上去像没做完。
//
// 全局样式、主题色都在 index.html 与 main.ts 里定,不在这里做,
// 免得首屏先渲染一次默认样式再切主题(闪一下)。
import zhCN from 'ant-design-vue/es/locale/zh_CN'
</script>

<template>
  <a-config-provider :locale="zhCN">
    <RouterView />
  </a-config-provider>
</template>

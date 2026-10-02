<script setup lang="ts">
// 404。直接给一个链接而不是空白页 ——
// 用户看到空白会以为服务挂了,看到 404 知道是自己走错了。
</script>

<template>
  <div class="auth-shell">
    <a-result status="404" title="页面不存在" sub-title="你访问的地址没有对应的页面。">
      <template #extra>
        <RouterLink to="/">返回首页</RouterLink>
      </template>
    </a-result>
  </div>
</template>

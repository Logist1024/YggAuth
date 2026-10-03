<script setup lang="ts">
// 后台外壳:侧边菜单 + 内容区。
//
// 菜单来自后端并已按权限过滤,这里不再重复过滤一次 ——
// 两处各写一遍规则,总有一处会漏更新。
//
// 分组标题同样来自后端(menuItem.Group)。九项平铺时,
// 「OIDC 客户端」「材质库」这类专有名词夹在「账号管理」中间,
// 新管理员看不出哪些跟自己有关;分组比逐项加解释更省地方。
import { computed } from 'vue'
import { useRoute, useRouter } from 'vue-router'

import { useAdminStore, type MenuItem } from '../stores/admin'

const store = useAdminStore()
const route = useRoute()
const router = useRouter()

const selectedKeys = computed(() => [String(route.name)])

/**
 * 把扁平菜单拆成「置顶项 + 若干分组」。
 *
 * 分组顺序取分组**首次出现**的顺序,而不是按名字排序 ——
 * 顺序由后端的菜单定义决定,前端排序会让两边不一致。
 */
const layout = computed(() => {
  const ungrouped: MenuItem[] = []
  const groups: { title: string; items: MenuItem[] }[] = []

  for (const item of store.visibleMenu) {
    if (!item.group) {
      ungrouped.push(item)
      continue
    }
    let g = groups.find((x) => x.title === item.group)
    if (!g) {
      g = { title: item.group, items: [] }
      groups.push(g)
    }
    g.items.push(item)
  }

  return { ungrouped, groups }
})

async function onLogout(): Promise<void> {
  await store.logout()
  await router.push({ name: 'login' })
}
</script>

<template>
  <a-layout style="min-height: 100vh">
    <a-layout-sider theme="light" width="220">
      <div style="padding: 16px; font-weight: 600; color: #3b6ea5; font-size: 16px">
        YggAuth 管理后台
      </div>
      <a-menu mode="inline" :selected-keys="selectedKeys">
        <a-menu-item v-for="item in layout.ungrouped" :key="item.key">
          <RouterLink :to="item.path ?? '/'">{{ item.title }}</RouterLink>
        </a-menu-item>

        <a-menu-item-group v-for="g in layout.groups" :key="g.title" :title="g.title">
          <a-menu-item v-for="item in g.items" :key="item.key">
            <RouterLink :to="item.path ?? '/'">{{ item.title }}</RouterLink>
          </a-menu-item>
        </a-menu-item-group>
      </a-menu>
    </a-layout-sider>

    <a-layout>
      <a-layout-header style="background: #fff; display: flex; align-items: center; padding: 0 24px">
        <span style="font-weight: 600">{{ route.meta.title ?? '管理后台' }}</span>
        <span style="flex: 1" />
        <span class="muted" style="margin-right: 12px">{{ store.account?.username }}</span>
        <a-button type="link" @click="onLogout">退出</a-button>
      </a-layout-header>
      <a-layout-content>
        <RouterView />
      </a-layout-content>
    </a-layout>
  </a-layout>
</template>

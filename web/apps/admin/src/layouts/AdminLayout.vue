<script setup lang="ts">
// 后台外壳:侧边菜单 + 内容区。
//
// 菜单来自后端并已按权限过滤,这里不再重复过滤一次 ——
// 两处各写一遍规则,总有一处会漏更新。
//
// 分组标题同样来自后端(menuItem.Group)。九项平铺时,
// 「OIDC 客户端」「材质库」这类专有名词夹在「账号管理」中间,
// 新管理员看不出哪些跟自己有关;分组比逐项加解释更省地方。
import { computed, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'

import { useAdminStore, type MenuItem } from '../stores/admin'
import { publicConfig } from '@yggauth/shared'

const store = useAdminStore()
const route = useRoute()
const router = useRouter()

/**
 * 站点名:来自 GET /api/public/config(路由守卫已在渲染前拉过)。
 *
 * 左栏以前写死「YggAuth 管理后台」—— 后台改了站点名,这行还是旧的,
 * 又是一次「改了没用」,而且没有任何报错。
 */
const siteName = publicConfig().site_name || 'YggAuth'

/**
 * 侧栏是否收起。
 *
 * 两个触发源写同一个 ref:窄屏时由 Sider 的 breakpoint 自动收起,
 * 屏幕够宽时由顶栏的「菜单」按钮手动切换。只留其中一个的话,
 * 自动收起后用户就再也打不开菜单了。
 */
const collapsed = ref(false)

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
    <!--
      collapsedWidth=0:菜单项只有文字没有图标,收成 80px 的窄条
      只会留下一列空白。宽度不足时整条让开,内容区拿回全部空间。
      trigger 关掉自带箭头(收起后它同样无处可点),改用顶栏按钮。
    -->
    <a-layout-sider
      v-model:collapsed="collapsed"
      theme="light"
      width="220"
      breakpoint="lg"
      collapsed-width="0"
      :trigger="null"
      collapsible
    >
      <div style="padding: 16px; font-weight: 600; color: #3b6ea5; font-size: 16px">
        {{ siteName }} 管理后台
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
      <a-layout-header style="background: #fff; display: flex; align-items: center; padding: 0 16px">
        <a-button type="text" class="only-narrow" style="margin-right: 8px" @click="collapsed = !collapsed">
          菜单
        </a-button>
        <span style="font-weight: 600">{{ route.meta.title ?? '管理后台' }}</span>
        <span style="flex: 1" />
        <span class="muted" style="margin-right: 12px">{{ store.account?.username }}</span>
        <a-button type="link" @click="onLogout">退出</a-button>
      </a-layout-header>
      <a-layout-content>
        <!--
          跨页提示。守卫把越权访问送回仪表盘时,靠这里把原因说出来 ——
          没有它,那次跳转在用户眼里就是「点了菜单没反应」。
          与登录页那条同源(都是 store.notice):那条解释「为什么又站在登录页」,
          这条解释「为什么回到仪表盘」。放在内容区顶部而不是顶栏:
          它属于这一次跳转,关掉就不该再占地方。
        -->
        <a-alert
          v-if="store.notice"
          type="info"
          show-icon
          closable
          style="margin: 16px 16px 0"
          :message="store.notice"
          @close="store.clearNotice()"
        />
        <!-- 过渡只包住内容区:侧栏与顶栏是常驻的,跟着一起淡入会整屏闪一下 -->
        <RouterView v-slot="{ Component }">
          <Transition name="route">
            <component :is="Component" />
          </Transition>
        </RouterView>
      </a-layout-content>
    </a-layout>
  </a-layout>
</template>

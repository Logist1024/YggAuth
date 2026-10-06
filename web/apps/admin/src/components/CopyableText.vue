<script setup lang="ts">
import { onUnmounted, ref } from 'vue'

/**
 * 「一段字符串 + 复制按钮」,用于邀请码、客户端密钥这类手抄易错的东西。
 *
 * 抽成组件的两个理由:
 * 1. 复制是这两个页面的主操作,此前只有邀请码页有按钮、密钥页没有,
 *    同一件事两种行为,用户得先猜哪个才是对的;
 * 2. 「复制失败怎么办」的降级提示(非安全上下文 Clipboard API 不可用)
 *    在两处各写一遍迟早会漂移 —— 与其假装复制成功,不如直说让对方手动选。
 */
const props = withDefaults(
  defineProps<{
    /** 要复制的原文,同时也是默认展示的内容。 */
    value: string
    /** 按钮文案后缀,如「邀请码」「密钥」。 */
    label?: string
    /** 展示成等宽代码块;警示条里的长串内联展示更省地方。 */
    boxed?: boolean
  }>(),
  { label: '', boxed: true },
)

const copied = ref(false)
const err = ref('')
/** setTimeout 句柄。组件卸载后不能再写 ref,所以先记下来、离开时清掉。 */
let timer: ReturnType<typeof setTimeout> | undefined

onUnmounted(() => {
  if (timer) clearTimeout(timer)
})

async function copy(): Promise<void> {
  err.value = ''
  try {
    await navigator.clipboard.writeText(props.value)
    copied.value = true
    if (timer) clearTimeout(timer)
    timer = setTimeout(() => {
      copied.value = false
    }, 2000)
  } catch {
    copied.value = false
    err.value = '浏览器不允许自动复制,请手动选中后复制(Ctrl/Cmd + C)。'
  }
}
</script>

<template>
  <span class="copyable">
    <span class="copyable-main">
      <code v-if="boxed">{{ value }}</code>
      <span v-else>{{ value }}</span>
      <a-button size="small" @click="copy">
        {{ copied ? '已复制' : `复制${label}` }}
      </a-button>
    </span>
    <!-- 失败提示独占一行:挤在按钮后面会把整行顶得换行 -->
    <span v-if="err" class="error-text">{{ err }}</span>
  </span>
</template>

<style scoped>
.copyable {
  display: inline-block;
}

.copyable-main {
  display: inline-flex;
  align-items: center;
  gap: 8px;
}

.error-text {
  display: block;
  margin-top: 4px;
}
</style>

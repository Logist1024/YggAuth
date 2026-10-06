/**
 * 展示层的时间格式化。
 *
 * 单独成模块而不是各自在页面里写 `new Date(x).toLocaleString()` ——
 * 后者在字段名对不上、值缺失、或后端还没写入时会渲染成
 * **「Invalid Date」**,一屏 12 行那样的字符串看起来像数据坏了,
 * 实际只是契约漂移。一个破折号更容易让人看出「这里没有值」,
 * 而不是让人以为系统时间错乱。
 */

/**
 * 把 ISO/RFC3339 时间串格式化为本地可读时间。
 *
 * @param value 后端返回的时间串。空值、无法解析的值都安全。
 * @param fallback 无法解析时显示的占位符,默认「—」。
 */
export function formatTime(value: string | null | undefined, fallback = '—'): string {
  if (!value) return fallback
  const t = new Date(value)
  if (Number.isNaN(t.getTime())) return fallback
  return t.toLocaleString()
}

/**
 * 相对时间,如「3 分钟前」。
 *
 * 列表里逐行显示绝对时间时,同一分钟内的几十条记录会长得一模一样;
 * 相对时间把「刚刚发生」和「上个月发生」直接区分开。
 *
 * 超过 30 天回退到绝对时间 —— 「87 天前」不如具体日期有用。
 */
export function formatRelative(value: string | null | undefined, now = Date.now()): string {
  if (!value) return '—'
  const t = new Date(value).getTime()
  if (Number.isNaN(t)) return '—'

  const diff = now - t
  if (diff < 0) return formatTime(value)

  const seconds = Math.floor(diff / 1000)
  if (seconds < 60) return '刚刚'
  const minutes = Math.floor(seconds / 60)
  if (minutes < 60) return `${minutes} 分钟前`
  const hours = Math.floor(minutes / 60)
  if (hours < 24) return `${hours} 小时前`
  const days = Math.floor(hours / 24)
  if (days <= 30) return `${days} 天前`

  return formatTime(value)
}

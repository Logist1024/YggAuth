/**
 * 展示层的小工具。
 *
 * 单独成模块的原因与 format.ts 相同:这类逻辑写在各页面里,
 * 代价不是多几行,而是「有的页面记得回顶、有的忘了」这种
 * 从代码上看不出来的不一致。
 */

/**
 * 把页面滚回顶部。
 *
 * 用在换页、换筛选这类**整屏数据替换**的场景。分页器通常贴在列表
 * 底部,用户点完「下一页」时视线正停在原处,而新列表的第一行已经
 * 跑到屏幕上方 —— 不回顶就得自己往上滚,才看得到刚查出来的数据。
 *
 * 用瞬时而不是平滑滚动:数据已经换掉,滑行的几百毫秒里页面停留在
 * 一个「新数据 + 旧位置」的错位状态,反而更像「点了没反应」。
 */
export function backToTop(): void {
  window.scrollTo({ top: 0 })
}

/**
 * 分页页脚的总数文案,给 `a-pagination` 的 `:show-total` 用。
 *
 * 列表页过去只显示页码:一共多少条、当前看的是哪一段,都无从得知 ——
 * 而总数是每个列表接口本来就回的 `total`,缺的只是把它摆出来。
 *
 * `total` 为 0 时返回空串:那一页已经有空状态文案(「还没有…」),
 * 再补一行「共 0 条」只是噪音。
 *
 * @param total 后端返回的总数。
 * @param range 当前页区间,antd 给的是 1-based [起, 止]。
 */
export function showTotal(total: number, range: [number, number]): string {
  if (total <= 0) return ''
  return `共 ${total} 条,当前 ${range[0]}–${range[1]}`
}

// ---------------------------------------------------------------- 地址栏里的列表状态

/**
 * 从地址栏读一段文本。
 *
 * 查询参数的类型是 `string | string[] | null`:`?q=a&q=b` 会解析成数组,
 * 直接渲染就成了「a,b」。这里一律取第一项,缺失或类型不对就当空串 ——
 * 页面拿到的永远是自己认识的值,手滑的链接不至于把表格渲染崩。
 */
export function readText(raw: unknown): string {
  if (typeof raw === 'string') return raw
  if (Array.isArray(raw) && typeof raw[0] === 'string') return raw[0]
  return ''
}

/**
 * 从地址栏读页码:非整数、小于 1 一律退回 1。
 */
export function readPage(raw: unknown): number {
  const n = Number(readText(raw))
  return Number.isInteger(n) && n >= 1 ? n : 1
}

/**
 * 从地址栏读每页条数,只认分页器提供的那几档(antd 默认
 * 10/20/50/100),其余退回 20。
 *
 * 放任 `?size=99999` 生效,换来的是一屏渲染不动的表格
 * 和一次注定被后端截断的查询 —— 那不是「用户的设置」,是脏链接。
 */
export function readPageSize(
  raw: unknown,
  fallback = 20,
  options: readonly number[] = [10, 20, 50, 100],
): number {
  const n = Number(readText(raw))
  return options.includes(n) ? n : fallback
}

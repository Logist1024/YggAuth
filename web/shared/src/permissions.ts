/**
 * 权限与菜单。
 *
 * admin-web 的菜单**由后端下发**,前端只按权限点过滤。
 * 这样加一个菜单不需要改前端,也不需要「前端记得同步一次权限表」——
 * 那种同步迟早会漏,然后表现成一个管理员看到了不该看到的入口。
 */

export interface Permission {
  /** 权限点,如 oidc:client:write。支持 * 通配。 */
  key: string
  /** 人类可读名称,直接显示在页面上。 */
  name: string
  description?: string
}

/**
 * 判断主体是否持有某权限点。
 *
 * 支持末位通配:持有 `oidc:*` 即视为持有 `oidc:client:write`。
 * 这与 Go 侧 rbac.Can 的语义一致 —— 两边必须一样,
 * 否则会出现「前端显示按钮,后端一律 403」或反过来。
 */
export function can(permissions: string[], key: string): boolean {
  if (permissions.includes('*')) {
    return true
  }
  if (permissions.includes(key)) {
    return true
  }

  const idx = key.indexOf(':')
  if (idx <= 0) {
    return false
  }
  return permissions.includes(`${key.slice(0, idx)}:*`)
}

/**
 * 过滤菜单项。
 *
 * 没有子项、且自身权限不满足的菜单项直接丢弃;有子项的保留父项,
 * 但子项全被过滤掉时父项也不留 —— 一个空菜单比没有菜单更让人困惑。
 */
export function filterMenu<T extends { permission?: string; children?: T[] }>(
  items: T[],
  permissions: string[],
): T[] {
  const out: T[] = []
  for (const item of items) {
    if (item.children && item.children.length > 0) {
      const children = filterMenu(item.children, permissions)
      if (children.length > 0) {
        out.push({ ...item, children })
      }
      continue
    }
    if (!item.permission || can(permissions, item.permission)) {
      out.push(item)
    }
  }
  return out
}
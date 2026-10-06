/**
 * 站点公开配置(GET /api/public/config,见 docs/configuration.md §7.2)。
 *
 * 这条端点的存在理由是「站点名称这项设置得真的生效」:
 * `index.html` 里的标题是**构建期**字符串,后台改了站点名,
 * 浏览器标签与后台左栏还是旧的 —— 改了没用,而且没有任何报错。
 *
 * 与 `/api/auth/policy` 同一原则:展示规则只有一份真源,前端不抄。
 */
import { createClient } from './api'

export interface PublicConfig {
  site_name: string
  logo_url: string
  support_email: string
  registration_mode: string
  password_min_length: number
  require_email_verification: boolean
}

/** 拉不到时的兜底值:与 index.html 的构建期标题、后端默认值一致。 */
export const FALLBACK_PUBLIC_CONFIG: PublicConfig = {
  site_name: 'YggAuth',
  logo_url: '',
  support_email: '',
  registration_mode: 'open',
  password_min_length: 8,
  require_email_verification: true,
}

const client = createClient()
let current: PublicConfig = FALLBACK_PUBLIC_CONFIG
let loading: Promise<PublicConfig> | null = null

/** 读取最近一次拿到的公开配置(未加载或加载失败时是兜底值)。 */
export function publicConfig(): PublicConfig {
  return current
}

/**
 * 拉取公开配置。两个 SPA 各在首次导航时调一次。
 *
 * 失败**不抛**:站点名拿不到不该让登录页起不来 ——
 * 用兜底值继续,最坏情况是标签页显示默认名称。
 */
export async function loadPublicConfig(): Promise<PublicConfig> {
  if (!loading) {
    loading = client
      .get<PublicConfig>('/api/public/config')
      .then((data) => {
        current = { ...FALLBACK_PUBLIC_CONFIG, ...data }
        return current
      })
      .catch(() => current)
  }
  return loading
}

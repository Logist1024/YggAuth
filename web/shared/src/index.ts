/**
 * 共享的类型与工具。
 *
 * 两个 SPA 都从这里取:统一响应包结构、错误码、品牌主题、
 * 以及**从后端下发的校验规则**。
 */

// ---------------------------------------------------------------- 统一响应包

/**
 * 后端所有 JSON 接口的统一信封(ADR-008)。
 *
 * 注意:协议端点(/oauth/*、/mc/*)不走这套结构,它们有各自的格式。
 * 这里的类型只适用于 /api/* 与 /api/admin/*。
 */
export interface Envelope<T> {
  code: number
  message: string
  data: T
}

// 错误码表在 ./codes.ts,末尾统一再导出。
//
// 单独成模块而不是写在这里,是为了让 api.ts 能直接引用 ——
// 本文件末尾有 `export * from './api'`,从 api.ts 反向 import 本文件
// 会形成环,而当初为了躲这个环就在 api.ts 里抄了一份常量,
// 两份定义漂移后导致了「成功响应被当成错误」。详见 codes.ts。

export function isEnvelope(value: unknown): value is Envelope<unknown> {
  return (
    typeof value === 'object' &&
    value !== null &&
    'code' in value &&
    'message' in value &&
    'data' in value
  )
}

// ---------------------------------------------------------------- 品牌主题

export type ClientId = 'app' | 'mc'

export interface BrandTheme {
  clientId: ClientId
  /** 页面标题后缀,出现在浏览器标签与登录页。 */
  title: string
  /** 主色(Ant Design 的 primary 色)。 */
  primary: string
  /** 副标题。 */
  subtitle: string
}

/**
 * 两套品牌主题。
 *
 * 同一个账号从网页控制台登录、从游戏内启动器登录时看到不同的主色,
 * 是让用户知道自己「正在用哪条路径登录」最省事的办法 ——
 * 钓鱼站最容易伪造的就是一个长得像的登录页。
 */
export const BRAND_THEMES: Record<ClientId, BrandTheme> = {
  app: {
    clientId: 'app',
    title: 'YggAuth',
    subtitle: '账号中心',
    primary: '#3b6ea5',
  },
  mc: {
    clientId: 'mc',
    title: 'YggAuth',
    subtitle: 'Minecraft 登录',
    primary: '#5a8f3c',
  },
}

export const DEFAULT_CLIENT_ID: ClientId = 'app'

/**
 * 从查询串里解析 client_id。
 *
 * 只接受白名单里的取值:未知值一律退回默认主题,
 * 而不是把任意字符串透传给下游 —— 那既是注入面,
 * 也会让一个手滑的链接变成一个没样式的页面。
 */
export function parseClientId(raw: string | null | undefined): ClientId {
  return raw === 'mc' || raw === 'app' ? raw : DEFAULT_CLIENT_ID
}
export * from './codes'
export * from './validation'
export * from './permissions'
export * from './api'
export * from './format'
export * from './ui'
export * from './audit'
export * from './site'

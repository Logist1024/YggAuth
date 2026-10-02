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

/** 业务错误码,与 Go 侧 internal/platform/apperr 保持一致。 */
export const ErrCode = {
  OK: 10000,
  INVALID_ARGUMENT: 10001,
  UNAUTHORIZED: 10002,
  INTERNAL: 10003,
  FORBIDDEN: 10004,
  CONFLICT: 10005,
  NOT_FOUND: 10006,
  RATE_LIMITED: 10007,
} as const

/** 服务端会话过期后返回的码。 */
export const ErrCodeSessionExpired = 20008

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
export * from './validation'
export * from './permissions'
export * from './api'

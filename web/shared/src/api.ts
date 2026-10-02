/**
 * 统一 API 客户端。
 *
 * 两个 SPA 共用同一份错误处理逻辑:
 * 拆信封、抛 ApiError、401 触发一次刷新、刷新失败跳登录。
 * 逻辑一旦分散到两个应用里,行为迟早会不一致 ——
 * 然后表现为「管理后台能自动续期,账号中心不能」。
 */
import { ErrCodeSessionExpired, isEnvelope } from './index'

export class ApiError extends Error {
  readonly code: number
  readonly status: number

  constructor(message: string, code: number, status: number) {
    super(message)
    this.name = 'ApiError'
    this.code = code
    this.status = status
  }

  /** 会话失效,应当引导用户重新登录。 */
  get requiresLogin(): boolean {
    return this.status === 401 || this.code === ErrCodeSessionExpired
  }
}

export interface ApiOptions {
  /** 请求基址。留空表示同源。 */
  baseURL?: string
  /** 401 时的回调,用于触发 token 刷新或跳转登录。 */
  onUnauthorized?: () => Promise<boolean> | boolean
  /** 取 CSRF token。 */
  csrfToken?: () => string | null
  /** 取 Bearer token(管理后台用会话 cookie,账号站未必需要)。 */
  bearerToken?: () => string | null
}

interface RequestOptions {
  method?: string
  body?: unknown
  /** 原始二进制请求体(上传皮肤)。 */
  raw?: BodyInit
  headers?: Record<string, string>
  signal?: AbortSignal
  /** 内部使用:已经重试过一次,避免无限递归。 */
  retried?: boolean
}

export class ApiClient {
  private readonly options: ApiOptions

  constructor(options: ApiOptions = {}) {
    this.options = options
  }

  async request<T>(path: string, opts: RequestOptions = {}): Promise<T> {
    const headers: Record<string, string> = { Accept: 'application/json', ...opts.headers }
    const method = opts.method ?? 'GET'

    let body: BodyInit | undefined
    if (opts.raw !== undefined) {
      body = opts.raw
    } else if (opts.body !== undefined) {
      headers['Content-Type'] = 'application/json'
      body = JSON.stringify(opts.body)
    }

    const csrf = this.options.csrfToken?.()
    if (csrf && method !== 'GET' && method !== 'HEAD') {
      headers['X-CSRF-Token'] = csrf
    }
    const bearer = this.options.bearerToken?.()
    if (bearer) {
      headers.Authorization = `Bearer ${bearer}`
    }

    const url = `${this.options.baseURL ?? ''}${path}`
    const resp = await fetch(url, { method, headers, body, signal: opts.signal, credentials: 'same-origin' })

    if (resp.status === 204) {
      return undefined as T
    }

    const text = await resp.text()
    let parsed: unknown = null
    if (text) {
      try {
        parsed = JSON.parse(text)
      } catch {
        // 非 JSON 响应(反向代理的错误页之类)。
        // 直接按状态码构造错误,而不是把 HTML 塞给用户看。
        if (!resp.ok) {
          throw new ApiError(`请求失败(HTTP ${resp.status})`, resp.status, resp.status)
        }
        throw new ApiError('响应不是合法的 JSON', ErrCode_INTERNAL_FALLBACK, resp.status)
      }
    }

    if (isEnvelope(parsed)) {
      if (parsed.code !== ErrCode_OK) {
        const err = new ApiError(parsed.message, parsed.code, resp.status)
        if (err.requiresLogin && !opts.retried) {
          const recovered = await this.recoverSession()
          if (recovered) {
            return this.request<T>(path, { ...opts, retried: true })
          }
        }
        throw err
      }
      return parsed.data as T
    }

    if (!resp.ok) {
      throw new ApiError(`请求失败(HTTP ${resp.status})`, resp.status, resp.status)
    }
    return parsed as T
  }

  /** 尝试恢复会话。返回 false 表示恢复不了,应当跳登录。 */
  private async recoverSession(): Promise<boolean> {
    if (!this.options.onUnauthorized) {
      return false
    }
    try {
      return await this.options.onUnauthorized()
    } catch {
      return false
    }
  }

  get<T>(path: string, signal?: AbortSignal): Promise<T> {
    return this.request<T>(path, { method: 'GET', signal })
  }

  post<T>(path: string, body?: unknown, signal?: AbortSignal): Promise<T> {
    return this.request<T>(path, { method: 'POST', body, signal })
  }

  put<T>(path: string, body?: unknown, signal?: AbortSignal): Promise<T> {
    return this.request<T>(path, { method: 'PUT', body, signal })
  }

  patch<T>(path: string, body?: unknown, signal?: AbortSignal): Promise<T> {
    return this.request<T>(path, { method: 'PATCH', body, signal })
  }

  delete<T>(path: string, signal?: AbortSignal): Promise<T> {
    return this.request<T>(path, { method: 'DELETE', signal })
  }
}

// 与后端对齐的两个常量。放在这里而不是从 index.ts 导入是为了
// 避免 api.ts 与 index.ts 互相依赖形成环。
const ErrCode_INTERNAL_FALLBACK = 10003
const ErrCode_OK = 10000

/** 构造一个默认客户端,应用启动时用配置补齐。 */
export function createClient(options: ApiOptions = {}): ApiClient {
  return new ApiClient(options)
}

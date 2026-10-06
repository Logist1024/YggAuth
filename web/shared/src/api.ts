/**
 * 统一 API 客户端。
 *
 * 两个 SPA 共用同一份错误处理逻辑:
 * 拆信封、抛 ApiError、401 触发一次刷新、刷新失败跳登录。
 * 逻辑一旦分散到两个应用里,行为迟早会不一致 ——
 * 然后表现为「管理后台能自动续期,账号中心不能」。
 */
import { ErrCode, ErrCodeSessionExpired, codeHint, codeTitle } from './codes'
import { isEnvelope } from './index'

/**
 * 同源校验没过时的建议。
 *
 * 单独拎出来,是因为它在错误码上和「没有权限」是同一个 30001:
 * 后端 RequirePermission 报「缺少权限:xxx」,CSRFProtect 报「请求来源校验失败」。
 * 提示只跟错误码走的话,用户拿到的是「你没权限」,于是跑去要权限 ——
 * 而真正要改的是访问地址(反向代理改写了 Host、或用与站点配置不一致的
 * IP/端口访问)。等后端补一个专用错误码后,这段特判就可以删掉。
 */
const SAME_ORIGIN_HINT =
  '同源校验没通过:一般是访问地址与站点配置不一致(换过 IP、端口,或经反向代理改写了 Host),换成本站配置的地址再试。'

/**
 * 后端返回的业务错误。
 *
 * 除了原始的 code / status,还预先算好了三样展示用的东西 ——
 * 调用方不该自己去查错误码表,那样每多一个展示位就多一份可能漂移的映射。
 */
export class ApiError extends Error {
  readonly code: number
  readonly status: number
  /** 面向用户的一句话说明。后端信封的 message 优先,缺失时按错误码兜底。 */
  readonly title: string
  /** 用户可以采取的行动。没有可操作建议时为空串。 */
  readonly hint: string

  constructor(message: string, code: number, status: number) {
    const title = message.trim() || codeTitle(code) || '请求失败'
    // message 是各展示位直接引用的文本,这里把错误码一并带上:
    // 用户报障时能直接念出来,不用再翻浏览器控制台。
    super(`${title}（错误码 ${code}）`)
    this.name = 'ApiError'
    this.code = code
    this.status = status
    this.title = title
    this.hint =
      code === ErrCode.FORBIDDEN && title.includes('来源校验') ? SAME_ORIGIN_HINT : codeHint(code)
  }

  /**
   * 会话失效,应当引导用户重新登录。
   *
   * 只认「这次响应说明登录态没了」的错误。密码错(20002)与账号被锁
   * (20006)的 HTTP 状态同样是 401,但它们说的是**这次输入不对**,
   * 不是**你没有登录态** —— 混进来会让登录页在用户填错密码时
   * 跑一遍会话恢复,把一句「密码错了」演成「登录状态已过期」。
   */
  get requiresLogin(): boolean {
    if (this.code === ErrCodeSessionExpired) return true
    if (this.status !== 401) return false
    return this.code !== ErrCode.INVALID_PASSWORD && this.code !== ErrCode.ACCOUNT_LOCKED
  }

  /** 供支持人员定位的一行信息:错误码 + HTTP 状态。 */
  get trace(): string {
    return `错误码 ${this.code} · HTTP ${this.status}`
  }
}

/** 可以直接渲染的错误提示。 */
export interface ErrorBanner {
  /** 一句话说明发生了什么。 */
  title: string
  /** 可操作的建议。为空表示这条错误没什么可建议的,不占位。 */
  hint: string
  /** 错误码与 HTTP 状态。网络层失败时为空串。 */
  trace: string
}

/**
 * 把任意异常转成可直接渲染的错误提示。
 *
 * 覆盖三类:后端业务错误(ApiError)、网络层失败(fetch 抛 TypeError)、
 * 以及代码里的意外异常 —— 最后这一类用调用方给的兜底文案。
 */
export function errorBanner(err: unknown, fallback: string): ErrorBanner {
  if (err instanceof ApiError) {
    return { title: err.title, hint: err.hint, trace: err.trace }
  }
  // fetch 在断网、DNS 失败、被 CORS 拦下时抛 TypeError。
  // 这类失败没有错误码可给,硬凑一个只会误导排查方向。
  if (err instanceof TypeError) {
    return { title: '无法连接服务器', hint: '请检查网络连接后重试。', trace: '' }
  }
  return { title: fallback, hint: '', trace: '' }
}

/**
 * 本地校验与提示类的错误 —— 没有异常、也没有错误码可给。
 *
 * 与 errorBanner 的分工:那个把**捕获到的异常**转成提示,
 * 这个直接包装一句我们自己知道的话(「名称不能为空」)。
 *
 * 返回结构而不是字符串,是为了让渲染端只有一种横幅形态 ——
 * 否则同一页面上「后端报错」和「本地校验失败」会长得不一样。
 */
export function bannerMessage(title: string, hint = ''): ErrorBanner {
  return { title, hint, trace: '' }
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
        throw new ApiError('服务器返回了非预期的响应', ErrCode.INTERNAL, resp.status)
      }
    }

    if (isEnvelope(parsed)) {
      if (parsed.code !== ErrCode.OK) {
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
      throw new ApiError('服务器返回了非预期的响应', ErrCode.INTERNAL, resp.status)
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

// 错误码常量统一在 ./codes.ts —— 这里曾经为了绕开循环依赖抄过一份,
// 结果与后端漂移,把成功响应当成了失败。别再抄第二份。

/** 构造一个默认客户端,应用启动时用配置补齐。 */
export function createClient(options: ApiOptions = {}): ApiClient {
  return new ApiClient(options)
}

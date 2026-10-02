/**
 * 校验规则 —— 全部来自后端,前端不硬编码任何阈值。
 *
 * 这是 M6 的一条硬要求:前端与后端**共用同一份定义**。
 * 抄一份到前端是最容易做到、也最容易失效的做法:
 * 运维把 PASSWORD_MIN_LENGTH 从 8 提到 12,前端还在提示
 * 「至少 8 位」,用户填 8 位然后被后端拒绝 ——
 * 一个看起来像 bug 的 bug,而且只有用户会发现。
 *
 * 所以这里的规则全部通过 GET /api/auth/policy 拉取。
 */

export interface AccountPolicy {
  password_min_length: number
  password_max_length: number
  password_reject_common: boolean
  username_min_length: number
  username_max_length: number
  registration_mode: string
  require_email_verification: boolean
}

/** 拉取失败时的兜底值,与 Go 侧 DefaultPolicy 保持一致。 */
export const FALLBACK_POLICY: AccountPolicy = {
  password_min_length: 8,
  password_max_length: 128,
  password_reject_common: true,
  username_min_length: 3,
  username_max_length: 32,
  registration_mode: 'open',
  require_email_verification: true,
}

export interface ValidationResult {
  ok: boolean
  /** 面向用户的中文提示;ok 为真时为空。 */
  message: string
}

const ok: ValidationResult = { ok: true, message: '' }

function fail(message: string): ValidationResult {
  return { ok: false, message }
}

/**
 * 按字符数(而非字节数)计算长度。
 *
 * 中文密码按字节算会被无理由拒绝 —— 与后端的 rune 计数一致。
 */
function runeLength(value: string): number {
  return Array.from(value).length
}

export function validatePassword(password: string, policy: AccountPolicy): ValidationResult {
  const n = runeLength(password)
  if (n < policy.password_min_length) {
    return fail(`密码长度不足 ${policy.password_min_length} 位`)
  }
  if (n > policy.password_max_length) {
    return fail(`密码长度超过 ${policy.password_max_length} 位`)
  }
  if (n === 0) {
    return fail('密码不能为空')
  }
  return ok
}

const USERNAME_RE = /^[A-Za-z0-9_]+$/

export function validateUsername(username: string, policy: AccountPolicy): ValidationResult {
  const n = runeLength(username)
  if (n < policy.username_min_length) {
    return fail(`用户名至少 ${policy.username_min_length} 个字符`)
  }
  if (n > policy.username_max_length) {
    return fail(`用户名最多 ${policy.username_max_length} 个字符`)
  }
  if (!USERNAME_RE.test(username)) {
    return fail('用户名只能包含字母、数字与下划线')
  }
  return ok
}

/**
 * 邮箱校验刻意从简。
 *
 * 用一个「看起来对」的宽松正则挡住明显的笔误即可:
 * 真正的判定在后端 —— 前端正则再严密也拦不住有人直接调接口。
 */
export function validateEmail(email: string): ValidationResult {
  const trimmed = email.trim()
  if (!trimmed) {
    return fail('邮箱不能为空')
  }
  if (!/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(trimmed)) {
    return fail('邮箱格式不正确')
  }
  return ok
}

/** MC 玩家名规则,与 Go 侧 minecraft.ValidateName 一致。 */
const MC_NAME_RE = /^[A-Za-z0-9_]{3,16}$/

export function validateMCName(name: string): ValidationResult {
  if (!MC_NAME_RE.test(name)) {
    return fail('玩家名需为 3–16 位字母、数字或下划线')
  }
  return ok
}

/** 客户端 id 与客户端密钥的字符集(与 Go 侧一致)。 */
const CLIENT_ID_RE = /^[A-Za-z0-9_-]{4,64}$/

export function validateClientId(value: string): ValidationResult {
  if (!CLIENT_ID_RE.test(value)) {
    return fail('客户端标识为 4–64 位字母、数字、下划线或连字符')
  }
  return ok
}

/** 回调地址必须是 HTTPS;localhost 放行 HTTP 以便本地开发。 */
export function validateRedirectURI(uri: string): ValidationResult {
  if (!uri) {
    return fail('回调地址不能为空')
  }
  let parsed: URL
  try {
    parsed = new URL(uri)
  } catch {
    return fail('回调地址不是合法的 URL')
  }
  if (parsed.protocol === 'https:') {
    return ok
  }
  if (parsed.protocol === 'http:' && isLoopback(parsed.hostname)) {
    return ok
  }
  return fail('回调地址必须使用 HTTPS(仅本机允许 HTTP)')
}

function isLoopback(hostname: string): boolean {
  return hostname === 'localhost' || hostname === '127.0.0.1' || hostname === '[::1]'
}
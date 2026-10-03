/**
 * 业务错误码表 —— Go 侧 internal/platform/apperr 的**镜像**。
 *
 * 数值以后端为准,这里只负责让前端能按语义判断、并给用户一句人话。
 *
 * 为什么单独一个模块,而不是塞进 index.ts:
 * api.ts 需要这些常量,而 index.ts 末尾有 `export * from './api'` ——
 * 从 api.ts 反向 import index.ts 会形成环。之前正是为了躲这个环,
 * api.ts 里**又抄了一份** `const ErrCode_OK = 10000`,而 apperr 的
 * 成功码是 0。两份定义一旦漂移,`code !== OK` 恒真,于是每一个接口
 * 调用都抛错,而抛出来的错误信息恰好是信封里的 "ok" ——
 * 用户看到的就是登录页上一句莫名其妙的「ok」。
 *
 * 结论:常量不要抄第二份。这个模块不 import 任何人,谁都可以安全引用。
 */

export const ErrCode = {
  /** 成功。**不是 10000** —— 后端 apperr.CodeOK 就是 0。 */
  OK: 0,

  // ------------------------------------------------------------ 1xxxx 通用
  INVALID_ARGUMENT: 10001,
  RATE_LIMITED: 10002,
  INTERNAL: 10003,
  NOT_FOUND: 10004,
  CONFLICT: 10005,
  PAYLOAD_TOO_LARGE: 10006,
  UNAVAILABLE: 10007,

  // ------------------------------------------------------------ 2xxxx 账号与认证
  UNAUTHORIZED: 20001,
  INVALID_PASSWORD: 20002,
  ACCOUNT_DISABLED: 20003,
  EMAIL_TAKEN: 20004,
  USERNAME_TAKEN: 20005,
  ACCOUNT_LOCKED: 20006,
  WEAK_PASSWORD: 20007,
  INVALID_TOKEN: 20008,
  EMAIL_UNVERIFIED: 20009,
  INVITE_REQUIRED: 20010,
  INVITE_INVALID: 20011,
  SESSION_EXPIRED: 20012,

  // ------------------------------------------------------------ 3xxxx 权限
  FORBIDDEN: 30001,
  PERMISSION_DENIED: 30002,

  // ------------------------------------------------------------ 4xxxx OIDC
  OIDC_INVALID_REQUEST: 40001,
  OIDC_CLIENT_AUTH_FAILED: 40002,
  OIDC_REDIRECT_MISMATCH: 40003,
  OIDC_PKCE_FAILED: 40004,
  OIDC_SCOPE_DENIED: 40005,
  OIDC_UNSUPPORTED_GRANT: 40006,

  // ------------------------------------------------------------ 5xxxx Minecraft
  MC_NAME_INVALID: 50001,
  MC_NAME_TAKEN: 50002,
  MC_TOKEN_INVALID: 50003,
  MC_LOGIN_DISABLED: 50004,
  MC_TEXTURE_INVALID: 50005,
  MC_TEXTURE_TOO_LARGE: 50006,
  MC_SIGNATURE_ERROR: 50007,
  MC_READ_ONLY: 50008,
} as const

export type ErrCodeValue = (typeof ErrCode)[keyof typeof ErrCode]

/** 服务端会话过期后返回的码。401 之外还要看它,因为刷新失败时 HTTP 可能仍是 200。 */
export const ErrCodeSessionExpired = ErrCode.SESSION_EXPIRED

/**
 * 错误码的兜底标题。
 *
 * 正常情况下用后端信封里的 message(它本身就是中文且更具体),
 * 这张表只在 message 为空时兜底 —— 所以它必须与 apperr 的 meta 表一致。
 */
const TITLES: Readonly<Record<number, string>> = {
  [ErrCode.INVALID_ARGUMENT]: '参数校验失败',
  [ErrCode.RATE_LIMITED]: '请求过于频繁',
  [ErrCode.INTERNAL]: '服务器内部错误',
  [ErrCode.NOT_FOUND]: '资源不存在',
  [ErrCode.CONFLICT]: '资源冲突',
  [ErrCode.PAYLOAD_TOO_LARGE]: '请求体过大',
  [ErrCode.UNAVAILABLE]: '服务暂时不可用',

  [ErrCode.UNAUTHORIZED]: '未认证或凭证无效',
  [ErrCode.INVALID_PASSWORD]: '邮箱或密码错误',
  [ErrCode.ACCOUNT_DISABLED]: '账号已被禁用',
  [ErrCode.EMAIL_TAKEN]: '邮箱已注册',
  [ErrCode.USERNAME_TAKEN]: '用户名已存在',
  [ErrCode.ACCOUNT_LOCKED]: '登录失败次数过多,账号已锁定',
  [ErrCode.WEAK_PASSWORD]: '密码不符合安全策略',
  [ErrCode.INVALID_TOKEN]: '令牌无效或已过期',
  [ErrCode.EMAIL_UNVERIFIED]: '邮箱尚未验证',
  [ErrCode.INVITE_REQUIRED]: '该系统需要邀请码才能注册',
  [ErrCode.INVITE_INVALID]: '邀请码无效或已用尽',
  [ErrCode.SESSION_EXPIRED]: '登录态已过期,请重新登录',

  [ErrCode.FORBIDDEN]: '权限不足',
  [ErrCode.PERMISSION_DENIED]: '需要更高的权限',

  [ErrCode.OIDC_INVALID_REQUEST]: 'OIDC 请求参数错误',
  [ErrCode.OIDC_CLIENT_AUTH_FAILED]: 'OIDC 客户端认证失败',
  [ErrCode.OIDC_REDIRECT_MISMATCH]: 'redirect_uri 不在白名单',
  [ErrCode.OIDC_PKCE_FAILED]: 'PKCE 校验失败',
  [ErrCode.OIDC_SCOPE_DENIED]: '未授权该作用域',
  [ErrCode.OIDC_UNSUPPORTED_GRANT]: '不支持的授权类型',

  [ErrCode.MC_NAME_INVALID]: 'MC 用户名格式非法',
  [ErrCode.MC_NAME_TAKEN]: 'MC 用户名已被占用',
  [ErrCode.MC_TOKEN_INVALID]: 'MC 令牌无效',
  [ErrCode.MC_LOGIN_DISABLED]: '该账号未开放 MC 登录',
  [ErrCode.MC_TEXTURE_INVALID]: '材质格式非法',
  [ErrCode.MC_TEXTURE_TOO_LARGE]: '材质文件过大',
  [ErrCode.MC_SIGNATURE_ERROR]: 'MC 签名校验失败',
  [ErrCode.MC_READ_ONLY]: '皮肤站处于只读模式,禁止上传',
}

/**
 * 给用户的**可操作建议**。
 *
 * 判据只有一条:读完这句话,用户能不能做点什么。
 * 做不到的就不写 —— 「请联系管理员」这种话占了一行却不解决任何问题,
 * 所以只有确实需要人工介入的错误码才给。没有建议的错误码留空。
 */
const HINTS: Readonly<Record<number, string>> = {
  [ErrCode.INVALID_ARGUMENT]: '请检查填写的内容是否符合格式要求。',
  [ErrCode.RATE_LIMITED]: '请稍等片刻再试。',
  [ErrCode.INTERNAL]: '请稍后重试;若反复出现,请把下方的错误码提供给管理员。',
  [ErrCode.NOT_FOUND]: '目标可能已被删除,刷新页面后再试。',
  [ErrCode.CONFLICT]: '该操作与现有数据冲突,请检查后重试。',
  [ErrCode.PAYLOAD_TOO_LARGE]: '请缩小文件体积或减少提交的内容。',
  [ErrCode.UNAVAILABLE]: '服务正在维护或负载过高,请稍后再试。',

  [ErrCode.UNAUTHORIZED]: '请重新登录。',
  [ErrCode.INVALID_PASSWORD]: '请确认邮箱和密码是否正确。连续失败多次会暂时锁定账号。',
  [ErrCode.ACCOUNT_DISABLED]: '请联系管理员解除禁用。',
  [ErrCode.EMAIL_TAKEN]: '该邮箱已注册,可直接登录,或改用其他邮箱。',
  [ErrCode.USERNAME_TAKEN]: '请换一个用户名。',
  [ErrCode.ACCOUNT_LOCKED]: '请等待锁定时间结束后再试,或联系管理员解锁。',
  [ErrCode.WEAK_PASSWORD]: '请按页面提示调整密码。',
  [ErrCode.INVALID_TOKEN]: '该链接已失效,请重新获取。',
  [ErrCode.EMAIL_UNVERIFIED]: '请先点击注册邮件里的验证链接完成激活。',
  [ErrCode.INVITE_REQUIRED]: '请向管理员索取邀请码。',
  [ErrCode.INVITE_INVALID]: '请确认邀请码是否输入正确,或向管理员索取新的邀请码。',
  [ErrCode.SESSION_EXPIRED]: '请重新登录。',

  [ErrCode.FORBIDDEN]: '当前账号没有访问该功能的权限。',
  [ErrCode.PERMISSION_DENIED]: '请让管理员为你的账号授予相应权限。',

  [ErrCode.OIDC_INVALID_REQUEST]: '请检查授权请求的参数是否完整。',
  [ErrCode.OIDC_CLIENT_AUTH_FAILED]: '请检查客户端的 client_id 与 client_secret。',
  [ErrCode.OIDC_REDIRECT_MISMATCH]: '请确认 redirect_uri 已在客户端的白名单中登记。',
  [ErrCode.OIDC_PKCE_FAILED]: '请重新发起授权请求。',
  [ErrCode.OIDC_SCOPE_DENIED]: '该客户端未被授权请求此作用域。',
  [ErrCode.OIDC_UNSUPPORTED_GRANT]: '该客户端不支持此授权类型。',

  [ErrCode.MC_NAME_INVALID]: '请检查用户名格式是否符合要求。',
  [ErrCode.MC_NAME_TAKEN]: '该用户名已被占用,请换一个。',
  [ErrCode.MC_TOKEN_INVALID]: '请重新登录后再试。',
  [ErrCode.MC_LOGIN_DISABLED]: '该账号未开放 MC 登录,请先在账号设置中开启。',
  [ErrCode.MC_TEXTURE_INVALID]: '请上传 PNG 格式的皮肤或披风文件。',
  [ErrCode.MC_TEXTURE_TOO_LARGE]: '请压缩图片后重试。',
  [ErrCode.MC_SIGNATURE_ERROR]: '请重新登录后再试。',
  [ErrCode.MC_READ_ONLY]: '皮肤站当前处于只读模式,暂时无法上传。',
}

/** 错误码对应的兜底标题。未知码返回空串,由调用方决定怎么显示。 */
export function codeTitle(code: number): string {
  return TITLES[code] ?? ''
}

/** 错误码对应的可操作建议。没有建议时返回空串。 */
export function codeHint(code: number): string {
  return HINTS[code] ?? ''
}

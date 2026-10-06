# API 设计

> HTTP 接口、统一响应信封、错误码、限流、CORS。所有接口前缀 `/api`、`/oauth`、`/mc`、`/admin`、`/device`、`/health`、`/metrics`(见 `internal/transport/webserver.go`)。

---

## 一、通用约定

### 1.1 响应包(ADR-008)

所有 JSON 接口统一包装:

```json
{ "code": 0, "message": "ok", "data": { ... } }
```

错误时:

```json
{ "code": 20002, "message": "邮箱或密码错误", "data": null }
```

HTTP 状态码**同时**正确设置(如 400/401/403/404/429/500),前端以 `code` 判断业务语义(见 `internal/platform/apperr/apperr.go`)。

### 1.2 错误码区间

| 区间 | 含义 |
|---|---|
| `0` | 成功 |
| `1xxxx` | 通用错误(参数、限流、服务器内部错误) |
| `2xxxx` | 账号与认证 |
| `3xxxx` | 权限 |
| `4xxxx` | OIDC |
| `5xxxx` | Minecraft |

**数值一经发布不可更改,只能追加**(见 `internal/platform/apperr/apperr.go`)。

### 1.3 完整错误码表

| code | HTTP | 含义 |
|---|---|---|
| 0 | 200 | 成功 |
| 10001 | 400 | 参数校验失败 |
| 10002 | 429 | 请求过于频繁 |
| 10003 | 500 | 服务器内部错误 |
| 10004 | 404 | 资源不存在 |
| 10005 | 409 | 资源冲突 |
| 10006 | 413 | 请求体过大 |
| 10007 | 503 | 服务暂时不可用 |
| 20001 | 401 | 未认证或凭证无效 |
| 20002 | 401 | 邮箱或密码错误 |
| 20003 | 403 | 账号已被禁用 |
| 20004 | 409 | 邮箱已注册 |
| 20005 | 409 | 用户名已存在 |
| 20006 | 429 | 登录失败次数过多,账号已锁定 |
| 20007 | 400 | 密码不符合安全策略 |
| 20008 | 400 | 令牌无效或已过期 |
| 20009 | 409 | 邮箱尚未验证 |
| 20010 | 403 | 该系统需要邀请码才能注册 |
| 20011 | 400 | 邀请码无效或已用尽 |
| 20012 | 401 | 登录态已过期,请重新登录 |
| 20013 | 403 | 首次登录必须修改密码 |
| 20014 | 403 | 注册已关闭 |
| 30001 | 403 | 权限不足 |
| 30002 | 403 | 需要更高的权限 |
| 40001 | 400 | OIDC 请求参数错误 |
| 40002 | 401 | OIDC 客户端认证失败 |
| 40003 | 400 | redirect_uri 不在白名单 |
| 40004 | 400 | PKCE 校验失败 |
| 40005 | 400 | 未授权该作用域 |
| 40006 | 400 | 不支持的授权类型 |
| 50001 | 400 | MC 用户名格式非法 |
| 50002 | 400 | MC 用户名已被占用 |
| 50003 | 401 | MC 令牌无效 |
| 50004 | 403 | 该账号未开放 MC 登录 |
| 50005 | 400 | 材质格式非法 |
| 50006 | 413 | 材质文件过大 |
| 50007 | 401 | MC 签名校验失败 |
| 50008 | 403 | 皮肤站处于只读模式 |

### 1.4 认证方式

| 场景 | 方式 |
|---|---|
| 终端用户站 | Session Cookie(`ygg_session`,HttpOnly,SameSite=Lax,Secure) |
| 管理后台 | 同上,叠加权限点校验 |
| OIDC userinfo / 业务系统调用 | `Authorization: Bearer <access_token>` |
| MC 协议 | `Authorization: Bearer <mc_access_token>` |

### 1.5 CORS(见 docs/security.md 第三节)

- 绝不允许通配符配合凭据(即 `Access-Control-Allow-Origin: *` 与 `Access-Control-Allow-Credentials: true` 不可同时出现)
- 生产环境按 `PUBLIC_BASE_URL` 精确匹配
- 开发环境按 `APP_CORS_ORIGINS`(逗号分隔)匹配

---

## 二、账号内核 API

前缀 `/api/auth`。全部使用 Cookie 会话认证(除公开接口外)。

### 2.1 认证

| 方法 | 路径 | 认证 | 说明 |
|---|---|---|---|
| POST | `/api/auth/register` | 公开 | 注册 |
| POST | `/api/auth/login` | 公开 | 登录 |
| POST | `/api/auth/logout` | 会话 | 登出当前会话 |
| POST | `/api/auth/logout-all` | 会话 | 登出所有会话 |
| GET | `/api/auth/session` | 会话 | 获取当前登录态 |
| POST | `/api/auth/password/forgot` | 公开 | 请求重置密码 |
| POST | `/api/auth/password/reset` | 公开 | 重置密码 |
| POST | `/api/auth/email/verify` | 公开 | 验证邮箱 |
| POST | `/api/auth/email/resend` | 会话 | 重发验证邮件 |
| GET | `/api/auth/policy` | 公开 | 注册/密码校验策略 |
| GET | `/api/public/config` | 公开 | 站点公开配置(站点名、主题色、是否开放注册) |

**注册成功响应**:
```json
{
  "code": 0,
  "message": "ok",
  "data": {
    "account": { "id": "...", "username": "player_one", "email": "user@example.com" },
    "verify_url": "https://auth.example.com/verify-email?token=..."
  }
}
```

`verify_url` **只在邮件投递不出去的部署里出现**(`MAILER_TRANSPORT=console` 或未配置 SMTP):那是完成邮箱验证的唯一路径。SMTP 部署只回 `account` —— 令牌只从邮件这条路的出去。

### 2.2 账号管理(需会话)

| 方法 | 路径 | 权限 | 说明 |
|---|---|---|---|
| PATCH | `/api/account` | 会话 | 更新当前账号信息 |
| PATCH | `/api/account/password` | 会话 | 修改密码 |
| GET | `/api/account/sessions` | 会话 | 查看当前账号的会话列表 |
| DELETE | `/api/account/sessions/:id` | 会话 | 吊销指定会话 |
| GET | `/api/account/audit` | 会话 | 查看当前账号的审计日志 |

---

## 三、OIDC API

前缀 `/oauth`。标准 OAuth 2.1 / OIDC 端点(见 `internal/oidc/`)。

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/oauth/.well-known/openid-configuration` | OpenID Connect Discovery |
| GET | `/oauth/.well-known/jwks.json` | JWKS 公钥 |
| POST | `/oauth/token` | 令牌端点 |
| GET | `/oauth/authorize` | 授权端点 |
| POST | `/oauth/device/authorize` | 设备授权端点 |
| POST | `/oauth/device/token` | 设备令牌端点 |
| POST | `/oauth/par` | PAR 端点 |
| GET | `/oauth/userinfo` | UserInfo 端点 |
| POST | `/oauth/revoke` | Token 吊销端点 |
| POST | `/oauth/sso` | SSO 静默发码 |

**关键约束**:
- 强制 PKCE(S256),`require_pkce` 默认 true(见 docs/security.md 第四节)
- `redirect_uri` 必须完整匹配白名单,拒绝通配符与前缀匹配(见 docs/security.md 第四节)
- refresh token 轮换:每次刷新签发新 token,旧 token 标记已用;检测到重放则吊销整条轮换链

---

## 四、Minecraft API

前缀 `/mc`。Yggdrasil 协议端点(见 `internal/minecraft/`)。

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | `/mc/authserver/server.jar` | 获取 server.jar |
| POST | `/mc/authserver/authenticate` | 玩家认证 |
| POST | `/mc/authserver/validate` | 令牌有效性校验 |
| POST | `/mc/authserver/unauthenticate` | 注销令牌 |
| GET | `/mc/authserver/profiles/minecraft/<username>` | 按用户名查档案 |
| GET | `/mc/authserver/profiles/<uuid>` | 按 UUID 查档案 |
| POST | `/mc.authserver/hasJoined` | 服务器端认证 |
| GET | `/mc/skins/<uuid>.png` | 皮肤文件 |
| GET | `/mc/capes/<uuid>.png` | 披风文件 |
| GET | `/mc/avatars/<uuid>.png` | 头像渲染 |

**公开接口**:`/mc/authserver/*`、`/mc/skins/*`、`/mc/capes/*`、`/mc/avatars/*` 不需要认证。

---

## 五、后台管理 API

前缀 `/api/admin`。全部需要会话 + 权限点校验(见 `internal/admin/`)。

### 5.1 账号管理

| 方法 | 路径 | 权限 | 说明 |
|---|---|---|---|
| GET | `/api/admin/accounts` | `account:read` | 账号列表 |
| GET | `/api/admin/accounts/:id` | `account:read` | 账号详情 |
| PATCH | `/api/admin/accounts/:id` | `account:write` | 修改账号 |
| POST | `/api/admin/accounts/:id/disable` | `account:write` | 禁用账号 |
| POST | `/api/admin/accounts/:id/reset-password` | `account:write` | 重置密码 |

### 5.2 权限管理

| 方法 | 路径 | 权限 | 说明 |
|---|---|---|---|
| GET | `/api/admin/roles` | `rbac:read` | 角色列表 |
| POST | `/api/admin/roles` | `rbac:write` | 创建角色 |
| PATCH | `/api/admin/roles/:id` | `rbac:write` | 修改角色 |
| DELETE | `/api/admin/roles/:id` | `rbac:write` | 删除角色 |
| GET | `/api/admin/permissions` | `rbac:read` | 权限点列表 |
| POST | `/api/admin/accounts/:id/roles` | `rbac:write` | 授权 |
| DELETE | `/api/admin/accounts/:id/roles/:role_id` | `rbac:write` | 取消授权 |

### 5.3 OIDC 客户端

| 方法 | 路径 | 权限 | 说明 |
|---|---|---|---|
| GET | `/api/admin/oidc/clients` | `oidc:client:read` | 客户端列表 |
| POST | `/api/admin/oidc/clients` | `oidc:client:write` | 创建客户端 |
| PATCH | `/api/admin/oidc/clients/:id` | `oidc:client:write` | 修改客户端 |
| DELETE | `/api/admin/oidc/clients/:id` | `oidc:client:write` | 删除客户端 |
| POST | `/api/admin/oidc/clients/:id/rotate-secret` | `oidc:client:write` | 轮换密钥 |
| POST | `/api/admin/oidc/tokens/revoke` | `oidc:token:revoke` | 吊销令牌 |

### 5.4 Minecraft 管理

| 方法 | 路径 | 权限 | 说明 |
|---|---|---|---|
| GET | `/api/admin/mc/profiles` | `minecraft:profile:read` | 玩家档案列表 |
| GET | `/api/admin/mc/profiles/:id` | `minecraft:profile:read` | 玩家档案详情 |
| DELETE | `/api/admin/mc/profiles/:id` | `minecraft:profile:write` | 删除玩家档案 |
| GET | `/api/admin/mc/textures` | `minecraft:texture:read` | 材质库列表 |
| POST | `/api/admin/mc/textures` | `minecraft:texture:write` | 上传材质 |
| GET | `/api/admin/mc/join` | `minecraft:server:read` | 服务器加入日志 |

### 5.5 审计

| 方法 | 路径 | 权限 | 说明 |
|---|---|---|---|
| GET | `/api/admin/audit` | `audit:read` | 审计日志列表 |
| POST | `/api/admin/audit/export` | `audit:export` | 导出审计日志 CSV |

### 5.6 密钥管理

| 方法 | 路径 | 权限 | 说明 |
|---|---|---|---|
| GET | `/api/admin/keys` | `setting:read` | 签名密钥列表 |
| POST | `/api/admin/keys/rotate` | `setting:write` | 轮换密钥 |

### 5.7 设置

| 方法 | 路径 | 权限 | 说明 |
|---|---|---|---|
| GET | `/api/admin/settings` | `setting:read` | 获取所有设置 |
| PATCH | `/api/admin/settings` | `setting:write` | 批量更新设置 |
| POST | `/api/admin/mail/test` | `setting:write` | 发测试邮件 |

---

## 六、公开配置端点

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/public/config` | 站点公开配置(站点名、主题色、是否开放注册、邀请码要求) |

此端点**不需要登录**,前端用它决定页面标题、品牌色、注册入口是否可见(见 `internal/transport/public.go`)。

---

## 七、限流阈值

见 `internal/platform/ratelimit/ratelimit.go`:

| 接口 | 窗口 | 上限 |
|---|---|---|
| 登录 | 1 分钟 | 5 次 |
| 注册 | 1 分钟 | 3 次 |
| 密码重置 | 1 分钟 | 3 次 |
| 通用 | 1 分钟 | 60 次 |

---

## 八、错误响应规范

- 内部错误 Detail 只进日志,**绝不**返回给调用方(见 docs/security.md 第十节)
- 错误消息用中文,前端以 `code` 判断语义,不以 HTTP 状态码判断业务含义
- 前端错误提示库:`web/shared/src/codes.ts` 与后端 `apperr` 码表保持同步

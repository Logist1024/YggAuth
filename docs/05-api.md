# 05 · API 设计

## 一、通用约定

### 1.1 响应包

所有 JSON 接口统一包装(ADR-008):

```json
{ "code": 0, "message": "ok", "data": { } }
```

错误时:

```json
{ "code": 10001, "message": "邮箱或密码错误", "data": null }
```

HTTP 状态码**同时**正确设置(如 400/401/403/404/429/500),前端以 `code` 判断业务语义。

### 1.2 错误码

| 区间 | 含义 |
|---|---|
| `0` | 成功 |
| `1xxxx` | 通用错误(参数、限流、服务器内部错误) |
| `2xxxx` | 账号与认证 |
| `3xxxx` | 权限 |
| `4xxxx` | OIDC |
| `5xxxx` | Minecraft |

| code | HTTP | 含义 |
|---|---|---|
| 0 | 200 | 成功 |
| 10001 | 400 | 参数校验失败 |
| 10002 | 429 | 请求过于频繁 |
| 10003 | 500 | 服务器内部错误 |
| 20001 | 401 | 未认证或凭证无效 |
| 20002 | 401 | 邮箱或密码错误 |
| 20003 | 403 | 账号被禁用 |
| 20004 | 409 | 邮箱已注册 |
| 20005 | 409 | 用户名已存在 |
| 20006 | 429 | 登录失败次数过多,已锁定 |
| 20007 | 400 | 密码不符合策略 |
| 20008 | 400 | 令牌无效或已过期 |
| 20009 | 409 | 邮箱未验证 |
| 30001 | 403 | 权限不足 |
| 30002 | 403 | 需要更高权限 |
| 40001 | 400 | OIDC 参数错误 |
| 40002 | 401 | OIDC 客户端认证失败 |
| 40003 | 400 | redirect_uri 不在白名单 |
| 40004 | 400 | PKCE 校验失败 |
| 50001 | 400 | MC 用户名格式非法 |
| 50002 | 400 | MC 用户名已被占用 |
| 50003 | 401 | MC 令牌无效 |
| 50004 | 403 | 该账号未开放 MC 登录 |
| 50005 | 400 | 材质格式非法 |
| 50006 | 413 | 材质文件过大 |

### 1.3 认证方式

| 场景 | 方式 |
|---|---|
| 终端用户站 | Session Cookie(`ygg_session`,HttpOnly,SameSite=Lax) |
| 管理后台 | 同上,叠加权限点校验 |
| OIDC userinfo / 业务系统调用 | `Authorization: Bearer <access_token>` |
| MC 协议 | `Authorization: Bearer <mc_access_token>` |

## 二、账号内核 API

前缀 `/api`。全部使用 Cookie 会话认证(除公开接口外)。

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

**注册请求**
```json
POST /api/auth/register
{
  "username": "player_one",
  "email": "user@example.com",
  "password": "correct-horse-battery",
  "invite_code": "optional"
}
```

**登录请求**
```json
POST /api/auth/login
{
  "email": "user@example.com",
  "password": "correct-horse-battery"
}
```

**登录成功响应**
```json
{
  "code": 0,
  "message": "ok",
  "data": {
    "account": { "id": "...", "username": "player_one", "email": "user@example.com" },
    "email_verified": true,
    "mc_login_enabled": true
  }
}
```

登录失败时**统一返回 20002「邮箱或密码错误」**,不区分「邮箱不存在」还是「密码错误」,防止账号枚举。账号被锁定时返回 20006。

### 2.2 账号

| 方法 | 路径 | 认证 | 说明 |
|---|---|---|---|
| GET | `/api/account` | 会话 | 当前账号信息 |
| PATCH | `/api/account` | 会话 | 修改用户名/邮箱 |
| PATCH | `/api/account/password` | 会话 | 修改密码(需旧密码) |
| GET | `/api/account/sessions` | 会话 | 活跃会话列表 |
| DELETE | `/api/account/sessions/:id` | 会话 | 踢掉指定会话 |
| GET | `/api/account/audit` | 会话 + `account:read` | 我的操作记录 |

## 三、OIDC API

前缀 `/oauth`。标准端点,`application/x-www-form-urlencoded` 编解码。

> **不套 CSRF**:`/oauth` 整段挂在根上,不经过 `/api` 的 `CSRFProtect`。
> 令牌、内省这些端点是机器对机器的调用,标准 OAuth 客户端不会、也不该为了
> 发一次令牌请求先取一个 CSRF token。浏览器只会跟随 `GET` 授权跳转,
> 而那些跳转不改变服务端状态。

| 方法 | 路径 | 认证 | 说明 |
|---|---|---|---|
| GET | `/oauth/.well-known/openid-configuration` | 公开 | Discovery |
| GET | `/oauth/.well-known/jwks.json` | 公开 | 公钥集 |
| GET | `/oauth/authorize` | 会话 | 授权页(未登录则跳登录) |
| POST | `/oauth/authorize/decision` | 会话 | 同意/拒绝授权 |
| POST | `/oauth/token` | 公开 | 换取令牌 |
| GET/POST | `/oauth/userinfo` | Bearer | 用户信息 |
| POST | `/oauth/introspect` | 客户端认证 | 令牌内省 |
| POST | `/oauth/revoke` | 客户端认证 | 吊销令牌 |
| GET/POST | `/oauth/endsession` | 会话 | RP 发起登出 |
| POST | `/oauth/par` | 公开 | 推送授权请求(RFC 9126) |
| POST | `/oauth/device/auth` | 公开 | 设备授权请求(RFC 8628) |

> 令牌端点**不走**统一响应包 —— RFC 6749 规定它返回 OAuth 标准结构,
> 标准客户端解析不了 `{code,message,data}`。同理,`/oauth/par` 成功时返回
> `201` + `request_uri`(RFC 9126 §2.2)。

**Discovery 响应**(节选)
```json
{
  "issuer": "https://auth.example.com",
  "authorization_endpoint": "https://auth.example.com/oauth/authorize",
  "token_endpoint": "https://auth.example.com/oauth/token",
  "userinfo_endpoint": "https://auth.example.com/oauth/userinfo",
  "jwks_uri": "https://auth.example.com/oauth/.well-known/jwks.json",
  "pushed_authorization_request_endpoint": "https://auth.example.com/oauth/par",
  "device_authorization_endpoint": "https://auth.example.com/oauth/device/auth",
  "response_types_supported": ["code"],
  "grant_types_supported": ["authorization_code", "refresh_token", "client_credentials",
                            "urn:ietf:params:oauth:grant-type:device_code"],
  "code_challenge_methods_supported": ["S256"],
  "scopes_supported": ["openid", "profile", "email", "offline_access"]
}
```

> `issuer` 必须与实际访问域名**完全一致**(含协议),否则标准 OIDC 客户端会在验签阶段拒绝所有令牌。

> `grant_types_supported` 里**没有** `password`:OAuth 2.1 移除了 ROPC。
> 本项目也没有装配它,所以该 `grant_type` 会被按 RFC 6749 拒绝。

**Token 响应**
```json
{
  "access_token": "...",
  "token_type": "Bearer",
  "expires_in": 3600,
  "refresh_token": "...",
  "scope": "openid profile email",
  "id_token": "..."
}
```

> `refresh_token` **只在**请求了 `offline_access` 时签发。
> `id_token` 只在请求了 `openid` 时签发。

### 3.1 SSO

| 方法 | 路径 | 认证 | 说明 |
|---|---|---|---|
| GET | `/api/sso/status` | 公开 | 当前 SSO 状态(前端决定走同意页还是登录页) |
| POST | `/api/sso/decision` | 会话 | 同意范围校验 |
| POST | `/api/sso/logout` | 公开 | 全局登出 |

**流程**:
1. 业务系统把浏览器送到 `/oauth/authorize?client_id=xxx&...`
2. 若已有有效会话且该客户端此前已获得同意 → **静默**发码重定向回业务系统
3. 若无会话 → 跳转 `/login?continue=<原授权请求>`,登录成功后回到步骤 2
4. 若有会话但未同意过 → 返回同意页数据,前端渲染后 `POST /oauth/authorize/decision`

> 待确认的授权请求暂存在**带 HMAC 签名的 HttpOnly cookie** 里,而不是服务端。
> 这样保持单进程无状态部署(ADR-007 已接受多副本限制),且篡改会在验签时被拒。
> 还原时用原始查询串让 fosite **重新解析一遍**,不在业务层手工拼请求 ——
> 否则迟早会漏掉某个校验项。

Cookie 域由 `APP_PUBLIC_DOMAIN` 推导父域(如 `auth.example.com` → `.example.com`),实现跨子域共享登录态。

> **本地测试注意**:`localhost` 无父域,必须 `SSO_COOKIE_DOMAIN=` 留空且 `SSO_COOKIE_SECURE=false`,否则浏览器丢弃 cookie,静默发码静默失效。

### 3.2 设备码流程(RFC 8628)

设备端在电视/命令行上登录:设备拿不到浏览器,用户改用手机完成授权。

| 方法 | 路径 | 认证 | 说明 |
|---|---|---|---|
| POST | `/oauth/device/auth` | 公开 | 换设备码与用户码 |
| POST | `/oauth/token` | 公开 | `grant_type=urn:ietf:params:oauth:grant-type:device_code` 轮询 |
| GET | `/api/device?user_code=xxx` | 会话 | 查询待批准信息 |
| POST | `/api/device/decision` | 会话 | 批准 / 拒绝 |

**流程**:
1. 设备调 `/oauth/device/auth` → 拿到 `device_code`(256 位随机)、`user_code`(8 位,格式 `XXXX-XXXX`)、
   `verification_uri_complete`、`interval`
2. 设备展示 `verification_uri_complete`,用户在手机上输入 `user_code`
3. 设备反复调 `/oauth/token`:
   - 用户还没操作 → `{"error":"authorization_pending"}`
   - 用户拒绝 → `{"error":"access_denied"}`
   - 设备码过期 → `{"error":"expired_token"}`
   - 已批准 → 正常返回令牌

**约束**:
- 设备码**一次性**,换过令牌即作废
- 设备码与申请它的 `client_id` 绑定,换客户端会被拒
- 未登录用户不能批准设备授权

> 用户码字母表去掉了 `0/O/1/I/l` 这类易混字符,并允许用户输入成小写、
> 无连字符 —— 用户要在另一台设备上手抄这串码,一个混淆字符就多一类
> 「明明输对了却提示无效」的支持工单。

### 3.3 客户端管理

客户端的登记与轮换目前通过管理后台(`/api/admin/clients`)完成,
明细见 [docs/05-api.md](05-api.md) 第五节。

## 四、Minecraft Yggdrasil API

前缀 `/mc`。完整协议说明见 [06-mc-protocol.md](./06-mc-protocol.md)。

| 方法 | 路径 | 认证 | 说明 |
|---|---|---|---|
| GET | `/mc/` | 公开 | metadata(skinDomains) |
| POST | `/mc/authenticate` | 公开 | 用户名+密码 → token |
| POST | `/mc/refresh` | Bearer | 刷新令牌 |
| POST | `/mc/validate` | Bearer | 校验令牌有效性 |
| POST | `/mc/invalidate` | Bearer | 作废令牌 |
| POST | `/mc/signout` | 公开 | 用户名+密码 → 登出全部 |
| POST | `/mc/join` | Bearer | 由 serverId 生成 clientToken |
| GET | `/mc/hasJoined` | 公开 | 进服校验(HMAC 五生效点) |
| GET | `/mc/profile/:uuid` | Bearer | 查档案 |
| POST | `/mc/profiles/minecraft` | Bearer | 按名批量查 UUID |
| GET | `/mc/textures/:hash` | 公开 | 下载材质 |
| GET | `/mc/skin/:uuidOrName` | 公开 | 皮肤下载 |
| GET | `/mc/avatar/:uuidOrName` | 公开 | 头像(带尺寸参数) |

**`hasJoined` 是协议的安全核心** —— authlib-injector 在进服前调用它,用客户端算出的 `serverIdHash` 验证服务器确实由本服务签发。

### 4.1 皮肤管理(需会话)

| 方法 | 路径 | 权限 | 说明 |
|---|---|---|---|
| POST | `/api/account/mc/bind` | 会话 | 绑定 MC 档案到账号 |
| PUT | `/api/account/mc/texture` | 会话 | 上传皮肤/披风 |
| GET | `/api/account/mc/texture` | 会话 | 查询当前材质 |
| DELETE | `/api/account/mc/texture/:type` | 会话 | 删除材质 |
| PATCH | `/api/account/mc/name` | 会话 | 改名 |
| PATCH | `/api/account/mc/login-enabled` | 会话 | 开关 MC 登录 |

**上传材质**
```
PUT /api/account/mc/texture
Content-Type: multipart/form-data

file=<PNG 文件>   type=skin|cape
```

**头像**
```
GET /mc/avatar/<uuidOrName>?size=64&hd=false
```

## 五、管理后台 API

前缀 `/api/admin`。全部需要会话 + 权限点(**权限点名称已变更,见 [04-decisions.md](./04-decisions.md) C-3**)。

| 方法 | 路径 | 权限点 | 说明 |
|---|---|---|---|
| GET | `/api/admin/me` | 登录即可 | 当前管理员与权限点 |
| GET | `/api/admin/menus` | 登录即可 | 按权限点过滤后的菜单 |
| GET | `/api/admin/dashboard` | 登录即可 | 统计数据 |
| GET | `/api/admin/accounts` | `account:read` | 账号列表 |
| PATCH | `/api/admin/accounts/:id` | `account:write` | 修改账号状态等 |
| GET | `/api/admin/roles` | `rbac:read` | 角色列表 |
| POST | `/api/admin/roles` | `rbac:write` | 创建角色 |
| PATCH | `/api/admin/roles/:id` | `rbac:write` | 修改角色 |
| DELETE | `/api/admin/roles/:id` | `rbac:write` | 删除角色 |
| POST | `/api/admin/roles/grant` | `rbac:write` | 授予角色 |
| POST | `/api/admin/roles/revoke` | `rbac:write` | 撤销角色 |
| GET | `/api/admin/permission-points` | `rbac:read` | 全部权限点 |
| GET | `/api/admin/audit` | `audit:read` | 审计检索 |
| GET | `/api/admin/audit/export` | `audit:export` | 导出 CSV |
| GET | `/api/admin/invitations` | `rbac:write` | 邀请列表 |
| POST | `/api/admin/invitations` | `rbac:write` | 创建邀请码 |
| GET | `/api/admin/clients` | `oidc:client:read` | OIDC 客户端列表 |
| POST | `/api/admin/clients` | `oidc:client:write` | 登记客户端 |
| PATCH | `/api/admin/clients/:id` | `oidc:client:write` | 修改客户端 |
| POST | `/api/admin/clients/:id/rotate-secret` | `oidc:client:write` | 轮换密钥 |
| POST | `/api/admin/keys/rotate` | `oidc:client:write` | 轮换 OIDC 签名密钥 |
| GET | `/api/admin/mc/profiles` | `minecraft:profile:read` | 玩家档案 |
| PATCH | `/api/admin/mc/profiles/:id` | `minecraft:profile:write` | 改名/封禁 |
| GET | `/api/admin/mc/textures` | `minecraft:texture:read` | 材质库 |
| DELETE | `/api/admin/mc/textures/:hash` | `minecraft:texture:write` | 删除材质 |
| GET | `/api/admin/settings` | `setting:read` | 应用配置 |
| PATCH | `/api/admin/settings` | `setting:write` | 修改配置 |

**权限点映射变化**(C-3):旧版 `admin:oauth:client:read` → 新版 `oidc:client:read`;旧版 `admin:mc:profile:read` → 新版 `minecraft:profile:read`。

## 六、系统 API

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/health/live` | 进程存活,恒 200 |
| GET | `/health/ready` | 就绪,含 DB 连接状态 |
| GET | `/metrics` | Prometheus 指标 |
| `*` | 其余路径 | 返回前端 `index.html`(SPA 路由) |

## 七、限流

| 维度 | 阈值 | 维度键 |
|---|---|---|
| 登录 | 5 次 / 分钟 | IP + 邮箱 |
| 密码重置请求 | 3 次 / 小时 | IP |
| 邮件重发 | 1 次 / 60 秒,5 次 / 天 | 账号 |
| OIDC authorize | 60 次 / 分钟 | IP |
| OIDC token | 120 次 / 分钟 | IP |
| MC authenticate | 60 次 / 分钟 | IP |
| 材质上传 | 10 次 / 小时 | 账号 |
| 材质下载 | 300 次 / 分钟 | IP |

超限返回 **429 + code 10002**。

## 八、CORS

- 生产:仅允许登记在 `oidc.client.redirect_uris` 的 origin
- 开发:可通过 `CORS_ALLOWED_ORIGINS` 环境变量显式配置
- **绝不使用通配符 `*` 配合凭据**

---

**上一篇**:[04-decisions.md](./04-decisions.md) —— 关键决策记录
**下一篇**:[06-mc-protocol.md](./06-mc-protocol.md) —— Minecraft 协议适配

# 安全设计

> 威胁模型、安全约束、上线检查清单。**安全相关代码在 `internal/platform/httpx/`、`internal/platform/keys/`、`internal/security/`**。

---

## 一、威胁模型

假设攻击者可进行:暴力破解撞库、网络嗅探、SQL 注入尝试、XSS/CSRF 注入、数据库泄露、恶意 OAuth 客户端、社会工程。

**不在威胁模型内**:本机 root 权限、DDoS(交给上游防护)、供应链投毒(用依赖扫描缓解)。

---

## 二、密码安全

### 2.1 策略

| 规则 | 值 | 理由 |
|---|---|---|
| 最小长度 | 8 | NIST SP 800-63B 建议 |
| 最大长度 | 128 | 旧版 18 位偏短,变更 C-1 |
| 复杂度 | **不强制** | NIST 明确不推荐强制字符组合,反而降低安全性 |
| 常见密码 | 拒绝 | 查 top 10000 弱口令库(`internal/identity/account/password.go`) |
| 泄露密码 | 拒绝 | 接入 Have I Been Pwned k-anonymity API |

**注意**:HIBP 泄露检查本期**未接线**(见 docs/todo.md),但密码策略框架已预留接口。

### 2.2 存储

```go
hash, err := argon2.IDKey([]byte(password), salt,
    argon2.DefaultTime, argon2.DefaultMemory, argon2.DefaultThreads, 32)
```

- 算法:**argon2id**(内存硬,抗 GPU/ASIC,优于 bcrypt 与 scrypt)
- 参数:`time=1, memory=64MB, threads=4`(RFC 9106 推荐的第二档)
- 每个密码独立随机 salt(16 字节)
- 存储格式:`$argon2id$v=19$m=65536,t=1,p=4$<salt>$<hash>`,参数内嵌,便于未来升级
- 数据库只存 hash,**不存明文,不存可逆密文**

### 2.3 登录防护

| 措施 | 参数 |
|---|---|
| 账号锁定 | 5 次失败 → 锁 15 分钟(`identity.credential.failed_attempts` + `locked_until`) |
| IP 限流 | 登录接口 5 次/分钟(见 `internal/platform/ratelimit/ratelimit.go`) |
| 账号枚举防护 | 失败统一返回「邮箱或密码错误」,响应时间尽量一致 |
| 常数时间比较 | 用 `subtle.ConstantTimeCompare` |

---

## 三、传输与会话

### 3.1 传输

- **生产强制 HTTPS**;HTTP 请求 301 跳 HTTPS
- HSTS:`max-age=31536000; includeSubDomains`
- Cookie 属性:

```
Set-Cookie: ygg_session=<token>; HttpOnly; Secure; SameSite=Lax; Path=/; Domain=<SSO_COOKIE_DOMAIN>
```

`HttpOnly` 防 JS 读取,`Secure` 防明文传输,`SameSite=Lax` 防 CSRF。

### 3.2 会话令牌

| 措施 | 说明 |
|---|---|
| 熵 | 32 字节 CSPRNG,base64url 编码(`internal/identity/session/session.go`) |
| 存储 | **只存 sha256 hash**,泄露不可直接劫持 |
| 滑动过期 | 每次活跃刷新 `idle_expires_at` |
| 绝对过期 | `expires_at` 不因活跃而延长 |
| 登出即删 | 服务端删除记录,不等客户端清 cookie |
| 改密码后 | 吊销该账号全部会话 |

### 3.3 CSRF 防护

- Cookie `SameSite=Lax` 挡住跨站 POST
- 写操作校验 `Origin` / `Referer`(见 `internal/platform/httpx/middleware.go`)
- OAuth 回调用 `state` 参数(CSRF token)
- 关键管理操作要求**重新输入密码**

### 3.4 点击劫持防护

- `X-Frame-Options: DENY`
- `Content-Security-Policy: frame-ancestors 'none'`

---

## 四、OIDC / OAuth 安全

| 风险 | 防护 |
|---|---|
| 授权码截获 | **强制 PKCE(S256)**,`require_pkce` 默认 true |
| `redirect_uri` 攻击 | 白名单**完整字符串相等**,拒绝通配符与前缀匹配 |
| CSRF 登录攻击 | 强制 `state` 参数校验 |
| 重放攻击 | 授权码一次性 + 60s 过期 + 绑定 client_id 与 redirect_uri |
| refresh token 泄露 | **轮换机制**:每次刷新签发新 token,旧 token 标记已用;检测到重放则**吊销整条轮换链** |
| ID token 伪造 | 非对称签名(RS256),客户端本地验签 |
| 客户端密钥泄露 | 只存 argon2id hash,不可逆 |
| open redirect | `redirect_uri` 必须完整匹配白名单,**不做前缀匹配**(见 `internal/oidc/client.go`) |
| 越权 scope | 同意页展示并记录用户实际授权的 scope |

**关键**:`redirect_uri` 匹配必须是**完整字符串相等**,绝不能 `strings.HasPrefix` 或忽略路径部分。这是 OIDC 实现最常见的漏洞来源。

---

## 五、MC 域安全

| 风险 | 防护 |
|---|---|
| 冒名进服 | `hasJoined` 的 HMAC-SHA1 五生效点校验 |
| 令牌重放 | serverId 时间窗 + 一次性消费 |
| 皮肤 SSRF | 下载外部皮肤时校验 URL 协议白名单(`http`/`https`),拒绝 `file://`、`gopher://`、内网 IP |
| 文件名注入 | sha256 哈希命名,不接受用户上传文件名 |

---

## 六、密钥与配置安全

### 6.1 签名密钥

- 签名密钥存 PostgreSQL(`oidc.jwks` / `minecraft.signature_key`),不写进 `.env`
- 密钥 master secret(`KEY_MASTER_SECRET`)是加密签名密钥的包裹密钥,**永远不进数据库**
- 密钥轮换见 `internal/platform/keys/keys.go`

### 6.2 日志脱敏

- 令牌、密钥**不**写进日志(见 `internal/platform/log/log.go`)
- 邮件正文**不**写进日志(见 `internal/platform/mailer/mailer.go`)
- 启动日志只打配置摘要,敏感项脱敏(见 `cmd/yggauth/main.go`)
- 错误 Detail 只进日志,**绝不**返回给调用方(见 `internal/platform/apperr/apperr.go`)

### 6.3 密码与密钥不入日志

测试守此不变量:`internal/config/config_test.go` 验证启动日志不含密码与密钥。

---

## 七、审计

- 审计表 `identity.audit_log` **只追加**,应用层无任何删除接口
- 审计事件包括:登录、登出、注册、密码重置、权限变更、设置修改
- 审计日志导出 CSV(后台 `/api/admin/audit/export`)

---

## 八、CORS

- 绝不允许通配符配合凭据(即 `Access-Control-Allow-Origin: *` 与 `Access-Control-Allow-Credentials: true` 不可同时出现)
- 生产环境按 `PUBLIC_BASE_URL` 精确匹配
- 开发环境按 `APP_CORS_ORIGINS`(逗号分隔)匹配

---

## 九、限流

- 登录:5 次/分钟
- 注册:3 次/分钟
- 通用:60 次/分钟
- 实现:`internal/platform/ratelimit/ratelimit.go`

---

## 十、上线检查清单

- [ ] HTTPS 已配置,HSTS 已启用
- [ ] `KEY_MASTER_SECRET` 已生成并写入 `.env`(随机 32 字节 hex)
- [ ] `DB_PASSWORD` 已设置(非空)
- [ ] `PUBLIC_BASE_URL` 与实际访问域名完全一致
- [ ] `SMTP_*` 已配置(否则验证邮件发不出)
- [ ] 首个管理员已创建(见 docs/configuration.md 第四节)
- [ ] 设置已生效(`make golden-path` 冒烟测试通过)
- [ ] 审计表只追加(无删除接口)
- [ ] 错误 Detail 不外泄(`internal/platform/httpx/httpx_test.go` 覆盖)

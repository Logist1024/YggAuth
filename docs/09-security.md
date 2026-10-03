# 09 · 安全设计

## 一、威胁模型

假设攻击者可进行:暴力破解撞库、网络嗅探、SQL 注入尝试、XSS/CSRF 注入、数据库泄露、恶意 OAuth 客户端、社会工程。

**不在威胁模型内**:本机 root 权限、DDoS(交给上游防护)、供应链投毒(用依赖扫描缓解)。

## 二、密码安全

### 2.1 策略

| 规则 | 值 | 理由 |
|---|---|---|
| 最小长度 | 8 | NIST SP 800-63B 建议 |
| 最大长度 | 128 | 变更 C-1,旧版 18 位偏短 |
| 复杂度 | **不强制** | NIST 明确不推荐强制字符组合,反而降低安全性 |
| 常见密码 | 拒绝 | 查 top 10000 弱口令库 |
| 泄露密码 | 拒绝 | 接入 Have I Been Pwned k-anonymity API |

**依据**:NIST SP 800-63B「数字身份指南」明确反对强制复杂度规则,主张用长度 + 黑名单。这是变更 C-2 的理由。

### 2.2 存储

```go
hash, err := argon2.IDKey([]byte(password), salt, 
    argon2.DefaultTime, argon2.DefaultMemory, argon2.DefaultThreads, 32)
```

- 算法:**argon2id**(内存硬,抗 GPU/ASIC,优于 bcrypt 与 scrypt)
- 参数:time=1, memory=64MB, threads=4(RFC 9106 推荐的第二档)
- 每个密码独立随机 salt(16 字节)
- 存储格式:`$argon2id$v=19$m=65536,t=1,p=4$<salt>$<hash>`,参数内嵌,**便于未来升级**
- 数据库只存 hash,**不存明文,不存可逆密文**

### 2.3 登录防护

| 措施 | 参数 |
|---|---|
| 账号锁定 | 5 次失败 → 锁 15 分钟 |
| IP 限流 | 登录接口 5 次/分钟 |
| 账号枚举防护 | 失败统一返回「邮箱或密码错误」,响应时间尽量一致 |
| 常数时间比较 | 用 `subtle.ConstantTimeCompare` |

## 三、传输与会话

### 3.1 传输

- **生产强制 HTTPS**;HTTP 请求 301 跳 HTTPS
- HSTS:`max-age=31536000; includeSubDomains`
- Cookie 属性:

```
Set-Cookie: ygg_session=<token>;
  HttpOnly; Secure; SameSite=Lax; Path=/;
  Domain=<SSO_COOKIE_DOMAIN>
```

`HttpOnly` 防 JS 读取,`Secure` 防明文传输,`SameSite=Lax` 防 CSRF。

### 3.2 会话令牌

| 措施 | 说明 |
|---|---|
| 熵 | 32 字节 CSPRNG,base64url 编码 |
| 存储 | **只存 sha256 hash**,泄露不可直接劫持 |
| 滑动过期 | 每次活跃刷新 `idle_expires_at` |
| 绝对过期 | `expires_at` 不因活跃而延长 |
| 登出即删 | 服务端删除记录,不等客户端清 cookie |
| 改密码后 | 吊销该账号全部会话 |

### 3.3 CSRF 防护

- Cookie `SameSite=Lax` 挡住跨站 POST
- 写操作校验 `Origin` / `Referer`
- OAuth 回调用 `state` 参数(CSRF token)
- 关键管理操作要求**重新输入密码**

## 四、OIDC / OAuth 安全

| 风险 | 防护 |
|---|---|
| 授权码截获 | **强制 PKCE(S256)**,`require_pkce` 默认 true |
| `redirect_uri` 攻击 | 白名单精确匹配,拒绝通配符与前缀匹配 |
| CSRF 登录攻击 | 强制 `state` 参数校验 |
| 重放攻击 | 授权码一次性 + 60s 过期 + 绑定 client_id 与 redirect_uri |
| refresh token 泄露 | **轮换机制**:每次刷新签发新 token,旧 token 标记已用;检测到重放则**吊销整条轮换链** |
| ID token 伪造 | 非对称签名(RS256),客户端本地验签 |
| 客户端密钥泄露 | 只存 argon2id hash,不可逆 |
| open redirect | `redirect_uri` 必须完整匹配白名单,**不做前缀匹配** |
| 越权 scope | 同意页展示并记录用户实际授权的 scope |

**关键**:`redirect_uri` 匹配必须是**完整字符串相等**,绝不能 `strings.HasPrefix` 或忽略路径部分。这是 OIDC 实现最常见的漏洞来源。

## 五、MC 域安全

| 风险 | 防护 |
|---|---|
| 冒名进服 | `hasJoined` 的 HMAC-SHA1 五生效点校验 |
| 令牌重放 | serverId 时间窗 + 一次性消费 |
| 离线服务器绕过 | 未登记的 serverId → 204 拒绝 |
| 令牌跨域使用 | MC 令牌与 OAuth 令牌表、签名密钥全隔离 |
| 恶意材质 | PNG 魔数 + 实际解码校验 + 2MB 上限 + 尺寸白名单 |
| 存储耗尽 | 上传限流 10 次/小时 + sha256 去重 + 引用计数回收 |
| 外部站拖垮 | 独立客户端 + 5s 超时 + 熔断 |

## 六、数据保护

### 6.1 静态加密

| 数据 | 保护 |
|---|---|
| 签名私钥 | AES-256-GCM 加密,密钥来自 `KEY_MASTER_SECRET` |
| 数据库中的密码 | argon2id 不可逆 hash |
| 会话/令牌 | 只存 hash |
| 客户端密钥 | argon2id hash |

### 6.2 传输与存储加密

- TLS 传输(nginx 终结)
- 备份文件加密(gpg 或 age)
- 皮肤文件 0644,**不设可执行位**

### 6.3 日志脱敏

**禁止进入日志**:密码、令牌、私钥、完整 cookie、邮件正文、请求体中的敏感字段。

```go
log.Info("login", "account_id", id, "ip", ip, "outcome", "success")
// 不记录: "password", "..."   "token", "..."
```

启动时打印配置**只打 key 名不打 value**;`KEY_MASTER_SECRET` / `DB_PASSWORD` 强制脱敏。

## 七、输入校验与注入

| 攻击 | 防护 |
|---|---|
| SQL 注入 | **全部参数化查询**(sqlc 生成),无字符串拼接 |
| XSS | 后端输出 JSON;前端 Vue 默认转义,禁用 `v-html` |
| CSRF | 见 3.3 |
| SSRF | 外部皮肤站 URL 后台配置,非用户输入;限制协议为 https,禁用内网地址 |
| 路径穿越 | 文件存储 key 由 sha256 生成,**不用用户输入做路径** |
| 开放重定向 | `redirect_uri` 白名单精确匹配 |
| 请求走私 | nginx 与 Go 的 HTTP 解析保持一致;限制请求头大小 |
| 拒绝服务 | 限流 + 请求体大小限制 + 超时 |

**文件上传三重校验**:
1. 扩展名 / Content-Type
2. 文件魔数(PNG 签名)
3. **实际解码**(用 `image.DecodeConfig` 验证真实性)

## 八、依赖与供应链

| 工具 | 用途 | 频率 |
|---|---|---|
| `govulncheck` | Go 依赖漏洞 | 每次 CI |
| Trivy | 镜像漏洞扫描 | 每次 CI |
| `pnpm audit` | 前端依赖漏洞 | 每次 CI |

**策略**:
- 锁文件(`go.sum` / `pnpm-lock.yaml`)必须提交
- 镜像用 distroless,无 shell 无包管理器
- 定期重建基础镜像,获取安全补丁

## 九、审计

### 9.1 必须记录的事件

| 事件 | 字段 |
|---|---|
| 登录成功/失败 | account_id, ip, outcome |
| 账号锁定 | account_id, failed_attempts |
| 登出 | account_id, session_id |
| 密码修改/重置 | account_id, 方式 |
| 角色授予/撤销 | target_account_id, role_id, 操作人 |
| 客户端密钥轮换 | client_id, 操作人 |
| 签名密钥轮换 | 操作人 |
| 账号禁用 | target_account_id, 操作人 |
| 皮肤下架 | texture_hash, 操作人 |
| 材质删除 | texture_hash, 操作人 |

### 9.2 审计日志保护

- **只追加**,应用层无删除接口
- 保留期 90 天,归档 365 天
- 归档文件加密存储
- 审计库不可写时**降级为文件日志**,不阻断登录(可靠性 > 完整性,但两者都要尽力)

## 十、上线前检查清单

- [ ] 全部密码使用 argon2id,参数符合 RFC 9106
- [ ] `KEY_MASTER_SECRET` 为随机 32 字节,**非默认值**
- [ ] 所有 Cookie 带 `HttpOnly` + `Secure` + `SameSite`
- [ ] 强制 HTTPS + HSTS
- [ ] 所有 SQL 参数化,无字符串拼接
- [ ] `redirect_uri` 精确匹配,已测试恶意变体
- [ ] PKCE 强制启用且仅接受 S256
- [ ] refresh token 轮换 + 重放检测
- [ ] 限流在**所有**认证端点生效
- [ ] 日志无敏感信息(抽查启动日志与登录日志)
- [ ] 依赖漏洞扫描通过
- [ ] 密钥不在 Git 历史中
- [ ] 备份与恢复流程**已实际演练过**
- [ ] 域隔离测试通过(MC 故障不影响 OIDC)
- [ ] 错误响应不泄露内部细节(无 stack trace)

---

**上一篇**:[08-deployment.md](./08-deployment.md) —— 容器化部署
**下一篇**:[10-test-deployment.md](./10-test-deployment.md) —— 测试环境部署
**回到**:[00-overview.md](./00-overview.md) —— 项目总纲

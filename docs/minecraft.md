# Minecraft 协议适配

> Yggdrasil 协议实现、皮肤站设计、玩家档案与改名、SSRF 防护。

---

## 一、Yggdrasil 协议

Yggdrasil 是 Mojang 的 Minecraft 认证服务协议。私有服务器需配合 `authlib-injector` 使用。

### 1.1 端点

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | `/mc/authserver/authenticate` | 玩家认证 |
| POST | `/mc/authserver/validate` | 令牌有效性校验 |
| POST | `/mc/authserver/unauthenticate` | 注销令牌 |
| POST | `/mc/authserver/profiles/minecraft/<username>` | 按用户名查档案 |
| GET | `/mc/authserver/profiles/<uuid>` | 按 UUID 查档案 |
| POST | `/mc/authserver/hasJoined` | 服务器端认证 |
| POST | `/mc/authserver/server.jar` | 获取 server.jar |

### 1.2 认证流程

```
玩家客户端 → authenticate → 返回 accessToken + profile
玩家客户端 → hasJoined?serverId=<serverId> → 服务器验证
```

**关键约束**:`hasJoined` 的 HMAC-SHA1 五生效点校验,防止冒名进服。

### 1.3 协议错误码

见 `internal/minecraft/protocol.go`:

| code | 含义 |
|---|---|
| 50001 | MC 用户名格式非法(必须 3-16 位 alphanumeric + _) |
| 50002 | MC 用户名已被占用 |
| 50003 | MC 令牌无效 |
| 50004 | 该账号未开放 MC 登录(`mc_login_enabled = false`) |
| 50007 | MC 签名校验失败(HMAC 不匹配) |
| 50008 | 皮肤站处于只读模式 |

---

## 二、玩家档案

### 2.1 UUID 推导(关键!不可改)

```go
func ProfileUUID(accountID uuid.UUID) uuid.UUID {
    return uuid.NewSHA1(profileNamespace, accountID[:])
}
```

- `profile_uuid = uuidv5(NAMESPACE, account_id)`
- NAMESPACE 常量:`6ba7b811-9dad-11d1-80b4-00c04fd430c8`(UUIDv5 NSEC)
- **命名空间常量一旦发布永不可改** —— 改了所有玩家 UUID 都会变,历史皮肤、别名、白名单全部失效

### 2.2 档案表

```sql
CREATE TABLE minecraft.profile (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id  UUID NOT NULL UNIQUE REFERENCES identity.account(id) ON DELETE CASCADE,
    uuid        UUID NOT NULL,           -- profile_uuid = uuidv5(namespace, account_id)
    username    TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

**唯一性**:一个账号对应一个档案,一个档案对应一个 MC 用户名。

### 2.3 改名

改名后旧名保留**(不立即释放)**,防止抢注。保留期由 `MC_NAME_RETENTION_DAYS`(默认 30 天)控制。

---

## 三、皮肤站

### 3.1 上传

- 格式:PNG,最大 4KB(默认皮肤)或 8KB(自定义皮肤)
- 命名:`<sha256>.png`,按 sha256 去重
- 存储:本地磁盘(`DATA_DIR/textures/`),按 sha256 前两位分目录

### 3.2 披风

- 格式:PNG,最大 4KB
- 存储:`DATA_DIR/capes/`

### 3.3 头像渲染

- 端点:`GET /mc/avatars/<uuid>.png`
- 异步渲染:首次请求生成并缓存 PNG
- 尺寸:8x8(默认)、16x16、64x64 三档

### 3.4 外部皮肤站回源

- 支持从外部 URL 下载皮肤
- **SSRF 防护**:URL 协议白名单(`http`/`https`),拒绝 `file://`、`gopher://`、内网 IP
- 见 `internal/minecraft/external.go`

---

## 四、安全约束

### 4.1 随机数要求

令牌随机数至少 32 字节 CSPRNG(见 `internal/platform/rand/rand.go`)。

### 4.2 皮肤 SSRF

- 下载外部皮肤时校验 URL 协议白名单
- 拒绝 `file://`、`gopher://`、内网 IP(10.x、172.16-31.x、192.168.x、127.x)
- 见 `internal/minecraft/external.go`

### 4.3 域隔离(ADR-003/004)

- MC 令牌与 OIDC 令牌存储在不同 schema
- MC 签名密钥独立(见 `minecraft.signature_key`)
- MC 域故障不影响 OIDC 域

---

## 五、管理后台 MC 接口

见 docs/api.md 第五节(5.4)。

主要功能:
- 玩家档案列表/详情
- 材质库管理
- 服务器加入日志

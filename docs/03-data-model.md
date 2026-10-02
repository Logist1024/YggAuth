# 03 · 数据模型

> PostgreSQL 16+,通过 goose 迁移、sqlc 查询。所有时间戳使用 `timestamptz`,所有 ID 使用 UUIDv4。

## 一、schema 划分

| schema | 职责 | 谁访问 |
|---|---|---|
| `identity` | 账号、凭据、会话、RBAC、审计 | 全部 |
| `oidc` | OIDC 客户端、授权码、令牌、同意、签名密钥 | OIDC 域 |
| `minecraft` | 玩家档案、名称历史、MC 令牌、材质、签名密钥 | MC 域 |
| `app` | 运行时可改的应用配置 | 后台 |

`identity` 域中立:**不含任何 oidc / minecraft 语义的表**。注意 `account.mc_login_enabled` 字段除外 —— 它是一个布尔开关,内核只当作「通用开关」处理,不理解其含义。更严格的方案是把它放到 `minecraft` schema 由 MC 域自己管理,但考虑到登录决策发生在内核,暂保留在 account 上并在代码注释说明。

## 二、identity schema

### 2.1 `identity.account`

```sql
CREATE TABLE identity.account (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    username            TEXT NOT NULL,
    username_lower      TEXT NOT NULL UNIQUE,          -- 大小写不敏感唯一
    email               TEXT NOT NULL UNIQUE,          -- 强制唯一(变更 C-10)
    status              TEXT NOT NULL DEFAULT 'active'
                        CHECK (status IN ('active','disabled','locked','pending_verification')),
    mc_login_enabled    BOOLEAN NOT NULL DEFAULT TRUE, -- 通用布尔开关,内核不理解其语义
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_account_email_lower ON identity.account (lower(email));
```

**约束说明**:
- `email` 存原始大小写,唯一索引建在 `lower(email)` 上(旧版存 `email` + 隐式比较,索引效率差);
- `username` 保留原样展示,`username_lower` 用于唯一性判断;
- `status` 用 CHECK 约束枚举,不用 PG enum(改枚举值不需要 `ALTER TYPE`,迁移更灵活)。

### 2.2 `identity.credential`

```sql
CREATE TABLE identity.credential (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id      UUID NOT NULL REFERENCES identity.account(id) ON DELETE CASCADE,
    algo            TEXT NOT NULL DEFAULT 'argon2id'
                    CHECK (algo IN ('argon2id')),
    hash            TEXT NOT NULL,
    params          JSONB NOT NULL,                    -- m/t/p 参数,便于未来升级
    failed_attempts INT NOT NULL DEFAULT 0,
    locked_until    TIMESTAMPTZ,
    changed_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (account_id, algo)                          -- 每账号每算法一条,支持并存迁移
);
```

### 2.3 `identity.session`

**ADR-004:唯一存终端用户登录态。**

```sql
CREATE TABLE identity.session (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id       UUID NOT NULL REFERENCES identity.account(id) ON DELETE CASCADE,
    token_hash       BYTEA NOT NULL UNIQUE,            -- sha256(token),不存明文
    sso_session_id   UUID,                             -- SSO 联动标识
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at       TIMESTAMPTZ NOT NULL,             -- 绝对过期
    idle_expires_at  TIMESTAMPTZ NOT NULL,             -- 滑动过期
    revoked_at       TIMESTAMPTZ,
    ip               INET,
    user_agent       TEXT
);

CREATE INDEX idx_session_account ON identity.session (account_id, revoked_at);
CREATE INDEX idx_session_idle_exp ON identity.session (idle_expires_at) WHERE revoked_at IS NULL;
```

**只存 hash 的理由**:数据库泄露时,攻击者拿到的 session hash 无法直接用于会话劫持。

### 2.4 RBAC(统一模型,ADR-005)

```sql
CREATE TABLE identity.permission (
    code         TEXT PRIMARY KEY,                     -- 'account:read' 三段式
    description  TEXT NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE identity.role (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    code         TEXT NOT NULL UNIQUE,
    name         TEXT NOT NULL,
    description  TEXT,
    is_system    BOOLEAN NOT NULL DEFAULT FALSE,      -- 系统内置角色不可删
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE identity.role_permission (
    role_id       UUID NOT NULL REFERENCES identity.role(id) ON DELETE CASCADE,
    permission    TEXT NOT NULL REFERENCES identity.permission(code) ON DELETE CASCADE,
    PRIMARY KEY (role_id, permission)
);

CREATE TABLE identity.account_role (
    account_id  UUID NOT NULL REFERENCES identity.account(id) ON DELETE CASCADE,
    role_id     UUID NOT NULL REFERENCES identity.role(id) ON DELETE CASCADE,
    granted_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    granted_by  UUID REFERENCES identity.account(id),
    PRIMARY KEY (account_id, role_id)
);
```

权限点清单(完整定义在迁移的 seed SQL 中):

| 权限点 | 说明 |
|---|---|
| `account:read` | 查看账号 |
| `account:write` | 管理账号(禁用、改邮箱、重置密码) |
| `rbac:read` | 查看角色权限 |
| `rbac:write` | 管理角色、授权 |
| `audit:read` | 查看审计日志 |
| `audit:export` | 导出审计日志 |
| `oidc:client:read` / `oidc:client:write` | OIDC 客户端管理 |
| `oidc:token:revoke` | 吊销令牌 |
| `minecraft:profile:read` / `minecraft:profile:write` | 玩家档案 |
| `minecraft:texture:read` / `minecraft:texture:write` | 材质库 |
| `minecraft:server:read` / `minecraft:server:write` | 服务器白名单 |
| `setting:read` / `setting:write` | 应用配置 |

### 2.5 `identity.token` 与 `identity.email_verification` / `identity.password_reset`

```sql
CREATE TABLE identity.email_token (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id  UUID NOT NULL REFERENCES identity.account(id) ON DELETE CASCADE,
    token_hash  BYTEA NOT NULL UNIQUE,
    purpose     TEXT NOT NULL CHECK (purpose IN ('verify_email','reset_password')),
    expires_at  TIMESTAMPTZ NOT NULL,
    used_at     TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_email_token_account ON identity.email_token (account_id, purpose);
```

**合并 verify 与 reset 的理由**:结构完全相同,合并比两张同构表更简洁,`purpose` 字段已足够区分。

### 2.6 `identity.invitation`

```sql
CREATE TABLE identity.invitation (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    code              TEXT NOT NULL UNIQUE,
    email             TEXT,                            -- 可选,绑定特定邮箱
    max_uses          INT  NOT NULL DEFAULT 1,
    used_count        INT  NOT NULL DEFAULT 0,
    expires_at        TIMESTAMPTZ NOT NULL,
    created_by        UUID REFERENCES identity.account(id),
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    revoked_at        TIMESTAMPTZ
);
```

### 2.7 `identity.audit_event`

```sql
CREATE TABLE identity.audit_event (
    id           BIGSERIAL PRIMARY KEY,                -- 时序数据,用 bigserial
    occurred_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    account_id   UUID,                                 -- 可空:系统事件
    actor        TEXT NOT NULL,                       -- 'account' | 'system' | 'admin:<uuid>'
    action       TEXT NOT NULL,                       -- 'login.success' 等
    target_type  TEXT,                                -- 'account' | 'oidc_client' | ...
    target_id    TEXT,
    outcome      TEXT NOT NULL CHECK (outcome IN ('success','failure')),
    ip           INET,
    user_agent   TEXT,
    metadata     JSONB
);

CREATE INDEX idx_audit_time    ON identity.audit_event (occurred_at DESC);
CREATE INDEX idx_audit_account ON identity.audit_event (account_id, occurred_at DESC);
CREATE INDEX idx_audit_action  ON identity.audit_event (action, occurred_at DESC);
```

用 `BIGSERIAL` 而非 UUID:审计是高频追加的时序数据,bigint 更省空间、索引更小、范围查询更快。

**分区策略**:数据量小时不分区;超过 1000 万行后按月做 range 分区。

## 三、oidc schema

### 3.1 `oidc.client`

```sql
CREATE TABLE oidc.client (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    client_id           TEXT NOT NULL UNIQUE,
    client_secret_hash  BYTEA,                         -- NULL = 公开客户端
    name                TEXT NOT NULL,
    redirect_uris       TEXT[] NOT NULL,               -- PG 原生数组,不用 JSON
    grant_types         TEXT[] NOT NULL,
    scopes              TEXT[] NOT NULL,
    require_pkce        BOOLEAN NOT NULL DEFAULT TRUE,
    access_token_ttl    INTERVAL NOT NULL DEFAULT '1 hour',
    refresh_token_ttl   INTERVAL NOT NULL DEFAULT '30 days',
    authorization_code_ttl INTERVAL NOT NULL DEFAULT '60 seconds',
    status              TEXT NOT NULL DEFAULT 'active'
                        CHECK (status IN ('active','disabled')),
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

**相比旧版的改进**:旧版用 `TEXT` 存 JSON 数组,每次查询都要 `jsonb_array_elements_text` 解析。PG 的 `TEXT[]` 支持 GIN 索引和 `= ANY()` 查询,性能与可读性都更好。

### 3.2 令牌与授权码

```sql
CREATE TABLE oidc.authorization_code (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    code_hash         BYTEA NOT NULL UNIQUE,
    client_id         TEXT NOT NULL REFERENCES oidc.client(client_id),
    account_id        UUID NOT NULL REFERENCES identity.account(id),
    redirect_uri      TEXT NOT NULL,
    scopes            TEXT[] NOT NULL,
    nonce             TEXT,
    code_challenge    TEXT NOT NULL,
    code_challenge_method TEXT NOT NULL CHECK (code_challenge_method = 'S256'),
    expires_at        TIMESTAMPTZ NOT NULL,
    used_at           TIMESTAMPTZ,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE oidc.access_token (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    token_hash   BYTEA NOT NULL UNIQUE,
    client_id    TEXT NOT NULL REFERENCES oidc.client(client_id),
    account_id   UUID NOT NULL REFERENCES identity.account(id),
    scopes       TEXT[] NOT NULL,
    expires_at   TIMESTAMPTZ NOT NULL,
    revoked_at   TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE oidc.refresh_token (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    token_hash      BYTEA NOT NULL UNIQUE,
    client_id       TEXT NOT NULL REFERENCES oidc.client(client_id),
    account_id      UUID NOT NULL REFERENCES identity.account(id),
    scopes          TEXT[] NOT NULL,
    rotated_from    UUID REFERENCES oidc.refresh_token(id),   -- 轮换链,防重放
    expires_at      TIMESTAMPTZ NOT NULL,
    revoked_at      TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE oidc.consent (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id  UUID NOT NULL REFERENCES identity.account(id),
    client_id   TEXT NOT NULL REFERENCES oidc.client(client_id),
    scopes      TEXT[] NOT NULL,
    granted_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (account_id, client_id)
);
```

**`rotated_from` 字段是 refresh token 轮换的重放检测机制**:刷新时旧 token 标记为已用并签发新 token,若同一旧 token 再次出现即判定为泄露,吊销整条链。

### 3.3 `oidc.signing_key`

```sql
CREATE TABLE oidc.signing_key (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    kid            TEXT NOT NULL UNIQUE,               -- 'oidc-<random>'
    algo           TEXT NOT NULL DEFAULT 'RS256',
    public_jwk     JSONB NOT NULL,                     -- 明文,需公开
    private_jwk_enc BYTEA NOT NULL,                    -- AES-256-GCM 加密
    status         TEXT NOT NULL DEFAULT 'active'
                   CHECK (status IN ('active','retired','revoked')),
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    retired_at     TIMESTAMPTZ
);
```

**私钥加密存储**:用 `KEY_MASTER_SECRET` 做 AES-256-GCM 加密。数据库泄露时攻击者拿不到可直接使用的私钥。

## 四、minecraft schema

### 4.1 `minecraft.profile`

```sql
CREATE TABLE minecraft.profile (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id   UUID NOT NULL UNIQUE REFERENCES identity.account(id) ON DELETE CASCADE,
    uuid         UUID NOT NULL UNIQUE,                 -- MC 玩家 UUID(确定性派生)
    current_name TEXT NOT NULL UNIQUE,                 -- 3-16 位 [A-Za-z0-9_]
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

**`uuid` 确定性派生**:`uuid = uuidv5(namespace, account_id)`,保证同一账号永远同一 UUID,不泄露任何可关联信息。

### 4.2 `minecraft.name_history`

```sql
CREATE TABLE minecraft.name_history (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    profile_id   UUID NOT NULL REFERENCES minecraft.profile(id) ON DELETE CASCADE,
    name         TEXT NOT NULL,
    changed_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_name_history_name ON minecraft.name_history (name);
```

旧名保留一段时间(待定,见 [04-decisions.md](./04-decisions.md) 第三节),期间禁止他人注册。

### 4.3 `minecraft.access_token`

**与 OIDC 完全隔离(ADR-004):**

```sql
CREATE TABLE minecraft.access_token (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    token_hash     BYTEA NOT NULL UNIQUE,
    profile_id     UUID NOT NULL REFERENCES minecraft.profile(id) ON DELETE CASCADE,
    client_token   TEXT,
    expires_at     TIMESTAMPTZ NOT NULL,
    revoked_at     TIMESTAMPTZ,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE minecraft.server_session (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    profile_id        UUID NOT NULL REFERENCES minecraft.profile(id) ON DELETE CASCADE,
    server_id         TEXT NOT NULL,
    ip                INET,
    joined_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    disconnected_at   TIMESTAMPTZ
);
```

### 4.4 `minecraft.texture` 与 `minecraft.avatar`

```sql
CREATE TABLE minecraft.texture (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    hash         TEXT NOT NULL UNIQUE,                 -- sha256 hex,去重键
    type         TEXT NOT NULL CHECK (type IN ('skin','cape')),
    size         INTEGER NOT NULL,                     -- 字节数
    width        INTEGER NOT NULL,
    height       INTEGER NOT NULL,
    mime         TEXT NOT NULL DEFAULT 'image/png',
    ref_count    INTEGER NOT NULL DEFAULT 0,           -- 引用计数,0 可回收
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE minecraft.profile_texture (
    profile_id   UUID NOT NULL REFERENCES minecraft.profile(id) ON DELETE CASCADE,
    texture_id   UUID NOT NULL REFERENCES minecraft.texture(id) ON DELETE CASCADE,
    type         TEXT NOT NULL CHECK (type IN ('skin','cape')),
    is_active    BOOLEAN NOT NULL DEFAULT TRUE,
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (profile_id, type)
);

CREATE TABLE minecraft.avatar (
    profile_id   UUID PRIMARY KEY REFERENCES minecraft.profile(id) ON DELETE CASCADE,
    hash         TEXT NOT NULL,                        -- 头像内容 hash,用于缓存
    rendered_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

**`ref_count` 机制**:同一份 PNG 可能被多个 profile 引用(sha256 去重),引用归零后可回收文件。

## 五、app schema

```sql
CREATE TABLE app.setting (
    key         TEXT PRIMARY KEY,
    value       JSONB NOT NULL,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_by  UUID REFERENCES identity.account(id)
);
```

后台可改的配置(注册开关、密码策略、邮件设置等)存这里,优先级高于环境变量。

## 六、索引与性能

| 场景 | 索引 |
|---|---|
| 登录(邮箱) | `idx_account_email_lower` |
| 会话校验 | `idx_session_idle_exp` 部分索引(仅未吊销) |
| 审计检索 | `idx_audit_time` / `idx_audit_account` |
| MC 按名查 profile | `profile.current_name` UNIQUE |
| 材质去重 | `texture.hash` UNIQUE |
| OIDC 令牌校验 | 各表 `token_hash` UNIQUE |

## 七、迁移策略

- goose 版本化迁移,文件名 `00001_init_identity.sql`、`00002_init_oidc.sql`…
- **只向前**:已合并到 main 的迁移不修改,变更一律新增
- 生产变更走 `goose up`,回滚需显式 `goose down` 且谨慎
- 迁移前自动备份(见 [08-deployment.md](./08-deployment.md))

## 八、sqlc 使用约定

```sql
-- name: GetAccountByEmail :one
SELECT * FROM identity.account WHERE lower(email) = lower($1);
```

- 所有查询写进 `db/queries/*.sql`,**禁止在 Go 代码里拼 SQL 字符串**
- 生成物提交到 `internal/platform/db/query/*.go`
- CI 执行 `make sqlc && git diff --exit-code` 校验一致性

---

**上一篇**:[02-architecture.md](./02-architecture.md) —— 架构设计
**下一篇**:[04-decisions.md](./04-decisions.md) —— 关键决策记录

---

## 九、实施补充(M1 落地时的 schema 调整)

> 本节记录 M1 写迁移时对前八节设计的**增量调整**。
> 迁移一旦合入即不可修改(见第七节「只向前」),所以这里把实际形态固定下来。
> 调整原因统一是:fosite 的会话持久化、Yggdrasil 协议的安全要求需要额外的列或表。

### 9.1 identity 调整

| 调整 | 内容 | 原因 |
|---|---|---|
| `account.email` | 去掉列级 `UNIQUE`,改为唯一索引 `idx_account_email_lower ON (lower(email))` | 邮箱大小写不敏感唯一,列级 UNIQUE 做不到 |
| `account` 新增 | `email_verified_at TIMESTAMPTZ` | 原设计在 `identity.token` 一节提到验证状态,但表里没有承载字段 |
| `account.status` 默认值 | `pending_verification` | 与「注册后需验证邮箱才能用」的实际流程一致 |
| `credential.params` 默认值 | `'{}'::jsonb` | 避免插入时必填 |
| `session` 新增 | `revoke_reason TEXT` | 区分「主动登出 / 改密码吊销 / 管理员踢下线」,便于审计追溯 |
| `session` 新增 | `idx_session_sso` | 全局登出按 `sso_session_id` 批量吊销 |
| `email_token` 新增 | `idx_email_token_expires` | 后台定期清理过期令牌 |
| `invitation` 新增 | `max_uses > 0`、`used_count >= 0` 的 CHECK | 防止负数或零次邀请码被写进来 |
| `audit_event` 新增 | `idx_audit_target` | 后台按对象维度检索(哪个客户端、哪张材质) |

另外新增 `ygg_touch_updated_at()` 触发器函数,所有带 `updated_at` 的表共用。

### 9.2 oidc 调整:为 fosite 持久化会话

fosite 的令牌不是一个字符串,而是「签名 + 会话数据 + 原始请求」三件套。
换令牌阶段要完整还原这些信息,因此三张令牌表各增加三列:

```sql
signature      BYTEA NOT NULL UNIQUE,  -- fosite 用来按请求参数反查会话
session        JSONB NOT NULL,         -- 序列化后的 fosite.Session
request        JSONB NOT NULL,         -- 序列化后的原始 fosite.Request
```

- `oidc.access_token.account_id` 改为**可空**:`client_credentials` 流程没有用户主体。
- `oidc.refresh_token` 与 `oidc.access_token` 增加 `revoked_reason`,记录吊销原因。
- `oidc.consent` 增加 `session_id` 与 `session JSONB`:同意记录要能还原 OIDC 会话,
  否则 userinfo / id_token 拿不到 claims。

### 9.3 oidc 新增表

| 表 | 用途 |
|---|---|
| `oidc.pushed_authorization_request` | RFC 9126 推送授权请求。`request_uri_hash` 唯一,带 `expires_at` 与 `used_at` |
| `oidc.device_code` | RFC 8628 设备码流程。`user_code_hash` 单独建唯一索引,与 `device_code_hash` 区分 |

### 9.4 minecraft 调整与新增

| 调整 | 内容 | 原因 |
|---|---|---|
| `name_history` 新增 | `reusable_at TIMESTAMPTZ` | 替代第三节「待定事项 1」。旧名默认保留 `MC_NAME_RETENTION_DAYS` 天,期间禁止他人注册;`NULL` 表示永久保留 |
| `profile` 新增 | `idx_profile_name_lower` | 按名查档案走 `current_name`,MC 协议传的是原样大小写 |
| `access_token` 新增 | `revoked_reason` | 区分「主动登出 / 刷新轮换 / 管理员吊销」 |
| **新增** `minecraft.server` | 登记在册的 MC 服务器:`server_id` + `shared_secret` + 启用开关 | 「离线服务器绕过」的防线。`hasJoined` 只认登记过的 serverId,未登记一律 204 |
| **新增** `minecraft.signing_key` | MC 域独立签名密钥,`kid` 前缀 `mc-` | 第五节「隔离性要求」里提到过但前面章节没定义表 |
| **新增** `minecraft.external_binding` | 外部皮肤站绑定 + base_url | 4.5 的外部回源需要持久化绑定关系。`base_url` 只允许后台配置,不是用户输入(防 SSRF) |
| `texture` 新增 | `last_used_at`、`idx_texture_refcount` | 引用计数回收需要判断「ref_count 归零且长期未访问」 |

`server_session` 增加 `verified_at`:同一 serverId 只允许 `hasJoined` 成功一次(防重放)。

### 9.5 app 调整

`app.setting` 预置 11 条默认值(注册模式、密码长度、会话时长、邮件节流、改名保留期等),
与迁移种子一致,启动后由后台接管。

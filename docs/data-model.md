# 数据模型

> PostgreSQL 16+,4 个 schema。所有 ID 使用 UUIDv4,所有时间戳使用 `timestamptz`。迁移文件在 `db/migrations/`,查询定义在 `db/queries/`。

---

## 一、schema 划分

| schema | 职责 | 谁访问 |
|---|---|---|
| `identity` | 账号、凭据、会话、RBAC、审计、邮件令牌 | 全部域 |
| `oidc` | OIDC 客户端、授权码、令牌、同意、签名密钥 | OIDC 域 |
| `minecraft` | 玩家档案、名称历史、MC 令牌、材质、签名密钥 | MC 域 |
| `app` | 运行时可改的应用配置(后台修改) | admin 域 |

**域中立原则**: `identity` schema 不含任何 `oidc` / `minecraft` 语义的表(ADR-002/005)。注意 `account.mc_login_enabled` 字段 —— 它是布尔开关,内核只当作「通用开关」处理,不理解其含义(见 docs/architecture.md 第五节豁免说明)。

---

## 二、identity schema 表结构

### 2.1 `identity.account`

```sql
CREATE TABLE identity.account (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    username        TEXT NOT NULL,
    username_lower  TEXT NOT NULL UNIQUE,
    email           TEXT NOT NULL UNIQUE,
    status          TEXT NOT NULL DEFAULT 'active'
                    CHECK (status IN ('active','disabled','locked','pending_verification')),
    mc_login_enabled BOOLEAN NOT NULL DEFAULT TRUE,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

**关键点**:
- `email` 强制唯一(C-10),避免找回密码歧义
- `username_lower` 大小写不敏感唯一,`username` 保留原样展示
- `status` 用 CHECK 约束枚举,不用 PG enum(改枚举值不需要 `ALTER TYPE`)
- `mc_login_enabled` 是通用布尔开关,内核不理解其语义(见 docs/architecture.md 第五节)

### 2.2 `identity.credential`

```sql
CREATE TABLE identity.credential (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id      UUID NOT NULL REFERENCES identity.account(id) ON DELETE CASCADE,
    algo            TEXT NOT NULL DEFAULT 'argon2id'
                    CHECK (algo IN ('argon2id')),
    hash            TEXT NOT NULL,
    params          JSONB NOT NULL,
    failed_attempts INT NOT NULL DEFAULT 0,
    locked_until    TIMESTAMPTZ,
    changed_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (account_id, algo)
);
```

- 每账号每算法一条,支持并存迁移
- `params` 存 `{"m":65536,"t":1,"p":4}`,便于未来升级参数而不用改代码

### 2.3 `identity.session` (ADR-004)

```sql
CREATE TABLE identity.session (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id       UUID NOT NULL REFERENCES identity.account(id) ON DELETE CASCADE,
    token_hash       BYTEA NOT NULL UNIQUE,
    sso_session_id   UUID,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at       TIMESTAMPTZ NOT NULL,
    idle_expires_at  TIMESTAMPTZ NOT NULL,
    revoked_at       TIMESTAMPTZ,
    ip               INET,
    user_agent       TEXT
);
```

**唯一存终端用户登录态**。OIDC access_token、refresh_token、MC access_token 各存在自己的 schema 下,不混存。

- `token_hash` 只存 sha256,数据库泄露不可直接劫持
- `expires_at` 绝对过期,不因活跃而延长
- `idle_expires_at` 滑动过期,每次活跃刷新

### 2.4 RBAC (ADR-005)

```sql
CREATE TABLE identity.permission (
    code         TEXT PRIMARY KEY,
    description  TEXT NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE identity.role (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    code        TEXT NOT NULL UNIQUE,
    name        TEXT NOT NULL,
    description TEXT,
    is_system   BOOLEAN NOT NULL DEFAULT FALSE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE identity.role_permission (
    role_id    UUID NOT NULL REFERENCES identity.role(id) ON DELETE CASCADE,
    permission TEXT NOT NULL REFERENCES identity.permission(code) ON DELETE CASCADE,
    PRIMARY KEY (role_id, permission)
);

CREATE TABLE identity.account_role (
    account_id UUID NOT NULL REFERENCES identity.account(id) ON DELETE CASCADE,
    role_id    UUID NOT NULL REFERENCES identity.role(id) ON DELETE CASCADE,
    granted_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    granted_by  UUID REFERENCES identity.account(id),
    PRIMARY KEY (account_id, role_id)
);
```

**单一命名空间**,权限点用 `域:资源:动作` 三段式。完整清单见迁移文件 `db/migrations/00001_init_identity.sql`。

### 2.5 其他 identity 表

```sql
-- 审计日志(只追加)
CREATE TABLE identity.audit_log (
    id         BIGSERIAL PRIMARY KEY,
    account_id UUID REFERENCES identity.account(id),
    action     TEXT NOT NULL,
    target     TEXT,
    metadata   JSONB,
    ip         INET,
    user_agent TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 邮件令牌(合并 verify_email 与 reset_password)
CREATE TABLE identity.email_token (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id  UUID NOT NULL REFERENCES identity.account(id) ON DELETE CASCADE,
    token_hash  BYTEA NOT NULL UNIQUE,
    purpose     TEXT NOT NULL CHECK (purpose IN ('verify_email','reset_password')),
    expires_at  TIMESTAMPTZ NOT NULL,
    used_at     TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

---

## 三、oidc schema

```sql
CREATE TABLE oidc.client (
    client_id     TEXT PRIMARY KEY,
    client_secret_hash TEXT,
    grant_types   TEXT[] NOT NULL,
    response_types TEXT[] NOT NULL,
    scopes        TEXT[] NOT NULL,
    redirect_uris TEXT[] NOT NULL,
    pkce_required BOOLEAN NOT NULL DEFAULT TRUE,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE oidc.authorization_code (...);
CREATE TABLE oidc.access_token (...);
CREATE TABLE oidc.refresh_token (...);
CREATE TABLE oidc.consent (...);
CREATE TABLE oidc.jwks (...);  -- 签名密钥存储
```

与 minecraft schema 完全隔离(ADR-004):令牌表不同、签名密钥不同。

---

## 四、minecraft schema

```sql
CREATE TABLE minecraft.profile (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id      UUID NOT NULL UNIQUE REFERENCES identity.account(id) ON DELETE CASCADE,
    uuid            UUID NOT NULL,           -- profile_uuid = uuidv5(namespace, account_id)
    username        TEXT NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE minecraft.texture (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    profile_id      UUID NOT NULL REFERENCES minecraft.profile(id) ON DELETE CASCADE,
    sha256          TEXT NOT NULL UNIQUE,    -- 去重键
    url             TEXT NOT NULL,
    variant         TEXT NOT NULL DEFAULT 'default',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

**玩家 UUID 推导**: `uuidv5(NAMESPACE, account_id)`,命名空间常量永不可改(改了所有玩家 UUID 都会变,历史皮肤、别名、白名单全部失效,见 docs/minecraft.md)。

---

## 五、app schema

```sql
CREATE TABLE app.setting (
    key         TEXT PRIMARY KEY,
    value       TEXT NOT NULL,
    updated_by  UUID REFERENCES identity.account(id),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

**双层配置模型**(ADR-012):
- `env` 是首次启动的种子(来自 `.env.example`)
- `setting` 表是运行时现值(后台修改)
- env 空值不种行:防止非法零值让整轮 Sync 失败

---

## 六、迁移与生成

### 迁移

- 工具: `pressly/goose/v3` (库,非 CLI)
- 文件: `db/migrations/*.sql`,按数字序号命名
- 嵌入: `db/embed.go` 用 `//go:embed` 把迁移文件编进二进制
- 自动迁移: `DB_AUTO_MIGRATE=true` (默认开启,见 docs/configuration.md)

### 查询生成

- 工具: `sqlc`
- 定义: `db/queries/*.sql`
- 生成物: `internal/platform/db/query/*.sql.go` (必须提交)
- 命令: `make sqlc`
- CI 校验: `make sqlc-diff` (比较生成物与查询定义是否一致)

---

## 七、索引策略

- 所有外键建索引
- 高频查询字段建索引(`account.email`, `session.idle_expires_at` WHERE `revoked_at IS NULL`)
- 审计表只追加,不建删除索引

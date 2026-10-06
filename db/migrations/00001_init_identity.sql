-- M1 基线迁移:identity schema
--
-- 域中立性(ADR-005 / ADR-010):本 schema 只认识「身份 + 凭据 + 会话 + 权限」,
-- 不得出现任何具体业务域的语义。account.mc_login_enabled 是唯一的例外,
-- 它只是一个通用布尔开关,内核不理解其含义(见 docs/data-model.md 第一节)。

-- +goose Up
-- +goose StatementBegin

CREATE SCHEMA IF NOT EXISTS identity;

-- updated_at 自动维护。所有需要「最后修改时间」的表共用这一个触发器函数。
CREATE OR REPLACE FUNCTION ygg_touch_updated_at() RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at := now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

-- ---------------------------------------------------------------- 账号

CREATE TABLE identity.account (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    username         TEXT NOT NULL,
    -- 大小写不敏感唯一。username 保留原样用于展示,唯一性判断走这一列。
    username_lower   TEXT NOT NULL UNIQUE,
    -- 存原始大小写,唯一索引建在 lower(email) 上(变更 C-10:强制邮箱唯一)
    email            TEXT NOT NULL,
    status           TEXT NOT NULL DEFAULT 'pending_verification'
                     CHECK (status IN ('active','disabled','locked','pending_verification')),
    -- 通用布尔开关。内核只当「通用开关」处理,不理解其业务语义。
    mc_login_enabled BOOLEAN NOT NULL DEFAULT TRUE,
    email_verified_at TIMESTAMPTZ,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX idx_account_email_lower ON identity.account (lower(email));
CREATE INDEX idx_account_status ON identity.account (status);

CREATE TRIGGER trg_account_updated_at
    BEFORE UPDATE ON identity.account
    FOR EACH ROW EXECUTE FUNCTION ygg_touch_updated_at();

-- ---------------------------------------------------------------- 凭据

CREATE TABLE identity.credential (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id      UUID NOT NULL REFERENCES identity.account(id) ON DELETE CASCADE,
    algo            TEXT NOT NULL DEFAULT 'argon2id'
                    CHECK (algo IN ('argon2id')),
    hash            TEXT NOT NULL,
    -- m/t/p 参数内嵌在 hash 字符串里;params 额外存一份结构化的,便于升级与审计
    params          JSONB NOT NULL DEFAULT '{}'::jsonb,
    failed_attempts INT NOT NULL DEFAULT 0,
    locked_until    TIMESTAMPTZ,
    changed_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- 每账号每算法一条,支持多算法并存迁移
    UNIQUE (account_id, algo)
);

CREATE INDEX idx_credential_locked ON identity.credential (locked_until)
    WHERE locked_until IS NOT NULL;

-- ---------------------------------------------------------------- 会话
--
-- ADR-004:session 只存终端用户登录态。OIDC 刷新令牌与 MC 访问令牌
-- 各自独立成表,不混用、不共享存储与签名密钥。

CREATE TABLE identity.session (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id      UUID NOT NULL REFERENCES identity.account(id) ON DELETE CASCADE,
    -- 只存 sha256(token)。数据库泄露时 hash 无法直接用于会话劫持。
    token_hash      BYTEA NOT NULL UNIQUE,
    sso_session_id  UUID,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at      TIMESTAMPTZ NOT NULL,   -- 绝对过期,不因活跃而延长
    idle_expires_at TIMESTAMPTZ NOT NULL,   -- 滑动过期
    revoked_at      TIMESTAMPTZ,
    revoke_reason   TEXT,
    ip              INET,
    user_agent      TEXT
);

CREATE INDEX idx_session_account ON identity.session (account_id, revoked_at);
-- 会话校验是最高频查询,只索引未吊销的行
CREATE INDEX idx_session_idle_exp ON identity.session (idle_expires_at)
    WHERE revoked_at IS NULL;
CREATE INDEX idx_session_sso ON identity.session (sso_session_id)
    WHERE sso_session_id IS NOT NULL;

-- ---------------------------------------------------------------- RBAC
--
-- ADR-005:统一为单一 RBAC 模型,取代旧版 admin_/mc_/oauth_ 六张表。

CREATE TABLE identity.permission (
    code        TEXT PRIMARY KEY,              -- 三段式:域:资源:动作
    description TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE identity.role (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    code        TEXT NOT NULL UNIQUE,
    name        TEXT NOT NULL,
    description TEXT,
    is_system   BOOLEAN NOT NULL DEFAULT FALSE,  -- 系统内置角色不可删
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TRIGGER trg_role_updated_at
    BEFORE UPDATE ON identity.role
    FOR EACH ROW EXECUTE FUNCTION ygg_touch_updated_at();

CREATE TABLE identity.role_permission (
    role_id    UUID NOT NULL REFERENCES identity.role(id) ON DELETE CASCADE,
    permission TEXT NOT NULL REFERENCES identity.permission(code) ON DELETE CASCADE,
    PRIMARY KEY (role_id, permission)
);

CREATE INDEX idx_role_permission_code ON identity.role_permission (permission);

CREATE TABLE identity.account_role (
    account_id UUID NOT NULL REFERENCES identity.account(id) ON DELETE CASCADE,
    role_id    UUID NOT NULL REFERENCES identity.role(id) ON DELETE CASCADE,
    granted_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    granted_by UUID REFERENCES identity.account(id) ON DELETE SET NULL,
    PRIMARY KEY (account_id, role_id)
);

CREATE INDEX idx_account_role_role ON identity.account_role (role_id);

-- ---------------------------------------------------------------- 邮箱令牌
--
-- 验证邮箱与重置密码结构完全相同,用 purpose 区分,比两张同构表简洁。

CREATE TABLE identity.email_token (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id UUID NOT NULL REFERENCES identity.account(id) ON DELETE CASCADE,
    token_hash BYTEA NOT NULL UNIQUE,
    purpose    TEXT NOT NULL CHECK (purpose IN ('verify_email','reset_password')),
    expires_at TIMESTAMPTZ NOT NULL,
    used_at    TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_email_token_account ON identity.email_token (account_id, purpose);
-- 定期清理过期令牌时用
CREATE INDEX idx_email_token_expires ON identity.email_token (expires_at);

-- ---------------------------------------------------------------- 邀请码

CREATE TABLE identity.invitation (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    code        TEXT NOT NULL UNIQUE,
    email       TEXT,                                   -- 可选,绑定特定邮箱
    max_uses    INT NOT NULL DEFAULT 1 CHECK (max_uses > 0),
    used_count  INT NOT NULL DEFAULT 0 CHECK (used_count >= 0),
    expires_at  TIMESTAMPTZ NOT NULL,
    created_by  UUID REFERENCES identity.account(id) ON DELETE SET NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    revoked_at  TIMESTAMPTZ
);

CREATE INDEX idx_invitation_expires ON identity.invitation (expires_at);

-- ---------------------------------------------------------------- 审计
--
-- BIGSERIAL 而非 UUID:审计是高频追加的时序数据,bigint 更省空间、索引更小。
-- 数据量小时不分区,超过 1000 万行后再按月 range 分区。

CREATE TABLE identity.audit_event (
    id          BIGSERIAL PRIMARY KEY,
    occurred_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    account_id  UUID,                                   -- 可空:系统事件
    actor       TEXT NOT NULL,                          -- 'account' | 'system' | 'admin:<uuid>'
    action      TEXT NOT NULL,                          -- 'login.success' 等
    target_type TEXT,                                   -- 'account' | 'oidc_client' | ...
    target_id   TEXT,
    outcome     TEXT NOT NULL CHECK (outcome IN ('success','failure')),
    ip          INET,
    user_agent  TEXT,
    metadata    JSONB NOT NULL DEFAULT '{}'::jsonb
);

CREATE INDEX idx_audit_time    ON identity.audit_event (occurred_at DESC);
CREATE INDEX idx_audit_account ON identity.audit_event (account_id, occurred_at DESC);
CREATE INDEX idx_audit_action  ON identity.audit_event (action, occurred_at DESC);
CREATE INDEX idx_audit_target  ON identity.audit_event (target_type, target_id, occurred_at DESC);

-- ---------------------------------------------------------------- 种子数据

-- 权限点全集。命名空间唯一,三段式 域:资源:动作(ADR-005)。
INSERT INTO identity.permission (code, description) VALUES
    ('account:read',              '查看账号'),
    ('account:write',             '管理账号:禁用、改邮箱、重置密码'),
    ('rbac:read',                 '查看角色与权限点'),
    ('rbac:write',                '管理角色、授权与撤权'),
    ('audit:read',                '查看审计日志'),
    ('audit:export',              '导出审计日志'),
    ('oidc:client:read',          '查看 OIDC 客户端'),
    ('oidc:client:write',         '管理 OIDC 客户端与轮换密钥'),
    ('oidc:token:revoke',         '吊销 OIDC 令牌'),
    ('minecraft:profile:read',    '查看玩家档案'),
    ('minecraft:profile:write',   '管理玩家档案:改名、封禁'),
    ('minecraft:texture:read',    '查看材质库'),
    ('minecraft:texture:write',   '管理材质库'),
    ('minecraft:server:read',     '查看服务器白名单'),
    ('minecraft:server:write',    '管理服务器白名单'),
    ('setting:read',              '查看应用配置'),
    ('setting:write',             '修改应用配置');

-- 系统内置角色。is_system = true 的角色不允许删除。
INSERT INTO identity.role (code, name, description, is_system) VALUES
    ('platform_admin', '平台管理员', '持有全部权限点,用于系统初始引导与运维兜底', TRUE),
    ('user',           '普通用户',   '终端用户默认角色,不含任何后台权限点', TRUE);

INSERT INTO identity.role_permission (role_id, permission)
SELECT r.id, p.code
FROM identity.role r, identity.permission p
WHERE r.code = 'platform_admin';

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP SCHEMA IF EXISTS identity CASCADE;
DROP FUNCTION IF EXISTS ygg_touch_updated_at();
-- +goose StatementEnd

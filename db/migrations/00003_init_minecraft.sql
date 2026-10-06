-- M1 基线迁移:minecraft schema
--
-- 与 oidc schema 完全隔离(ADR-004):令牌表不同、签名密钥不同、
-- 校验逻辑不同。MC 域故障不得影响 OIDC 链路。

-- +goose Up
-- +goose StatementBegin

CREATE SCHEMA IF NOT EXISTS minecraft;

-- ---------------------------------------------------------------- 玩家档案

CREATE TABLE minecraft.profile (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id   UUID NOT NULL UNIQUE REFERENCES identity.account(id) ON DELETE CASCADE,
    -- uuidv5(namespace, account_id) 确定性派生,同一账号永远是同一个 UUID
    uuid         UUID NOT NULL UNIQUE,
    current_name TEXT NOT NULL UNIQUE CHECK (current_name ~ '^[A-Za-z0-9_]{3,16}$'),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_profile_name_lower ON minecraft.profile (lower(current_name));

CREATE TRIGGER trg_profile_updated_at
    BEFORE UPDATE ON minecraft.profile
    FOR EACH ROW EXECUTE FUNCTION ygg_touch_updated_at();

-- ---------------------------------------------------------------- 改名历史
--
-- 旧名保留期内禁止他人注册,避免「抢注腾出的名字」造成的冒名风险。

CREATE TABLE minecraft.name_history (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    profile_id UUID NOT NULL REFERENCES minecraft.profile(id) ON DELETE CASCADE,
    name       TEXT NOT NULL,
    changed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- 保留期到点后才允许被再次占用;NULL 表示永久保留
    reusable_at TIMESTAMPTZ
);

CREATE INDEX idx_name_history_name ON minecraft.name_history (lower(name));
CREATE INDEX idx_name_history_reusable ON minecraft.name_history (reusable_at)
    WHERE reusable_at IS NOT NULL;

-- ---------------------------------------------------------------- MC 访问令牌
--
-- 与 oidc.access_token 完全隔离:不同的表、不同的校验逻辑、不同的签名密钥。

CREATE TABLE minecraft.access_token (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    -- 只存 sha256(token);令牌明文是 32 位无横线小写 hex,MC 服务端会解析
    token_hash  BYTEA NOT NULL UNIQUE,
    profile_id  UUID NOT NULL REFERENCES minecraft.profile(id) ON DELETE CASCADE,
    client_token TEXT,
    expires_at  TIMESTAMPTZ NOT NULL,
    revoked_at  TIMESTAMPTZ,
    revoked_reason TEXT,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_mc_token_profile ON minecraft.access_token (profile_id, expires_at);
CREATE INDEX idx_mc_token_client  ON minecraft.access_token (client_token)
    WHERE client_token IS NOT NULL;
CREATE INDEX idx_mc_token_expires ON minecraft.access_token (expires_at)
    WHERE revoked_at IS NULL;

-- ---------------------------------------------------------------- 进服会话
--
-- /mc/join 记录 serverId,hasJoined 时凭它做五生效点校验,
-- 并用时间窗 + 一次性消费防重放。

CREATE TABLE minecraft.server_session (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    profile_id       UUID NOT NULL REFERENCES minecraft.profile(id) ON DELETE CASCADE,
    server_id        TEXT NOT NULL UNIQUE,
    ip               INET,
    joined_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- hasJoined 成功时打点,同一 serverId 只允许成功一次(防重放)
    verified_at      TIMESTAMPTZ,
    disconnected_at  TIMESTAMPTZ
);

CREATE INDEX idx_server_session_profile ON minecraft.server_session (profile_id, joined_at DESC);

-- 已登记的 MC 服务器。只有登记在册的 server_id 才能通过 hasJoined,
-- 未登记的一律 204 拒绝 —— 这是「离线服务器绕过」的防线。
CREATE TABLE minecraft.server (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    server_id     TEXT NOT NULL UNIQUE,
    name          TEXT NOT NULL,
    -- 与 authlib-injector 配置的预共享密钥,hasJoined 五生效点校验用
    shared_secret TEXT NOT NULL,
    enabled       BOOLEAN NOT NULL DEFAULT TRUE,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TRIGGER trg_server_updated_at
    BEFORE UPDATE ON minecraft.server
    FOR EACH ROW EXECUTE FUNCTION ygg_touch_updated_at();

-- ---------------------------------------------------------------- 签名密钥
--
-- MC 域独立签名密钥(kid 前缀 mc-),与 oidc.signing_key(oidc- 前缀)分离。

CREATE TABLE minecraft.signing_key (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    kid             TEXT NOT NULL UNIQUE,               -- 'mc-<random>'
    algo            TEXT NOT NULL DEFAULT 'RS256',
    public_jwk      JSONB NOT NULL,
    private_jwk_enc BYTEA NOT NULL,                      -- AES-256-GCM 加密
    status          TEXT NOT NULL DEFAULT 'active'
                    CHECK (status IN ('active','retired','revoked')),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    retired_at      TIMESTAMPTZ
);

CREATE UNIQUE INDEX idx_mc_signing_key_active ON minecraft.signing_key (status)
    WHERE status = 'active';

-- ---------------------------------------------------------------- 材质

CREATE TABLE minecraft.texture (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    -- sha256 hex,去重键
    hash       TEXT NOT NULL UNIQUE,
    type       TEXT NOT NULL CHECK (type IN ('skin','cape')),
    size       INTEGER NOT NULL CHECK (size > 0),
    width      INTEGER NOT NULL CHECK (width > 0),
    height     INTEGER NOT NULL CHECK (height > 0),
    mime       TEXT NOT NULL DEFAULT 'image/png',
    -- 引用计数。同一份 PNG 可能被多个 profile 引用,归零后可回收文件
    ref_count  INTEGER NOT NULL DEFAULT 0 CHECK (ref_count >= 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- 回收用:最后被访问时间
    last_used_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_texture_refcount ON minecraft.texture (ref_count, last_used_at);

CREATE TRIGGER trg_texture_updated_at
    BEFORE UPDATE ON minecraft.texture
    FOR EACH ROW EXECUTE FUNCTION ygg_touch_updated_at();

CREATE TABLE minecraft.profile_texture (
    profile_id UUID NOT NULL REFERENCES minecraft.profile(id) ON DELETE CASCADE,
    texture_id UUID NOT NULL REFERENCES minecraft.texture(id) ON DELETE CASCADE,
    type       TEXT NOT NULL CHECK (type IN ('skin','cape')),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (profile_id, type)
);

CREATE INDEX idx_profile_texture_texture ON minecraft.profile_texture (texture_id);

CREATE TRIGGER trg_profile_texture_updated_at
    BEFORE UPDATE ON minecraft.profile_texture
    FOR EACH ROW EXECUTE FUNCTION ygg_touch_updated_at();

-- ---------------------------------------------------------------- 头像缓存

CREATE TABLE minecraft.avatar (
    profile_id  UUID PRIMARY KEY REFERENCES minecraft.profile(id) ON DELETE CASCADE,
    -- 渲染时的皮肤 hash。皮肤变更后 hash 不匹配即重新渲染
    hash        TEXT NOT NULL,
    rendered_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ---------------------------------------------------------------- 外部皮肤站绑定

-- MC_SKIN_EXTERNAL=true 时的回源目标。只允许后台配置,不是用户输入,
-- 防 SSRF(见 docs/security.md 第七节)。
CREATE TABLE minecraft.external_binding (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    profile_id  UUID NOT NULL UNIQUE REFERENCES minecraft.profile(id) ON DELETE CASCADE,
    provider    TEXT NOT NULL,
    external_user_id TEXT NOT NULL,
    base_url    TEXT NOT NULL,
    enabled     BOOLEAN NOT NULL DEFAULT TRUE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TRIGGER trg_external_binding_updated_at
    BEFORE UPDATE ON minecraft.external_binding
    FOR EACH ROW EXECUTE FUNCTION ygg_touch_updated_at();

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP SCHEMA IF EXISTS minecraft CASCADE;
-- +goose StatementEnd

-- M1 基线迁移:oidc schema
--
-- 与 minecraft schema 的令牌、签名密钥完全隔离(ADR-004):
-- 类型不同、存储不同、签名密钥不同、issuer 不同。

-- +goose Up
-- +goose StatementBegin

CREATE SCHEMA IF NOT EXISTS oidc;

-- ---------------------------------------------------------------- 客户端

CREATE TABLE oidc.client (
    id                    UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    client_id             TEXT NOT NULL UNIQUE,
    -- 只存 argon2id hash,不可逆。NULL 表示公开客户端(PKCE 强制)
    client_secret_hash    TEXT,
    name                  TEXT NOT NULL,
    description           TEXT,
    -- PG 原生数组,不用 JSON 文本:支持 GIN 索引与 = ANY() 查询
    redirect_uris         TEXT[] NOT NULL,
    grant_types           TEXT[] NOT NULL,
    scopes                TEXT[] NOT NULL,
    require_pkce          BOOLEAN NOT NULL DEFAULT TRUE,
    access_token_ttl      INTERVAL NOT NULL DEFAULT INTERVAL '1 hour',
    refresh_token_ttl     INTERVAL NOT NULL DEFAULT INTERVAL '30 days',
    authorization_code_ttl INTERVAL NOT NULL DEFAULT INTERVAL '60 seconds',
    status                TEXT NOT NULL DEFAULT 'active'
                          CHECK (status IN ('active','disabled')),
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- 至少要有一个回调地址,否则等于开放重定向
    CONSTRAINT client_redirect_uris_not_empty CHECK (cardinality(redirect_uris) > 0)
);

CREATE INDEX idx_client_redirect_uris ON oidc.client USING GIN (redirect_uris);
CREATE INDEX idx_client_status ON oidc.client (status);

CREATE TRIGGER trg_client_updated_at
    BEFORE UPDATE ON oidc.client
    FOR EACH ROW EXECUTE FUNCTION ygg_touch_updated_at();

-- ---------------------------------------------------------------- 授权码
--
-- fosite 的会话数据以 JSONB 原样序列化存储(session / request 两列)。
-- 这样既保留「只存 hash」的边界(明文授权码不落库),又能完整还原
-- fosite 在换令牌阶段需要的原始请求。

CREATE TABLE oidc.authorization_code (
    id                    UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    code_hash             BYTEA NOT NULL UNIQUE,
    -- fosite 用来按请求参数反查会话的签名
    code_signature        BYTEA NOT NULL UNIQUE,
    client_id             TEXT NOT NULL REFERENCES oidc.client(client_id) ON DELETE CASCADE,
    account_id            UUID NOT NULL REFERENCES identity.account(id) ON DELETE CASCADE,
    redirect_uri          TEXT NOT NULL,
    scopes                TEXT[] NOT NULL,
    nonce                 TEXT,
    code_challenge        TEXT NOT NULL,
    code_challenge_method TEXT NOT NULL CHECK (code_challenge_method = 'S256'),
    session               JSONB NOT NULL,
    request               JSONB NOT NULL,
    expires_at            TIMESTAMPTZ NOT NULL,
    used_at               TIMESTAMPTZ,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_auth_code_client ON oidc.authorization_code (client_id);
CREATE INDEX idx_auth_code_expires ON oidc.authorization_code (expires_at);

-- ---------------------------------------------------------------- 令牌

CREATE TABLE oidc.access_token (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    token_hash    BYTEA NOT NULL UNIQUE,
    signature     BYTEA NOT NULL UNIQUE,
    client_id     TEXT NOT NULL REFERENCES oidc.client(client_id) ON DELETE CASCADE,
    account_id    UUID REFERENCES identity.account(id) ON DELETE CASCADE,
    scopes        TEXT[] NOT NULL,
    session       JSONB NOT NULL,
    request       JSONB NOT NULL,
    expires_at    TIMESTAMPTZ NOT NULL,
    revoked_at    TIMESTAMPTZ,
    revoked_reason TEXT,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- account_id 可空:client_credentials 流程没有用户主体
CREATE INDEX idx_access_token_account ON oidc.access_token (account_id, expires_at);
CREATE INDEX idx_access_token_client  ON oidc.access_token (client_id);
CREATE INDEX idx_access_token_expires ON oidc.access_token (expires_at)
    WHERE revoked_at IS NULL;

CREATE TABLE oidc.refresh_token (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    token_hash    BYTEA NOT NULL UNIQUE,
    signature     BYTEA NOT NULL UNIQUE,
    client_id     TEXT NOT NULL REFERENCES oidc.client(client_id) ON DELETE CASCADE,
    account_id    UUID NOT NULL REFERENCES identity.account(id) ON DELETE CASCADE,
    scopes        TEXT[] NOT NULL,
    -- 轮换链。刷新时旧令牌标记已用并签发新令牌;
    -- 若同一旧令牌再次出现即判定为泄露,吊销整条链。
    rotated_from  UUID REFERENCES oidc.refresh_token(id) ON DELETE SET NULL,
    session       JSONB NOT NULL,
    request       JSONB NOT NULL,
    expires_at    TIMESTAMPTZ NOT NULL,
    revoked_at    TIMESTAMPTZ,
    revoked_reason TEXT,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_refresh_token_account ON oidc.refresh_token (account_id, expires_at);
CREATE INDEX idx_refresh_token_rotated ON oidc.refresh_token (rotated_from)
    WHERE rotated_from IS NOT NULL;

-- ---------------------------------------------------------------- 用户同意

CREATE TABLE oidc.consent (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id  UUID NOT NULL REFERENCES identity.account(id) ON DELETE CASCADE,
    client_id   TEXT NOT NULL REFERENCES oidc.client(client_id) ON DELETE CASCADE,
    scopes      TEXT[] NOT NULL,
    session_id  TEXT NOT NULL DEFAULT '',
    granted_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- fosite 的 OIDC 会话快照(userinfo / id_token claims 的来源)
    session     JSONB NOT NULL,
    UNIQUE (account_id, client_id)
);

-- ---------------------------------------------------------------- 推送授权请求(PAR)
--
-- RFC 9126。客户端先把授权请求推到授权服务器,换取一个 request_uri,
-- 降低前端 URL 泄露授权参数的风险。

CREATE TABLE oidc.pushed_authorization_request (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    request_uri_hash BYTEA NOT NULL UNIQUE,
    client_id    TEXT NOT NULL REFERENCES oidc.client(client_id) ON DELETE CASCADE,
    account_id   UUID REFERENCES identity.account(id) ON DELETE CASCADE,
    request      JSONB NOT NULL,
    expires_at   TIMESTAMPTZ NOT NULL,
    used_at      TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_par_expires ON oidc.pushed_authorization_request (expires_at);

-- ---------------------------------------------------------------- 设备码流程(RFC 8628)
--
-- 无浏览器客户端(电视、命令行)用设备码换令牌。

CREATE TABLE oidc.device_code (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    device_code_hash    BYTEA NOT NULL UNIQUE,
    user_code_hash      BYTEA NOT NULL UNIQUE,
    client_id           TEXT NOT NULL REFERENCES oidc.client(client_id) ON DELETE CASCADE,
    account_id          UUID REFERENCES identity.account(id) ON DELETE CASCADE,
    scopes              TEXT[] NOT NULL,
    status              TEXT NOT NULL DEFAULT 'pending'
                        CHECK (status IN ('pending','approved','denied','consumed','expired')),
    session             JSONB NOT NULL,
    request             JSONB NOT NULL,
    expires_at          TIMESTAMPTZ NOT NULL,
    last_polled_at      TIMESTAMPTZ,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_device_code_client ON oidc.device_code (client_id, status);

-- ---------------------------------------------------------------- 签名密钥
--
-- 私钥用 KEY_MASTER_SECRET 做 AES-256-GCM 加密后入库。
-- 数据库泄露时攻击者拿不到可直接使用的私钥。

CREATE TABLE oidc.signing_key (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    kid             TEXT NOT NULL UNIQUE,              -- 'oidc-<random>'
    algo            TEXT NOT NULL DEFAULT 'RS256',
    public_jwk      JSONB NOT NULL,                    -- 明文,需要公开
    private_jwk_enc BYTEA NOT NULL,                   -- AES-256-GCM 加密
    status          TEXT NOT NULL DEFAULT 'active'
                    CHECK (status IN ('active','retired','revoked')),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    retired_at      TIMESTAMPTZ
);

-- 只允许一个 active 密钥对外签名;其余保留用于验签历史令牌
CREATE UNIQUE INDEX idx_signing_key_active ON oidc.signing_key (status)
    WHERE status = 'active';

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP SCHEMA IF EXISTS oidc CASCADE;
-- +goose StatementEnd
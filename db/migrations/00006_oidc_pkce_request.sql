-- M3 补充迁移:PKCE 请求会话表
--
-- fosite 的 PKCE handler 以 AuthorizeCodeSignature(code) 为键存取挑战值。
-- 授权码表以 sha256(code) 为键,两者是不同的键,所以需要独立存储。
--
-- 只在「无授权码」流程(PKCE 单独使用)才会没有对应授权码行。

-- +goose Up
-- +goose StatementBegin

CREATE TABLE oidc.pkce_request (
    signature         BYTEA PRIMARY KEY,
    challenge         TEXT        NOT NULL,
    challenge_method  TEXT        NOT NULL,
    session           JSONB       NOT NULL DEFAULT '{}'::jsonb,
    request           JSONB       NOT NULL DEFAULT '{}'::jsonb,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_pkce_request_created ON oidc.pkce_request (created_at);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS oidc.pkce_request;
-- +goose StatementEnd
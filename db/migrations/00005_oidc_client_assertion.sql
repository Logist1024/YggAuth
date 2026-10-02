-- M3 补充迁移:客户端断言 JTI 表
--
-- RFC 7523 的 client_assertion_type=JWT 需要防重放:同一个 jti 只能用一次。
-- fosite 的 ClientManager 接口要求我们提供这个存储,原基线里没有对应表。

-- +goose Up
-- +goose StatementBegin

CREATE TABLE oidc.client_assertion (
    jti        TEXT PRIMARY KEY,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_client_assertion_expires ON oidc.client_assertion (expires_at);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS oidc.client_assertion;
-- +goose StatementEnd
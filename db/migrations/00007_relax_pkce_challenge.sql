-- M3 修正:PKCE 挑战在授权码表上应当可空
--
-- 原设计(00002)假设 code_challenge / code_challenge_method 恒为非空,
-- 因为 PKCE 存放在授权码行里。但实际实现里 PKCE 有独立的存储
-- (oidc.pkce_request,见 00006),授权码表上的两列只是冗余快照,
-- 没有启用 PKCE 的客户端就会写空值并触发 CHECK 约束。
--
-- 放宽为可空是对现实行为的如实反映,不是绕过校验 ——
-- PKCE 该不该要求,由 OIDC_REQUIRE_PKCE 配置与 fosite 的 PKCE handler 决定。

-- +goose Up
-- +goose StatementBegin

ALTER TABLE oidc.authorization_code
    ALTER COLUMN code_challenge DROP NOT NULL,
    ALTER COLUMN code_challenge_method DROP NOT NULL;

ALTER TABLE oidc.authorization_code
    DROP CONSTRAINT IF EXISTS authorization_code_code_challenge_method_check;

ALTER TABLE oidc.authorization_code
    ADD CONSTRAINT authorization_code_code_challenge_method_check
    CHECK (code_challenge_method IS NULL OR code_challenge_method = 'S256');

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

ALTER TABLE oidc.authorization_code
    DROP CONSTRAINT IF EXISTS authorization_code_code_challenge_method_check;

ALTER TABLE oidc.authorization_code
    ADD CONSTRAINT authorization_code_code_challenge_method_check
    CHECK (code_challenge_method = 'S256');

ALTER TABLE oidc.authorization_code
    ALTER COLUMN code_challenge_method SET NOT NULL,
    ALTER COLUMN code_challenge SET NOT NULL;

-- +goose StatementEnd
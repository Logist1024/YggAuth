-- name: CreateClient :one
INSERT INTO oidc.client (
    client_id, client_secret_hash, name, description, redirect_uris,
    grant_types, scopes, require_pkce, access_token_ttl, refresh_token_ttl,
    authorization_code_ttl)
VALUES (
    sqlc.arg('client_id'),
    sqlc.narg('client_secret_hash')::text,
    sqlc.arg('name'),
    sqlc.narg('description')::text,
    sqlc.arg('redirect_uris')::text[],
    sqlc.arg('grant_types')::text[],
    sqlc.arg('scopes')::text[],
    sqlc.arg('require_pkce')::boolean,
    sqlc.arg('access_token_ttl')::interval,
    sqlc.arg('refresh_token_ttl')::interval,
    sqlc.arg('authorization_code_ttl')::interval)
RETURNING *;

-- name: GetClient :one
SELECT * FROM oidc.client WHERE client_id = $1;

-- name: ListClients :many
SELECT * FROM oidc.client
WHERE (sqlc.narg('status')::text IS NULL OR status = sqlc.narg('status'))
  AND (sqlc.narg('search')::text IS NULL
       OR client_id LIKE sqlc.narg('search') OR name LIKE sqlc.narg('search'))
ORDER BY created_at DESC
LIMIT sqlc.arg('limit') OFFSET sqlc.arg('offset');

-- name: CountClients :one
SELECT count(*) FROM oidc.client
WHERE (sqlc.narg('status')::text IS NULL OR status = sqlc.narg('status'))
  AND (sqlc.narg('search')::text IS NULL
       OR client_id LIKE sqlc.narg('search') OR name LIKE sqlc.narg('search'));

-- name: UpdateClient :one
UPDATE oidc.client
SET name = sqlc.arg('name'),
    description = sqlc.narg('description')::text,
    redirect_uris = sqlc.arg('redirect_uris')::text[],
    grant_types = sqlc.arg('grant_types')::text[],
    scopes = sqlc.arg('scopes')::text[],
    require_pkce = sqlc.arg('require_pkce')::boolean,
    access_token_ttl = sqlc.arg('access_token_ttl')::interval,
    refresh_token_ttl = sqlc.arg('refresh_token_ttl')::interval,
    authorization_code_ttl = sqlc.arg('authorization_code_ttl')::interval,
    status = sqlc.arg('status')::text
WHERE client_id = sqlc.arg('client_id')
RETURNING *;

-- name: UpdateClientSecret :one
-- 轮换密钥。旧密钥立即失效,属于有意为之的破坏性变更。
UPDATE oidc.client
SET client_secret_hash = sqlc.arg('client_secret_hash')::text
WHERE client_id = sqlc.arg('client_id')
RETURNING *;

-- name: DeleteClient :execrows
DELETE FROM oidc.client WHERE client_id = $1;

-- ---------------------------------------------------------------- 授权码

-- name: CreateAuthorizationCode :one
INSERT INTO oidc.authorization_code (
    code_hash, code_signature, client_id, account_id, redirect_uri, scopes,
    nonce, code_challenge, code_challenge_method, session, request, expires_at)
VALUES (
    sqlc.arg('code_hash')::bytea,
    sqlc.arg('code_signature')::bytea,
    sqlc.arg('client_id'),
    sqlc.arg('account_id'),
    sqlc.arg('redirect_uri'),
    sqlc.arg('scopes')::text[],
    sqlc.narg('nonce')::text,
    sqlc.narg('code_challenge')::text,
    sqlc.narg('code_challenge_method')::text,
    sqlc.arg('session')::jsonb,
    sqlc.arg('request')::jsonb,
    sqlc.arg('expires_at')::timestamptz)
RETURNING *;

-- name: GetAuthorizationCodeByHash :one
SELECT * FROM oidc.authorization_code WHERE code_hash = sqlc.arg('code_hash')::bytea;

-- name: InvalidateAuthorizationCode :execrows
-- 核销授权码。条件里带 used_at IS NULL,天然防重放:
-- 同一个码换第二次令牌会命中 0 行。
UPDATE oidc.authorization_code SET used_at = now()
WHERE code_hash = sqlc.arg('code_hash')::bytea AND used_at IS NULL;

-- name: DeleteExpiredAuthorizationCodes :execrows
DELETE FROM oidc.authorization_code
WHERE expires_at < now() - interval '1 day';

-- ---------------------------------------------------------------- 访问令牌

-- name: CreateAccessToken :one
INSERT INTO oidc.access_token (
    token_hash, signature, client_id, account_id, scopes, session, request, expires_at)
VALUES (
    sqlc.arg('token_hash')::bytea,
    sqlc.arg('signature')::bytea,
    sqlc.arg('client_id'),
    sqlc.narg('account_id')::uuid,
    sqlc.arg('scopes')::text[],
    sqlc.arg('session')::jsonb,
    sqlc.arg('request')::jsonb,
    sqlc.arg('expires_at')::timestamptz)
RETURNING *;

-- name: GetAccessTokenBySignature :one
SELECT * FROM oidc.access_token WHERE signature = sqlc.arg('signature')::bytea;

-- name: GetAccessTokenByHash :one
SELECT * FROM oidc.access_token WHERE token_hash = sqlc.arg('token_hash')::bytea;

-- name: RevokeAccessToken :execrows
UPDATE oidc.access_token
SET revoked_at = now(), revoked_reason = sqlc.arg('reason')::text
WHERE signature = sqlc.arg('signature')::bytea AND revoked_at IS NULL;

-- name: RevokeAccessTokenByHash :execrows
UPDATE oidc.access_token
SET revoked_at = now(), revoked_reason = sqlc.arg('reason')::text
WHERE token_hash = sqlc.arg('token_hash')::bytea AND revoked_at IS NULL;

-- name: RevokeAccessTokensByClient :execrows
UPDATE oidc.access_token
SET revoked_at = now(), revoked_reason = sqlc.arg('reason')::text
WHERE client_id = sqlc.arg('client_id') AND revoked_at IS NULL;

-- name: RevokeAccessTokensByAccount :execrows
UPDATE oidc.access_token
SET revoked_at = now(), revoked_reason = sqlc.arg('reason')::text
WHERE account_id = sqlc.arg('account_id')::uuid AND revoked_at IS NULL;

-- name: DeleteExpiredAccessTokens :execrows
DELETE FROM oidc.access_token
WHERE expires_at < now() - interval '1 day';

-- ---------------------------------------------------------------- 刷新令牌

-- name: CreateRefreshToken :one
INSERT INTO oidc.refresh_token (
    token_hash, signature, client_id, account_id, scopes, rotated_from,
    session, request, expires_at)
VALUES (
    sqlc.arg('token_hash')::bytea,
    sqlc.arg('signature')::bytea,
    sqlc.arg('client_id'),
    sqlc.arg('account_id'),
    sqlc.arg('scopes')::text[],
    sqlc.narg('rotated_from')::uuid,
    sqlc.arg('session')::jsonb,
    sqlc.arg('request')::jsonb,
    sqlc.arg('expires_at')::timestamptz)
RETURNING *;

-- name: GetRefreshTokenBySignature :one
SELECT * FROM oidc.refresh_token WHERE signature = sqlc.arg('signature')::bytea;

-- name: GetRefreshTokenByHash :one
SELECT * FROM oidc.refresh_token WHERE token_hash = sqlc.arg('token_hash')::bytea;

-- name: RevokeRefreshTokenBySignature :execrows
UPDATE oidc.refresh_token
SET revoked_at = now(), revoked_reason = sqlc.arg('reason')::text
WHERE signature = sqlc.arg('signature')::bytea AND revoked_at IS NULL;

-- name: RevokeRefreshTokenChain :execrows
-- 检测到重放时吊销整条轮换链:沿着 rotated_from 往上把祖先全部作废。
WITH RECURSIVE chain AS (
    SELECT id FROM oidc.refresh_token WHERE signature = sqlc.arg('signature')::bytea
    UNION ALL
    SELECT rt.id FROM oidc.refresh_token rt
    JOIN chain c ON rt.rotated_from = c.id
)
UPDATE oidc.refresh_token
SET revoked_at = now(), revoked_reason = sqlc.arg('reason')::text
WHERE id IN (SELECT id FROM chain) AND revoked_at IS NULL;

-- name: RevokeRefreshTokensByAccount :execrows
UPDATE oidc.refresh_token
SET revoked_at = now(), revoked_reason = sqlc.arg('reason')::text
WHERE account_id = sqlc.arg('account_id')::uuid AND revoked_at IS NULL;

-- name: RevokeRefreshTokensByClient :execrows
UPDATE oidc.refresh_token
SET revoked_at = now(), revoked_reason = sqlc.arg('reason')::text
WHERE client_id = sqlc.arg('client_id') AND revoked_at IS NULL;

-- name: DeleteExpiredRefreshTokens :execrows
DELETE FROM oidc.refresh_token
WHERE expires_at < now() - interval '1 day';

-- ---------------------------------------------------------------- 同意记录

-- name: UpsertConsent :one
INSERT INTO oidc.consent (account_id, client_id, scopes, session_id, session)
VALUES (
    sqlc.arg('account_id'),
    sqlc.arg('client_id'),
    sqlc.arg('scopes')::text[],
    sqlc.arg('session_id'),
    sqlc.arg('session')::jsonb)
ON CONFLICT (account_id, client_id) DO UPDATE
SET scopes = EXCLUDED.scopes,
    session_id = EXCLUDED.session_id,
    session = EXCLUDED.session,
    granted_at = now()
RETURNING *;

-- name: GetConsent :one
SELECT * FROM oidc.consent WHERE account_id = $1 AND client_id = $2;

-- name: DeleteConsent :execrows
DELETE FROM oidc.consent WHERE account_id = $1 AND client_id = $2;

-- ---------------------------------------------------------------- PAR(RFC 9126)

-- name: CreatePAR :one
INSERT INTO oidc.pushed_authorization_request (
    request_uri_hash, client_id, account_id, request, expires_at)
VALUES (
    sqlc.arg('request_uri_hash')::bytea,
    sqlc.arg('client_id'),
    sqlc.narg('account_id')::uuid,
    sqlc.arg('request')::jsonb,
    sqlc.arg('expires_at')::timestamptz)
RETURNING *;

-- name: GetPAR :one
SELECT * FROM oidc.pushed_authorization_request
WHERE request_uri_hash = sqlc.arg('request_uri_hash')::bytea
  AND used_at IS NULL
  AND expires_at > now();

-- name: ConsumePAR :execrows
-- 一次性消费:PAR 的 request_uri 用掉即作废。
UPDATE oidc.pushed_authorization_request SET used_at = now()
WHERE request_uri_hash = sqlc.arg('request_uri_hash')::bytea AND used_at IS NULL;

-- name: DeleteExpiredPAR :execrows
DELETE FROM oidc.pushed_authorization_request
WHERE expires_at < now() - interval '1 day';

-- ---------------------------------------------------------------- 设备码(RFC 8628)

-- name: CreateDeviceCode :one
INSERT INTO oidc.device_code (
    device_code_hash, user_code_hash, client_id, account_id, scopes,
    session, request, expires_at)
VALUES (
    sqlc.arg('device_code_hash')::bytea,
    sqlc.arg('user_code_hash')::bytea,
    sqlc.arg('client_id'),
    sqlc.narg('account_id')::uuid,
    sqlc.arg('scopes')::text[],
    sqlc.arg('session')::jsonb,
    sqlc.arg('request')::jsonb,
    sqlc.arg('expires_at')::timestamptz)
RETURNING *;

-- name: GetDeviceCodeByDeviceHash :one
SELECT * FROM oidc.device_code WHERE device_code_hash = sqlc.arg('device_code_hash')::bytea;

-- name: GetDeviceCodeByUserHash :one
SELECT * FROM oidc.device_code WHERE user_code_hash = sqlc.arg('user_code_hash')::bytea;

-- name: ApproveDeviceCode :one
-- 用户在浏览器端批准。approved 之前轮询令牌一律返回 authorization_pending。
UPDATE oidc.device_code
SET status = 'approved',
    account_id = sqlc.arg('account_id'),
    session = sqlc.arg('session')::jsonb,
    request = sqlc.arg('request')::jsonb
WHERE id = sqlc.arg('id') AND status = 'pending'
RETURNING *;

-- name: DenyDeviceCode :execrows
UPDATE oidc.device_code SET status = 'denied'
WHERE id = sqlc.arg('id') AND status = 'pending';

-- name: TouchDeviceCodePoll :exec
-- 记录最近一次轮询,用于检测过于频繁的轮询。
UPDATE oidc.device_code SET last_polled_at = now() WHERE id = sqlc.arg('id');

-- name: ConsumeDeviceCode :execrows
UPDATE oidc.device_code SET status = 'consumed' WHERE id = sqlc.arg('id');

-- name: DeleteExpiredDeviceCodes :execrows
DELETE FROM oidc.device_code WHERE expires_at < now() - interval '1 day';

-- ---------------------------------------------------------------- 客户端断言 JWT

-- name: CreateClientAssertion :one
-- 防重放:同一个 jti 只允许用一次。
INSERT INTO oidc.client_assertion (jti, expires_at)
VALUES (sqlc.arg('jti'), sqlc.arg('expires_at')::timestamptz)
ON CONFLICT (jti) DO NOTHING
RETURNING jti;

-- name: DeleteExpiredClientAssertions :execrows
DELETE FROM oidc.client_assertion WHERE expires_at < now() - interval '1 hour';

-- ---------------------------------------------------------------- 签名密钥

-- name: GetActiveSigningKey :one
SELECT * FROM oidc.signing_key WHERE status = 'active';

-- name: GetSigningKey :one
SELECT * FROM oidc.signing_key WHERE kid = $1;

-- name: ListSigningKeys :many
SELECT * FROM oidc.signing_key ORDER BY created_at DESC;

-- name: RetireActiveSigningKey :exec
-- 轮换第一步:把当前 active 密钥降级为 retired。
--
-- 旧密钥**不删除** —— 已经发出的令牌还在用它签名,删掉会让存量令牌
-- 立刻验签失败。它继续留在 JWKS 里直到被显式 revoke。
UPDATE oidc.signing_key
SET status = 'retired', retired_at = now()
WHERE status = 'active';

-- name: CreateSigningKey :one
-- 轮换第二步:插入新的 active 密钥。
--
-- 必须与上一步分成两条语句:idx_signing_key_active 是 status 列上的
-- 唯一索引,而同一条语句里的数据修改 CTE 对该索引的检查不可见 ——
-- 合成一条会稳定撞上 duplicate key。
INSERT INTO oidc.signing_key (kid, algo, public_jwk, private_jwk_enc, status)
VALUES (sqlc.arg('kid'), sqlc.arg('algo'), sqlc.arg('public_jwk')::jsonb,
        sqlc.arg('private_jwk_enc')::bytea, 'active')
RETURNING *;
-- ---------------------------------------------------------------- PKCE 会话

-- name: CreatePKCERequest :one
INSERT INTO oidc.pkce_request (signature, challenge, challenge_method, session, request)
VALUES (
    sqlc.arg('signature')::bytea,
    sqlc.arg('challenge'),
    sqlc.arg('challenge_method'),
    sqlc.arg('session')::jsonb,
    sqlc.arg('request')::jsonb)
RETURNING *;

-- name: GetPKCERequest :one
SELECT * FROM oidc.pkce_request WHERE signature = sqlc.arg('signature')::bytea;

-- name: DeletePKCERequest :execrows
DELETE FROM oidc.pkce_request WHERE signature = sqlc.arg('signature')::bytea;

-- name: DeleteExpiredPKCERequests :execrows
DELETE FROM oidc.pkce_request WHERE created_at < now() - interval '1 day';

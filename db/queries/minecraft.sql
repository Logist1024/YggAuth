-- name: GetProfileByAccount :one
SELECT * FROM minecraft.profile WHERE account_id = $1;

-- name: GetProfileByUUID :one
SELECT * FROM minecraft.profile WHERE uuid = $1;

-- name: GetProfileByName :one
SELECT * FROM minecraft.profile WHERE current_name = $1;

-- name: ListProfilesByAccount :many
SELECT * FROM minecraft.profile WHERE account_id = sqlc.arg('account_id');

-- name: CreateProfile :one
INSERT INTO minecraft.profile (account_id, uuid, current_name)
VALUES (sqlc.arg('account_id'), sqlc.arg('uuid'), sqlc.arg('current_name'))
RETURNING *;

-- name: RenameProfile :one
-- 改名。current_name 上的唯一索引会挡住重名 —— 这是最后一道防线,
-- 上层的前置检查只是为了给出可读的错误信息。
UPDATE minecraft.profile
SET current_name = sqlc.arg('current_name'), updated_at = now()
WHERE id = sqlc.arg('id')
RETURNING *;

-- name: GetMCLoginEnabled :one
SELECT mc_login_enabled FROM identity.account WHERE id = $1;

-- ---------------------------------------------------------------- 改名历史

-- name: InsertNameHistory :one
INSERT INTO minecraft.name_history (profile_id, name, reusable_at)
VALUES (sqlc.arg('profile_id'), sqlc.arg('name'), sqlc.narg('reusable_at')::timestamptz)
RETURNING *;

-- name: GetNameHistory :many
SELECT * FROM minecraft.name_history
WHERE profile_id = sqlc.arg('profile_id')
ORDER BY changed_at DESC;

-- name: GetReusableNameConflict :one
-- 查这个名字是否还在保留期内。reusable_at IS NULL 表示永久保留。
SELECT * FROM minecraft.name_history
WHERE lower(name) = lower(sqlc.arg('name'))
  AND (reusable_at IS NULL OR reusable_at > now())
ORDER BY changed_at DESC
LIMIT 1;

-- name: ExtendNameRetention :exec
-- 把仍然有效期的旧名延长到新的保留期。改名链上可能有多个旧名,
-- 逐个延长才能保证「上一次改名腾出的名字」也受同一条规则约束。
UPDATE minecraft.name_history
SET reusable_at = sqlc.arg('reusable_at')::timestamptz
WHERE profile_id = sqlc.arg('profile_id')
  AND (reusable_at IS NULL OR reusable_at < sqlc.arg('reusable_at')::timestamptz);

-- ---------------------------------------------------------------- 访问令牌

-- name: CreateMCAccessToken :one
INSERT INTO minecraft.access_token (token_hash, profile_id, client_token, expires_at)
VALUES (sqlc.arg('token_hash')::bytea, sqlc.arg('profile_id'),
        sqlc.narg('client_token')::text, sqlc.arg('expires_at')::timestamptz)
RETURNING *;

-- name: GetMCAccessToken :one
SELECT * FROM minecraft.access_token WHERE token_hash = sqlc.arg('token_hash')::bytea;

-- name: GetMCAccessTokenWithProfile :one
-- 带 profile 联查:MC 端点的每一次令牌校验都要这个组合,
-- 分两次查等于给并发刷新留了个竞态窗口。
SELECT t.id, t.profile_id, t.client_token, t.expires_at, t.revoked_at, t.revoked_reason,
       t.created_at,
       p.uuid, p.current_name, p.account_id
FROM minecraft.access_token t
JOIN minecraft.profile p ON p.id = t.profile_id
WHERE t.token_hash = sqlc.arg('token_hash')::bytea;

-- name: RotateMCAccessToken :one
-- 刷新:吊销旧令牌并签发新的一条,在同一条语句里完成。
--
-- 分成两步的话,中间崩溃会留下「旧令牌已废、新令牌没发」的空窗,
-- 玩家会被直接踢下线。
WITH old AS (
    UPDATE minecraft.access_token
    SET revoked_at = now(), revoked_reason = 'refreshed'
    WHERE token_hash = sqlc.arg('old_hash')::bytea AND revoked_at IS NULL
    RETURNING profile_id, client_token
)
INSERT INTO minecraft.access_token (token_hash, profile_id, client_token, expires_at)
SELECT sqlc.arg('new_hash')::bytea, old.profile_id, old.client_token,
       sqlc.arg('expires_at')::timestamptz
FROM old
RETURNING *;

-- name: RevokeMCAccessToken :execrows
UPDATE minecraft.access_token
SET revoked_at = now(), revoked_reason = sqlc.arg('reason')::text
WHERE token_hash = sqlc.arg('token_hash')::bytea AND revoked_at IS NULL;

-- name: RevokeMCAccessTokensByProfile :execrows
UPDATE minecraft.access_token
SET revoked_at = now(), revoked_reason = sqlc.arg('reason')::text
WHERE profile_id = sqlc.arg('profile_id') AND revoked_at IS NULL;

-- name: DeleteExpiredMCAccessTokens :execrows
DELETE FROM minecraft.access_token
WHERE expires_at < now() - interval '1 day';

-- ---------------------------------------------------------------- 进服会话

-- name: CreateServerSession :one
INSERT INTO minecraft.server_session (profile_id, server_id, ip)
VALUES (sqlc.arg('profile_id'), sqlc.arg('server_id'), sqlc.narg('ip')::inet)
RETURNING *;

-- name: GetServerSession :one
SELECT * FROM minecraft.server_session WHERE server_id = sqlc.arg('server_id');

-- name: MarkServerSessionVerified :execrows
-- 防重放:同一 serverId 只允许成功核销一次。
-- 条件里带 verified_at IS NULL,第二次 hasJoined 命中 0 行。
UPDATE minecraft.server_session SET verified_at = now()
WHERE server_id = sqlc.arg('server_id') AND verified_at IS NULL;

-- name: DeleteExpiredServerSessions :execrows
DELETE FROM minecraft.server_session
WHERE joined_at < now() - sqlc.arg('window')::interval;

-- ---------------------------------------------------------------- MC 服务器

-- name: GetMCServer :one
SELECT * FROM minecraft.server WHERE server_id = $1;

-- name: ListMCServers :many
SELECT * FROM minecraft.server ORDER BY created_at;

-- name: CreateMCServer :one
INSERT INTO minecraft.server (server_id, name, shared_secret, enabled)
VALUES (sqlc.arg('server_id'), sqlc.arg('name'), sqlc.arg('shared_secret'),
        sqlc.arg('enabled')::boolean)
RETURNING *;

-- name: UpdateMCServer :one
UPDATE minecraft.server
SET name = sqlc.arg('name'), shared_secret = sqlc.arg('shared_secret'),
    enabled = sqlc.arg('enabled')::boolean, updated_at = now()
WHERE server_id = sqlc.arg('server_id')
RETURNING *;

-- name: DeleteMCServer :execrows
DELETE FROM minecraft.server WHERE server_id = $1;

-- ---------------------------------------------------------------- 签名密钥

-- name: GetActiveMCSigningKey :one
SELECT * FROM minecraft.signing_key WHERE status = 'active';

-- name: GetMCSigningKey :one
SELECT * FROM minecraft.signing_key WHERE kid = $1;

-- name: ListMCSigningKeys :many
SELECT * FROM minecraft.signing_key ORDER BY created_at DESC;

-- name: RetireActiveMCSigningKey :exec
-- 与 oidc 侧同样的理由:同一语句里改 CTE 再往带唯一索引的列插值,
-- PostgreSQL 看不见 CTE 的效果,必然撞 duplicate key。拆成两条。
UPDATE minecraft.signing_key
SET status = 'retired', retired_at = now()
WHERE status = 'active';

-- name: CreateMCSigningKey :one
INSERT INTO minecraft.signing_key (kid, algo, public_jwk, private_jwk_enc, status)
VALUES (sqlc.arg('kid'), sqlc.arg('algo'), sqlc.arg('public_jwk')::jsonb,
        sqlc.arg('private_jwk_enc')::bytea, 'active')
RETURNING *;

-- name: ListPendingServerSessions :many
-- 列出某玩家尚未核销、且仍在时间窗内的进服会话。
--
-- hasJoined 收到的是**签名**(serverIdHash),不是原始 serverId,
-- 要重算就得把该玩家的候选会话逐个试一遍。因此候选集必须小 ——
-- 时间窗和「一次核销」两条约束共同把它压到个位数。
SELECT * FROM minecraft.server_session
WHERE profile_id = sqlc.arg('profile_id')
  AND verified_at IS NULL
  AND joined_at > now() - sqlc.arg('window')::interval
ORDER BY joined_at DESC
LIMIT 20;

-- name: ListPendingServerSessionsBefore :many
-- 同上,但用**调用方算好的截止时间**而不是数据库的 now()。
--
-- 时间窗的判定必须用应用时钟:服务注入的是可替换的 Clock,
-- 测试要靠推进它来验证过期行为;而数据库的 now() 推不动。
-- 顺带消除了「数据库时钟与进程时钟漂移」带来的判定不一致。
SELECT * FROM minecraft.server_session
WHERE profile_id = sqlc.arg('profile_id')
  AND verified_at IS NULL
  AND joined_at >= sqlc.arg('cutoff')::timestamptz
ORDER BY joined_at DESC
LIMIT 20;

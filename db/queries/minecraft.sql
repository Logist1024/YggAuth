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

-- ---------------------------------------------------------------- 纹理

-- name: GetTextureByHash :one
SELECT * FROM minecraft.texture WHERE hash = sqlc.arg('hash');

-- name: CreateTexture :one
INSERT INTO minecraft.texture (hash, type, size, mime, width, height)
VALUES (sqlc.arg('hash'), sqlc.arg('type')::text, sqlc.arg('size')::int,
        sqlc.arg('mime')::text, sqlc.arg('width')::int, sqlc.arg('height')::int)
RETURNING *;

-- name: AddTextureReference :exec
UPDATE minecraft.texture SET ref_count = ref_count + 1 WHERE hash = sqlc.arg('hash');

-- name: DropTextureReference :execrows
UPDATE minecraft.texture SET ref_count = greatest(ref_count - 1, 0) WHERE hash = sqlc.arg('hash');

-- name: TouchTexture :exec
UPDATE minecraft.texture SET last_accessed_at = now() WHERE hash = sqlc.arg('hash');

-- name: ListGarbageTextures :many
-- 回收候选:引用计数归零且超过保留期的纹理。
SELECT * FROM minecraft.texture
WHERE ref_count = 0 AND last_accessed_at < now() - sqlc.arg('grace')::interval
LIMIT sqlc.arg('batch')::int;

-- name: DeleteTexture :execrows
DELETE FROM minecraft.texture WHERE hash = sqlc.arg('hash');

-- ---------------------------------------------------------------- 材质绑定

-- name: GetProfileTexture :one
SELECT t.*, pt.type AS bound_type, pt.profile_id AS bound_profile
FROM minecraft.profile_texture pt
JOIN minecraft.texture t ON t.id = pt.texture_id
WHERE pt.profile_id = sqlc.arg('profile_id') AND pt.type = sqlc.arg('type')::text;

-- name: ListProfileTextures :many
SELECT * FROM minecraft.profile_texture WHERE profile_id = sqlc.arg('profile_id');

-- name: UpsertProfileTexture :exec
INSERT INTO minecraft.profile_texture (profile_id, texture_id, type)
VALUES (sqlc.arg('profile_id'), sqlc.arg('texture_id'), sqlc.arg('type')::text)
ON CONFLICT (profile_id, type) DO UPDATE
SET texture_id = EXCLUDED.texture_id, updated_at = now()
RETURNING texture_id;

-- name: DeleteProfileTexture :execrows
DELETE FROM minecraft.profile_texture
WHERE profile_id = sqlc.arg('profile_id') AND type = sqlc.arg('type')::text;

-- ---------------------------------------------------------------- 头像缓存

-- name: GetAvatar :one
SELECT * FROM minecraft.avatar WHERE profile_id = sqlc.arg('profile_id');

-- name: UpsertAvatar :exec
-- 表里**不存图像**,只存「这张头像是从哪份皮肤渲染出来的」。
--
-- 图像本身在 storage 的 avatars/<profile_uuid>.png。这样分是因为
-- 图像是二进制大对象,进数据库会让一次 SELECT 拖上几 MB;
-- 而这里需要的只是「缓存是否失效」这一个判断。
INSERT INTO minecraft.avatar (profile_id, hash)
VALUES (sqlc.arg('profile_id'), sqlc.arg('hash')::text)
ON CONFLICT (profile_id) DO UPDATE
SET hash = EXCLUDED.hash, rendered_at = now();

-- name: DeleteAvatar :execrows
DELETE FROM minecraft.avatar WHERE profile_id = sqlc.arg('profile_id');

-- name: GetExternalBinding :one
SELECT * FROM minecraft.external_binding WHERE profile_id = sqlc.arg('profile_id');

-- name: UpsertExternalBinding :exec
-- 一个 profile 只绑定一个外部站(profile_id 上有唯一约束),
-- 所以按 profile_id 冲突消解,而不是按 (profile_id, kind)。
--
-- base_url 存进数据而不是只读配置:玩家指向自建镜像时不需要重启服务。
INSERT INTO minecraft.external_binding (profile_id, provider, external_user_id, base_url, enabled)
VALUES (sqlc.arg('profile_id'), sqlc.arg('provider')::text, sqlc.arg('external_user_id')::text,
        sqlc.arg('base_url')::text, sqlc.arg('enabled')::boolean)
ON CONFLICT (profile_id) DO UPDATE
SET provider = EXCLUDED.provider, external_user_id = EXCLUDED.external_user_id,
    base_url = EXCLUDED.base_url, enabled = EXCLUDED.enabled, updated_at = now();

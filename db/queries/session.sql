-- name: CreateSession :one
-- 只存 sha256(token),不存明文。
INSERT INTO identity.session (
    account_id, token_hash, sso_session_id, expires_at, idle_expires_at, ip, user_agent)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: GetActiveSessionByTokenHash :one
-- 会话校验是最高频查询:只看未吊销、未过期的行。
SELECT * FROM identity.session
WHERE token_hash = $1
  AND revoked_at IS NULL
  AND idle_expires_at > now()
  AND expires_at > now();

-- name: GetSessionByID :one
SELECT * FROM identity.session WHERE id = $1;

-- name: TouchSession :one
-- 滑动过期:只推进 idle_expires_at,绝不改 expires_at(绝对过期不因活跃而延长)。
UPDATE identity.session SET last_seen_at = now(), idle_expires_at = $2
WHERE id = $1 AND revoked_at IS NULL
RETURNING *;

-- name: RevokeSession :one
UPDATE identity.session
SET revoked_at = now(), revoke_reason = $2
WHERE id = $1 AND revoked_at IS NULL
RETURNING *;

-- name: RevokeAllSessions :execrows
-- 改密码后调用:吊销该账号全部会话。
UPDATE identity.session SET revoked_at = now(), revoke_reason = $2
WHERE account_id = $1 AND revoked_at IS NULL;

-- name: ListActiveSessions :many
SELECT * FROM identity.session
WHERE account_id = $1
  AND revoked_at IS NULL
  AND idle_expires_at > now()
  AND expires_at > now()
ORDER BY last_seen_at DESC;

-- name: CountActiveSessions :one
SELECT count(*) FROM identity.session
WHERE account_id = $1 AND revoked_at IS NULL
  AND idle_expires_at > now() AND expires_at > now();

-- name: LinkSessionToSSO :exec
UPDATE identity.session SET sso_session_id = $2 WHERE id = $1;

-- name: SetSessionMustChangePassword :exec
-- 把凭据上的 must_change 真源投到本次会话上,认证中间件读这一列即可,
-- 不必为判断强制改密在每个请求上多查一次凭据(见迁移 00008 的注释)。
UPDATE identity.session SET must_change_password = $2 WHERE id = $1;

-- name: GetSessionBySSOID :many
-- 全局登出:把同一 SSO 会话下的所有终端用户会话一起吊销。
SELECT * FROM identity.session
WHERE sso_session_id = $1 AND revoked_at IS NULL;

-- name: DeleteExpiredSessions :execrows
-- 后台清理任务用:删除已过期或已吊销超过 30 天的会话。
DELETE FROM identity.session
WHERE (expires_at < now() - interval '30 days')
   OR (revoked_at IS NOT NULL AND revoked_at < now() - interval '30 days');
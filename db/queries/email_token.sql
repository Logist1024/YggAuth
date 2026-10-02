-- name: CreateEmailToken :one
INSERT INTO identity.email_token (account_id, token_hash, purpose, expires_at)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: GetEmailTokenByHash :one
-- 只取未使用且未过期的。校验和标记使用在同一个事务里完成。
SELECT * FROM identity.email_token
WHERE token_hash = $1
  AND used_at IS NULL
  AND expires_at > now();

-- name: ConsumeEmailToken :one
-- 一次性使用:条件里带 used_at IS NULL,天然防重放。
UPDATE identity.email_token SET used_at = now()
WHERE token_hash = $1 AND used_at IS NULL AND expires_at > now()
RETURNING *;

-- name: InvalidateEmailTokens :execrows
-- 重发验证邮件时作废同用途的旧令牌。
UPDATE identity.email_token SET used_at = now()
WHERE account_id = $1 AND purpose = $2 AND used_at IS NULL;

-- name: GetLastEmailToken :one
-- 邮件重发冷却:看最近一次发信时间。
SELECT * FROM identity.email_token
WHERE account_id = $1 AND purpose = $2
ORDER BY created_at DESC
LIMIT 1;

-- name: CountEmailTokensSince :one
-- 邮件每日发送上限(5 次/天)。
SELECT count(*) FROM identity.email_token
WHERE account_id = $1 AND purpose = $2 AND created_at > $3;

-- name: DeleteExpiredEmailTokens :execrows
DELETE FROM identity.email_token WHERE expires_at < now() - interval '7 days';
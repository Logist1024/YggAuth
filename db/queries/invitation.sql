-- name: CreateInvitation :one
INSERT INTO identity.invitation (code, email, max_uses, expires_at, created_by)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: GetInvitationByCode :one
SELECT * FROM identity.invitation
WHERE code = $1 AND revoked_at IS NULL AND expires_at > now();

-- name: ConsumeInvitation :one
-- 核销邀请码。used_count 的上限判断放在条件里,避免读改写竞态。
-- $2 是可选的邮箱绑定:邀请码绑定了邮箱时,只有该邮箱能使用。
UPDATE identity.invitation
SET used_count = used_count + 1
WHERE code = $1
  AND revoked_at IS NULL
  AND expires_at > now()
  AND used_count < max_uses
  AND (sqlc.narg('email')::text IS NULL OR email IS NULL OR lower(email) = lower(sqlc.narg('email')))
RETURNING *;

-- name: ListInvitations :many
SELECT i.*, a.username AS created_by_username
FROM identity.invitation i
LEFT JOIN identity.account a ON a.id = i.created_by
WHERE ($1::text IS NULL OR i.code LIKE $1)
ORDER BY i.created_at DESC
LIMIT $2 OFFSET $3;

-- name: RevokeInvitation :execrows
UPDATE identity.invitation SET revoked_at = now()
WHERE id = $1 AND revoked_at IS NULL;

-- name: DeleteExpiredInvitations :execrows
DELETE FROM identity.invitation WHERE expires_at < now() - interval '30 days';
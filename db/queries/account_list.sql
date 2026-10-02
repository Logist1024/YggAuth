-- name: ListAccounts :many
-- 搜索同时匹配用户名与邮箱;status 为 NULL 时不过滤状态。
SELECT * FROM identity.account
WHERE (sqlc.narg('status')::text IS NULL OR status = sqlc.narg('status'))
  AND (sqlc.narg('search')::text IS NULL
       OR username_lower LIKE sqlc.narg('search') OR lower(email) LIKE sqlc.narg('search'))
ORDER BY created_at DESC
LIMIT sqlc.arg('limit') OFFSET sqlc.arg('offset');

-- name: CountAccounts :one
SELECT count(*) FROM identity.account
WHERE (sqlc.narg('status')::text IS NULL OR status = sqlc.narg('status'))
  AND (sqlc.narg('search')::text IS NULL
       OR username_lower LIKE sqlc.narg('search') OR lower(email) LIKE sqlc.narg('search'));
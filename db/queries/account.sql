-- name: CreateAccount :one
INSERT INTO identity.account (username, username_lower, email, status, mc_login_enabled)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: GetAccountByID :one
SELECT * FROM identity.account WHERE id = $1;

-- name: GetAccountByEmail :one
-- 邮箱唯一性按 lower(email) 判断(变更 C-10),登录/找回密码都走这里。
SELECT * FROM identity.account WHERE lower(email) = lower($1);

-- name: GetAccountByUsername :one
SELECT * FROM identity.account WHERE username_lower = $1;



-- name: UpdateAccountProfile :one
-- 只改展示层字段。username_lower 由服务层保证同步更新。
UPDATE identity.account
SET username = $2, username_lower = $3, email = $4
WHERE id = $1
RETURNING *;

-- name: UpdateAccountStatus :one
UPDATE identity.account SET status = $2 WHERE id = $1 RETURNING *;

-- name: SetAccountEmailVerified :one
UPDATE identity.account
SET email_verified_at = now(), status = 'active'
WHERE id = $1
RETURNING *;

-- name: SetMCLoginEnabled :one
-- mc_login_enabled 是通用布尔开关,内核不理解其业务语义(见迁移注释)。
UPDATE identity.account SET mc_login_enabled = $2 WHERE id = $1 RETURNING *;

-- name: DeleteAccount :execrows
DELETE FROM identity.account WHERE id = $1;
-- name: ListPermissions :many
SELECT * FROM identity.permission ORDER BY code;

-- name: GetPermission :one
SELECT * FROM identity.permission WHERE code = $1;

-- name: CreateRole :one
INSERT INTO identity.role (code, name, description, is_system)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: GetRoleByID :one
SELECT * FROM identity.role WHERE id = $1;

-- name: GetRoleByCode :one
SELECT * FROM identity.role WHERE code = $1;

-- name: ListRoles :many
SELECT * FROM identity.role
WHERE (sqlc.narg('search')::text IS NULL OR code LIKE sqlc.narg('search') OR name LIKE sqlc.narg('search'))
ORDER BY is_system DESC, code;

-- name: UpdateRole :one
-- is_system 的内置角色不允许改名改 code,避免破坏依赖角色 code 的脚本。
UPDATE identity.role
SET name = $2, description = $3
WHERE id = $1 AND is_system = FALSE
RETURNING *;

-- name: DeleteRole :execrows
-- 内置角色不可删。返回 0 行表示「角色不存在」或「是内置角色」,由服务层区分。
DELETE FROM identity.role WHERE id = $1 AND is_system = FALSE;

-- name: SetRolePermissions :exec
-- 授权前先清空再写入,整体替换语义。
DELETE FROM identity.role_permission WHERE role_id = $1;

-- name: AddRolePermission :exec
INSERT INTO identity.role_permission (role_id, permission)
VALUES ($1, $2)
ON CONFLICT DO NOTHING;

-- name: ListRolePermissions :many
SELECT p.* FROM identity.role_permission rp
JOIN identity.permission p ON p.code = rp.permission
WHERE rp.role_id = $1
ORDER BY p.code;

-- name: GrantRole :one
INSERT INTO identity.account_role (account_id, role_id, granted_by)
VALUES ($1, $2, $3)
ON CONFLICT (account_id, role_id) DO UPDATE SET granted_by = EXCLUDED.granted_by
RETURNING *;

-- name: RevokeRole :execrows
DELETE FROM identity.account_role WHERE account_id = $1 AND role_id = $2;

-- name: ListAccountRoles :many
SELECT r.* FROM identity.account_role ar
JOIN identity.role r ON r.id = ar.role_id
WHERE ar.account_id = $1
ORDER BY r.code;

-- name: ListRoleAccounts :many
SELECT a.* FROM identity.account_role ar
JOIN identity.account a ON a.id = ar.account_id
WHERE ar.role_id = $1
ORDER BY a.created_at DESC;

-- name: ListAccountPermissions :many
-- 权限求值的唯一入口:一次查完账号经角色持有的全部权限点。
-- 含通配符展开(如 mc:* → 匹配所有 minecraft:* 权限点)。
SELECT DISTINCT p.code FROM identity.account_role ar
JOIN identity.role_permission rp ON rp.role_id = ar.role_id
JOIN identity.permission p ON p.code = rp.permission
WHERE ar.account_id = $1
ORDER BY p.code;

-- name: HasPermission :one
SELECT EXISTS (
    SELECT 1 FROM identity.account_role ar
    JOIN identity.role_permission rp ON rp.role_id = ar.role_id
    WHERE ar.account_id = $1 AND rp.permission = $2
);

-- name: CountAccountsWithRole :one
SELECT count(*) FROM identity.account_role WHERE role_id = $1;
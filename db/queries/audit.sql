-- name: InsertAuditEvent :one
-- 审计表只追加,应用层没有任何删除接口(见 docs/09-security.md 9.2)。
INSERT INTO identity.audit_event (
    account_id, actor, action, target_type, target_id, outcome, ip, user_agent, metadata)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING *;

-- name: SearchAuditEvents :many
-- 后台审计检索。account_id 为 NULL 时不按账号过滤。
SELECT * FROM identity.audit_event
WHERE ($1::uuid IS NULL OR account_id = $1)
  AND ($2::text IS NULL OR action LIKE $2)
  AND ($3::text IS NULL OR outcome = $3)
  AND ($4::timestamptz IS NULL OR occurred_at >= $4)
  AND ($5::timestamptz IS NULL OR occurred_at <= $5)
  AND ($6::text IS NULL OR target_type = $6)
  AND ($7::text IS NULL OR target_id = $7)
ORDER BY occurred_at DESC, id DESC
LIMIT $8 OFFSET $9;

-- name: CountAuditEvents :one
SELECT count(*) FROM identity.audit_event
WHERE ($1::uuid IS NULL OR account_id = $1)
  AND ($2::text IS NULL OR action LIKE $2)
  AND ($3::text IS NULL OR outcome = $3)
  AND ($4::timestamptz IS NULL OR occurred_at >= $4)
  AND ($5::timestamptz IS NULL OR occurred_at <= $5)
  AND ($6::text IS NULL OR target_type = $6)
  AND ($7::text IS NULL OR target_id = $7);

-- name: ExportAuditEvents :many
-- 导出 CSV 用:流式分页,不设上限,但仍走同一组过滤条件。
SELECT id, occurred_at, account_id, actor, action, target_type, target_id,
       outcome, ip, user_agent, metadata
FROM identity.audit_event
WHERE ($1::timestamptz IS NULL OR occurred_at >= $1)
  AND ($2::timestamptz IS NULL OR occurred_at <= $2)
ORDER BY id
LIMIT $3 OFFSET $4;

-- name: CountAuditEventsBefore :one
-- 归档任务用:统计某个时间点之前的行数。
SELECT count(*) FROM identity.audit_event WHERE occurred_at < $1;
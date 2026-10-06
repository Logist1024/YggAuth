-- name: InsertAuditEvent :one
-- 审计表只追加,应用层没有任何删除接口(见 docs/security.md 9.2)。
INSERT INTO identity.audit_event (
    account_id, actor, action, target_type, target_id, outcome, ip, user_agent, metadata)
VALUES (
    sqlc.narg('account_id')::uuid,
    sqlc.arg('actor'),
    sqlc.arg('action'),
    sqlc.narg('target_type')::text,
    sqlc.narg('target_id')::text,
    sqlc.arg('outcome'),
    sqlc.narg('ip')::inet,
    sqlc.narg('user_agent')::text,
    sqlc.arg('metadata')::jsonb)
RETURNING *;

-- name: SearchAuditEvents :many
-- 后台审计检索。可选参数为 NULL 时不参与过滤。
SELECT * FROM identity.audit_event
WHERE (sqlc.narg('account_id')::uuid IS NULL OR account_id = sqlc.narg('account_id')::uuid)
  AND (sqlc.narg('action')::text IS NULL OR action LIKE sqlc.narg('action')::text)
  AND (sqlc.narg('outcome')::text IS NULL OR outcome = sqlc.narg('outcome')::text)
  AND (sqlc.narg('from')::timestamptz IS NULL OR occurred_at >= sqlc.narg('from')::timestamptz)
  AND (sqlc.narg('to')::timestamptz IS NULL OR occurred_at <= sqlc.narg('to')::timestamptz)
  AND (sqlc.narg('target_type')::text IS NULL OR target_type = sqlc.narg('target_type')::text)
  AND (sqlc.narg('target_id')::text IS NULL OR target_id = sqlc.narg('target_id')::text)
ORDER BY occurred_at DESC, id DESC
LIMIT sqlc.arg('limit') OFFSET sqlc.arg('offset');

-- name: CountAuditEvents :one
SELECT count(*) FROM identity.audit_event
WHERE (sqlc.narg('account_id')::uuid IS NULL OR account_id = sqlc.narg('account_id')::uuid)
  AND (sqlc.narg('action')::text IS NULL OR action LIKE sqlc.narg('action')::text)
  AND (sqlc.narg('outcome')::text IS NULL OR outcome = sqlc.narg('outcome')::text)
  AND (sqlc.narg('from')::timestamptz IS NULL OR occurred_at >= sqlc.narg('from')::timestamptz)
  AND (sqlc.narg('to')::timestamptz IS NULL OR occurred_at <= sqlc.narg('to')::timestamptz)
  AND (sqlc.narg('target_type')::text IS NULL OR target_type = sqlc.narg('target_type')::text)
  AND (sqlc.narg('target_id')::text IS NULL OR target_id = sqlc.narg('target_id')::text);

-- name: ExportAuditEvents :many
-- 导出 CSV 用:只按时间窗过滤,规模由调用方控制。
SELECT id, occurred_at, account_id, actor, action, target_type, target_id,
       outcome, ip, user_agent, metadata
FROM identity.audit_event
WHERE (sqlc.narg('from')::timestamptz IS NULL OR occurred_at >= sqlc.narg('from')::timestamptz)
  AND (sqlc.narg('to')::timestamptz IS NULL OR occurred_at <= sqlc.narg('to')::timestamptz)
ORDER BY id
LIMIT sqlc.arg('limit') OFFSET sqlc.arg('offset');

-- name: CountAuditEventsBefore :one
-- 归档任务用:统计某个时间点之前的行数。
SELECT count(*) FROM identity.audit_event WHERE occurred_at < $1;

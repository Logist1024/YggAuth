-- name: GetSetting :one
SELECT * FROM app.setting WHERE key = $1;

-- name: ListSettings :many
SELECT * FROM app.setting ORDER BY key;

-- name: UpsertSetting :one
INSERT INTO app.setting (key, value, updated_by)
VALUES ($1, $2, $3)
ON CONFLICT (key) DO UPDATE
SET value = EXCLUDED.value, updated_by = EXCLUDED.updated_by, updated_at = now()
RETURNING *;

-- name: DeleteSetting :execrows
DELETE FROM app.setting WHERE key = $1;
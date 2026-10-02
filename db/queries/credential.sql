-- name: UpsertCredential :one
-- 每账号每算法一条(UNIQUE(account_id, algo)),支持多算法并存迁移。
INSERT INTO identity.credential (account_id, algo, hash, params)
VALUES ($1, $2, $3, $4)
ON CONFLICT (account_id, algo) DO UPDATE
SET hash = EXCLUDED.hash,
    params = EXCLUDED.params,
    failed_attempts = 0,
    locked_until = NULL,
    changed_at = now()
RETURNING *;

-- name: GetCredential :one
SELECT * FROM identity.credential WHERE account_id = $1 AND algo = $2;

-- name: RecordFailedAttempt :one
-- 登录失败计数 +1。达到阈值时把 locked_until 推后 lock_secs 秒。
UPDATE identity.credential
SET failed_attempts = failed_attempts + 1,
    locked_until = CASE
        WHEN sqlc.arg('lock')::boolean THEN now() + make_interval(secs => sqlc.arg('lock_secs'))
        ELSE locked_until
    END
WHERE account_id = sqlc.arg('account_id') AND algo = sqlc.arg('algo')
RETURNING *;

-- name: ResetFailedAttempts :one
-- 登录成功后清零。
UPDATE identity.credential
SET failed_attempts = 0, locked_until = NULL
WHERE account_id = $1 AND algo = $2
RETURNING *;

-- name: TouchCredentialChangedAt :exec
UPDATE identity.credential SET changed_at = now() WHERE account_id = $1 AND algo = $2;

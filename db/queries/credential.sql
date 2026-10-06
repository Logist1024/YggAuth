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

-- name: SetCredentialMustChange :exec
-- 首登强制改密(P1)的真源开关:引导建号置 true,改密/重置密码置 false。
-- 不并进 UpsertCredential 是因为「写入新的哈希」和「要求用户改密」是两件
-- 独立的事:引导流程写完哈希还要再要求改密,合并会逼调用方打擦边球。
UPDATE identity.credential
SET must_change = $3
WHERE account_id = $1 AND algo = $2;

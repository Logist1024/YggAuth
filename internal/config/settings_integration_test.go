//go:build integration

package config_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/yggauth/yggauth/internal/config"
	"github.com/yggauth/yggauth/internal/platform/db/testdb"
)

// TestSyncSeedsFromEnvAndRespectsManualEdits 是 docs/configuration.md
// §6.5 的验收:env 是**种子**,setting 是**现值**。
//
// 三种行的处理方式各不相同,任何一种错了都表现为「改了没反应」:
//   - 缺行        → 补上 env 种子(升级上来的库缺键会静默退回代码默认值);
//   - 行还在、没人改过(updated_by IS NULL)且与 env 不同 → env 赢
//     (迁移种的是硬编码默认值,不是用户的 .env);
//   - 行被后台改过 → setting 赢,env 只是种子。
func TestSyncSeedsFromEnvAndRespectsManualEdits(t *testing.T) {
	pool := testdb.Fresh(t)

	cfg := &config.Config{}
	cfg.Auth.RegistrationMode = "invite_only"
	cfg.Auth.MCLoginDefault = true
	cfg.Auth.PasswordMinLength = 10
	cfg.Auth.PasswordMaxLength = 128
	cfg.Auth.PasswordRejectCommon = false
	cfg.Auth.SessionIdleTTL = 24 * time.Hour
	cfg.Auth.SessionMaxTTL = 72 * time.Hour
	cfg.Auth.LoginMaxFailedAttempts = 7
	cfg.Auth.LoginLockDuration = 5 * time.Minute
	cfg.Mail.VerifyCooldown = 90 * time.Second
	cfg.Mail.VerifyDailyLimit = 20
	cfg.MC.NameRetentionDays = 30

	var logs bytes.Buffer
	store := config.NewSettingStore(pool)
	snap := config.NewSnapshot(config.SettingsOptions{
		Store:  store,
		Cfg:    cfg,
		Logger: slog.New(slog.NewTextHandler(&logs, nil)),
	})
	require.NoError(t, snap.Sync(t.Context()))

	// 1) 没被人改过的迁移种子行 → env 赢
	require.Equal(t, 10, snap.Int(t.Context(), "password.min_length", 0),
		"env 的 password.min_length=10 应覆盖迁移种的 8")
	require.Equal(t, "invite_only", snap.String(t.Context(), "registration.mode", ""))
	require.Equal(t, 24, snap.Int(t.Context(), "session.idle_ttl_hours", 0),
		"迁移种的 168 小时应被 env 的 24 小时覆盖")

	// 2) 行被后台改过 → setting 赢,env 不再覆盖
	var accountID uuid.UUID
	require.NoError(t, pool.QueryRow(t.Context(), `
		INSERT INTO identity.account (username, username_lower, email, status)
		VALUES ('sync_user', 'sync_user', 'sync@example.com', 'active')
		RETURNING id`).Scan(&accountID))
	_, err := store.Upsert(t.Context(), "password.max_length",
		json.RawMessage("60"), &accountID)
	require.NoError(t, err)

	require.NoError(t, snap.Sync(t.Context()))
	require.Equal(t, 60, snap.Int(t.Context(), "password.max_length", 0),
		"后台改过的行不能再被 env 顶回去")
	require.Equal(t, 10, snap.Int(t.Context(), "password.min_length", 0),
		"没改过的行仍然由 env 维护")

	// 3) 缺行 → 补回 env 种子
	//
	// 注意这里断言的是**库**:内存快照在 TTL 内照旧提供旧值,
	// 那正是热缓存该有的行为(手工改库不该在 30 秒内被读到)。
	_, err = pool.Exec(t.Context(), `DELETE FROM app.setting WHERE key = 'mc.name_retention_days'`)
	require.NoError(t, err)
	_, err = store.Get(t.Context(), "mc.name_retention_days")
	require.Error(t, err, "库里这一行应当真的没了")

	require.NoError(t, snap.Sync(t.Context()))
	require.Equal(t, 30, snap.Int(t.Context(), "mc.name_retention_days", 0),
		"缺行必须由启动回填补上,否则会静默退回代码默认值")

	// 4) 与 env 不同的键必须被点名 —— 这行日志就是「我改了 .env 怎么没用」的答案
	require.Contains(t, logs.String(), "与 env 种子不同的键")
	require.Contains(t, logs.String(), "password.max_length")
}

//go:build integration

package transport_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/yggauth/yggauth/internal/config"
	"github.com/yggauth/yggauth/internal/platform/clock"
	"github.com/yggauth/yggauth/internal/platform/db/testdb"
	"github.com/yggauth/yggauth/internal/platform/keys"
	"github.com/yggauth/yggauth/internal/transport"
)

type publicConfigData struct {
	SiteName                 string `json:"site_name"`
	LogoURL                  string `json:"logo_url"`
	SupportEmail             string `json:"support_email"`
	RegistrationMode         string `json:"registration_mode"`
	PasswordMinLength        int    `json:"password_min_length"`
	RequireEmailVerification bool   `json:"require_email_verification"`
}

func getPublicConfig(t *testing.T, h http.Handler) publicConfigData {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/public/config", nil))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var env struct {
		Code int              `json:"code"`
		Data publicConfigData `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env), rec.Body.String())
	require.Equal(t, 0, env.Code, rec.Body.String())
	return env.Data
}

// TestPublicConfigEndpointRequiresNoLogin 覆盖 docs/configuration.md §7.2:
// 公开配置在登录之前就要能读 —— 登录页自己要用它拼站点名。
// 顺带确认这条端点只吐**展示必需**的字段,不夹带邮件、密钥这类配置。
func TestPublicConfigEndpointRequiresNoLogin(t *testing.T) {
	data := getPublicConfig(t, newSPAHandler(t))

	require.NotEmpty(t, data.SiteName)
	require.Equal(t, "open", data.RegistrationMode)
	require.Equal(t, 8, data.PasswordMinLength)
	require.True(t, data.RequireEmailVerification)
	require.Empty(t, data.LogoURL)
	require.Empty(t, data.SupportEmail)
}

// TestPublicConfigReflectsSettingsWithoutRestart 覆盖「站点名称改完就生效」:
// 改设置 → 下一次请求就是新名字,不重启、不重新构建前端。
// (前端启动时拉一次,浏览器标签与后台左栏随后都用它。)
func TestPublicConfigReflectsSettingsWithoutRestart(t *testing.T) {
	pool := testdb.Fresh(t)
	cfg := testConfig()
	cfg.App.PublicBaseURL = webBase
	// Sync 要求每个种子都合法:testConfig 是手搓的零值配置,没走 Load,
	// 少设一个字段,失败的会是某个**无关**的键,很难一眼看出原因。
	cfg.Auth.RegistrationMode = "invite_only"
	cfg.Auth.PasswordMinLength = 10
	cfg.Auth.PasswordMaxLength = 128
	cfg.Auth.LoginMaxFailedAttempts = 5
	cfg.Auth.SessionIdleTTL = 168 * time.Hour
	cfg.Auth.SessionMaxTTL = 720 * time.Hour

	sealer, err := keys.NewManager(keys.Options{
		MasterSecret: "public-config-test-secret-at-least-32-bytes",
		KidPrefix:    "oidc",
		BitSize:      2048,
	}, keys.NewPGStore(pool))
	require.NoError(t, err)

	settings := config.NewSnapshot(config.SettingsOptions{
		Store:  config.NewSettingStore(pool),
		Sealer: sealer,
		Cfg:    cfg,
	})
	require.NoError(t, settings.Sync(t.Context()))

	h := transport.New(transport.Deps{
		Config:   cfg,
		Logger:   nopLogger{},
		DB:       pool,
		Clock:    clock.New(),
		Settings: settings,
	}).Handler()

	// 初值 = env 种子
	data := getPublicConfig(t, h)
	require.Equal(t, "invite_only", data.RegistrationMode)
	require.Equal(t, 10, data.PasswordMinLength)

	// 改站点名 → 立刻是新值,不重启
	_, err = settings.Upsert(t.Context(), "site.name", json.RawMessage(`"晨星账号"`), nil)
	require.NoError(t, err)
	data = getPublicConfig(t, h)
	require.Equal(t, "晨星账号", data.SiteName)

	// 同一次请求里注册模式也是现值
	_, err = settings.Upsert(t.Context(), "registration.mode", json.RawMessage(`"closed"`), nil)
	require.NoError(t, err)
	data = getPublicConfig(t, h)
	require.Equal(t, "closed", data.RegistrationMode)
}

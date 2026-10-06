package config_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/yggauth/yggauth/internal/config"
)

// baseEnv 铺一份能让配置通过校验的最小环境。
func baseEnv(t *testing.T) {
	t.Helper()
	t.Setenv("APP_PUBLIC_DOMAIN", "auth.example.com")
	t.Setenv("PUBLIC_BASE_URL", "https://auth.example.com")
	t.Setenv("DB_PASSWORD", "s3cret")
	t.Setenv("KEY_MASTER_SECRET", strings.Repeat("a", 64))
}

func TestLoadDefaults(t *testing.T) {
	baseEnv(t)

	cfg, err := config.Load()
	require.NoError(t, err)

	require.Equal(t, "0.0.0.0", cfg.App.Host)
	require.Equal(t, 3000, cfg.App.Port)
	require.Equal(t, "open", cfg.Auth.RegistrationMode)
	require.True(t, cfg.OIDC.RequirePKCE)
	require.True(t, cfg.OIDC.CookieSecure)
	require.Equal(t, int64(2<<20), cfg.MC.SkinMaxSize, "默认皮肤上限 2MB")
}

func TestMissingRequiredFailsFast(t *testing.T) {
	// 一个都不设,应该一次性报出全部缺失项
	_, err := config.Load()
	require.Error(t, err)
	require.True(t, config.IsValidationError(err))

	msg := err.Error()
	require.Contains(t, msg, "APP_PUBLIC_DOMAIN 未设置")
	require.Contains(t, msg, "PUBLIC_BASE_URL 未设置")
	require.Contains(t, msg, "DB_PASSWORD 未设置")
	require.Contains(t, msg, "KEY_MASTER_SECRET 未设置")
}

func TestPublicBaseURLMustBeAbsolute(t *testing.T) {
	baseEnv(t)
	t.Setenv("PUBLIC_BASE_URL", "auth.example.com") // 缺协议

	_, err := config.Load()
	require.Error(t, err)
	require.Contains(t, err.Error(), "PUBLIC_BASE_URL 必须是绝对 URL")
}

func TestPasswordLengthRangeMustBeOrdered(t *testing.T) {
	baseEnv(t)
	t.Setenv("PASSWORD_MIN_LENGTH", "256")
	t.Setenv("PASSWORD_MAX_LENGTH", "16")

	_, err := config.Load()
	require.Error(t, err)
	require.Contains(t, err.Error(), "PASSWORD_MIN_LENGTH(256) 不能大于 PASSWORD_MAX_LENGTH(16)")
}

// 变更 C-1:默认区间必须是 8–128,不能再是旧版的 6–18。
func TestPasswordPolicyDefaultsMatchDecision(t *testing.T) {
	baseEnv(t)

	cfg, err := config.Load()
	require.NoError(t, err)
	require.Equal(t, 8, cfg.Auth.PasswordMinLength)
	require.Equal(t, 128, cfg.Auth.PasswordMaxLength)
}

func TestSessionIdleMustNotExceedMax(t *testing.T) {
	baseEnv(t)
	t.Setenv("SESSION_IDLE_TTL", "48h")
	t.Setenv("SESSION_MAX_TTL", "24h")

	_, err := config.Load()
	require.Error(t, err)
	require.Contains(t, err.Error(), "SESSION_IDLE_TTL")
}

func TestCorsWildcardRejected(t *testing.T) {
	baseEnv(t)
	t.Setenv("CORS_ALLOWED_ORIGINS", "*")

	_, err := config.Load()
	require.Error(t, err)
	require.Contains(t, err.Error(), "不能包含通配符")
}

// localhost 没有父域,把 cookie 域设成 localhost 会让 SSO 静默失效,
// 必须在启动时就拦下来而不是等到排查 SSO 时才发现。
func TestLocalhostCookieDomainRejected(t *testing.T) {
	baseEnv(t)
	t.Setenv("SSO_COOKIE_DOMAIN", "localhost")

	_, err := config.Load()
	require.Error(t, err)
	require.Contains(t, err.Error(), "SSO_COOKIE_DOMAIN 不能设为 localhost")
}

func TestSameSiteNoneRequiresSecure(t *testing.T) {
	baseEnv(t)
	t.Setenv("SSO_COOKIE_SAMESITE", "None")
	t.Setenv("SSO_COOKIE_SECURE", "false")

	_, err := config.Load()
	require.Error(t, err)
	require.Contains(t, err.Error(), "SSO_COOKIE_SECURE=true")
}

// 密钥与密码绝不能出现在启动日志里(docs/security.md 6.3)。
func TestSensitiveValuesAreRedacted(t *testing.T) {
	baseEnv(t)

	cfg, err := config.Load()
	require.NoError(t, err)

	redacted := cfg.Redacted()
	require.Equal(t, "***", redacted["db.password"])
	require.Equal(t, "***", redacted["key.master_secret"])

	// 非敏感运维项保留可读
	require.Equal(t, 3000, redacted["app.port"])
	require.Equal(t, "info", redacted["log.level"])
}

func TestSMTPRequiresHost(t *testing.T) {
	baseEnv(t)
	t.Setenv("MAILER_TRANSPORT", "smtp")

	_, err := config.Load()
	require.Error(t, err)
	require.Contains(t, err.Error(), "SMTP_HOST 未设置")
}

func TestInvalidEnumRejected(t *testing.T) {
	baseEnv(t)
	t.Setenv("REGISTRATION_MODE", "semi_open")

	_, err := config.Load()
	require.Error(t, err)
	require.Contains(t, err.Error(), "REGISTRATION_MODE 取值必须是 open/invite_only")
}

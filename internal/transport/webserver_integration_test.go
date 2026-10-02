//go:build integration

package transport_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/yggauth/yggauth/internal/identity"
	"github.com/yggauth/yggauth/internal/identity/account"
	"github.com/yggauth/yggauth/internal/identity/audit"
	"github.com/yggauth/yggauth/internal/identity/rbac"
	"github.com/yggauth/yggauth/internal/identity/session"
	"github.com/yggauth/yggauth/internal/platform/clock"
	"github.com/yggauth/yggauth/internal/platform/db/testdb"
	"github.com/yggauth/yggauth/internal/transport"
	"github.com/yggauth/yggauth/internal/webserver"
)

const (
	webHost = "auth.example.com"
	webBase = "https://auth.example.com"
)

// newSPAHandler 装配一个带 SPA 与账号内核的完整路由。
func newSPAHandler(t *testing.T) http.Handler {
	t.Helper()

	pool := testdb.Fresh(t)
	clk := clock.New()
	cfg := testConfig()
	cfg.App.PublicBaseURL = webBase
	cfg.Auth.SessionIdleTTL = 168 * time.Hour
	cfg.Auth.SessionMaxTTL = 720 * time.Hour

	identitySvc := identity.New(identity.Deps{
		Accounts: account.NewPgRepository(pool),
		Tokens:   account.NewPgTokenStore(pool),
		Sessions: session.NewPgRepository(pool),
		RBAC:     rbac.NewPgRepository(pool),
		Audit:    audit.NewPgRepository(pool),
		Hasher: account.NewHasher(account.Params{
			MemoryKiB: 8 * 1024, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32,
		}),
		Clock:   clk,
		Account: account.Config{Policy: account.Policy{MinLength: 8, MaxLength: 128}, RegistrationMode: "open", LoginEnabledDefault: true, MaxFailedAttempts: 5, LockDuration: time.Minute, EmailTokenTTL: time.Hour},
		Session: session.Config{IdleTTL: 168 * time.Hour, MaxTTL: 720 * time.Hour},
		Logger:  nopLogger{},
	})

	return transport.New(transport.Deps{
		Config: cfg,
		Logger: nopLogger{},
		DB:     pool,
		Clock:  clk,
		Identity: transport.IdentityDeps{
			Service: identitySvc,
			Cookie:  transport.AuthConfig{CookieName: "ygg_session"},
			Handler: identity.NewHandler(identitySvc, identity.CookieConfig{
				Name: "ygg_session", Secure: false, SameSite: http.SameSiteLaxMode, IdleTTL: 168 * time.Hour,
			}, webBase),
		},
	}).Handler()
}

// nopLogger 是测试用日志器。
type nopLogger struct{}

func (nopLogger) Info(string, ...any)  {}
func (nopLogger) Warn(string, ...any)  {}
func (nopLogger) Error(string, ...any) {}
func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Host = webHost
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestEmbeddedAssetsArePresent(t *testing.T) {
	accountOK, adminOK := webserver.Available()

	require.True(t, accountOK,
		"account-web 的构建产物缺失:需要先在 web/ 下执行 pnpm build")
	require.True(t, adminOK,
		"admin-web 的构建产物缺失:需要先在 web/ 下执行 pnpm build")
}

func TestAccountSPAServesIndex(t *testing.T) {
	h := newSPAHandler(t)

	rec := get(t, h, "/")
	require.Equal(t, http.StatusOK, rec.Code)

	body := rec.Body.String()
	require.Contains(t, body, "<div id=\"app\">", "根路径应返回 account-web 的 index.html")
	require.Contains(t, rec.Header().Get("Content-Type"), "text/html")
}

// TestSPADeepLinkFallsBackToIndex 验证深链接回退。
//
// 用户刷新 /security 时拿到的必须是应用外壳,而不是 404 ——
// 刷新是 SPA 最常见的操作,这里挂掉等于整个站点不可用。
func TestSPADeepLinkFallsBackToIndex(t *testing.T) {
	h := newSPAHandler(t)

	for _, path := range []string{"/security", "/skin", "/sessions", "/some/deep/unknown/path"} {
		rec := get(t, h, path)
		require.Equal(t, http.StatusOK, rec.Code, "深链接 %s 必须能打开", path)
		require.Contains(t, rec.Body.String(), "<div id=\"app\">", "路径 %s", path)
	}
}

func TestAdminSPAIsMountedUnderPrefix(t *testing.T) {
	h := newSPAHandler(t)

	rec := get(t, h, "/admin/")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "<div id=\"app\">")

	// 后台的深链接同样要能回退
	rec = get(t, h, "/admin/clients")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "<div id=\"app\">")
}

func TestHashedAssetsAreServedWithLongCache(t *testing.T) {
	h := newSPAHandler(t)

	// 从 index.html 里抠出真实的 assets 路径,验证静态文件确实能取到。
	rec := get(t, h, "/")
	assetPath := findAssetPath(t, rec.Body.String())
	require.NotEmpty(t, assetPath, "index.html 应引用至少一个 assets 文件")

	rec = get(t, h, assetPath)
	require.Equal(t, http.StatusOK, rec.Code, "静态资源 %s 必须可访问", assetPath)
	require.Contains(t, rec.Header().Get("Cache-Control"), "immutable",
		"带哈希的文件名可以永久缓存")
	require.NotContains(t, rec.Header().Get("Cache-Control"), "no-cache")
}

// TestIndexHTMLIsNotCached 验证 index.html 不会被长缓存。
//
// index.html 引用的是带哈希的 assets 文件名;缓存住它等于让用户
// 永远拿不到新版前端,而且没有任何症状提示。
func TestIndexHTMLIsNotCached(t *testing.T) {
	h := newSPAHandler(t)

	rec := get(t, h, "/")
	require.Contains(t, rec.Header().Get("Cache-Control"), "no-cache")
}

// TestSPAFallbackDoesNotSwallowAPI 是本文件最重要的一条。
//
// SPA 的回退 handler 接受任意路径。如果它排在 API 之前,
// /api/auth/login 与 /oauth/token 都会拿到 index.html ——
// 表现是「后端接口全返回 HTML」,而服务端日志一片正常。
func TestSPAFallbackDoesNotSwallowAPI(t *testing.T) {
	h := newSPAHandler(t)

	cases := []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/api/auth/login"},
		{http.MethodPost, "/api/auth/register"},
		{http.MethodGet, "/api/auth/policy"},
		{http.MethodPost, "/oauth/par"},
		{http.MethodGet, "/oauth/jwks"},
		{http.MethodPost, "/mc/authenticate"},
		{http.MethodGet, "/mc/"},
	}

	for _, tc := range cases {
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader("{}"))
		req.Host = webHost
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		body := rec.Body.String()
		require.NotContains(t, body, "<div id=\"app\">",
			"%s %s 被 SPA 回退吞掉了", tc.method, tc.path)

		// 协议与 API 端点要么给出自己的响应,要么给出错误包,
		// 但绝不该是前端外壳。
		if strings.Contains(body, "<html") || strings.Contains(body, "<!doctype") {
			require.Fail(t, "%s %s 返回了 HTML 页面", tc.method, tc.path)
		}
	}
}

func TestPolicyEndpointIsPublicAndJSON(t *testing.T) {
	h := newSPAHandler(t)

	rec := get(t, h, "/api/auth/policy")
	require.Equal(t, http.StatusOK, rec.Code)

	var out struct {
		Data struct {
			PasswordMinLength int    `json:"password_min_length"`
			PasswordMaxLength int    `json:"password_max_length"`
			RegistrationMode  string `json:"registration_mode"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))

	// 前端据此渲染校验规则,所以这几个值必须真的存在。
	require.Positive(t, out.Data.PasswordMinLength)
	require.Greater(t, out.Data.PasswordMaxLength, out.Data.PasswordMinLength)
	require.NotEmpty(t, out.Data.RegistrationMode)
}

// findAssetPath 从 index.html 里找出第一个 assets 路径。
func findAssetPath(t *testing.T, html string) string {
	t.Helper()

	const marker = `"/assets/`
	idx := strings.Index(html, marker)
	if idx < 0 {
		return ""
	}

	rest := html[idx+1:]
	end := strings.IndexAny(rest, `"`)
	if end < 0 {
		return ""
	}
	return rest[:end]
}

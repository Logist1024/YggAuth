//go:build integration

package oidc_test

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/yggauth/yggauth/internal/config"
	"github.com/yggauth/yggauth/internal/identity"
	"github.com/yggauth/yggauth/internal/identity/account"
	"github.com/yggauth/yggauth/internal/identity/audit"
	"github.com/yggauth/yggauth/internal/identity/rbac"
	"github.com/yggauth/yggauth/internal/identity/session"
	"github.com/yggauth/yggauth/internal/oidc"
	"github.com/yggauth/yggauth/internal/platform/clock"
	"github.com/yggauth/yggauth/internal/platform/db/testdb"
	"github.com/yggauth/yggauth/internal/platform/keys"
	"github.com/yggauth/yggauth/internal/transport"
)

const (
	testIssuer    = "https://auth.example.com"
	testHost      = "auth.example.com"
	sessionCookie = "ygg_sso"
)

// nopLogger 是测试用日志器。
type nopLogger struct{}

func (nopLogger) Info(string, ...any) {}
func (nopLogger) Warn(string, ...any) {}

// Error 把授权/令牌端点的内部错误打到 stderr。
//
// 这两个端点按 RFC 6749 只向客户端返回通用错误码,一旦出问题,
// 服务端日志是唯一的线索 —— 集成测试里必须能看到它。
func (nopLogger) Error(msg string, args ...any) { fmt.Fprintln(os.Stderr, "DBG:", msg, args) }

// env 是装配好的授权服务测试环境。
type env struct {
	handler  http.Handler
	server   *oidc.Server
	clients  *oidc.ClientService
	identity *identity.Service
	device   *oidc.DeviceService
}

func testConfig() *config.Config {
	return &config.Config{
		App: config.App{
			PublicBaseURL:     testIssuer,
			TrustProxyHeaders: false,
		},
		Log:  config.Log{},
		Auth: config.Auth{SessionIdleTTL: 168 * time.Hour, SessionMaxTTL: 720 * time.Hour},
		OIDC: config.OIDC{
			KeyMasterSecret: "test-master-secret-at-least-32-bytes-long",
			AccessTokenTTL:  time.Hour,
			IDTokenTTL:      time.Hour,
			AuthCodeTTL:     60 * time.Second,
			RefreshTokenTTL: 30 * 24 * time.Hour,
			DeviceCodeTTL:   15 * time.Minute,
			RequirePKCE:     true,
			CookieName:      sessionCookie,
			CookieSecure:    false, // 测试走 httptest,不是 https
			CookieSameSite:  "Lax",
		},
	}
}

func newEnv(t *testing.T) *env {
	t.Helper()

	pool := testdb.Fresh(t)
	clk := clock.New()
	cfg := testConfig()

	identitySvc := identity.New(identity.Deps{
		Accounts: account.NewPgRepository(pool),
		Tokens:   account.NewPgTokenStore(pool),
		Sessions: session.NewPgRepository(pool),
		RBAC:     rbac.NewPgRepository(pool),
		Audit:    audit.NewPgRepository(pool),
		// argon2 参数:测试里用低参数。64MiB × 每次登录会让整套集成测试
		// 慢到不可用,而这里要验证的是流程,不是抗暴力破解的成本。
		Hasher: account.NewHasher(account.Params{
			MemoryKiB: 8 * 1024, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32,
		}),
		Clock:   clk,
		Account: account.Config{Policy: account.Policy{MinLength: 8, MaxLength: 128}, RegistrationMode: "open", LoginEnabledDefault: true, MaxFailedAttempts: 5, LockDuration: time.Minute, EmailTokenTTL: time.Hour},
		Session: session.Config{IdleTTL: 168 * time.Hour, MaxTTL: 720 * time.Hour},
		Logger:  nopLogger{},
	})

	keyManager, err := keys.NewManager(keys.Options{
		MasterSecret: cfg.OIDC.KeyMasterSecret,
		KidPrefix:    "oidc",
		BitSize:      2048,
	}, keys.NewPGStore(pool))
	require.NoError(t, err)

	server, err := oidc.NewServer(cfg, pool, clk, keyManager, nopLogger{})
	require.NoError(t, err)

	adapter := oidc.NewSessionAdapter(identitySvc.Sessions, identitySvc, oidc.CookieConfig{
		Name:     sessionCookie,
		Secure:   false,
		SameSite: http.SameSiteLaxMode,
	})
	deviceSvc := oidc.NewDeviceService(pool, clk.Now, cfg.OIDC.DeviceCodeTTL, 5)

	oidcHandler := oidc.NewHandler(oidc.HandlerDeps{
		Server:   server,
		Cookies:  server.CookieConfig(),
		Sessions: adapter,
		Device:   deviceSvc,
		Limiter:  oidc.NewLimiter(nil),
		Logger:   nopLogger{},
	})

	handler := transport.New(transport.Deps{
		Config: cfg,
		Logger: nopLogger{},
		DB:     pool,
		Clock:  clk,
		Identity: transport.IdentityDeps{
			Service: identitySvc,
			Cookie:  transport.AuthConfig{CookieName: sessionCookie},
			Handler: identity.NewHandler(identitySvc, identity.CookieConfig{
				Name:     sessionCookie,
				Secure:   false, // 测试走 httptest,不是 https
				SameSite: http.SameSiteLaxMode,
				IdleTTL:  168 * time.Hour,
			}, testIssuer),
		},
		OIDC: transport.OIDCDeps{
			Server:  server,
			Handler: oidcHandler,
			SSO:     oidc.NewSSO(oidcHandler),
			Device:  deviceSvc,
		},
	}).Handler()

	return &env{
		handler:  handler,
		server:   server,
		clients:  server.Clients,
		identity: identitySvc,
		device:   deviceSvc,
	}
}

// envelope 是统一响应包。
type envelope struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

// get 发起一次带 cookie 的 GET。
func (e *env) get(t *testing.T, path string, cookies []*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	// Host 必须与 Origin 一致,否则 CSRF 同源校验会(正确地)拒绝
	req.Host = testHost
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	return rec
}

// postForm 发起一次表单 POST。
func (e *env) postForm(t *testing.T, path string, form url.Values, cookies []*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Host = testHost
	req.Header.Set("Origin", testIssuer)
	for _, c := range cookies {
		req.AddCookie(c)
	}

	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	return rec
}

// postJSON 发起一次 JSON POST。
func (e *env) postJSON(t *testing.T, path string, body any, cookies []*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()

	raw, err := json.Marshal(body)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(raw)))
	req.Header.Set("Content-Type", "application/json")
	req.Host = testHost
	req.Header.Set("Origin", testIssuer)
	for _, c := range cookies {
		req.AddCookie(c)
	}

	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	return rec
}

// oauthToken 走令牌端点。
func (e *env) oauthToken(t *testing.T, form url.Values, cookies []*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	return e.postForm(t, "/oauth/token", form, cookies)
}

// register 登记客户端。
//
// requirePKCE 与 public 是两件独立的事:保密客户端同样可以被要求使用 PKCE。
// 把两者混为一谈会让保密客户端被静默降级成拿不到密钥的公开客户端。
func (e *env) register(t *testing.T, id string, requirePKCE, public bool) oidc.ClientRecord {
	t.Helper()

	rec, err := e.clients.Create(t.Context(), oidc.CreateClientInput{
		ClientID:        id,
		Name:            "测试客户端",
		RedirectURIs:    []string{"https://app.example.com/callback"},
		GrantTypes:      []string{"authorization_code", "refresh_token", "client_credentials", "urn:ietf:params:oauth:grant-type:device_code"},
		Scopes:          []string{"openid", "profile", "email", "offline_access"},
		RequirePKCE:     requirePKCE,
		Public:          public,
		AccessTokenTTL:  time.Hour,
		RefreshTokenTTL: 30 * 24 * time.Hour,
		AuthCodeTTL:     60 * time.Second,
	})
	require.NoError(t, err)
	return rec
}

// registerClient 登记一个保密且要求 PKCE 的客户端。
func (e *env) registerClient(t *testing.T, id string) oidc.ClientRecord {
	t.Helper()
	return e.register(t, id, true, false)
}

// login 注册、验证邮箱并登录一个账号,返回会话 cookie。
//
// 注册与登录走 JSON(账号内核的 API 约定),而 OAuth 端点走表单 ——
// 两者不是一套,混用会踩 CSRF 与解析差异的坑。
func (e *env) login(t *testing.T, username, password string) []*http.Cookie {
	t.Helper()

	rec := e.postJSON(t, "/api/auth/register", map[string]string{
		"username": username,
		"email":    username + "@example.com",
		"password": password,
	}, nil)
	require.Equal(t, http.StatusCreated, rec.Code, "注册失败: %s", rec.Body.String())
	e.verifyEmail(t, rec)

	rec = e.postJSON(t, "/api/auth/login", map[string]string{
		"email":    username + "@example.com",
		"password": password,
	}, nil)
	require.Equal(t, http.StatusOK, rec.Code, "登录失败: %s", rec.Body.String())

	cookies := rec.Result().Cookies()
	require.NotEmpty(t, cookies, "登录应当下发会话 cookie")
	return cookies
}

// verifyEmail 从注册响应里取出验证链接并完成邮箱验证。
//
// 未验证邮箱不允许登录是账号内核的硬规则,测试不能绕过它 ——
// 绕过就等于没测到真实部署里的那条路径。
func (e *env) verifyEmail(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()

	verifyURL, ok := parseEnvelope(t, rec)["verify_url"].(string)
	require.True(t, ok, "注册响应应当带回验证链接")

	parsed, err := url.Parse(verifyURL)
	require.NoError(t, err)

	token := parsed.Query().Get("token")
	require.NotEmpty(t, token)

	vrec := e.postJSON(t, "/api/auth/email/verify", map[string]string{"token": token}, nil)
	require.Equal(t, http.StatusOK, vrec.Code, "验证邮箱失败: %s", vrec.Body.String())
}

// pkcePair 生成一对 S256 挑战。
func pkcePair() (verifier, challenge string) {
	verifier = "verifier-" + strings.Repeat("x", 48)
	challenge = s256Base64(verifier)
	return verifier, challenge
}

// s256Base64 算 PKCE 的 S256 挑战值。
func s256Base64(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// tokenSet 是令牌端点的标准响应。
type tokenSet struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	Scope        string `json:"scope"`
}

// decodeTokens 解析令牌端点响应。
func decodeTokens(t *testing.T, rec *httptest.ResponseRecorder) tokenSet {
	t.Helper()
	var tokens tokenSet
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &tokens), "令牌响应不是合法 JSON: %s", rec.Body.String())
	return tokens
}

// newAuthedGet 构造带 Bearer 头的 GET 请求。
func newAuthedGet(path, token string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Host = testHost
	req.Header.Set("Authorization", "Bearer "+token)
	return req
}

// parseEnvelope 解出统一响应包里的 data。
func parseEnvelope(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var env envelope
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env), "响应不是合法 JSON: %s", rec.Body.String())
	require.Equal(t, 0, env.Code, "业务码非零: %s", rec.Body.String())

	var data map[string]any
	require.NoError(t, json.Unmarshal(env.Data, &data))
	return data
}

// clientRecordID 从注册结果里取 client_id。
func clientRecordID(rec oidc.ClientRecord) string { return rec.ID }

//go:build integration

package identity_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/yggauth/yggauth/internal/config"

	"github.com/yggauth/yggauth/internal/identity"
	"github.com/yggauth/yggauth/internal/identity/account"
	"github.com/yggauth/yggauth/internal/identity/audit"
	"github.com/yggauth/yggauth/internal/identity/rbac"
	"github.com/yggauth/yggauth/internal/identity/session"
	"github.com/yggauth/yggauth/internal/platform/apperr"
	"github.com/yggauth/yggauth/internal/platform/clock"

	"github.com/yggauth/yggauth/internal/platform/db/testdb"

	"github.com/yggauth/yggauth/internal/transport"
)

type env struct {
	handler http.Handler
	svc     *identity.Service
	// pool 与 handler 指向同一个库:首启引导、直接查表这类
	// 「绕过 service 的断言」需要它,否则这类用例只能各起一套装配。
	pool *testdb.Pool
}

// newEnv 用真实 PostgreSQL 装配完整账号内核与路由。
func newEnv(t *testing.T) *env {
	t.Helper()

	pool := testdb.Fresh(t)
	clk := clock.New()

	svc := identity.New(identity.Deps{
		Accounts: account.NewPgRepository(pool),
		Tokens:   account.NewPgTokenStore(pool),
		Sessions: session.NewPgRepository(pool),
		RBAC:     rbac.NewPgRepository(pool),
		Audit:    audit.NewPgRepository(pool),
		Hasher: account.NewHasher(account.Params{
			MemoryKiB: 8 * 1024, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32,
		}),
		Clock: clk,
		Account: account.Config{
			Policy: account.Policy{
				MinLength: 8, MaxLength: 128,
				RejectCommon:    true,
				CommonPasswords: account.CommonPasswordSet(),
			},
			LoginEnabledDefault: true,
			MaxFailedAttempts:   5,
			LockDuration:        15 * time.Minute,
			EmailTokenTTL:       24 * time.Hour,
			RegistrationMode:    "open",
		},
		Session: session.Config{
			IdleTTL: 168 * time.Hour,
			MaxTTL:  720 * time.Hour,
		},
		Logger: nopLogger{},
	})

	handler := transport.New(transport.Deps{
		Config: &config.Config{
			App: config.App{TrustProxyHeaders: false},
			Log: config.Log{},
		},
		Logger: nopLogger{},
		DB:     pool,
		Clock:  clk,
		Identity: transport.IdentityDeps{
			Service: svc,
			Handler: identity.NewHandler(svc, identity.CookieConfig{
				Name:              "ygg_session",
				Secure:            false, // 测试走 httptest,不是 https
				SameSite:          http.SameSiteLaxMode,
				IdleTTL:           168 * time.Hour,
				TrustProxyHeaders: false,
			}, "https://auth.example.com"),
			Cookie: transport.AuthConfig{CookieName: "ygg_session"},
		},
	}).Handler()

	return &env{handler: handler, svc: svc, pool: pool}
}

type nopLogger struct{}

func (nopLogger) Info(string, ...any)  {}
func (nopLogger) Warn(string, ...any)  {}
func (nopLogger) Error(string, ...any) {}

type response struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

func do(t *testing.T, h http.Handler, method, path string, body any, cookies []*http.Cookie) (*httptest.ResponseRecorder, response) {
	t.Helper()

	var reader *strings.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		require.NoError(t, err)
		reader = strings.NewReader(string(raw))
	} else {
		reader = strings.NewReader("")
	}

	req := httptest.NewRequest(method, path, reader)
	// Host 要与 Origin 一致,否则 CSRF 同源校验会(正确地)拒绝
	req.Host = "auth.example.com"
	req.Header.Set("Content-Type", "application/json")
	// 写操作要过 CSRF 同源校验
	req.Header.Set("Origin", "https://auth.example.com")
	for _, c := range cookies {
		req.AddCookie(c)
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	var parsed response
	if rec.Body.Len() > 0 {
		_ = json.Unmarshal(rec.Body.Bytes(), &parsed)
	}
	return rec, parsed
}

// ---------------------------------------------------------------- 全链路

// M2 验收:注册 → 验证邮箱 → 登录 → 拿到会话 → 访问受保护接口。
func TestFullChainRegisterLoginProtectedAPI(t *testing.T) {
	env := newEnv(t)

	// 1. 注册
	rec, body := do(t, env.handler, http.MethodPost, "/api/auth/register", map[string]string{
		"username": "player_one",
		"email":    "user@example.com",
		"password": "correct-horse-battery",
	}, nil)
	require.Equal(t, http.StatusCreated, rec.Code, "body=%s", rec.Body.String())
	require.Equal(t, int(apperr.CodeOK), body.Code)

	require.Equal(t, int(apperr.CodeOK), body.Code, "注册失败: %s", rec.Body.String())

	var regData struct {
		Account struct {
			ID    string `json:"id"`
			Email string `json:"email"`
		} `json:"account"`
		VerifyURL string `json:"verify_url"`
	}
	require.NoError(t, json.Unmarshal(body.Data, &regData))
	require.NotEmpty(t, regData.Account.ID)
	require.Contains(t, regData.VerifyURL, "token=")

	token := regData.VerifyURL[strings.Index(regData.VerifyURL, "token=")+len("token="):]

	// 2. 验证邮箱
	rec, body = do(t, env.handler, http.MethodPost, "/api/auth/email/verify",
		map[string]string{"token": token}, nil)
	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())
	require.Equal(t, int(apperr.CodeOK), body.Code)

	// 3. 未登录时访问受保护接口 → 401
	rec, body = do(t, env.handler, http.MethodGet, "/api/account", nil, nil)
	require.Equal(t, http.StatusUnauthorized, rec.Code)
	require.Equal(t, int(apperr.CodeUnauthorized), body.Code)

	// 4. 登录
	rec, body = do(t, env.handler, http.MethodPost, "/api/auth/login", map[string]string{
		"email":    "user@example.com",
		"password": "correct-horse-battery",
	}, nil)
	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())

	setCookies := rec.Result().Cookies()
	sessionCookie := findCookie(setCookies, "ygg_session")
	require.NotNil(t, sessionCookie, "登录必须下发会话 cookie")
	require.True(t, sessionCookie.HttpOnly, "会话 cookie 必须 HttpOnly")
	require.NotEmpty(t, sessionCookie.Value)

	var loginData struct {
		Account struct {
			ID       string `json:"id"`
			Username string `json:"username"`
		} `json:"account"`
		EmailVerified bool `json:"email_verified"`
	}
	require.NoError(t, json.Unmarshal(body.Data, &loginData))
	require.Equal(t, "player_one", loginData.Account.Username)
	require.True(t, loginData.EmailVerified)

	// 5. 带 cookie 访问受保护接口 → 200
	rec, body = do(t, env.handler, http.MethodGet, "/api/account", nil,
		[]*http.Cookie{sessionCookie})
	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())
	require.Equal(t, int(apperr.CodeOK), body.Code)

	// 6. 会话列表里能看到当前会话
	rec, body = do(t, env.handler, http.MethodGet, "/api/account/sessions", nil,
		[]*http.Cookie{sessionCookie})
	require.Equal(t, http.StatusOK, rec.Code)

	var sessionsData struct {
		Sessions []struct {
			ID      string `json:"id"`
			Current bool   `json:"current"`
		} `json:"sessions"`
	}
	require.NoError(t, json.Unmarshal(body.Data, &sessionsData))
	require.Len(t, sessionsData.Sessions, 1)
	require.True(t, sessionsData.Sessions[0].Current)

	// 7. 登出后会话立即失效
	rec, _ = do(t, env.handler, http.MethodPost, "/api/auth/logout", nil,
		[]*http.Cookie{sessionCookie})
	require.Equal(t, http.StatusOK, rec.Code)

	rec, body = do(t, env.handler, http.MethodGet, "/api/account", nil,
		[]*http.Cookie{sessionCookie})
	require.Equal(t, http.StatusUnauthorized, rec.Code, "登出后旧 cookie 必须失效")
	require.Equal(t, int(apperr.CodeSessionExpired), body.Code)
}

func findCookie(cookies []*http.Cookie, name string) *http.Cookie {
	for _, c := range cookies {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// 数据库里只应存令牌哈希,不能存明文。
func TestSessionTokenStoredAsHashOnly(t *testing.T) {
	env := newEnv(t)
	pool := testdb.Connect(t).Pool

	acc := registerAndVerify(t, env)
	rec, _ := do(t, env.handler, http.MethodPost, "/api/auth/login", map[string]string{
		"email": acc.Email, "password": "correct-horse-battery",
	}, nil)
	require.Equal(t, http.StatusOK, rec.Code)

	plain := findCookie(rec.Result().Cookies(), "ygg_session").Value

	var stored []byte
	require.NoError(t, pool.QueryRow(t.Context(),
		`SELECT token_hash FROM identity.session WHERE account_id = $1`, acc.ID).Scan(&stored))

	require.NotContains(t, string(stored), plain, "库里绝不能出现令牌明文")
	require.Len(t, stored, 32, "存的是 sha256 摘要,固定 32 字节")
}

// 密码以 argon2id 哈希存储,明文不出现在库里。
func TestPasswordStoredAsArgon2id(t *testing.T) {
	env := newEnv(t)
	pool := testdb.Connect(t).Pool

	acc := registerAndVerify(t, env)

	var hash string
	require.NoError(t, pool.QueryRow(t.Context(),
		`SELECT hash FROM identity.credential WHERE account_id = $1`, acc.ID).Scan(&hash))

	require.True(t, strings.HasPrefix(hash, "$argon2id$"), "哈希算法应为 argon2id,实际: %s", hash)
	require.NotContains(t, hash, "correct-horse-battery")
}

// 连续失败触发锁定,且锁定状态由数据库真实记录。
func TestAccountLockPersistsInDatabase(t *testing.T) {
	env := newEnv(t)
	pool := testdb.Connect(t).Pool

	acc := registerAndVerify(t, env)

	for range 5 {
		rec, body := do(t, env.handler, http.MethodPost, "/api/auth/login", map[string]string{
			"email": acc.Email, "password": "wrong-password",
		}, nil)
		require.NotEqual(t, http.StatusOK, rec.Code)
		require.NotEqual(t, int(apperr.CodeOK), body.Code)
	}

	var failed int
	var lockedUntil *time.Time
	require.NoError(t, pool.QueryRow(t.Context(),
		`SELECT failed_attempts, locked_until FROM identity.credential WHERE account_id = $1`,
		acc.ID).Scan(&failed, &lockedUntil))
	require.Equal(t, 5, failed)
	require.NotNil(t, lockedUntil, "达到阈值后必须写入 locked_until")

	// 锁定期内正确密码也被拒
	rec, body := do(t, env.handler, http.MethodPost, "/api/auth/login", map[string]string{
		"email": acc.Email, "password": "correct-horse-battery",
	}, nil)
	require.Equal(t, http.StatusTooManyRequests, rec.Code)
	require.Equal(t, int(apperr.CodeAccountLocked), body.Code)
}

// 角色授权后立即生效,不需要重新登录 —— 权限点每次请求重新求值。
func TestPermissionTakesEffectWithoutRelogin(t *testing.T) {
	env := newEnv(t)

	acc := registerAndVerify(t, env)

	rec, _ := do(t, env.handler, http.MethodPost, "/api/auth/login", map[string]string{
		"email": acc.Email, "password": "correct-horse-battery",
	}, nil)
	cookie := findCookie(rec.Result().Cookies(), "ygg_session")

	// 授权前:没有权限点
	perms, err := env.svc.PermissionsFor(t.Context(), acc.ID)
	require.NoError(t, err)
	require.Empty(t, perms)

	// 给一个自定义角色并授予
	role, err := env.svc.RBAC.CreateRole(t.Context(), rbac.CreateRoleInput{
		Code:        "mc_operator",
		Name:        "游戏运营",
		Permissions: []string{"minecraft:profile:read"},
	})
	require.NoError(t, err)
	require.NoError(t, env.svc.RBAC.Grant(t.Context(), acc.ID, role.ID, uuid.Nil))

	perms, err = env.svc.PermissionsFor(t.Context(), acc.ID)
	require.NoError(t, err)
	require.Equal(t, []string{"minecraft:profile:read"}, perms)

	can, err := env.svc.RBAC.Can(t.Context(), acc.ID, "minecraft:profile:read")
	require.NoError(t, err)
	require.True(t, can)

	// 旧会话立刻带上新权限
	rec, body := do(t, env.handler, http.MethodGet, "/api/auth/session", nil,
		[]*http.Cookie{cookie})
	require.Equal(t, http.StatusOK, rec.Code)

	var sessionData struct {
		Permissions []string `json:"permissions"`
	}
	require.NoError(t, json.Unmarshal(body.Data, &sessionData))
	require.Equal(t, []string{"minecraft:profile:read"}, sessionData.Permissions)

	// 撤权后立即失效
	require.NoError(t, env.svc.RBAC.Revoke(t.Context(), acc.ID, role.ID))

	rec, body = do(t, env.handler, http.MethodGet, "/api/auth/session", nil,
		[]*http.Cookie{cookie})
	require.Equal(t, http.StatusOK, rec.Code)
	require.NoError(t, json.Unmarshal(body.Data, &sessionData))
	require.Empty(t, sessionData.Permissions, "撤权应立即生效")
}

// 内置角色不可删;持有账号的角色也不能删。
func TestRoleDeletionRules(t *testing.T) {
	env := newEnv(t)

	err := env.svc.RBAC.DeleteRole(t.Context(), mustRoleID(t, env, "platform_admin"))
	require.Error(t, err, "内置角色不可删")
	require.True(t, apperr.Is(err, apperr.CodeForbidden))

	acc := registerAndVerify(t, env)
	role, err := env.svc.RBAC.CreateRole(t.Context(), rbac.CreateRoleInput{
		Code: "temp_role", Name: "临时角色",
	})
	require.NoError(t, err)

	require.NoError(t, env.svc.RBAC.Grant(t.Context(), acc.ID, role.ID, uuid.Nil))
	err = env.svc.RBAC.DeleteRole(t.Context(), role.ID)
	require.Error(t, err, "仍被持有的角色不可删")
	require.True(t, apperr.Is(err, apperr.CodeConflict))

	require.NoError(t, env.svc.RBAC.Revoke(t.Context(), acc.ID, role.ID))
	require.NoError(t, env.svc.RBAC.DeleteRole(t.Context(), role.ID))
}

// 不存在的权限点不允许写进角色 —— 否则会产生永远匹配不上的幽灵条目。
func TestSetPermissionsRejectsUnknownCode(t *testing.T) {
	env := newEnv(t)

	role, err := env.svc.RBAC.CreateRole(t.Context(), rbac.CreateRoleInput{
		Code: "bad_role", Name: "错误角色",
		Permissions: []string{"not:a:real:permission"},
	})
	_ = role
	require.Error(t, err)
	require.True(t, apperr.Is(err, apperr.CodeInvalidArgument))
}

// 审计事件落库,并可按账号检索。
func TestAuditEventsAreRecordedAndSearchable(t *testing.T) {
	env := newEnv(t)
	acc := registerAndVerify(t, env)

	rec, _ := do(t, env.handler, http.MethodPost, "/api/auth/login", map[string]string{
		"email": acc.Email, "password": "correct-horse-battery",
	}, nil)
	cookie := findCookie(rec.Result().Cookies(), "ygg_session")

	rec, body := do(t, env.handler, http.MethodGet, "/api/account/audit", nil,
		[]*http.Cookie{cookie})
	require.Equal(t, http.StatusOK, rec.Code)

	var auditData struct {
		Total  int `json:"total"`
		Events []struct {
			Action  string `json:"action"`
			Outcome string `json:"outcome"`
		} `json:"events"`
	}
	require.NoError(t, json.Unmarshal(body.Data, &auditData))
	require.Positive(t, auditData.Total)

	actions := map[string]bool{}
	for _, e := range auditData.Events {
		actions[e.Action] = true
	}
	require.True(t, actions["account.register"], "注册应留痕")
	require.True(t, actions["account.verify_email"], "邮箱验证应留痕")
	require.True(t, actions["account.login"], "登录应留痕")
}

// 改密码后吊销全部会话。
func TestPasswordChangeRevokesAllSessions(t *testing.T) {
	env := newEnv(t)
	acc := registerAndVerify(t, env)

	login := func() *http.Cookie {
		rec, _ := do(t, env.handler, http.MethodPost, "/api/auth/login", map[string]string{
			"email": acc.Email, "password": "correct-horse-battery",
		}, nil)
		require.Equal(t, http.StatusOK, rec.Code)
		return findCookie(rec.Result().Cookies(), "ygg_session")
	}

	c1, c2 := login(), login()

	rec, _ := do(t, env.handler, http.MethodPatch, "/api/account/password", map[string]string{
		"old_password": "correct-horse-battery",
		"new_password": "brand-new-strong-pass",
	}, []*http.Cookie{c1})
	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())

	// 两个旧会话都必须失效
	for i, c := range []*http.Cookie{c1, c2} {
		rec, _ := do(t, env.handler, http.MethodGet, "/api/account", nil, []*http.Cookie{c})
		require.Equal(t, http.StatusUnauthorized, rec.Code, "第 %d 个旧会话应失效", i)
	}

	// 新密码可以登录
	rec, _ = do(t, env.handler, http.MethodPost, "/api/auth/login", map[string]string{
		"email": acc.Email, "password": "brand-new-strong-pass",
	}, nil)
	require.Equal(t, http.StatusOK, rec.Code)
}

// 跨站写请求必须被 CSRF 中间件拦下。
func TestCSRFProtection(t *testing.T) {
	env := newEnv(t)
	acc := registerAndVerify(t, env)

	rec, _ := do(t, env.handler, http.MethodPost, "/api/auth/login", map[string]string{
		"email": acc.Email, "password": "correct-horse-battery",
	}, nil)
	cookie := findCookie(rec.Result().Cookies(), "ygg_session")

	req := httptest.NewRequest(http.MethodPost, "/api/auth/logout", strings.NewReader("{}"))
	req.Host = "auth.example.com"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://evil.example.com")
	req.AddCookie(cookie)

	rec2 := httptest.NewRecorder()
	env.handler.ServeHTTP(rec2, req)

	require.Equal(t, http.StatusForbidden, rec2.Code, "跨站写请求必须被拒绝")
}

// ---------------------------------------------------------------- 辅助

type testAccount struct {
	ID    uuid.UUID
	Email string
}

func registerAndVerify(t *testing.T, env *env) testAccount {
	t.Helper()

	email := fmt.Sprintf("user-%s@example.com", uuid.NewString()[:8])
	regRec, body := do(t, env.handler, http.MethodPost, "/api/auth/register", map[string]string{
		"username": "player_" + uuid.NewString()[:6],
		"email":    email,
		"password": "correct-horse-battery",
	}, nil)

	require.Equal(t, int(apperr.CodeOK), body.Code, "注册失败: %s", regRec.Body.String())

	var regData struct {
		Account struct {
			ID string `json:"id"`
		} `json:"account"`
		VerifyURL string `json:"verify_url"`
	}
	require.NoError(t, json.Unmarshal(body.Data, &regData))

	id, err := uuid.Parse(regData.Account.ID)
	require.NoError(t, err)

	token := regData.VerifyURL[strings.Index(regData.VerifyURL, "token=")+len("token="):]
	rec, _ := do(t, env.handler, http.MethodPost, "/api/auth/email/verify",
		map[string]string{"token": token}, nil)
	require.Equal(t, http.StatusOK, rec.Code)

	return testAccount{ID: id, Email: email}
}

func mustRoleID(t *testing.T, env *env, code string) uuid.UUID {
	t.Helper()
	roles, err := env.svc.RBAC.ListRoles(t.Context(), "")
	require.NoError(t, err)
	for _, r := range roles {
		if r.Code == code {
			return r.ID
		}
	}
	t.Fatalf("找不到内置角色 %s", code)
	return uuid.Nil
}

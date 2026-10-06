//go:build integration

package admin_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"golang.org/x/crypto/bcrypt"

	"github.com/yggauth/yggauth/internal/admin"
	"github.com/yggauth/yggauth/internal/config"
	"github.com/yggauth/yggauth/internal/identity"
	"github.com/yggauth/yggauth/internal/identity/account"
	"github.com/yggauth/yggauth/internal/identity/audit"
	"github.com/yggauth/yggauth/internal/identity/rbac"
	"github.com/yggauth/yggauth/internal/identity/session"
	"github.com/yggauth/yggauth/internal/oidc"
	"github.com/yggauth/yggauth/internal/platform/apperr"
	"github.com/yggauth/yggauth/internal/platform/clock"
	"github.com/yggauth/yggauth/internal/platform/db"
	"github.com/yggauth/yggauth/internal/platform/db/testdb"
	"github.com/yggauth/yggauth/internal/platform/keys"
	"github.com/yggauth/yggauth/internal/transport"
)

type nopLogger struct{}

func (nopLogger) Error(string, ...any) {}
func (nopLogger) Info(string, ...any)  {}
func (nopLogger) Warn(string, ...any)  {}

type env struct {
	handler       http.Handler
	svc           *identity.Service
	clientService *oidc.ClientService
	keys          *keys.Manager
	// pool 供测试直接写库造数据 —— MC 档案/材质这类由协议流程维护的表,
	// 走一遍登录链路太绕,要测的是后台的查询本身。
	pool *db.Pool
	// settings 是运行时配置快照,用于断言热更新后的读取结果。
	settings *config.Snapshot
	// mailer 是发件器热替换的桩,记录每一次重建与收到的配置 ——
	// 「改 mail.* 立刻生效」没有它就只剩一行日志可测。
	mailer *recordingMailer
}

func newEnv(t *testing.T) *env {
	t.Helper()

	pool := testdb.Fresh(t)
	clk := clock.New()
	// 后台的客户端页要读写签名密钥,这里装配一套真密钥管理器
	testKeys, err := keys.NewManager(keys.Options{
		MasterSecret: "admin-test-secret-at-least-32-bytes-long",
		KidPrefix:    "oidc",
		BitSize:      2048,
	}, keys.NewPGStore(pool))
	require.NoError(t, err)
	clientSvc := oidc.NewClientService(pool, oidc.NewSecretHasher(testSecretHasher))

	// 运行时配置快照:后台改设置的用例要走**和生产同一套**路径
	// (登记表校验 + 敏感值加密),否则测到的是另一套代码。
	settings := config.NewSnapshot(config.SettingsOptions{
		Store:  config.NewSettingStore(pool),
		Sealer: testKeys,
	})

	// 发件器热替换的桩:见 settings_integration_test.go 里的 recordingMailer
	mailerStub := &recordingMailer{}

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
			Settings:            settings,
		},
		Session: session.Config{IdleTTL: 168 * time.Hour, MaxTTL: 720 * time.Hour, Settings: settings},
		Logger:  nopLogger{},
	})

	handler := transport.New(transport.Deps{
		Config:   &config.Config{},
		Logger:   nopLogger{},
		DB:       pool,
		Clock:    clk,
		Settings: settings,
		Identity: transport.IdentityDeps{Service: svc, Handler: identity.NewHandler(svc,
			identity.CookieConfig{Name: "ygg_session", SameSite: http.SameSiteLaxMode},
			"https://auth.example.com"), Cookie: transport.AuthConfig{CookieName: "ygg_session"}},
		Admin: transport.AdminDeps{
			Service: svc,
			Handler: admin.NewHandler(admin.Deps{
				Identity:      svc,
				OIDC:          clientSvc,
				OIDCKeys:      testKeys,
				Settings:      settings,
				Mailer:        mailerStub,
				DB:            pool,
				Logger:        nopLogger{},
				PublicBaseURL: "https://auth.example.com",
			}),
		},
	}).Handler()

	return &env{
		handler: handler, svc: svc, clientService: clientSvc, keys: testKeys,
		pool: pool, settings: settings, mailer: mailerStub,
	}
}

type envelope struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

func call(t *testing.T, h http.Handler, method, path string, body any, cookies []*http.Cookie) (*httptest.ResponseRecorder, envelope) {
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
	req.Host = "admin.example.com"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://admin.example.com")
	for _, c := range cookies {
		req.AddCookie(c)
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	var parsed envelope
	if rec.Body.Len() > 0 {
		_ = json.Unmarshal(rec.Body.Bytes(), &parsed)
	}
	return rec, parsed
}

// makeAdmin 造一个持有 platform_admin 角色的账号,并返回它的会话 cookie。
func makeAdmin(t *testing.T, e *env) *http.Cookie {
	t.Helper()

	email := "admin-" + uuid.NewString()[:8] + "@example.com"
	_, env0 := call(t, e.handler, http.MethodPost, "/api/auth/register", map[string]string{
		"username": "admin_" + uuid.NewString()[:6],
		"email":    email,
		"password": "correct-horse-battery",
	}, nil)
	require.Equal(t, int(apperr.CodeOK), env0.Code)

	var reg struct {
		Account struct {
			ID string `json:"id"`
		} `json:"account"`
		VerifyURL string `json:"verify_url"`
	}
	require.NoError(t, json.Unmarshal(env0.Data, &reg))
	id, err := uuid.Parse(reg.Account.ID)
	require.NoError(t, err)

	token := reg.VerifyURL[strings.Index(reg.VerifyURL, "token=")+len("token="):]
	rec, _ := call(t, e.handler, http.MethodPost, "/api/auth/email/verify",
		map[string]string{"token": token}, nil)
	require.Equal(t, http.StatusOK, rec.Code)

	// 授予平台管理员
	roles, err := e.svc.RBAC.ListRoles(t.Context(), "")
	require.NoError(t, err)
	var adminRole uuid.UUID
	for _, role := range roles {
		if role.Code == "platform_admin" {
			adminRole = role.ID
		}
	}
	require.NotEqual(t, uuid.Nil, adminRole, "迁移里应有 platform_admin 角色")
	require.NoError(t, e.svc.RBAC.Grant(t.Context(), id, adminRole, uuid.Nil))

	rec, _ = call(t, e.handler, http.MethodPost, "/api/auth/login", map[string]string{
		"email": email, "password": "correct-horse-battery",
	}, nil)
	require.Equal(t, http.StatusOK, rec.Code)

	for _, c := range rec.Result().Cookies() {
		if c.Name == "ygg_session" {
			return c
		}
	}
	t.Fatal("登录未下发会话 cookie")
	return nil
}

// makeUser 造一个没有任何权限点的普通账号。
func makeUser(t *testing.T, e *env) (uuid.UUID, *http.Cookie) {
	t.Helper()

	email := "user-" + uuid.NewString()[:8] + "@example.com"
	_, env0 := call(t, e.handler, http.MethodPost, "/api/auth/register", map[string]string{
		"username": "user_" + uuid.NewString()[:6],
		"email":    email,
		"password": "correct-horse-battery",
	}, nil)

	var reg struct {
		Account struct {
			ID string `json:"id"`
		} `json:"account"`
		VerifyURL string `json:"verify_url"`
	}
	require.NoError(t, json.Unmarshal(env0.Data, &reg))
	id, err := uuid.Parse(reg.Account.ID)
	require.NoError(t, err)

	token := reg.VerifyURL[strings.Index(reg.VerifyURL, "token=")+len("token="):]
	rec, _ := call(t, e.handler, http.MethodPost, "/api/auth/email/verify",
		map[string]string{"token": token}, nil)
	require.Equal(t, http.StatusOK, rec.Code)

	rec, _ = call(t, e.handler, http.MethodPost, "/api/auth/login", map[string]string{
		"email": email, "password": "correct-horse-battery",
	}, nil)
	require.Equal(t, http.StatusOK, rec.Code)

	for _, c := range rec.Result().Cookies() {
		if c.Name == "ygg_session" {
			return id, c
		}
	}
	t.Fatal("登录未下发会话 cookie")
	return uuid.Nil, nil
}

// ---------------------------------------------------------------- 测试

func TestAdminEndpointsRequireLogin(t *testing.T) {
	e := newEnv(t)

	for _, path := range []string{
		"/api/admin/me",
		"/api/admin/accounts",
		"/api/admin/roles",
		"/api/admin/audit",
		"/api/admin/settings",
	} {
		rec, body := call(t, e.handler, http.MethodGet, path, nil, nil)
		require.Equal(t, http.StatusUnauthorized, rec.Code, "%s 未登录时应 401", path)
		require.Equal(t, int(apperr.CodeUnauthorized), body.Code)
	}
}

// 普通用户即使登录了,没有权限点也必须被后台接口拒绝。
func TestAdminEndpointsRequirePermission(t *testing.T) {
	e := newEnv(t)
	_, userCookie := makeUser(t, e)

	rec, body := call(t, e.handler, http.MethodGet, "/api/admin/accounts", nil,
		[]*http.Cookie{userCookie})
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Equal(t, int(apperr.CodeForbidden), body.Code)
}

func TestAdminMeAndMenus(t *testing.T) {
	e := newEnv(t)
	cookie := makeAdmin(t, e)

	rec, body := call(t, e.handler, http.MethodGet, "/api/admin/me", nil,
		[]*http.Cookie{cookie})
	require.Equal(t, http.StatusOK, rec.Code)

	var me struct {
		Roles []struct {
			Code string `json:"code"`
		} `json:"roles"`
		Permissions []string `json:"permissions"`
	}
	require.NoError(t, json.Unmarshal(body.Data, &me))
	require.NotEmpty(t, me.Roles)
	require.NotEmpty(t, me.Permissions)

	rec, body = call(t, e.handler, http.MethodGet, "/api/admin/menus", nil,
		[]*http.Cookie{cookie})
	require.Equal(t, http.StatusOK, rec.Code)

	var menus struct {
		Menus []struct {
			Key string `json:"key"`
		} `json:"menus"`
	}
	require.NoError(t, json.Unmarshal(body.Data, &menus))
	require.NotEmpty(t, menus.Menus, "管理员应看到菜单")

	// 普通用户的菜单应被过滤到只剩无权限要求的仪表盘
	_, userCookie := makeUser(t, e)
	_, body = call(t, e.handler, http.MethodGet, "/api/admin/menus", nil,
		[]*http.Cookie{userCookie})
	require.NoError(t, json.Unmarshal(body.Data, &menus))
	for _, m := range menus.Menus {
		require.Equal(t, "dashboard", m.Key, "无权限用户不应看到 %s", m.Key)
	}
}

func TestAdminRoleLifecycle(t *testing.T) {
	e := newEnv(t)
	cookie := makeAdmin(t, e)

	rec, body := call(t, e.handler, http.MethodPost, "/api/admin/roles", map[string]any{
		"code":        "ops_team",
		"name":        "运营组",
		"description": "日常运营",
		"permissions": []string{"account:read", "audit:read"},
	}, []*http.Cookie{cookie})
	require.Equal(t, http.StatusCreated, rec.Code, "body=%s", rec.Body.String())

	var created struct {
		Role struct {
			ID string `json:"id"`
		} `json:"role"`
	}
	require.NoError(t, json.Unmarshal(body.Data, &created))
	roleID := created.Role.ID

	// 权限点列表里能看到它
	rec, body = call(t, e.handler, http.MethodGet, "/api/admin/roles", nil,
		[]*http.Cookie{cookie})
	require.Equal(t, http.StatusOK, rec.Code)

	var roles struct {
		Roles []struct {
			Code        string   `json:"code"`
			Permissions []string `json:"permissions"`
			IsSystem    bool     `json:"is_system"`
		} `json:"roles"`
	}
	require.NoError(t, json.Unmarshal(body.Data, &roles))

	found := false
	for _, r := range roles.Roles {
		if r.Code == "ops_team" {
			found = true
			require.Len(t, r.Permissions, 2)
		}
	}
	require.True(t, found)

	// 修改
	rec, _ = call(t, e.handler, http.MethodPatch, "/api/admin/roles/"+roleID, map[string]any{
		"name":        "运营组 A",
		"description": "改过了",
	}, []*http.Cookie{cookie})
	require.Equal(t, http.StatusOK, rec.Code)

	// 删除
	rec, _ = call(t, e.handler, http.MethodDelete, "/api/admin/roles/"+roleID, nil,
		[]*http.Cookie{cookie})
	require.Equal(t, http.StatusOK, rec.Code)
}

// 不存在的权限点必须被拒绝,否则会产生永远匹配不上的幽灵条目。
func TestAdminRoleRejectsUnknownPermission(t *testing.T) {
	e := newEnv(t)
	cookie := makeAdmin(t, e)

	rec, body := call(t, e.handler, http.MethodPost, "/api/admin/roles", map[string]any{
		"code":        "bad_role",
		"name":        "错误角色",
		"permissions": []string{"no:such:permission"},
	}, []*http.Cookie{cookie})
	require.NotEqual(t, http.StatusCreated, rec.Code)
	require.Equal(t, int(apperr.CodeInvalidArgument), body.Code)
}

// 内置角色不允许通过后台删除。
func TestAdminCannotDeleteSystemRole(t *testing.T) {
	e := newEnv(t)
	cookie := makeAdmin(t, e)

	roles, err := e.svc.RBAC.ListRoles(t.Context(), "")
	require.NoError(t, err)

	for _, role := range roles {
		if !role.IsSystem {
			continue
		}
		rec, body := call(t, e.handler, http.MethodDelete, "/api/admin/roles/"+role.ID.String(),
			nil, []*http.Cookie{cookie})
		require.Equal(t, http.StatusForbidden, rec.Code, "内置角色 %s 不可删", role.Code)
		require.Equal(t, int(apperr.CodeForbidden), body.Code)
	}
}

func TestAdminGrantAndRevokeRole(t *testing.T) {
	e := newEnv(t)
	cookie := makeAdmin(t, e)
	userID, _ := makeUser(t, e)

	rec, body := call(t, e.handler, http.MethodPost, "/api/admin/roles", map[string]any{
		"code": "read_only", "name": "只读", "permissions": []string{"account:read"},
	}, []*http.Cookie{cookie})
	require.Equal(t, http.StatusCreated, rec.Code)

	var created struct {
		Role struct {
			ID string `json:"id"`
		} `json:"role"`
	}
	require.NoError(t, json.Unmarshal(body.Data, &created))

	rec, _ = call(t, e.handler, http.MethodPost, "/api/admin/roles/grant", map[string]string{
		"account_id": userID.String(),
		"role_id":    created.Role.ID,
	}, []*http.Cookie{cookie})
	require.Equal(t, http.StatusOK, rec.Code)

	can, err := e.svc.RBAC.Can(t.Context(), userID, "account:read")
	require.NoError(t, err)
	require.True(t, can)

	rec, _ = call(t, e.handler, http.MethodPost, "/api/admin/roles/revoke", map[string]string{
		"account_id": userID.String(),
		"role_id":    created.Role.ID,
	}, []*http.Cookie{cookie})
	require.Equal(t, http.StatusOK, rec.Code)

	can, err = e.svc.RBAC.Can(t.Context(), userID, "account:read")
	require.NoError(t, err)
	require.False(t, can)
}

func TestAdminDisableAccount(t *testing.T) {
	e := newEnv(t)
	cookie := makeAdmin(t, e)
	userID, userCookie := makeUser(t, e)

	rec, _ := call(t, e.handler, http.MethodPatch, "/api/admin/accounts/"+userID.String(),
		map[string]string{"status": "disabled"}, []*http.Cookie{cookie})
	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())

	// 被禁用账号的现有登录态必须立即失效
	rec, body := call(t, e.handler, http.MethodGet, "/api/account", nil,
		[]*http.Cookie{userCookie})
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Equal(t, int(apperr.CodeAccountDisabled), body.Code)

	// 也不能再登录
	rec, body = call(t, e.handler, http.MethodPost, "/api/auth/login", map[string]string{
		"email": "", "password": "",
	}, nil)
	require.NotEqual(t, http.StatusOK, rec.Code)
	require.NotEqual(t, int(apperr.CodeOK), body.Code)
}

// 管理员不能把自己禁用 —— 否则可能把自己锁在门外。
func TestAdminCannotDisableSelf(t *testing.T) {
	e := newEnv(t)
	cookie := makeAdmin(t, e)

	_, body := call(t, e.handler, http.MethodGet, "/api/admin/me", nil,
		[]*http.Cookie{cookie})
	var me struct {
		Account struct {
			ID string `json:"id"`
		} `json:"account"`
	}
	require.NoError(t, json.Unmarshal(body.Data, &me))

	rec, resp := call(t, e.handler, http.MethodPatch,
		"/api/admin/accounts/"+me.Account.ID,
		map[string]string{"status": "disabled"}, []*http.Cookie{cookie})
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Equal(t, int(apperr.CodeForbidden), resp.Code)
}

func TestAdminAuditSearchAndExport(t *testing.T) {
	e := newEnv(t)
	cookie := makeAdmin(t, e)
	makeUser(t, e)

	rec, body := call(t, e.handler, http.MethodGet, "/api/admin/audit?limit=100", nil,
		[]*http.Cookie{cookie})
	require.Equal(t, http.StatusOK, rec.Code)

	var search struct {
		Total int `json:"total"`
	}
	require.NoError(t, json.Unmarshal(body.Data, &search))
	require.Positive(t, search.Total)

	rec = callRecorder(t, e.handler, "/api/admin/audit/export?limit=100", cookie)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "text/csv; charset=utf-8", rec.Header().Get("Content-Type"))
	require.Contains(t, rec.Header().Get("Content-Disposition"), "audit-")
	body1 := rec.Body.String()
	require.Contains(t, body1, "occurred_at")
	require.Contains(t, body1, "account.login")
}

func TestAdminSettings(t *testing.T) {
	e := newEnv(t)
	cookie := makeAdmin(t, e)

	rec, body := call(t, e.handler, http.MethodGet, "/api/admin/settings", nil,
		[]*http.Cookie{cookie})
	require.Equal(t, http.StatusOK, rec.Code)

	var settings struct {
		Settings map[string]json.RawMessage `json:"settings"`
	}
	require.NoError(t, json.Unmarshal(body.Data, &settings))
	require.Contains(t, settings.Settings, "registration.mode",
		"迁移里应写入默认配置")

	// 修改
	rec, body = call(t, e.handler, http.MethodPatch, "/api/admin/settings",
		map[string]any{"password.min_length": 10}, []*http.Cookie{cookie})
	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())
	require.NoError(t, json.Unmarshal(body.Data, &settings))
	require.JSONEq(t, "10", string(settings.Settings["password.min_length"]))
}

func TestAdminInvitations(t *testing.T) {
	e := newEnv(t)
	cookie := makeAdmin(t, e)

	rec, body := call(t, e.handler, http.MethodPost, "/api/admin/invitations",
		map[string]any{"max_uses": 2, "days": 3}, []*http.Cookie{cookie})
	require.Equal(t, http.StatusCreated, rec.Code, "body=%s", rec.Body.String())

	var inv struct {
		Code    string `json:"code"`
		MaxUses int32  `json:"max_uses"`
	}
	require.NoError(t, json.Unmarshal(body.Data, &inv))
	require.NotEmpty(t, inv.Code, "未指定 code 时应自动生成")
	require.Equal(t, int32(2), inv.MaxUses)

	rec, body = call(t, e.handler, http.MethodGet, "/api/admin/invitations", nil,
		[]*http.Cookie{cookie})
	require.Equal(t, http.StatusOK, rec.Code)

	var list struct {
		Invitations []struct {
			Code string `json:"code"`
		} `json:"invitations"`
	}
	require.NoError(t, json.Unmarshal(body.Data, &list))
	require.Len(t, list.Invitations, 1)
	require.Equal(t, inv.Code, list.Invitations[0].Code)
}

// 后台的两个 MC 列表:菜单里一直有入口,点进去却只有「功能暂未开放」的占位页。
func TestAdminMCProfilesAndTextures(t *testing.T) {
	e := newEnv(t)
	cookie := makeAdmin(t, e)

	// 直接写库造数据。这两张表由 MC 登录流程维护,走一遍协议链路太绕,
	// 这里要测的是后台的列表查询本身。
	_, err := e.pool.Exec(t.Context(), `
INSERT INTO minecraft.profile (account_id, uuid, current_name)
SELECT a.id, gen_random_uuid(), 'Admin_Player'
FROM identity.account a
ORDER BY a.created_at
LIMIT 1`)
	require.NoError(t, err)

	rec, body := call(t, e.handler, http.MethodGet,
		"/api/admin/mc/profiles?search=player", nil, []*http.Cookie{cookie})
	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())

	var profiles struct {
		Profiles []struct {
			CurrentName string `json:"current_name"`
			UUID        string `json:"uuid"`
			HasSkin     bool   `json:"has_skin"`
			HasCape     bool   `json:"has_cape"`
		} `json:"profiles"`
		Total int `json:"total"`
	}
	require.NoError(t, json.Unmarshal(body.Data, &profiles))
	require.Equal(t, 1, profiles.Total)
	require.Len(t, profiles.Profiles, 1)
	require.Equal(t, "Admin_Player", profiles.Profiles[0].CurrentName)
	require.NotEmpty(t, profiles.Profiles[0].UUID)
	require.False(t, profiles.Profiles[0].HasSkin, "没上传过皮肤时不该显示已上传")

	// 搜索是子串匹配,且不解释通配符:输入 % 应当只匹配名字里真有 % 的档案。
	rec, body = call(t, e.handler, http.MethodGet,
		"/api/admin/mc/profiles?search=%25", nil, []*http.Cookie{cookie})
	require.Equal(t, http.StatusOK, rec.Code)
	require.NoError(t, json.Unmarshal(body.Data, &profiles))
	require.Zero(t, profiles.Total, "% 不该被当成通配符")

	// 材质
	_, err = e.pool.Exec(t.Context(), `
INSERT INTO minecraft.texture (hash, type, size, width, height, ref_count)
VALUES (md5('admin-test'), 'skin', 2048, 64, 64, 1)`)
	require.NoError(t, err)

	rec, body = call(t, e.handler, http.MethodGet,
		"/api/admin/mc/textures?kind=skin", nil, []*http.Cookie{cookie})
	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())

	var textures struct {
		Textures []struct {
			Hash     string `json:"hash"`
			Type     string `json:"type"`
			Width    int32  `json:"width"`
			Height   int32  `json:"height"`
			RefCount int32  `json:"ref_count"`
		} `json:"textures"`
		Total int `json:"total"`
	}
	require.NoError(t, json.Unmarshal(body.Data, &textures))
	require.Equal(t, 1, textures.Total)
	require.Len(t, textures.Textures, 1)
	require.Equal(t, "skin", textures.Textures[0].Type)
	require.Equal(t, int32(64), textures.Textures[0].Width)
	require.Equal(t, int32(1), textures.Textures[0].RefCount)

	// kind 拼错要明说,不能静默不过滤
	rec, resp := call(t, e.handler, http.MethodGet,
		"/api/admin/mc/textures?kind=capee", nil, []*http.Cookie{cookie})
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Equal(t, int(apperr.CodeInvalidArgument), resp.Code)

	// 没有权限点的普通用户一律 403
	_, userCookie := makeUser(t, e)
	for _, path := range []string{"/api/admin/mc/profiles", "/api/admin/mc/textures"} {
		rec, _ := call(t, e.handler, http.MethodGet, path, nil, []*http.Cookie{userCookie})
		require.Equal(t, http.StatusForbidden, rec.Code, "%s 对无权限用户应 403", path)
	}
}

func callRecorder(t *testing.T, h http.Handler, path string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, strings.NewReader(""))
	req.Host = "admin.example.com"
	req.Header.Set("Origin", "https://admin.example.com")
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// testSecretHasher 是后台测试用的客户端密钥哈希器。
//
// 用 bcrypt 的最低成本参数:这里测的是「密钥能创建、能轮换、列表里不泄露」,
// 不是抗暴力破解的成本。
func testSecretHasher(secret string) (string, error) {
	digest, err := bcrypt.GenerateFromPassword([]byte(secret), bcrypt.MinCost)
	if err != nil {
		return "", err
	}
	return string(digest), nil
}

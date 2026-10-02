//go:build integration

package minecraft_test

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
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
	"github.com/yggauth/yggauth/internal/minecraft"
	"github.com/yggauth/yggauth/internal/oidc"
	"github.com/yggauth/yggauth/internal/platform/clock"
	"github.com/yggauth/yggauth/internal/platform/db/testdb"
	"github.com/yggauth/yggauth/internal/platform/keys"
	"github.com/yggauth/yggauth/internal/platform/storage"
	"github.com/yggauth/yggauth/internal/transport"
)

const (
	testIssuer = "https://auth.example.com"
	testHost   = "auth.example.com"
	// serverSecret 是测试用的预共享密钥
	serverSecret = "test-shared-secret-do-not-use-in-prod"
)

// nopLogger 是测试用日志器。
type nopLogger struct{}

func (nopLogger) Info(string, ...any)  {}
func (nopLogger) Warn(string, ...any)  {}
func (nopLogger) Error(string, ...any) {}

// env 是装配好的 MC 域测试环境。
type env struct {
	handler http.Handler
	svc     *minecraft.Service
	clk     *clock.Mock
	keys    *keys.Manager
	// textureDir 是皮肤存储根,用于断言去重后磁盘上的文件数
	textureDir string
	textures   *minecraft.TextureService
}

func testConfig() *config.Config {
	return &config.Config{
		App:  config.App{PublicBaseURL: testIssuer, TrustProxyHeaders: false},
		Log:  config.Log{},
		Auth: config.Auth{SessionIdleTTL: 168 * time.Hour, SessionMaxTTL: 720 * time.Hour},
		OIDC: config.OIDC{
			KeyMasterSecret: "mc-test-master-secret-at-least-32-bytes",
			CookieName:      "ygg_session",
			CookieSecure:    false,
			CookieSameSite:  "Lax",
		},
		MC: config.MC{
			Enabled:            true,
			ServerSharedSecret: serverSecret,
			HasJoinedWindow:    3 * time.Minute,
			NameRetentionDays:  90,
		},
	}
}

func newEnv(t *testing.T) *env { return newEnvWith(t, false) }

// newEnvReadOnly 造一个只读模式的皮肤站。
func newEnvReadOnly(t *testing.T) *env { return newEnvWith(t, true) }

// newEnvWith 装配一个测试环境。
func newEnvWith(t *testing.T, readOnly bool) *env {
	t.Helper()
	return newEnvWithStorage(t, readOnly, "")
}

// newEnvWithStorage 额外指定皮肤存储根。
//
// 复用同一个存储根的场景:先在可写环境里上传,再切到只读环境,
// 验证只读只挡上传、不挡下载。用一个全新的临时目录会
// 让下载测试因为「文件根本没在这台机器上」而通过,证明不了任何事。
func newEnvWithStorage(t *testing.T, readOnly bool, textureDir string) *env {
	t.Helper()

	pool := testdb.Fresh(t)
	// 从真实时间起步:令牌有效期有一半是数据库 now() 算的,
	// 模拟时钟与数据库时钟差太远会让「刚签发的令牌」立刻显得过期。
	clk := clock.NewMock(time.Now())
	cfg := testConfig()

	if textureDir == "" {
		textureDir = t.TempDir()
	}

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

	mcKeys, err := keys.NewManager(keys.Options{
		MasterSecret: cfg.OIDC.KeyMasterSecret,
		KidPrefix:    "mc",
		BitSize:      2048,
	}, minecraft.NewMCKeyStore(pool))
	require.NoError(t, err)
	_, err = mcKeys.Ensure(t.Context())
	require.NoError(t, err)

	svc := minecraft.NewService(pool, mcGateway(identitySvc), clk, minecraft.Options{
		FallbackSecret:    cfg.MC.ServerSharedSecret,
		TokenTTL:          24 * time.Hour,
		HasJoinedWindow:   cfg.MC.HasJoinedWindow,
		NameRetentionDays: cfg.MC.NameRetentionDays,
	})

	// 密钥解析器要指向服务自己:登记在册的服务器密钥必须优先于兜底密钥。
	svc.WithSecretResolver(svc.ResolveSecret)

	// ---- 皮肤站
	textureStore, err := storage.NewLocal(storage.LocalOptions{Root: textureDir})
	require.NoError(t, err)
	avatarStore, err := storage.NewLocal(storage.LocalOptions{Root: filepath.Join(textureDir, "avatars")})
	require.NoError(t, err)

	textureService := minecraft.NewTextureService(pool, textureStore, clk,
		minecraft.TextureOptions{Storage: textureStore, MaxSize: 2 << 20, ReadOnly: readOnly},
		nil)

	avatarService := minecraft.NewAvatarService(pool, avatarStore, textureService, minecraft.AvatarOptions{
		QueueSize: 64, DefaultSize: 64, Workers: 2, Logger: nopLogger{},
	})
	t.Cleanup(avatarService.Close)

	mcHandler := minecraft.NewHandler(minecraft.HandlerDeps{
		Service:      svc,
		Issuer:       cfg.App.BaseURL(),
		SkinDomain:   cfg.App.BaseURL(),
		PublicKeyPEM: minecraft.PublicKeyPEM(mcKeys),
	})

	textureHandler := minecraft.NewTextureHandler(minecraft.TextureHandlerDeps{
		Service:     textureService,
		MainService: svc,
		Avatars:     avatarService,
		Profiles:    svc,
		URLBase:     cfg.App.BaseURL(),
		MaxSize:     2 << 20,
		ReadOnly:    readOnly,
	})

	// ---- OIDC
	//
	// 也要装上:令牌隔离测试需要「两个域都在」才有意义。
	// 只装 MC 再拿 MC 令牌打 /oauth 得到的是 404,证明不了任何隔离性。
	oidcKeys, err := keys.NewManager(keys.Options{
		MasterSecret: cfg.OIDC.KeyMasterSecret,
		KidPrefix:    "oidc",
		BitSize:      2048,
	}, keys.NewPGStore(pool))
	require.NoError(t, err)

	oidcServer, err := oidc.NewServer(cfg, pool, clk, oidcKeys, nopLogger{})
	require.NoError(t, err)

	adapter := oidc.NewSessionAdapter(identitySvc.Sessions, identitySvc, oidc.CookieConfig{
		Name: "ygg_session", Secure: false, SameSite: http.SameSiteLaxMode,
	})
	oidcHandler := oidc.NewHandler(oidc.HandlerDeps{
		Server:   oidcServer,
		Cookies:  oidcServer.CookieConfig(),
		Sessions: adapter,
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
			Cookie:  transport.AuthConfig{CookieName: "ygg_session"},
			Handler: identity.NewHandler(identitySvc, identity.CookieConfig{
				Name: "ygg_session", Secure: false, SameSite: http.SameSiteLaxMode, IdleTTL: 168 * time.Hour,
			}, testIssuer),
		},
		MC: transport.MCDeps{
			Service:        svc,
			Handler:        mcHandler,
			Keys:           mcKeys,
			AccountAPI:     minecraft.NewAccountAPIHandler(svc),
			Avatars:        avatarService,
			Textures:       textureHandler,
			TextureService: textureService,
		},
		OIDC: transport.OIDCDeps{
			Server:  oidcServer,
			Handler: oidcHandler,
			SSO:     oidc.NewSSO(oidcHandler),
		},
	}).Handler()

	return &env{
		handler:    handler,
		svc:        svc,
		clk:        clk,
		keys:       mcKeys,
		textureDir: textureDir,
		textures:   textureService,
	}
}

// post 发起一次 JSON POST。
func (e *env) post(t *testing.T, path string, body any, bearer string) *httptest.ResponseRecorder {
	t.Helper()

	var reader *strings.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		require.NoError(t, err)
		reader = strings.NewReader(string(raw))
	} else {
		reader = strings.NewReader("")
	}

	req := httptest.NewRequest(http.MethodPost, path, reader)
	req.Host = testHost
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", testIssuer)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}

	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	return rec
}

// get 发起一次 GET。
func (e *env) get(t *testing.T, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Host = testHost
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	return rec
}

// 确认这些测试用的辅助类型满足编译期约束。
var (
	_ = sha1.New
	_ = hex.EncodeToString
)

// mcGateway 把身份内核适配成 MC 域需要的窄接口。
func mcGateway(identitySvc *identity.Service) minecraft.AccountGateway {
	return minecraft.NewAccountGateway(
		func(ctx context.Context, identifier, password string) (uuid.UUID, error) {
			return identitySvc.AuthenticateForGame(ctx, identifier, password, "", "")
		},
		identitySvc.GameLoginEnabled,
		identitySvc.SetGameLoginEnabled,
		func(ctx context.Context, accountID uuid.UUID) (string, error) {
			acc, err := identitySvc.LookupAccount(ctx, accountID)
			if err != nil {
				return "", err
			}
			return acc.Username, nil
		},
	)
}

// registerAndVerify 注册账号并完成邮箱验证,返回 (用户名, 密码)。
func registerAndVerify(t *testing.T, e *env, username, password string) {
	t.Helper()

	rec := e.post(t, "/api/auth/register", map[string]string{
		"username": username,
		"email":    username + "@example.com",
		"password": password,
	}, "")
	require.Equal(t, http.StatusCreated, rec.Code, "注册失败: %s", rec.Body.String())

	var created struct {
		Data struct {
			VerifyURL string `json:"verify_url"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &created))

	parsed, err := url.Parse(created.Data.VerifyURL)
	require.NoError(t, err)

	rec = e.post(t, "/api/auth/email/verify",
		map[string]string{"token": parsed.Query().Get("token")}, "")
	require.Equal(t, http.StatusOK, rec.Code, "验证邮箱失败: %s", rec.Body.String())
}

// authenticate 走一次 /mc/authenticate,返回 accessToken 与 playerUUID。
func (e *env) authenticate(t *testing.T, username, password, clientToken string) (token, playerUUID, name string) {
	t.Helper()

	rec := e.post(t, "/mc/authenticate", map[string]string{
		"username":    username,
		"password":    password,
		"clientToken": clientToken,
	}, "")
	require.Equal(t, http.StatusOK, rec.Code, "MC 登录失败: %s", rec.Body.String())

	var out struct {
		AccessToken     string `json:"accessToken"`
		ClientToken     string `json:"clientToken"`
		SelectedProfile struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"selectedProfile"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	return out.AccessToken, out.SelectedProfile.ID, out.SelectedProfile.Name
}

// expectedServerIDHash 按 MC 端的算法重算签名。
//
// 刻意**独立实现**一遍而不是调用 minecraft.ServerIDHash ——
// 如果两边共用同一个函数,测试只能证明「实现与实现一致」,
// 证明不了「实现与协议一致」。这正是本文件存在的意义。
func expectedServerIDHash(serverID, sharedSecret, profileUUID string) string {
	h := sha1.New()
	h.Write([]byte(serverID))
	h.Write([]byte(sharedSecret))
	h.Write([]byte(profileUUID))
	return hex.EncodeToString(h.Sum(nil))
}

// newRecorder 返回一个响应记录器。
func newRecorder() *httptest.ResponseRecorder { return httptest.NewRecorder() }

// sha1Hex 独立实现的 sha1 十六进制,供「拼接顺序错误」用例使用。
func sha1Hex(in string) string {
	sum := sha1.Sum([]byte(in))
	return hex.EncodeToString(sum[:])
}

// toUpper 供「uuid 大写」用例使用。
func toUpper(in string) string { return strings.ToUpper(in) }

// webLogin 注册账号、验证邮箱并登录,返回会话 cookie。
//
// **不**碰 MC 档案:测试里大多数用例要的是「一个已登录的浏览器会话」,
// 而 MC 档案要等用户走过 /mc/authenticate 才存在。顺手在这里查档案
// 会让整条链在第一次 MC 登录之前就失败。
func webLogin(t *testing.T, e *env, username, password string) *http.Cookie {
	t.Helper()

	registerAndVerify(t, e, username, password)

	// 顺手走一次 MC 登录:纹理绑定挂在 MC 档案上,
	// 而档案要等 /mc/authenticate 才建出来。少这一步,
	// 后续所有材质接口都会以「尚未绑定」拒绝。
	e.authenticate(t, username, password, "web-login")

	rec := e.post(t, "/api/auth/login", map[string]string{
		"email":    username + "@example.com",
		"password": password,
	}, "")
	require.Equal(t, http.StatusOK, rec.Code, "登录失败: %s", rec.Body.String())

	for _, c := range rec.Result().Cookies() {
		if c.Name == "ygg_session" {
			return c
		}
	}
	t.Fatal("登录成功但没有下发会话 cookie")
	return nil
}

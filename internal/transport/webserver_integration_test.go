//go:build integration

package transport_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/google/uuid"
	"github.com/yggauth/yggauth/internal/admin"
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
	webHost = "auth.example.com"
	webBase = "https://auth.example.com"
)

// newSPAHandler 装配一个带 SPA、账号内核、OIDC 与 MC 的完整路由。
//
// 三个域**都要装上**:域隔离测试需要「多个域同时存在」才有意义。
// 只装账号内核再断言 OIDC 端点正常,拿到的是 404,
// 证明不了任何隔离性 —— 这正是 M4 令牌隔离测试踩过的坑。
func newSPAHandler(t *testing.T) http.Handler {
	t.Helper()

	pool := testdb.Fresh(t)
	clk := clock.New()
	cfg := testConfig()
	cfg.App.PublicBaseURL = webBase
	cfg.Auth.SessionIdleTTL = 168 * time.Hour
	cfg.Auth.SessionMaxTTL = 720 * time.Hour
	cfg.OIDC.KeyMasterSecret = "web-test-master-secret-at-least-32-bytes"
	cfg.MC.ServerSharedSecret = "web-test-shared-secret"
	cfg.MC.HasJoinedWindow = 3 * time.Minute

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

	// ---- OIDC
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

	// ---- MC
	mcKeys, err := keys.NewManager(keys.Options{
		MasterSecret: cfg.OIDC.KeyMasterSecret,
		KidPrefix:    "mc",
		BitSize:      2048,
	}, minecraft.NewMCKeyStore(pool))
	require.NoError(t, err)
	_, err = mcKeys.Ensure(t.Context())
	require.NoError(t, err)

	mcService := minecraft.NewService(pool, mcGateway(identitySvc), clk, minecraft.Options{
		FallbackSecret:    cfg.MC.ServerSharedSecret,
		TokenTTL:          24 * time.Hour,
		HasJoinedWindow:   cfg.MC.HasJoinedWindow,
		NameRetentionDays: 90,
	})
	// 密钥解析器要指向服务自己:登记在册的服务器密钥必须优先于兜底密钥。
	mcService.WithSecretResolver(mcService.ResolveSecret)

	textureDir := t.TempDir()
	textureStore, err := storage.NewLocal(storage.LocalOptions{Root: textureDir})
	require.NoError(t, err)
	avatarStore, err := storage.NewLocal(storage.LocalOptions{Root: filepath.Join(textureDir, "avatars")})
	require.NoError(t, err)

	textureService := minecraft.NewTextureService(pool, textureStore, clk,
		minecraft.TextureOptions{Storage: textureStore, MaxSize: 2 << 20}, nil)
	avatarService := minecraft.NewAvatarService(pool, avatarStore, textureService, minecraft.AvatarOptions{
		QueueSize: 64, DefaultSize: 64, Workers: 1, Logger: nopLogger{},
	})
	t.Cleanup(avatarService.Close)

	return transport.New(transport.Deps{
		Admin: transport.AdminDeps{
			Service: identitySvc,
			Handler: admin.NewHandler(admin.Deps{
				Identity:      identitySvc,
				Settings:      config.NewSettingStore(pool),
				OIDC:          oidc.NewClientService(pool, oidc.NewSecretHasher(secretHasher)),
				OIDCKeys:      oidcKeys,
				DB:            pool,
				Logger:        nopLogger{},
				PublicBaseURL: webBase,
			}),
		},
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
		OIDC: transport.OIDCDeps{
			Server:  oidcServer,
			Handler: oidcHandler,
			SSO:     oidc.NewSSO(oidcHandler),
		},
		MC: transport.MCDeps{
			Service:        mcService,
			Handler:        minecraft.NewHandler(minecraft.HandlerDeps{Service: mcService, Issuer: webBase, SkinDomain: webBase, PublicKeyPEM: minecraft.PublicKeyPEM(mcKeys)}),
			Keys:           mcKeys,
			AccountAPI:     minecraft.NewAccountAPIHandler(mcService),
			Avatars:        avatarService,
			Textures:       minecraft.NewTextureHandler(minecraft.TextureHandlerDeps{Service: textureService, MainService: mcService, Avatars: avatarService, Profiles: mcService, URLBase: webBase, MaxSize: 2 << 20}),
			TextureService: textureService,
		},
	}).Handler()
}

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

// get 发起一次 GET。
func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	return request(t, h, http.MethodGet, path, "")
}

// nopLogger 是测试用日志器。
type nopLogger struct{}

func (nopLogger) Info(string, ...any)  {}
func (nopLogger) Warn(string, ...any)  {}
func (nopLogger) Error(string, ...any) {}

// secretHasher 是测试用的客户端密钥哈希器。
//
// 直接返回明文:测试环境里没有 bcrypt 的性能顾虑,而引入真实
// 哈希会让「创建客户端」这条用例每次多花上百毫秒。
func secretHasher(secret string) (string, error) { return secret, nil }

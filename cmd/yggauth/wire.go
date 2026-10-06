package main

import (
	"context"
	"log/slog"
	"net/http"
	"time"

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
	"github.com/yggauth/yggauth/internal/platform/db"
	"github.com/yggauth/yggauth/internal/platform/keys"
	"github.com/yggauth/yggauth/internal/platform/mailer"
	"github.com/yggauth/yggauth/internal/platform/ratelimit"
	"github.com/yggauth/yggauth/internal/platform/storage"

	"github.com/yggauth/yggauth/internal/transport"
)

// buildDeps 在编译期把各业务域显式装配起来(ADR-011)。
//
// 没有运行期注册的插件中心:少 import 一个域,这里就编译不过。
// 装配关系一目了然,比反射式的容器更可靠。
func buildDeps(cfg *config.Config, logger *slog.Logger, pool *db.Pool) transport.Deps {
	clk := clock.New()

	//
	// 签名密钥管理器做「有就复用、无就生成」,所以首次部署不需要手工初始化步骤。
	//
	// 它建在账号内核**之前**:密钥管理器同时是配置表敏感值的加解密器
	// (同一把 KEY_MASTER_SECRET、同一段 AES-256-GCM 实现),
	// 而密码策略、注册模式这些内核参数都要先从配置快照读。
	keyManager, err := keys.NewManager(keys.Options{
		MasterSecret: cfg.OIDC.KeyMasterSecret,
		KidPrefix:    "oidc",
		BitSize:      2048,
	}, keys.NewPGStore(pool))
	if err != nil {
		// 装配失败属于配置错误,直接 panic:带着半套授权服务启动的实例
		// 比启动失败更难排查,而且会对外提供「看起来能用」的授权端点
		panic("初始化签名密钥失败: " + err.Error())
	}

	// ---- 运行时配置(L1)
	//
	// env 是首次启动的种子,setting 是运行时的现值(docs/configuration.md 三)。
	// Sync 负责把种子回填进缺行/没人改过的行,并把「改了 env 也不会生效的键」
	// 打进启动日志 —— 调用点在迁移之后,所以这里的表一定是新的。
	settings := config.NewSnapshot(config.SettingsOptions{
		Store:  config.NewSettingStore(pool),
		Sealer: keyManager,
		Cfg:    cfg,
		Logger: logger,
	})
	if err := settings.Sync(context.Background()); err != nil {
		logger.Warn("settings: 启动回填失败,本轮按库里的现值运行", "error", err)
	}

	// ---- 发件器(可热替换)
	//
	// 初值取**设置里的现值**而不是 env:库里已经配好的 SMTP,
	// 不该因为重启一次就退回 .env 的旧值。读不出来时退回 env ——
	// 一行配置读失败不该让整个邮件功能消失。
	mailCfg, err := settings.MailerConfig(context.Background())
	if err != nil {
		logger.Warn("mail: 读取设置失败,本轮按 env 配置发信", "error", err)
		mailCfg = mailerCfg(cfg)
	}
	mailerHot := mailer.NewHot(mailCfg, logger)

	// ---- 账号内核
	identitySvc := identity.New(identity.Deps{
		Accounts: account.NewPgRepository(pool),
		Tokens:   account.NewPgTokenStore(pool),
		Sessions: session.NewPgRepository(pool),
		RBAC:     rbac.NewPgRepository(pool),
		Audit:    audit.NewPgRepository(pool),

		Hasher: newHasher(cfg),
		Clock:  clk,
		Account: account.Config{
			Settings:            settings,
			Policy:              newPolicy(cfg),
			LoginEnabledDefault: cfg.Auth.MCLoginDefault,
			Invitations:         account.NewInvitationStore(pool),
			MaxFailedAttempts:   cfg.Auth.LoginMaxFailedAttempts,
			LockDuration:        cfg.Auth.LoginLockDuration,
			EmailTokenTTL:       cfg.Auth.EmailTokenTTL,
			RegistrationMode:    cfg.Auth.RegistrationMode,
			Mailer:              mailerHot,
			// 邮件真的会送到收件人手里时,验证令牌只从邮件这一条路出去,
			// 注册响应不再内联 verify_url(见 account.Config.HideVerifyURL)
			HideVerifyURL: mailCfg.Transport == "smtp",
			// 邮件里的验证/重置链接用它拼(BaseURL 已去掉结尾斜杠)
			PublicBaseURL:    cfg.App.BaseURL(),
			VerifyCooldown:   cfg.Mail.VerifyCooldown,
			VerifyDailyLimit: cfg.Mail.VerifyDailyLimit,
		},
		Session: session.Config{
			Settings:       settings,
			IdleTTL:        cfg.Auth.SessionIdleTTL,
			MaxTTL:         cfg.Auth.SessionMaxTTL,
			MaxConcurrent:  0,
			RenewThreshold: cfg.Auth.SessionIdleTTL / 2,
		},
		Logger: loggerAdapter{logger},
	})

	identityHandler := identity.NewHandler(
		identitySvc,
		identity.CookieConfig{
			Name:              cfg.Auth.SessionCookieName,
			Domain:            cookieDomain(cfg),
			Secure:            cfg.OIDC.CookieSecure,
			SameSite:          sameSite(cfg.OIDC.CookieSameSite),
			IdleTTL:           cfg.Auth.SessionIdleTTL,
			TrustProxyHeaders: cfg.App.TrustProxyHeaders,
		},
		cfg.App.BaseURL(),
	)

	//
	// 密钥管理器在函数开头就建好了(它还是配置敏感值的 Sealer),
	// 这里不再重复创建。
	oidcServer, err := oidc.NewServer(cfg, pool, clk, keyManager, loggerAdapter{logger})
	if err != nil {
		panic("装配授权服务失败: " + err.Error())
	}

	sessionAdapter := oidc.NewSessionAdapter(identitySvc.Sessions, identitySvc, oidc.CookieConfig{
		Name:     cfg.OIDC.CookieName,
		Domain:   cookieDomain(cfg),
		Secure:   cfg.OIDC.CookieSecure,
		SameSite: sameSite(cfg.OIDC.CookieSameSite),
		MaxAge:   int(cfg.Auth.SessionIdleTTL.Seconds()),
	})

	deviceService := oidc.NewDeviceService(pool, clk.Now, cfg.OIDC.DeviceCodeTTL, devicePollInterval(cfg))

	oidcHandler := oidc.NewHandler(oidcHandlerDeps(cfg, oidcServer, sessionAdapter, deviceService, loggerAdapter{logger}))
	// ---- 管理后台
	// ---- Minecraft 认证域
	//
	// 依赖方向单向:MC 域 → 账号内核。MC 用自己的令牌表与签名密钥,
	// 与 OIDC 完全隔离(ADR-003),任何一边故障都不该影响另一边。
	mcKeys, err := keys.NewManager(keys.Options{
		MasterSecret: cfg.OIDC.KeyMasterSecret,
		KidPrefix:    "mc",
		BitSize:      2048,
	}, minecraft.NewMCKeyStore(pool))
	if err != nil {
		panic("初始化 MC 签名密钥失败: " + err.Error())
	}
	if _, err := mcKeys.Ensure(context.Background()); err != nil {
		panic("准备 MC 签名密钥失败: " + err.Error())
	}
	mcService := minecraft.NewService(pool, mcGateway(identitySvc), clk, minecraft.Options{
		FallbackSecret:    cfg.MC.ServerSharedSecret,
		TokenTTL:          mcTokenTTL(cfg),
		HasJoinedWindow:   cfg.MC.HasJoinedWindow,
		NameRetentionDays: cfg.MC.NameRetentionDays,
		Settings:          settings,
	})
	mcService.WithSecretResolver(mcServiceResolver(pool))
	mcService.WithSecretResolver(mcServiceResolver(pool))

	// ---- 皮肤站
	//
	// 纹理与头像用各自的存储根:两者键空间不同,分开能让「清头像缓存」
	// 这类运维操作不必误伤纹理文件。
	textureStore, err := mcStorage(cfg, "textures")
	if err != nil {
		panic("初始化皮肤存储失败: " + err.Error())
	}
	avatarStore, err := mcStorage(cfg, "avatars")
	if err != nil {
		panic("初始化头像存储失败: " + err.Error())
	}

	textureService := mcTextureService(cfg, pool, textureStore, clk, loggerAdapter{logger})
	avatarService := minecraft.NewAvatarService(pool, avatarStore, textureService, minecraft.AvatarOptions{
		QueueSize:   cfg.MC.AvatarQueueSize,
		DefaultSize: cfg.MC.AvatarSize,
		Logger:      loggerAdapter{logger},
	})

	textureHandler := minecraft.NewTextureHandler(minecraft.TextureHandlerDeps{
		Service:     textureService,
		MainService: mcService,
		Avatars:     avatarService,
		Profiles:    mcService,
		URLBase:     cfg.App.BaseURL(),
		MaxSize:     cfg.MC.SkinMaxSize,
		ReadOnly:    cfg.MC.ReadOnly,
	})

	mcHandler := minecraft.NewHandler(minecraft.HandlerDeps{
		Service:      mcService,
		Issuer:       cfg.App.BaseURL(),
		SkinDomain:   cfg.App.BaseURL(),
		PublicKeyPEM: minecraft.PublicKeyPEM(mcKeys),
		TrustProxy:   cfg.App.TrustProxyHeaders,
	})

	//
	// 放在授权服务之后:后台的「OIDC 客户端」页要读写客户端与签名密钥,
	// 依赖方向是单向的 —— 授权服务不需要知道后台存在。
	adminHandler := admin.NewHandler(admin.Deps{
		Identity:      identitySvc,
		OIDC:          oidcServer.Clients,
		OIDCKeys:      keyManager,
		Settings:      settings,
		Mailer:        mailerHot,
		DB:            pool,
		PublicBaseURL: cfg.App.BaseURL(),
	})

	return transport.Deps{
		Config:   cfg,
		Logger:   logger,
		DB:       pool,
		Clock:    clk,
		Settings: settings,
		MC: transport.MCDeps{
			Service:        mcService,
			Handler:        mcHandler,
			Keys:           mcKeys,
			AccountAPI:     minecraft.NewAccountAPIHandler(mcService),
			Avatars:        avatarService,
			Textures:       textureHandler,
			TextureService: textureService,
		},
		OIDC: transport.OIDCDeps{
			Server:  oidcServer,
			Handler: oidcHandler,
			SSO:     oidc.NewSSO(oidcHandler),
			Device:  deviceService,
		},
		Admin: transport.AdminDeps{
			Service: identitySvc,
			Handler: adminHandler,
		},
		Identity: transport.IdentityDeps{
			Service: identitySvc,
			Handler: identityHandler,
			Cookie: transport.AuthConfig{
				CookieName:        cfg.Auth.SessionCookieName,
				TrustProxyHeaders: cfg.App.TrustProxyHeaders,
			},
		},
	}
}

// cookieDomain 推导 cookie 域。
//
// 留空时按 APP_PUBLIC_DOMAIN 推导**父域**,让同一主域下的多个子站共享登录态
// (docs/deployment.md 5.1)。localhost 没有父域,必须留空。
func cookieDomain(cfg *config.Config) string {
	if cfg.OIDC.CookieDomain != "" {
		return cfg.OIDC.CookieDomain
	}
	domain := cfg.App.PublicDomain
	if domain == "" || domain == "localhost" {
		return ""
	}
	idx := indexByte(domain, '.')
	if idx <= 0 || idx == len(domain)-1 {
		return ""
	}
	return domain[idx:]
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}

func sameSite(v string) http.SameSite {
	switch v {
	case "Strict":
		return http.SameSiteStrictMode
	case "None":
		return http.SameSiteNoneMode
	default:
		return http.SameSiteLaxMode
	}
}

// newHasher 构造 argon2id 哈希器。
//
// 账号内核与首启引导必须共用同一个实现:引导建出来的号和注册出来的号
// 若参数不一致,登录时 Verify 会对着同一列 hash 读出不同的参数解释。
func newHasher(cfg *config.Config) *account.Hasher {
	return account.NewHasher(account.Params{
		MemoryKiB:   uint32(cfg.Auth.Argon2MemoryKiB),
		Iterations:  uint32(cfg.Auth.Argon2Iterations),
		Parallelism: uint8(cfg.Auth.Argon2Parallelism),
		SaltLength:  16,
		KeyLength:   32,
	})
}

// newPolicy 构造密码策略。同理,引导密码与注册密码走同一把尺子。
func newPolicy(cfg *config.Config) account.Policy {
	return account.Policy{
		MinLength:       cfg.Auth.PasswordMinLength,
		MaxLength:       cfg.Auth.PasswordMaxLength,
		RejectCommon:    cfg.Auth.PasswordRejectCommon,
		CommonPasswords: account.CommonPasswordSet(),
	}
}

// loggerAdapter 把 slog 适配成各域使用的最小日志接口。
type loggerAdapter struct{ l *slog.Logger }

func (a loggerAdapter) Info(msg string, args ...any)  { a.l.Info(msg, args...) }
func (a loggerAdapter) Warn(msg string, args ...any)  { a.l.Warn(msg, args...) }
func (a loggerAdapter) Error(msg string, args ...any) { a.l.Error(msg, args...) }

// mailerCfg 从总配置里截出邮件发送所需的片段。
func mailerCfg(cfg *config.Config) mailer.Config {
	return mailer.Config{
		Transport: cfg.Mail.Transport,
		From:      cfg.Mail.From,
		FromName:  cfg.Mail.FromName,
		SMTPHost:  cfg.Mail.SMTPHost,
		SMTPPort:  cfg.Mail.SMTPPort,
		Username:  cfg.Mail.SMTPUsername,
		Password:  cfg.Mail.SMTPPassword,
		// SMTP_TLS=true 表示「先明文连上再 STARTTLS」(587);
		// 关掉它则表示「连上就用隐式 TLS」(465)
		UseTLS:  !cfg.Mail.SMTPTLS,
		Timeout: 10 * time.Second,
	}
}

// oidcHandlerDeps 组装授权服务的 HTTP 依赖。
func oidcHandlerDeps(
	cfg *config.Config,
	server *oidc.Server,
	sessions oidc.SessionStore,
	device *oidc.DeviceService,
	logger loggerAdapter,
) oidc.HandlerDeps {
	return oidc.HandlerDeps{
		Server:   server,
		Cookies:  server.CookieConfig(),
		Sessions: sessions,
		Device:   device,
		Limiter:  oidc.NewLimiter(ratelimit.New(nil)),
		// 只有明确配置了反代才信任 X-Forwarded-For。
		// 默认不信任,否则攻击者可以随手伪造来源 IP 绕过限流
		TrustProxy: cfg.App.TrustProxyHeaders,
		Logger:     logger,
	}
}

// devicePollInterval 决定设备端建议的轮询间隔。
//
// RFC 8628 要求客户端「不得快于 interval 轮询」,过密轮询既浪费资源
// 也会放大拒绝服务面。默认 5 秒。
func devicePollInterval(cfg *config.Config) int {
	const defaultInterval = 5
	if cfg.OIDC.DeviceCodeTTL > 0 && cfg.OIDC.DeviceCodeTTL.Seconds() < defaultInterval {
		return 1
	}
	return defaultInterval
}

// mcServiceResolver 返回 MC 服务器的预共享密钥解析器。
//
// 直接查库而不是读配置:一个部署可能同时挂着多台 MC 服务器,密钥各不相同。
func mcServiceResolver(pool *db.Pool) minecraft.SecretResolver {
	svc := minecraft.NewService(pool, nil, clock.New(), minecraft.Options{})
	return func(ctx context.Context, serverID string) (string, minecraft.SecretStatus, error) {
		return svc.ResolveSecret(ctx, serverID)
	}
}

// mcTokenTTL 决定 MC 访问令牌的有效期。
//
// 刻意比 OIDC 的 access token 长:Minecraft 服务端不会主动续期,
// 令牌一断玩家就得重新走登录流程。
func mcTokenTTL(cfg *config.Config) time.Duration {
	const ttl = 24 * time.Hour
	if !cfg.MC.Enabled {
		return ttl
	}
	return ttl
}

// mcGateway 把身份内核适配成 MC 域需要的窄接口。
//
// 依赖方向在这里显式落地:MC 域依赖身份内核,反过来内核完全不知道 MC 的存在。
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

// 皮肤站相关服务的装配参数集中在下面,避免 wire.go 主体继续膨胀。

// mcStorage 创建本地磁盘存储。
//
// 纹理与头像用**同一个** Local 实例、但各给一个子根:两者键空间
// 不同(textures/<hash>.png 与 avatars/<uuid>.png),共用一个根也能工作,
// 分开则让「清空头像缓存」这类运维操作不必误伤纹理。
func mcStorage(cfg *config.Config, sub string) (*storage.Local, error) {
	root := cfg.Storage.TextureDir
	if sub == "avatars" {
		root = cfg.Storage.AvatarDir
	}
	return storage.NewLocal(storage.LocalOptions{Root: root})
}

// mcTextureService 装配纹理服务。
func mcTextureService(cfg *config.Config, pool *db.Pool, store storage.Storage, clk clock.Clock, logger loggerAdapter) *minecraft.TextureService {
	fetcher := minecraft.NewRemoteFetcher(minecraft.ExternalOptions{
		Enabled:          cfg.MC.SkinExternal,
		BaseURL:          externalSkinBaseURL(cfg),
		Timeout:          cfg.MC.SkinExternalTimeout,
		FailureThreshold: cfg.MC.SkinExternalFailures,
		ResetTimeout:     cfg.MC.SkinExternalReset,
		Logger:           logger,
	})

	return minecraft.NewTextureService(pool, store, clk, minecraft.TextureOptions{
		Storage:      store,
		MaxSize:      cfg.MC.SkinMaxSize,
		ReadOnly:     cfg.MC.ReadOnly,
		GarbageGrace: 7 * 24 * time.Hour,
		Logger:       logger,
	}, fetcher)
}

// externalSkinBaseURL 返回外部皮肤站地址。
func externalSkinBaseURL(cfg *config.Config) string {
	if cfg.MC.SkinExternalBaseURL != "" {
		return cfg.MC.SkinExternalBaseURL
	}
	return cfg.App.BaseURL()
}

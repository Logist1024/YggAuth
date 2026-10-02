package main

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/yggauth/yggauth/internal/admin"
	"github.com/yggauth/yggauth/internal/config"
	"github.com/yggauth/yggauth/internal/identity"
	"github.com/yggauth/yggauth/internal/identity/account"
	"github.com/yggauth/yggauth/internal/identity/audit"
	"github.com/yggauth/yggauth/internal/identity/rbac"
	"github.com/yggauth/yggauth/internal/identity/session"
	"github.com/yggauth/yggauth/internal/oidc"
	"github.com/yggauth/yggauth/internal/platform/clock"
	"github.com/yggauth/yggauth/internal/platform/db"
	"github.com/yggauth/yggauth/internal/platform/keys"
	"github.com/yggauth/yggauth/internal/platform/mailer"
	"github.com/yggauth/yggauth/internal/platform/ratelimit"

	"github.com/yggauth/yggauth/internal/transport"
)

// buildDeps 在编译期把各业务域显式装配起来(ADR-011)。
//
// 没有运行期注册的插件中心:少 import 一个域,这里就编译不过。
// 装配关系一目了然,比反射式的容器更可靠。
func buildDeps(cfg *config.Config, logger *slog.Logger, pool *db.Pool) transport.Deps {
	clk := clock.New()

	// ---- 账号内核
	identitySvc := identity.New(identity.Deps{
		Accounts: account.NewPgRepository(pool),
		Tokens:   account.NewPgTokenStore(pool),
		Sessions: session.NewPgRepository(pool),
		RBAC:     rbac.NewPgRepository(pool),
		Audit:    audit.NewPgRepository(pool),

		Hasher: account.NewHasher(account.Params{
			MemoryKiB:   uint32(cfg.Auth.Argon2MemoryKiB),
			Iterations:  uint32(cfg.Auth.Argon2Iterations),
			Parallelism: uint8(cfg.Auth.Argon2Parallelism),
			SaltLength:  16,
			KeyLength:   32,
		}),
		Clock: clk,
		Account: account.Config{
			Policy: account.Policy{
				MinLength:       cfg.Auth.PasswordMinLength,
				MaxLength:       cfg.Auth.PasswordMaxLength,
				RejectCommon:    cfg.Auth.PasswordRejectCommon,
				CommonPasswords: account.CommonPasswordSet(),
			},
			LoginEnabledDefault: cfg.Auth.MCLoginDefault,
			Invitations:         account.NewInvitationStore(pool),
			MaxFailedAttempts:   cfg.Auth.LoginMaxFailedAttempts,
			LockDuration:        cfg.Auth.LoginLockDuration,
			EmailTokenTTL:       cfg.Auth.EmailTokenTTL,
			RegistrationMode:    cfg.Auth.RegistrationMode,
			Mailer:              mailer.New(mailerCfg(cfg), logger),
		},
		Session: session.Config{
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
	// 密钥管理器做「有就复用、无就生成」,所以首次部署不需要手工初始化步骤。
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

	//
	// 放在授权服务之后:后台的「OIDC 客户端」页要读写客户端与签名密钥,
	// 依赖方向是单向的 —— 授权服务不需要知道后台存在。
	adminHandler := admin.NewHandler(admin.Deps{
		Identity:      identitySvc,
		OIDC:          oidcServer.Clients,
		OIDCKeys:      keyManager,
		Settings:      config.NewSettingStore(pool),
		DB:            pool,
		PublicBaseURL: cfg.App.BaseURL(),
	})

	return transport.Deps{
		Config: cfg,
		Logger: logger,
		DB:     pool,
		Clock:  clk,
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
// (docs/08-deployment.md 5.1)。localhost 没有父域,必须留空。
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

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
	"github.com/yggauth/yggauth/internal/platform/clock"
	"github.com/yggauth/yggauth/internal/platform/db"
	"github.com/yggauth/yggauth/internal/platform/mailer"

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

	adminHandler := admin.NewHandler(admin.Deps{
		Identity:      identitySvc,
		Settings:      config.NewSettingStore(pool),
		DB:            pool,
		PublicBaseURL: cfg.App.BaseURL(),
	})

	return transport.Deps{
		Config: cfg,
		Logger: logger,
		DB:     pool,
		Clock:  clk,
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

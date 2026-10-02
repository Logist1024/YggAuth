package oidc

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"net/http"
	"time"

	"github.com/go-jose/go-jose/v3"

	"github.com/ory/fosite"
	"github.com/ory/fosite/compose"
	"github.com/ory/fosite/token/jwt"
	"golang.org/x/crypto/bcrypt"

	"github.com/yggauth/yggauth/internal/config"
	"github.com/yggauth/yggauth/internal/oidc/session"
	"github.com/yggauth/yggauth/internal/platform/apperr"
	"github.com/yggauth/yggauth/internal/platform/clock"
	"github.com/yggauth/yggauth/internal/platform/db"
	"github.com/yggauth/yggauth/internal/platform/keys"
	"github.com/yggauth/yggauth/internal/platform/metrics"
)

// Server 是授权服务的门面。
//
// fosite 负责 OAuth 2.1 / OIDC 的协议实现(ADR-003),这一层只做三件事:
// 组装 fosite provider、把我们的存储适配给它、把标准端点暴露成 HTTP。
type Server struct {
	Provider fosite.OAuth2Provider
	Storage  *Storage
	Clients  *ClientService
	Keys     *keys.Manager
	Clock    clock.Clock

	// issuer 必须与实际访问域名完全一致,否则标准客户端会在验签阶段拒绝所有令牌
	// macKey 用于给暂存授权请求的 cookie 签名
	macKey []byte
	issuer string
	cfg    *config.Config
}

// NewServer 组装授权服务。
func NewServer(cfg *config.Config, pool *db.Pool, clk clock.Clock, keyManager *keys.Manager, logger Logger) (*Server, error) {
	// 先确保密钥可用:第一次部署在这里生成,之后直接复用库里的
	if _, err := keyManager.Ensure(context.Background()); err != nil {
		return nil, err
	}

	// fosite 与存储之间需要互相引用:存储要能反查客户端,
	// 而客户端加载又依赖查询能力。先建存储(客户端加载器后填),再建 provider。
	storage := NewStorage(pool, clk, nil)

	fositeCfg := &fosite.Config{
		AccessTokenLifespan:   cfg.OIDC.AccessTokenTTL,
		RefreshTokenLifespan:  cfg.OIDC.RefreshTokenTTL,
		AuthorizeCodeLifespan: cfg.OIDC.AuthCodeTTL,
		IDTokenLifespan:       cfg.OIDC.IDTokenTTL,
		IDTokenIssuer:         cfg.App.BaseURL(),

		// OAuth 2.1 的硬性要求:强制 PKCE,且只接受 S256。
		// plain 方式被明确禁用 —— 它对授权码截断攻击几乎没有防护。
		EnforcePKCE:                    cfg.OIDC.RequirePKCE,
		EnforcePKCEForPublicClients:    cfg.OIDC.RequirePKCE,
		EnablePKCEPlainChallengeMethod: false,

		// 绝不能把调试信息发给客户端:那会泄露数据库错误、SQL 片段之类的东西
		SendDebugMessagesToClients: false,

		ScopeStrategy:            fosite.ExactScopeStrategy,
		AudienceMatchingStrategy: fosite.DefaultAudienceMatchingStrategy,

		// fosite 的 HMAC 策略要求全局密钥恰好 32 字节(SHA-512/256 的块大小)。
		// 直接传配置里的原始字符串会在第一次签发令牌时才炸,这里统一派生。
		GlobalSecret: deriveGlobalSecret(cfg.OIDC.KeyMasterSecret),

		HashCost: 0,
	}

	activeKey, err := activeJWK(context.Background(), keyManager)
	if err != nil {
		return nil, err
	}

	// 显式列出 handler,不用 ComposeAllEnabled。
	//
	// AllEnabled 里含 resource-owner-password(ROPC)。OAuth 2.1 明确移除了
	// 它:ROPC 要求客户端直接收集用户密码,等于把认证责任从授权服务器
	// 推给了第三方,一旦客户端不可信,用户的凭据就等于交给了它。
	//
	// 同样不装配 RFC 7523 断言流程:它面向「服务端之间的令牌交换」,
	// 本项目没有对等服务端可信任,装上它只是多一条无人使用的攻击面。
	//
	// 不装配这两个流程时,对应的 grant_type 会按 RFC 6749 正确返回
	// unsupported_grant_type —— 这比「实现一段永远失败的存储接口」
	// 更诚实,也省掉了看起来能用、实际必然失败的代码。
	keyGetter := func(context.Context) (interface{}, error) { return activeKey, nil }

	provider := compose.Compose(
		fositeCfg,
		storage,
		&compose.CommonStrategy{
			CoreStrategy:               compose.NewOAuth2HMACStrategy(fositeCfg),
			OpenIDConnectTokenStrategy: compose.NewOpenIDConnectStrategy(keyGetter, fositeCfg),
			Signer:                     &jwt.DefaultSigner{GetPrivateKey: keyGetter},
		},
		// 授权码 + PKCE:OAuth 2.1 唯一推荐的终端用户流程
		compose.OAuth2AuthorizeExplicitFactory,
		compose.OAuth2AuthorizeImplicitFactory,
		compose.OAuth2ClientCredentialsGrantFactory,
		compose.OAuth2RefreshTokenGrantFactory,

		compose.OpenIDConnectExplicitFactory,
		compose.OpenIDConnectImplicitFactory,
		compose.OpenIDConnectHybridFactory,
		compose.OpenIDConnectRefreshFactory,

		compose.OAuth2TokenIntrospectionFactory,
		compose.OAuth2TokenRevocationFactory,

		compose.OAuth2PKCEFactory,
		compose.PushedAuthorizeHandlerFactory,
	)

	// 回填 provider 与客户端加载器:存储在还原请求时需要它们
	storage.provider = provider

	storage.loadClient = func(ctx context.Context, id string) (fosite.Client, error) {
		return storage.GetClient(ctx, id)
	}

	svc := NewClientService(pool, NewSecretHasher(clientSecretHasher(cfg)))

	return &Server{
		Provider: provider,
		Storage:  storage,
		Clients:  svc,
		Keys:     keyManager,
		Clock:    clk,
		issuer:   cfg.App.BaseURL(),
		macKey:   deriveMACKey(cfg.OIDC.KeyMasterSecret, "oidc-authorize-stash"),
		cfg:      cfg,
	}, nil
}

// Logger 是服务用到的日志器最小接口。
type Logger interface {
	Info(msg string, args ...any)
	Warn(msg string, args ...any)
	Error(msg string, args ...any)
}

// Issuer 返回对外的 issuer。
func (s *Server) Issuer() string { return s.issuer }

// NewSession 按账号信息构造 OIDC 会话。
func NewSession(subject, username, email string, emailVerified bool) *session.DefaultSession {
	return session.NewWithAccount(subject, username, email, emailVerified)
}

// clientSecretHasher 返回客户端密钥的哈希函数。
//
// 用 bcrypt 而不是账号密码那套 argon2id,理由有两条:
//  1. 客户端密钥是 256 位随机串,没有字典可爆破。内存硬 KDF 防的是
//     「低熵口令被离线穷举」,对高熵随机串不起额外作用,只是白白多占 64MiB。
//  2. fosite 的 SecretsHasher 默认实现就是 bcrypt。要么让密钥格式与它一致,
//     要么自己实现一整套 Hasher 接口 —— 后者意味着客户端密钥校验要
//     自己重写一遍认证逻辑,风险收益比很差。
func clientSecretHasher(_ *config.Config) func(string) (string, error) {
	return func(secret string) (string, error) {
		digest, err := bcrypt.GenerateFromPassword([]byte(secret), bcrypt.DefaultCost)
		if err != nil {
			return "", apperr.Newf(apperr.CodeInternal, "生成客户端密钥哈希失败: %v", err)
		}
		return string(digest), nil
	}
}

var (
	_ jwt.Signer = (*jwt.DefaultSigner)(nil)
	_            = time.Second
	_            = metrics.OutcomeSuccess
)

// activeJWK 返回当前用于签名的密钥。
func activeJWK(ctx context.Context, m *keys.Manager) (*jose.JSONWebKey, error) {
	rec, err := m.Ensure(ctx)
	if err != nil {
		return nil, err
	}
	priv, err := m.Decrypt(rec.PrivateEnc)
	if err != nil {
		return nil, err
	}
	return &jose.JSONWebKey{
		Key:       priv,
		KeyID:     rec.Kid,
		Algorithm: rec.Algo,
		Use:       "sig",
	}, nil
}

// jwks 返回当前可用于验签的公钥集合。
func (s *Server) jwks(ctx context.Context) (*jose.JSONWebKeySet, error) {
	records, err := s.Keys.List(ctx)
	if err != nil {
		return nil, err
	}
	set := &jose.JSONWebKeySet{Keys: []jose.JSONWebKey{}}
	for _, rec := range records {
		// revoked 的密钥必须从 JWKS 里消失:留着就等于对外承认它还能验签
		if rec.Status == keys.StatusRevoked {
			continue
		}
		pub, err := rec.PublicKey()
		if err != nil {
			// 一条坏密钥不该让整个 JWKS 端点 500
			continue
		}
		set.Keys = append(set.Keys, jose.JSONWebKey{
			Key:       pub,
			KeyID:     rec.Kid,
			Algorithm: rec.Algo,
			Use:       "sig",
		})
	}
	if len(set.Keys) == 0 {
		return nil, apperr.New(apperr.CodeInternal, "没有可用公钥")
	}
	return set, nil
}

// deriveMACKey 从主密钥派生一个用途限定的 HMAC 密钥。
//
// 不同用途用不同派生标签:一个泄漏的签名密钥不会连带影响另一个用途。
func deriveMACKey(master, purpose string) []byte {
	mac := hmac.New(sha256.New, []byte(master))
	mac.Write([]byte(purpose))
	return mac.Sum(nil)
}

// HandlerDeps 是 Handler 的装配参数。
type HandlerDeps struct {
	Server     *Server
	Cookies    CookieConfig
	Sessions   SessionStore
	Device     *DeviceService
	Limiter    *Limiter
	TrustProxy bool
	Logger     Logger
}

// NewHandler 创建授权服务的 HTTP 处理器。
func NewHandler(d HandlerDeps) *Handler {
	return &Handler{
		server:     d.Server,
		cookies:    d.Cookies,
		sessions:   d.Sessions,
		device:     d.Device,
		limiter:    d.Limiter,
		trustProxy: d.TrustProxy,
		logger:     d.Logger,
	}
}

// CookieConfig 返回本服务的会话 cookie 配置。
func (s *Server) CookieConfig() CookieConfig {
	return CookieConfig{
		Name:     s.cfg.OIDC.CookieName,
		Secure:   s.cfg.OIDC.CookieSecure,
		SameSite: sameSiteOf(s.cfg.OIDC.CookieSameSite),
		MaxAge:   int(s.cfg.Auth.SessionIdleTTL.Seconds()),
	}
}

// sameSiteOf 把配置字符串转成 http.SameSite。
func sameSiteOf(v string) http.SameSite {
	switch v {
	case "Strict":
		return http.SameSiteStrictMode
	case "None":
		return http.SameSiteNoneMode
	default:
		return http.SameSiteLaxMode
	}
}

// 它保护的是授权码、访问令牌、刷新令牌的 HMAC 签名。与签名私钥分开
// 派生:拿到全局密钥能伪造令牌,但拿不到 RSA 私钥,
// 无法伪造任何 id_token。
//
// 密钥来自 KEY_MASTER_SECRET 的派生值,而不是配置文件里的原始字符串 —-
// fosite 要求恰好 32 字节,直接传配置值会在第一次签发令牌时才炸。
//
// 它保护的是授权码、访问令牌、刷新令牌的 HMAC 签名。与签名私钥分开
// 派生:拿到全局密钥能伪造令牌,但拿不到 RSA 私钥,
// 无法伪造任何 id_token。
func deriveGlobalSecret(master string) []byte {
	sum := sha256.Sum256([]byte("fosite-global-secret\x00" + master))
	return sum[:]
}

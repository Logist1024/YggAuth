// Package config 负责从环境变量加载配置并在启动时校验。
//
// 两条硬规则(见 docs/deployment.md 5.2):
//  1. 必填项缺失或格式错误 → 启动即退出,并一次性列出**全部**问题;
//  2. 绝不静默使用默认值处理必填项。
//
// 平台层能力,与业务无关:这里不认识账号、令牌、皮肤,只认识「配置项」。
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ValidationError 汇总启动期发现的所有配置问题。
//
// 一次性报全部,而不是修一个报一个 —— 部署时不用反复重启试错。
type ValidationError struct {
	Problems []string
}

// Error 实现 error。
func (e *ValidationError) Error() string {
	if len(e.Problems) == 1 {
		return "配置错误: " + e.Problems[0]
	}
	return fmt.Sprintf("配置错误(共 %d 项):\n  - %s",
		len(e.Problems), strings.Join(e.Problems, "\n  - "))
}

// collector 收集配置问题。
type collector struct {
	problems []string
}

func (c *collector) addf(format string, args ...any) {
	c.problems = append(c.problems, fmt.Sprintf(format, args...))
}

func (c *collector) err() error {
	if len(c.problems) == 0 {
		return nil
	}
	sort.Strings(c.problems)
	return &ValidationError{Problems: c.problems}
}

// ---------------------------------------------------------------- 读取器
//
// reader 封装环境变量读取,顺便把「没配」和「配错了」区分开 ——
// 空字符串视为未设置,避免 `FOO=` 悄悄覆盖默认值。

type reader struct {
	col *collector
}

func newReader() reader { return reader{col: &collector{}} }

func (r reader) lookup(key string) (string, bool) {
	v, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(v) == "" {
		return "", false
	}
	return strings.TrimSpace(v), true
}

func (r reader) str(key, def string) string {
	if v, ok := r.lookup(key); ok {
		return v
	}
	return def
}

func (r reader) required(key, hint string) string {
	v, ok := r.lookup(key)
	if !ok {
		r.col.addf("%s 未设置(%s)", key, hint)
		return ""
	}
	return v
}

func (r reader) intVal(key string, def int) int {
	v, ok := r.lookup(key)
	if !ok {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		r.col.addf("%s 必须是整数,当前值: %s", key, v)
		return def
	}
	return n
}

func (r reader) intBetween(key string, def, min, max int) int {
	n := r.intVal(key, def)
	if n < min || n > max {
		r.col.addf("%s 必须在 %d~%d 之间,当前值: %d", key, min, max, n)
		return def
	}
	return n
}

func (r reader) boolVal(key string, def bool) bool {
	v, ok := r.lookup(key)
	if !ok {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		r.col.addf("%s 必须是 true/false,当前值: %s", key, v)
		return def
	}
	return b
}

func (r reader) duration(key string, def time.Duration) time.Duration {
	v, ok := r.lookup(key)
	if !ok {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		r.col.addf("%s 必须是时长(如 168h / 15m),当前值: %s", key, v)
		return def
	}
	if d <= 0 {
		r.col.addf("%s 必须大于 0,当前值: %s", key, v)
		return def
	}
	return d
}

// seconds 解析 `*_SECONDS` 变量:裸数字按秒,带单位按 time.ParseDuration。
//
// 单独一个方法而不是复用 duration,是因为**名字里已经写了 SECONDS**:
// .env.example 与文档里给的都是 `LOGIN_LOCK_SECONDS=900` 这种裸数字,
// 而 time.ParseDuration("900") 会因为缺单位直接报错 —— 照文档填反而启动失败。
// 实测踩过这个坑的有两个变量:LOGIN_LOCK_SECONDS 与 MAIL_VERIFY_COOLDOWN_SECONDS。
func (r reader) seconds(key string, def time.Duration) time.Duration {
	v, ok := r.lookup(key)
	if !ok {
		return def
	}
	if n, err := strconv.Atoi(v); err == nil {
		if n <= 0 {
			r.col.addf("%s 必须是大于 0 的秒数,当前值: %s", key, v)
			return def
		}
		return time.Duration(n) * time.Second
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		r.col.addf("%s 必须是秒数(900)或时长(15m),当前值: %s", key, v)
		return def
	}
	if d <= 0 {
		r.col.addf("%s 必须大于 0,当前值: %s", key, v)
		return def
	}
	return d
}

// csv 读逗号分隔列表并去掉空项。
func (r reader) csv(key string) []string {
	v, ok := r.lookup(key)
	if !ok {
		return nil
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func (r reader) oneOf(key, value string, allowed ...string) string {
	for _, a := range allowed {
		if value == a {
			return value
		}
	}
	r.col.addf("%s 取值必须是 %s 之一,当前值: %s", key, strings.Join(allowed, "/"), value)
	return allowed[0]
}

// ---------------------------------------------------------------- 配置结构

// Config 是全量配置。按 docs/development.md M1 的要求分为八组。
type Config struct {
	App     App
	DB      DB
	Auth    Auth
	Storage Storage
	OIDC    OIDC
	MC      MC
	Mail    Mail
	Log     Log
	// Bootstrap 是首启引导(L2 层,见 docs/configuration.md 第五节)。
	// 只在「库里还没有管理员」的一次性时刻生效,运行期配置以 app.setting 为准。
	Bootstrap Bootstrap
}

// Bootstrap 是首启引导配置。
//
// 一句话契约:**env 是首次启动的种子,setting 是运行时的现值** ——
// 这里的每一项都只在首次启动写库一次,之后既不读也不覆盖。
type Bootstrap struct {
	// Enabled 为 false 时首启不创建默认管理员(ADMIN_BOOTSTRAP=false)。
	// 关闭后仍可随时用 `yggauth admin create` 手工建号,不会永久失去管理入口。
	Enabled bool
	// Email / Username 是默认管理员的身份,重复启动时不再使用
	// (已存在管理员即跳过),因此给错也不会覆盖既有账号。
	Email    string
	Username string
	// Password 为空表示随机生成 24 位并**只打印一次**到启动日志。
	// 留空是更安全的默认:日志里的密码运维看完即弃,不落 .env。
	Password string
}

// App 是进程与应用级配置。
type App struct {
	Host string
	Port int
	// PublicDomain 是终端用户站域名,用于推导 SSO cookie 父域与 skinDomains
	PublicDomain string
	// PublicBaseURL 是对外完整基址,**OIDC issuer 就是它**,必须与实际访问域名完全一致
	PublicBaseURL string
	// AdminDomain 是管理后台域名。单域名部署时可为空,此时按路径前缀分流。
	AdminDomain string
	// DataDir 是运行时数据目录(皮肤、备份)
	DataDir string
	// TrustProxyHeaders 决定是否信任 X-Forwarded-For / X-Real-IP
	TrustProxyHeaders bool
	// ShutdownTimeout 是优雅退出的最长等待时间
	ShutdownTimeout time.Duration
	// MigrationsDir 非空时从磁盘读迁移,否则用编进二进制的迁移
	MigrationsDir string
}

// Addr 返回监听地址。
func (a App) Addr() string { return fmt.Sprintf("%s:%d", a.Host, a.Port) }

// BaseURL 返回去掉末尾斜杠的对外基址。
func (a App) BaseURL() string { return strings.TrimRight(a.PublicBaseURL, "/") }

// Storage 是文件存储配置。
// Storage 是运行时文件布局。
//
// 三个目录各自独立:纹理、头像、备份的清理策略与保留期都不同,
// 混在一个目录下就没法只回收其中一类。
type Storage struct {
	// TextureDir 是皮肤/披风目录,布局为 <sha256前2位>/<sha256>.png
	TextureDir string
	// AvatarDir 是渲染后的头像目录
	AvatarDir string
	// BackupDir 是迁移前自动备份的落盘目录
	BackupDir string
}

// DB 是数据库连接配置。
type DB struct {
	Host            string
	Port            int
	Name            string
	User            string
	Password        string
	SSLMode         string
	MaxConns        int
	MinConns        int
	MaxConnLifetime time.Duration
	MaxConnIdleTime time.Duration
	ConnectTimeout  time.Duration
	// AutoMigrate 为真时启动即执行迁移(见 docs/deployment.md 第六节)
	AutoMigrate bool
}

// Auth 是账号内核的认证与会话策略。
type Auth struct {
	PasswordMinLength int
	PasswordMaxLength int
	// PasswordRejectCommon 为真时拒绝弱口令库中的密码(变更 C-2)
	PasswordRejectCommon bool
	// HIBPEndpoint 是 Have I Been Pwned k-anonymity API 前缀。
	// 置空则跳过泄露检查(离线环境可用)。
	HIBPEndpoint string

	// Argon2 参数。默认取 RFC 9106 第二档(m=64MiB, t=1, p=4),
	// 参数内嵌进哈希串,便于将来整体升级而不影响存量密码。
	Argon2MemoryKiB   int
	Argon2Iterations  int
	Argon2Parallelism int

	LoginMaxFailedAttempts int
	LoginLockDuration      time.Duration

	SessionIdleTTL time.Duration
	SessionMaxTTL  time.Duration

	// RegistrationMode: open | invite_only
	RegistrationMode string
	// MCLoginDefault 是新账号的通用布尔开关默认值(变更 C-8)
	MCLoginDefault bool

	EmailTokenTTL     time.Duration
	InviteTokenTTL    time.Duration
	SessionCookieName string
}

// OIDC 是授权服务配置。
type OIDC struct {
	// KeyMasterSecret 用于 AES-256-GCM 加密签名私钥。
	// 格式:32 字节(64 位 hex)或任意 ≥32 字节的字符串,后者会被 SHA-256 派生。
	KeyMasterSecret string
	AccessTokenTTL  time.Duration
	IDTokenTTL      time.Duration
	AuthCodeTTL     time.Duration
	RefreshTokenTTL time.Duration
	// PushedAuthorizationRequestTTL 是 PAR 请求有效期
	PushedAuthorizationRequestTTL time.Duration
	// DeviceCodeTTL 是设备码流程有效期
	DeviceCodeTTL time.Duration
	// RequirePKCE 为真时所有客户端强制 PKCE
	RequirePKCE bool
	// CookieName 是 SSO 会话 cookie 名
	CookieName     string
	CookieDomain   string
	CookieSecure   bool
	CookieSameSite string
	// AllowedOrigins 是 CORS 白名单。生产建议留空走客户端回调地址白名单。
	AllowedOrigins []string
}

// MC 是 Minecraft 域配置。
type MC struct {
	Enabled bool
	// ReadOnly 为真时禁止上传,下载不受影响
	ReadOnly bool
	// SkinExternal 为真时,本地无材质且账号有外部绑定则回源拉取
	SkinExternal bool
	// SkinExternalTimeout 是外部皮肤站调用超时
	SkinExternalTimeout time.Duration
	// SkinExternalBaseURL 是外部皮肤站地址,默认取本服务的公开基址
	SkinExternalBaseURL string
	// SkinExternalFailures 是熔断阈值:连续失败达该次数后短路
	SkinExternalFailures int
	// SkinExternalReset 是熔断半开等待时长
	SkinExternalReset time.Duration

	// ServerSharedSecret 是与 authlib-injector 预共享的密钥,
	// hasJoined 五生效点校验用。可由 MC_SERVERS(登记的服务器列表)覆盖。
	ServerSharedSecret string
	// HasJoinedWindow 是 server_id 的有效时间窗,超出即拒绝(防重放)
	HasJoinedWindow time.Duration

	// SkinMaxSize 是上传体积上限
	SkinMaxSize int64
	// AvatarSize 是头像默认边长
	AvatarSize int
	// AvatarQueueSize 是异步渲染队列容量
	AvatarQueueSize int
	// NameRetentionDays 是改名后旧名保留天数,期间禁止他人注册
	NameRetentionDays int
}

// Mail 是邮件发送配置。
type Mail struct {
	// Transport: console | smtp
	Transport string
	From      string
	FromName  string

	SMTPHost     string
	SMTPPort     int
	SMTPUsername string
	SMTPPassword string
	SMTPTLS      bool

	// VerifyCooldown 是重发验证邮件的最小间隔
	VerifyCooldown time.Duration
	// VerifyDailyLimit 是单账号每日重发上限
	VerifyDailyLimit int
}

// Log 是日志配置。
type Log struct {
	// Level: debug | info | warn | error
	Level string
	// Format: json | text
	Format string
	// AddSource 是否记录调用位置
	AddSource bool
	// RequestBody 为真时把请求体写进日志。**生产必须为 false**(会泄露密码)
	RequestBody bool
	// SlowRequestThreshold 是慢请求日志阈值
	SlowRequestThreshold time.Duration
}

// ---------------------------------------------------------------- 加载

// Load 从环境变量加载配置并完成校验。
//
// 返回 *ValidationError 时,调用方应把 err.Error() 原样打到 stderr 后退出进程:
// 带着错误配置继续启动,只会在更晚的地方以更难排查的方式失败。
func Load() (*Config, error) {
	r := newReader()

	cfg := &Config{
		App: App{
			Host:              r.str("APP_HOST", "0.0.0.0"),
			Port:              r.intBetween("APP_PORT", 3000, 1, 65535),
			PublicDomain:      r.required("APP_PUBLIC_DOMAIN", "终端用户站域名,如 auth.example.com"),
			PublicBaseURL:     r.required("PUBLIC_BASE_URL", "对外完整基址,如 https://auth.example.com"),
			AdminDomain:       r.str("ADMIN_DOMAIN", ""),
			DataDir:           r.str("DATA_DIR", "./data"),
			TrustProxyHeaders: r.boolVal("TRUST_PROXY_HEADERS", false),
			ShutdownTimeout:   r.duration("SHUTDOWN_TIMEOUT", 20*time.Second),
			MigrationsDir:     r.str("MIGRATIONS_DIR", ""),
		},
		DB: DB{
			Host:            r.str("DB_HOST", "127.0.0.1"),
			Port:            r.intBetween("DB_PORT", 5432, 1, 65535),
			Name:            r.str("DB_NAME", "yggauth"),
			User:            r.str("DB_USER", "yggauth"),
			Password:        r.required("DB_PASSWORD", "数据库密码"),
			SSLMode:         r.str("DB_SSLMODE", "disable"),
			MaxConns:        r.intVal("DB_MAX_CONNS", 0),
			MinConns:        r.intBetween("DB_MIN_CONNS", 0, 0, 1024),
			MaxConnLifetime: r.duration("DB_MAX_CONN_LIFETIME", time.Hour),
			MaxConnIdleTime: r.duration("DB_MAX_CONN_IDLE_TIME", 30*time.Minute),
			ConnectTimeout:  r.duration("DB_CONNECT_TIMEOUT", 10*time.Second),
			AutoMigrate:     r.boolVal("DB_AUTO_MIGRATE", true),
		},
		Auth: Auth{
			PasswordMinLength:    r.intBetween("PASSWORD_MIN_LENGTH", 8, 1, 1024),
			PasswordMaxLength:    r.intBetween("PASSWORD_MAX_LENGTH", 128, 1, 4096),
			PasswordRejectCommon: r.boolVal("PASSWORD_REJECT_COMMON", true),
			HIBPEndpoint:         r.str("HIBP_ENDPOINT", "https://api.pwnedpasswords.com/range"),

			Argon2MemoryKiB:   r.intBetween("ARGON2_MEMORY_KIB", 64*1024, 8*1024, 1024*1024),
			Argon2Iterations:  r.intBetween("ARGON2_ITERATIONS", 1, 1, 16),
			Argon2Parallelism: r.intBetween("ARGON2_PARALLELISM", 4, 1, 16),

			LoginMaxFailedAttempts: r.intBetween("LOGIN_MAX_FAILED_ATTEMPTS", 5, 1, 100),
			LoginLockDuration:      r.seconds("LOGIN_LOCK_SECONDS", 900*time.Second),

			SessionIdleTTL: r.duration("SESSION_IDLE_TTL", 168*time.Hour),
			SessionMaxTTL:  r.duration("SESSION_MAX_TTL", 720*time.Hour),

			RegistrationMode: r.oneOf("REGISTRATION_MODE", r.str("REGISTRATION_MODE", "open"), "open", "invite_only"),
			MCLoginDefault:   r.boolVal("MC_LOGIN_DEFAULT", true),

			EmailTokenTTL:     r.duration("EMAIL_TOKEN_TTL", 24*time.Hour),
			InviteTokenTTL:    r.duration("INVITE_TOKEN_TTL", 7*24*time.Hour),
			SessionCookieName: r.str("SESSION_COOKIE_NAME", "ygg_session"),
		},
		Storage: Storage{
			// 皮肤文件按 sha256 前两位分目录,避免单目录文件过多
			TextureDir: r.str("TEXTURE_DIR", ""),
			BackupDir:  r.str("BACKUP_DIR", ""),
		},
		OIDC: OIDC{
			KeyMasterSecret:               r.required("KEY_MASTER_SECRET", "生成方式: openssl rand -hex 32"),
			AccessTokenTTL:                r.duration("OIDC_ACCESS_TOKEN_TTL", time.Hour),
			IDTokenTTL:                    r.duration("OIDC_ID_TOKEN_TTL", time.Hour),
			AuthCodeTTL:                   r.duration("OIDC_AUTH_CODE_TTL", 60*time.Second),
			RefreshTokenTTL:               r.duration("OIDC_REFRESH_TOKEN_TTL", 30*24*time.Hour),
			PushedAuthorizationRequestTTL: r.duration("OIDC_PAR_TTL", 90*time.Second),
			DeviceCodeTTL:                 r.duration("OIDC_DEVICE_CODE_TTL", 15*time.Minute),
			RequirePKCE:                   r.boolVal("OIDC_REQUIRE_PKCE", true),
			CookieName:                    r.str("SSO_COOKIE_NAME", "ygg_sso"),
			CookieDomain:                  r.str("SSO_COOKIE_DOMAIN", ""),
			CookieSecure:                  r.boolVal("SSO_COOKIE_SECURE", true),
			CookieSameSite:                r.oneOf("SSO_COOKIE_SAMESITE", r.str("SSO_COOKIE_SAMESITE", "Lax"), "Strict", "Lax", "None"),
			AllowedOrigins:                r.csv("CORS_ALLOWED_ORIGINS"),
		},
		MC: MC{
			Enabled:              r.boolVal("MC_ENABLED", true),
			ReadOnly:             r.boolVal("MC_READONLY", false),
			SkinExternal:         r.boolVal("MC_SKIN_EXTERNAL", false),
			SkinExternalTimeout:  r.duration("MC_SKIN_EXTERNAL_TIMEOUT", 5*time.Second),
			SkinExternalBaseURL:  r.str("MC_SKIN_EXTERNAL_BASE_URL", ""),
			SkinExternalFailures: r.intBetween("MC_SKIN_EXTERNAL_FAILURES", 5, 1, 100),
			SkinExternalReset:    r.duration("MC_SKIN_EXTERNAL_RESET", 60*time.Second),
			ServerSharedSecret:   r.str("MC_SERVER_SHARED_SECRET", ""),
			HasJoinedWindow:      r.duration("MC_HASJOINED_WINDOW", 3*time.Minute),
			SkinMaxSize:          int64(r.intBetween("MC_SKIN_MAX_SIZE_MB", 2, 1, 16)) << 20,
			AvatarSize:           r.intBetween("MC_AVATAR_SIZE", 64, 8, 512),
			AvatarQueueSize:      r.intBetween("MC_AVATAR_QUEUE_SIZE", 256, 1, 65536),
			NameRetentionDays:    r.intBetween("MC_NAME_RETENTION_DAYS", 90, 0, 3650),
		},
		Mail: Mail{
			Transport:        r.oneOf("MAILER_TRANSPORT", r.str("MAILER_TRANSPORT", "console"), "console", "smtp"),
			From:             r.str("MAILER_FROM", "noreply@localhost"),
			FromName:         r.str("MAILER_FROM_NAME", "YggAuth"),
			SMTPHost:         r.str("SMTP_HOST", ""),
			SMTPPort:         r.intBetween("SMTP_PORT", 587, 1, 65535),
			SMTPUsername:     r.str("SMTP_USER", ""),
			SMTPPassword:     r.str("SMTP_PASSWORD", ""),
			SMTPTLS:          r.boolVal("SMTP_TLS", true),
			VerifyCooldown:   r.seconds("MAIL_VERIFY_COOLDOWN_SECONDS", 60*time.Second),
			VerifyDailyLimit: r.intBetween("MAIL_VERIFY_DAILY_LIMIT", 5, 1, 1000),
		},
		Bootstrap: Bootstrap{
			Enabled:  r.boolVal("ADMIN_BOOTSTRAP", true),
			Email:    r.str("ADMIN_EMAIL", "admin@localhost.local"),
			Username: r.str("ADMIN_USERNAME", "admin"),
			// 秘密项走 str 而不是 required:留空 = 随机生成并打印一次
			Password: r.str("ADMIN_PASSWORD", ""),
		},
		Log: Log{
			Level:                r.oneOf("LOG_LEVEL", r.str("LOG_LEVEL", "info"), "debug", "info", "warn", "error"),
			Format:               r.oneOf("LOG_FORMAT", r.str("LOG_FORMAT", "json"), "json", "text"),
			AddSource:            r.boolVal("LOG_ADD_SOURCE", false),
			RequestBody:          r.boolVal("LOG_REQUEST_BODY", false),
			SlowRequestThreshold: r.duration("LOG_SLOW_REQUEST", time.Second),
		},
	}

	// 只在没显式配置时才推导默认值。
	// 无条件覆盖会让 TEXTURE_DIR 变成一个读了没用的环境变量 ——
	// 运维设了它却没有任何效果,排查起来相当费时。
	if cfg.Storage.TextureDir == "" {
		cfg.Storage.TextureDir = joinDataDir(cfg.App.DataDir, "textures")
	}
	if cfg.Storage.AvatarDir == "" {
		cfg.Storage.AvatarDir = joinDataDir(cfg.App.DataDir, "avatars")
	}
	if cfg.Storage.BackupDir == "" {
		cfg.Storage.BackupDir = joinDataDir(cfg.App.DataDir, "backups")
	}

	cfg.validate(r.col)
	if err := r.col.err(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func joinDataDir(base, sub string) string {
	if base == "" {
		base = "./data"
	}
	return strings.TrimRight(base, "/") + "/" + sub
}

// validate 做跨字段与格式校验。
func (c *Config) validate(col *collector) {
	// PUBLIC_BASE_URL 必须是绝对 URL —— 它就是 OIDC issuer,
	// 与实际访问域名不一致时标准客户端会在验签阶段拒绝所有令牌。
	if c.App.PublicBaseURL != "" {
		u, err := url.Parse(c.App.PublicBaseURL)
		switch {
		case err != nil:
			col.addf("PUBLIC_BASE_URL 解析失败: %v", err)
		case u.Scheme != "http" && u.Scheme != "https":
			col.addf("PUBLIC_BASE_URL 必须是绝对 URL(含协议),当前值: %s", c.App.PublicBaseURL)
		case u.Host == "":
			col.addf("PUBLIC_BASE_URL 缺少主机名,当前值: %s", c.App.PublicBaseURL)
		}
	}

	if c.Auth.PasswordMinLength > c.Auth.PasswordMaxLength {
		col.addf("PASSWORD_MIN_LENGTH(%d) 不能大于 PASSWORD_MAX_LENGTH(%d)",
			c.Auth.PasswordMinLength, c.Auth.PasswordMaxLength)
	}
	// 变更 C-1 的下界:低于 8 位不符合 NIST SP 800-63B,提示而不是硬失败
	if c.Auth.PasswordMinLength < 8 {
		col.addf("PASSWORD_MIN_LENGTH(%d) 低于 NIST SP 800-63B 建议的 8,确认这是有意为之", c.Auth.PasswordMinLength)
	}

	// 绝对过期必须大于滑动过期,否则会话一签发就同时满足两种过期条件。
	if c.Auth.SessionIdleTTL > c.Auth.SessionMaxTTL {
		col.addf("SESSION_IDLE_TTL(%s) 不能大于 SESSION_MAX_TTL(%s)",
			c.Auth.SessionIdleTTL, c.Auth.SessionMaxTTL)
	}

	if len(c.OIDC.KeyMasterSecret) < 16 {
		col.addf("KEY_MASTER_SECRET 长度不足 16 字符,建议用 openssl rand -hex 32 生成 32 字节随机值")
	}

	// SameSite=None 必须配 Secure,否则浏览器直接丢弃 cookie。
	if c.OIDC.CookieSameSite == "None" && !c.OIDC.CookieSecure {
		col.addf("SSO_COOKIE_SAMESITE=None 时必须 SSO_COOKIE_SECURE=true,否则浏览器会丢弃 cookie")
	}
	// localhost 没有父域,cookie 域必须留空,否则静默发码会静默失效。
	if c.OIDC.CookieDomain == "localhost" || strings.HasSuffix(c.OIDC.CookieDomain, ".localhost") {
		col.addf("SSO_COOKIE_DOMAIN 不能设为 localhost(无父域),必须留空由 APP_PUBLIC_DOMAIN 推导")
	}

	// CORS 绝不允许通配符配合凭据(见 docs/api.md 第八节)。
	for _, o := range c.OIDC.AllowedOrigins {
		if o == "*" {
			col.addf("CORS_ALLOWED_ORIGINS 不能包含通配符 *,它无法与凭据同时使用")
		}
	}

	// 邮件配置的跨字段校验与后台设置共用同一份规则(mail.go)。
	// env 挡住的错,后台换个入口也必须挡住 —— 两套规则必然漂移。
	if err := ValidateMailConfig(c.Mail.Transport, c.Mail.From, c.Mail.SMTPHost, c.Mail.SMTPUsername, EnvMailLabels); err != nil {
		col.addf("%s", err)
	}

	if c.DB.MinConns > 0 && c.DB.MaxConns > 0 && c.DB.MinConns > c.DB.MaxConns {
		col.addf("DB_MIN_CONNS(%d) 不能大于 DB_MAX_CONNS(%d)", c.DB.MinConns, c.DB.MaxConns)
	}

	if c.DB.SSLMode != "disable" && c.DB.SSLMode != "require" &&
		c.DB.SSLMode != "verify-ca" && c.DB.SSLMode != "verify-full" {
		col.addf("DB_SSLMODE 取值必须是 disable/require/verify-ca/verify-full 之一,当前值: %s", c.DB.SSLMode)
	}

	// 生产环境的安全护栏:HTTP + Secure cookie 组合会让 SSO 静默失效。
	if strings.HasPrefix(c.App.PublicBaseURL, "http://") &&
		c.OIDC.CookieSecure && !isLocalHost(c.App.PublicBaseURL) {
		col.addf("PUBLIC_BASE_URL 是 http 但 SSO_COOKIE_SECURE=true,cookie 会被浏览器丢弃;本地调试请设 SSO_COOKIE_SECURE=false")
	}
}

func isLocalHost(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	h := u.Hostname()
	return h == "localhost" || h == "127.0.0.1" || h == "::1" || strings.HasSuffix(h, ".local")
}

// ---------------------------------------------------------------- 脱敏输出

// Redacted 返回可安全写进启动日志的配置摘要。
//
// 敏感项一律脱敏(db/09-security.md 6.3:启动时只打 key 名不打 value,
// KEY_MASTER_SECRET / DB_PASSWORD 强制脱敏)。
func (c *Config) Redacted() map[string]any {
	return map[string]any{
		"app.host":                c.App.Host,
		"app.port":                c.App.Port,
		"app.public_domain":       c.App.PublicDomain,
		"app.public_base_url":     c.App.PublicBaseURL,
		"app.admin_domain":        c.App.AdminDomain,
		"app.data_dir":            c.App.DataDir,
		"app.trust_proxy_headers": c.App.TrustProxyHeaders,

		"db.host":            c.DB.Host,
		"db.port":            c.DB.Port,
		"db.name":            c.DB.Name,
		"db.user":            c.DB.User,
		"db.password":        mask,
		"db.sslmode":         c.DB.SSLMode,
		"db.auto_migrate":    c.DB.AutoMigrate,
		"key.master_secret":  mask,
		"auth.registration":  c.Auth.RegistrationMode,
		"auth.session_idle":  c.Auth.SessionIdleTTL.String(),
		"auth.session_max":   c.Auth.SessionMaxTTL.String(),
		"oidc.require_pkce":  c.OIDC.RequirePKCE,
		"oidc.cookie_domain": c.OIDC.CookieDomain,
		"oidc.cookie_secure": c.OIDC.CookieSecure,
		"mc.enabled":         c.MC.Enabled,
		"mc.readonly":        c.MC.ReadOnly,
		"mc.skin_external":   c.MC.SkinExternal,
		"mail.transport":     c.Mail.Transport,
		"mail.from":          c.Mail.From,
		"admin.bootstrap":    c.Bootstrap.Enabled,
		"log.level":          c.Log.Level,
		"log.format":         c.Log.Format,
	}
}

const mask = "***"

// ErrNotConfigured 表示缺少必填配置。
var ErrNotConfigured = errors.New("必填配置缺失")

// IsValidationError 判断错误是否为配置校验错误。
func IsValidationError(err error) bool {
	var ve *ValidationError
	return errors.As(err, &ve)
}

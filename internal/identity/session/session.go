// Package session 负责登录态:签发、校验、滑动续期、登出、踢下线。
//
// ADR-004:session 表只存终端用户登录态。授权域与游戏域各有独立的令牌表,
// 本包不感知它们的存在。
package session

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"net/netip"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/yggauth/yggauth/internal/domain"
	"github.com/yggauth/yggauth/internal/platform/apperr"
	"github.com/yggauth/yggauth/internal/platform/clock"
	"github.com/yggauth/yggauth/internal/platform/db"
	"github.com/yggauth/yggauth/internal/platform/db/query"
)

// tokenBytes 是会话令牌的熵。docs/security.md 3.2 要求 32 字节 CSPRNG。
const tokenBytes = 32

// Config 是会话策略。
type Config struct {
	// IdleTTL 是滑动过期:每次活跃都往前推
	IdleTTL time.Duration
	// MaxTTL 是绝对过期:**不因活跃而延长**,防止令牌永不过期
	MaxTTL time.Duration
	// MaxConcurrent 是单账号最大并发会话数。0 表示不限制。
	MaxConcurrent int
	// RenewThreshold 是「剩余空闲时间低于该值才续期」的阈值。
	// 每次请求都写库会给数据库带来不必要压力,按阈值续期足够精确。
	RenewThreshold time.Duration
	// Settings 是运行时配置快照:会话时长后台可改,**不重启生效**。
	// 为 nil 时退回 Config 里由 env 定下的值。
	Settings Settings
}

// Settings 是运行时配置的读取能力(见 docs/configuration.md §6.1)。
type Settings interface {
	Int(ctx context.Context, key string, fallback int) int
}

// Repository 是会话的数据访问接口。
type Repository interface {
	Create(ctx context.Context, in CreateInput) (domain.Session, error)
	GetActive(ctx context.Context, tokenHash []byte) (domain.Session, error)
	GetByID(ctx context.Context, id uuid.UUID) (domain.Session, error)
	ListActive(ctx context.Context, accountID uuid.UUID) ([]domain.Session, error)
	CountActive(ctx context.Context, accountID uuid.UUID) (int64, error)
	Touch(ctx context.Context, id uuid.UUID, idleExpiresAt time.Time) error
	Revoke(ctx context.Context, id uuid.UUID, reason string) error
	RevokeAll(ctx context.Context, accountID uuid.UUID, reason string) error
	LinkSSO(ctx context.Context, id uuid.UUID, ssoSessionID uuid.UUID) error
	// SetMustChangePassword 切换本会话的「强制改密」投影像(见迁移 00008)。
	SetMustChangePassword(ctx context.Context, id uuid.UUID, mustChange bool) error
	DeleteExpired(ctx context.Context, before time.Time) (int64, error)
	RevokeBySSO(ctx context.Context, ssoSessionID uuid.UUID, reason string) error
}

// CreateInput 是创建会话的入参。
type CreateInput struct {
	AccountID     uuid.UUID
	TokenHash     []byte
	ExpiresAt     time.Time
	IdleExpiresAt time.Time
	IP            string
	UserAgent     string
}

// Service 是会话业务逻辑。
type Service struct {
	repo   Repository
	clock  clock.Clock
	cfg    Config
	logger Logger
}

// Logger 是日志器最小接口。
type Logger interface {
	Info(msg string, args ...any)
	Warn(msg string, args ...any)
	Error(msg string, args ...any)
}

// NewService 创建会话服务。
func NewService(repo Repository, clk clock.Clock, cfg Config, logger Logger) *Service {
	return &Service{repo: repo, clock: clk, cfg: cfg, logger: logger}
}

// Issued 是一次会话签发结果。
type Issued struct {
	// Token 是明文令牌,**只在这里出现一次**,库里存的是它的哈希
	Token         string
	SessionID     uuid.UUID
	ExpiresAt     time.Time
	IdleExpiresAt time.Time
}

// ttls 返回当前生效的空闲/绝对超时。
//
// 每次都现读:会话时长是后台可改的键,签发时读一次配置缓存的话,
// 管理员调长会话时长后新会话依旧是老时长。
func (s *Service) ttls(ctx context.Context) (idle, max time.Duration) {
	idle, max = s.cfg.IdleTTL, s.cfg.MaxTTL
	if s.cfg.Settings == nil {
		return idle, max
	}
	idle = time.Duration(s.cfg.Settings.Int(ctx, "session.idle_ttl_hours", int(idle.Hours()))) * time.Hour
	max = time.Duration(s.cfg.Settings.Int(ctx, "session.max_ttl_hours", int(max.Hours()))) * time.Hour
	return idle, max
}

// Issue 签发新会话。
func (s *Service) Issue(ctx context.Context, accountID uuid.UUID, ip, userAgent string) (Issued, error) {
	plain, hash, err := newToken()
	if err != nil {
		return Issued{}, err
	}

	now := s.clock.Now()
	idleTTL, maxTTL := s.ttls(ctx)
	exp := now.Add(maxTTL)

	// 并发上限:超出时踢掉最早的会话。
	// 选「踢最早的」而不是「拒绝新登录」,是因为用户自己通常不会同时开多个浏览器,
	// 超限更可能是异常行为,此时保住旧会话比保住新会话体验更好。
	if s.cfg.MaxConcurrent > 0 {
		for {
			count, err := s.repo.CountActive(ctx, accountID)
			if err != nil {
				return Issued{}, err
			}
			if count < int64(s.cfg.MaxConcurrent) {
				break
			}
			active, err := s.repo.ListActive(ctx, accountID)
			if err != nil {
				return Issued{}, err
			}
			if len(active) == 0 {
				break
			}
			oldest := active[0]
			for _, sess := range active[1:] {
				if sess.LastSeenAt.Before(oldest.LastSeenAt) {
					oldest = sess
				}
			}
			if err := s.repo.Revoke(ctx, oldest.ID, "concurrent_limit"); err != nil {
				s.logger.Error("revoke oldest session failed", "session_id", oldest.ID, "error", err)
				break
			}
		}
	}

	sess, err := s.repo.Create(ctx, CreateInput{
		AccountID:     accountID,
		TokenHash:     hash,
		ExpiresAt:     exp,
		IdleExpiresAt: now.Add(idleTTL),
		IP:            ip,
		UserAgent:     truncateUA(userAgent),
	})
	if err != nil {
		return Issued{}, err
	}

	return Issued{
		Token:         plain,
		SessionID:     sess.ID,
		ExpiresAt:     exp,
		IdleExpiresAt: sess.IdleExpiresAt,
	}, nil
}

// MarkMustChangePassword 把凭据上的「必须改密」真源投到本次会话。
//
// 登录成功时调用:认证中间件每个请求都读会话行,读不到额外代价;
// 真源仍在凭据上,改密时清零并吊销全部会话,这份投影不会残留。
func (s *Service) MarkMustChangePassword(ctx context.Context, sessionID uuid.UUID, mustChange bool) error {
	if !mustChange {
		return nil
	}
	return s.repo.SetMustChangePassword(ctx, sessionID, true)
}

// Authenticated 是一次会话校验的结果。
type Authenticated struct {
	Session domain.Session
	// Renewed 表示本次是否推进了滑动过期
	Renewed bool
}

// Authenticate 校验会话令牌并按需续期。
//
// 两个过期条件都必须过:空闲超时(可续期)与绝对超时(不可续期)。
func (s *Service) Authenticate(ctx context.Context, plain string) (Authenticated, error) {
	if plain == "" {
		return Authenticated{}, apperr.New(apperr.CodeSessionExpired, "未登录")
	}

	now := s.clock.Now()
	sess, err := s.repo.GetActive(ctx, HashToken(plain))
	if err != nil {
		if apperr.Is(err, apperr.CodeNotFound) {
			return Authenticated{}, apperr.New(apperr.CodeSessionExpired, "登录态已过期,请重新登录")
		}
		return Authenticated{}, err
	}
	// 用注入的时钟再复核一次,而不是只信仓储的 SQL 条件。
	// 双重判定有两个理由:
	//  1. 数据库时钟与进程时钟可能漂移,判定权应当在本进程;
	//  2. 过期逻辑必须能被时钟注入驱动,否则测试只能真的去等时间流逝。
	if !sess.Active(now) {
		return Authenticated{}, apperr.New(apperr.CodeSessionExpired, "登录态已过期,请重新登录")
	}

	// 剩余空闲时间不足一半时才续期,避免每个请求都写一次库
	idleTTL, _ := s.ttls(ctx)
	if now.Add(idleTTL / 2).Before(sess.IdleExpiresAt) {
		return Authenticated{Session: sess}, nil
	}

	// 续期后的空闲过期不能越过绝对过期时间
	idle := now.Add(idleTTL)
	if idle.After(sess.ExpiresAt) {
		idle = sess.ExpiresAt
	}
	if err := s.repo.Touch(ctx, sess.ID, idle); err != nil {
		// 续期失败不影响本次请求,只是下一次可能会过期
		s.logger.Warn("session renew failed", "session_id", sess.ID, "error", err)
		return Authenticated{Session: sess}, nil
	}
	sess.IdleExpiresAt = idle
	sess.LastSeenAt = now
	return Authenticated{Session: sess, Renewed: true}, nil
}

// Revoke 吊销单个会话(登出当前)。
func (s *Service) Revoke(ctx context.Context, id uuid.UUID) error {
	return s.repo.Revoke(ctx, id, "logout")
}

// RevokeAll 吊销账号全部会话。
func (s *Service) RevokeAll(ctx context.Context, accountID uuid.UUID) error {
	return s.repo.RevokeAll(ctx, accountID, "logout_all")
}

// ListActive 列出账号的活跃会话。
func (s *Service) ListActive(ctx context.Context, accountID uuid.UUID) ([]domain.Session, error) {
	return s.repo.ListActive(ctx, accountID)
}

// RevokeOne 踢掉指定会话。会话不属于该账号时返回未找到,避免越权。
func (s *Service) RevokeOne(ctx context.Context, accountID, sessionID uuid.UUID) error {
	sess, err := s.repo.GetByID(ctx, sessionID)
	if err != nil {
		return err
	}
	if sess.AccountID != accountID {
		return apperr.ErrNotFound
	}
	return s.repo.Revoke(ctx, sessionID, "revoked_by_user")
}

// LinkSSO 把会话关联到 SSO 会话,供全局登出使用。
func (s *Service) LinkSSO(ctx context.Context, sessionID, ssoSessionID uuid.UUID) error {
	return s.repo.LinkSSO(ctx, sessionID, ssoSessionID)
}

// RevokeBySSO 吊销同一 SSO 会话下的全部登录态。
func (s *Service) RevokeBySSO(ctx context.Context, ssoSessionID uuid.UUID) error {
	return s.repo.RevokeBySSO(ctx, ssoSessionID, "sso_logout")
}

// ---------------------------------------------------------------- 工具

func newToken() (plain string, hash []byte, err error) {
	buf := make([]byte, tokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", nil, apperr.Newf(apperr.CodeInternal, "生成会话令牌失败: %v", err)
	}
	plain = base64.RawURLEncoding.EncodeToString(buf)
	return plain, HashToken(plain), nil
}

// HashToken 返回会话令牌的 sha256。
func HashToken(plain string) []byte {
	sum := sha256.Sum256([]byte(plain))
	return sum[:]
}

// truncateUA 截断 User-Agent,避免超长头把会话表撑大。
func truncateUA(ua string) string {
	const max = 512
	if len(ua) > max {
		return ua[:max]
	}
	return ua
}

// ---------------------------------------------------------------- PG 实现

// PgRepository 是基于 sqlc 的会话仓储。
type PgRepository struct{ q *query.Queries }

// NewPgRepository 创建会话仓储。
func NewPgRepository(pool *db.Pool) *PgRepository { return &PgRepository{q: query.New(pool)} }

func toDomain(row query.IdentitySession) domain.Session {
	s := domain.Session{
		ID:            row.ID,
		AccountID:     row.AccountID,
		CreatedAt:     row.CreatedAt,
		LastSeenAt:    row.LastSeenAt,
		ExpiresAt:     row.ExpiresAt,
		IdleExpiresAt: row.IdleExpiresAt,
		UserAgent:     row.UserAgent.String,
		// 投影像(见迁移 00008 注释):登录时由凭据抄过来,认证中间件只读它
		MustChangePassword: row.MustChangePassword,
	}
	if row.SsoSessionID.Valid {
		if id, err := uuid.FromBytes(row.SsoSessionID.Bytes[:]); err == nil {
			s.SSOSessionID = &id
		}
	}
	if row.RevokedAt.Valid {
		t := row.RevokedAt.Time
		s.RevokedAt = &t
	}
	if row.Ip != nil {
		s.IP = row.Ip.String()
	}
	return s
}

// Create 创建会话。
func (r *PgRepository) Create(ctx context.Context, in CreateInput) (domain.Session, error) {
	row, err := r.q.CreateSession(ctx, query.CreateSessionParams{
		AccountID:     in.AccountID,
		TokenHash:     in.TokenHash,
		SsoSessionID:  pgtype.UUID{},
		ExpiresAt:     in.ExpiresAt,
		IdleExpiresAt: in.IdleExpiresAt,
		Ip:            toInet(in.IP),
		UserAgent:     pgText(in.UserAgent),
	})
	if err != nil {
		return domain.Session{}, apperr.Newf(apperr.CodeInternal, "创建会话失败: %v", err)
	}
	return toDomain(row), nil
}

// GetActive 按令牌哈希取活跃会话。
func (r *PgRepository) GetActive(ctx context.Context, tokenHash []byte) (domain.Session, error) {
	row, err := r.q.GetActiveSessionByTokenHash(ctx, tokenHash)
	if err != nil {
		if db.IsNoRows(err) {
			return domain.Session{}, apperr.ErrNotFound
		}
		return domain.Session{}, apperr.Newf(apperr.CodeInternal, "查询会话失败: %v", err)
	}
	return toDomain(row), nil
}

// GetByID 按主键取会话。
func (r *PgRepository) GetByID(ctx context.Context, id uuid.UUID) (domain.Session, error) {
	row, err := r.q.GetSessionByID(ctx, id)
	if err != nil {
		if db.IsNoRows(err) {
			return domain.Session{}, apperr.ErrNotFound
		}
		return domain.Session{}, apperr.Newf(apperr.CodeInternal, "查询会话失败: %v", err)
	}
	return toDomain(row), nil
}

// ListActive 列出活跃会话。
func (r *PgRepository) ListActive(ctx context.Context, accountID uuid.UUID) ([]domain.Session, error) {
	rows, err := r.q.ListActiveSessions(ctx, accountID)
	if err != nil {
		return nil, apperr.Newf(apperr.CodeInternal, "列出会话失败: %v", err)
	}
	out := make([]domain.Session, 0, len(rows))
	for _, row := range rows {
		out = append(out, toDomain(row))
	}
	return out, nil
}

// CountActive 统计活跃会话数。
func (r *PgRepository) CountActive(ctx context.Context, accountID uuid.UUID) (int64, error) {
	n, err := r.q.CountActiveSessions(ctx, accountID)
	if err != nil {
		return 0, apperr.Newf(apperr.CodeInternal, "统计会话失败: %v", err)
	}
	return n, nil
}

// Touch 推进滑动过期。
func (r *PgRepository) Touch(ctx context.Context, id uuid.UUID, idleExpiresAt time.Time) error {
	_, err := r.q.TouchSession(ctx, query.TouchSessionParams{
		ID:            id,
		IdleExpiresAt: idleExpiresAt,
	})
	if err != nil {
		return apperr.Newf(apperr.CodeInternal, "续期会话失败: %v", err)
	}
	return nil
}

// Revoke 吊销单个会话。
func (r *PgRepository) Revoke(ctx context.Context, id uuid.UUID, reason string) error {
	_, err := r.q.RevokeSession(ctx, query.RevokeSessionParams{ID: id, RevokeReason: pgText(reason)})
	if err != nil {
		if db.IsNoRows(err) {
			return nil // 已经吊销过,幂等
		}
		return apperr.Newf(apperr.CodeInternal, "吊销会话失败: %v", err)
	}
	return nil
}

// RevokeAll 吊销账号全部会话。
func (r *PgRepository) RevokeAll(ctx context.Context, accountID uuid.UUID, reason string) error {
	if _, err := r.q.RevokeAllSessions(ctx, query.RevokeAllSessionsParams{
		AccountID:    accountID,
		RevokeReason: pgText(reason),
	}); err != nil {
		return apperr.Newf(apperr.CodeInternal, "吊销会话失败: %v", err)
	}
	return nil
}

// LinkSSO 关联 SSO 会话。
func (r *PgRepository) LinkSSO(ctx context.Context, id, ssoSessionID uuid.UUID) error {
	if err := r.q.LinkSessionToSSO(ctx, query.LinkSessionToSSOParams{
		ID:           id,
		SsoSessionID: pgtype.UUID{Bytes: ssoSessionID, Valid: true},
	}); err != nil {
		return apperr.Newf(apperr.CodeInternal, "关联 SSO 会话失败: %v", err)
	}
	return nil
}

// SetMustChangePassword 切换本会话的强制改密投影。
func (r *PgRepository) SetMustChangePassword(ctx context.Context, id uuid.UUID, mustChange bool) error {
	if err := r.q.SetSessionMustChangePassword(ctx, query.SetSessionMustChangePasswordParams{
		ID:                 id,
		MustChangePassword: mustChange,
	}); err != nil {
		return apperr.Newf(apperr.CodeInternal, "更新会话强制改密标记失败: %v", err)
	}
	return nil
}

// RevokeBySSO 吊销同一 SSO 会话下的全部会话。
func (r *PgRepository) RevokeBySSO(ctx context.Context, ssoSessionID uuid.UUID, reason string) error {
	rows, err := r.q.GetSessionBySSOID(ctx, pgtype.UUID{Bytes: ssoSessionID, Valid: true})
	if err != nil {
		return apperr.Newf(apperr.CodeInternal, "查询 SSO 会话失败: %v", err)
	}
	for _, row := range rows {
		if err := r.Revoke(ctx, row.ID, reason); err != nil {
			return err
		}
	}
	return nil
}

// toInet 把 IP 字符串转成 netip.Addr。
//
// 空串(拿不到真实 IP,例如未信任代理头)直接返回 nil,
// 让数据库存 NULL 而不是把空字符串写进 inet 列。
func toInet(ip string) *netip.Addr {
	if ip == "" {
		return nil
	}
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return nil
	}
	return &addr
}

// pgText 把字符串转成 pgtype.Text。
func pgText(s string) pgtype.Text {
	return pgtype.Text{String: s, Valid: s != ""}
}

// DeleteExpired 删除已过期或已吊销超过 retention 的会话,供后台任务调用。
func (r *PgRepository) DeleteExpired(ctx context.Context, before time.Time) (int64, error) {
	n, err := r.q.DeleteExpiredSessions(ctx)
	if err != nil {
		return 0, apperr.Newf(apperr.CodeInternal, "清理过期会话失败: %v", err)
	}
	_ = before
	return n, nil
}

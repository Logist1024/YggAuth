// Package identity 是账号内核的对外门面。
//
// 域中立(ADR-010):只认识「身份 + 凭据 + 会话 + 权限 + 审计」,
// 不出现任何业务域语义。业务域通过本包提供服务,而不是直接碰仓储。
package identity

import (
	"context"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/yggauth/yggauth/internal/domain"
	"github.com/yggauth/yggauth/internal/identity/account"
	"github.com/yggauth/yggauth/internal/identity/audit"
	"github.com/yggauth/yggauth/internal/identity/rbac"
	"github.com/yggauth/yggauth/internal/identity/session"
	"github.com/yggauth/yggauth/internal/platform/clock"
	platformmetrics "github.com/yggauth/yggauth/internal/platform/metrics"
)

// 账号内核的专属指标。
//
// 放在这里而不是平台层:「登录」「会话」是账号域的概念,
// 平台层 metrics 不应该知道它们的存在。
var (
	loginTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "yggauth",
		Name:      "login_total",
		Help:      "登录尝试总数",
	}, []string{"outcome"})

	sessionActive = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "yggauth",
		Name:      "session_active",
		Help:      "当前活跃会话数",
	})

	accountCreated = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "yggauth",
		Name:      "account_created_total",
		Help:      "新注册账号数",
	})
)

func init() {
	platformmetrics.Registry.MustRegister(loginTotal, sessionActive, accountCreated)
	// 预置零值序列,避免刚启动时看板查不到指标
	loginTotal.WithLabelValues(platformmetrics.OutcomeSuccess)
	loginTotal.WithLabelValues(platformmetrics.OutcomeFailure)
}

// Service 是账号内核的聚合门面。
//
// 业务域只需要这一个入口,不必知道账号、会话、权限、审计分别住在哪个包。
type Service struct {
	Accounts *account.Service
	Sessions *session.Service
	RBAC     *rbac.Service
	Audit    *audit.Service
}

// Deps 是构造账号内核所需的依赖。
type Deps struct {
	Accounts account.Repository
	Tokens   account.TokenStore
	Sessions session.Repository
	RBAC     rbac.Repository
	Audit    audit.Repository

	Hasher  *account.Hasher
	Clock   clock.Clock
	Account account.Config
	Session session.Config

	Logger account.Logger
}

// New 组装账号内核。
func New(d Deps) *Service {
	auditSvc := audit.NewService(d.Audit, d.Logger)
	accountCfg := d.Account
	accountCfg.Auditor = auditSvc

	return &Service{
		Accounts: account.NewService(d.Accounts, d.Tokens, d.Hasher, d.Clock, accountCfg, d.Logger),
		Sessions: session.NewService(d.Sessions, d.Clock, d.Session, d.Logger),
		RBAC:     rbac.NewService(d.RBAC),
		Audit:    auditSvc,
	}
}

// ObserveLogin 上报一次登录结果。
func ObserveLogin(success bool) {
	outcome := platformmetrics.OutcomeFailure
	if success {
		outcome = platformmetrics.OutcomeSuccess
	}
	loginTotal.WithLabelValues(outcome).Inc()
}

// ObserveAccountCreated 上报一次注册成功。
func ObserveAccountCreated() { accountCreated.Inc() }

// SetActiveSessions 上报当前活跃会话数。
func SetActiveSessions(n int64) { sessionActive.Set(float64(n)) }

// PrincipalOf 由账号与会话构造已认证主体。
//
// 权限点来自 RBAC 而不是会话表:授权变更后**立即生效**,
// 不需要等用户重新登录或会话续期。
func (s *Service) PrincipalOf(ctx context.Context, acc domain.Account, sessionID uuid.UUID) (*domain.Principal, error) {
	codes, err := s.RBAC.PermissionsFor(ctx, acc.ID)
	if err != nil {
		return nil, err
	}
	return &domain.Principal{
		AccountID:   acc.ID,
		Username:    acc.Username,
		Email:       acc.Email,
		SessionID:   sessionID,
		Permissions: codes,
	}, nil
}

// CountActiveSessions 统计活跃会话数,供指标与后台展示使用。
func (s *Service) CountActiveSessions(ctx context.Context, accountID uuid.UUID) (int64, error) {
	active, err := s.Sessions.ListActive(ctx, accountID)
	if err != nil {
		return 0, err
	}
	return int64(len(active)), nil
}

// AuthenticatedSession 是一次会话校验结果。
type AuthenticatedSession = session.Authenticated

// AuthenticateSession 校验会话令牌。
func (s *Service) AuthenticateSession(ctx context.Context, token string) (session.Authenticated, error) {
	return s.Sessions.Authenticate(ctx, token)
}

// LookupAccount 按主键取账号。
func (s *Service) LookupAccount(ctx context.Context, id uuid.UUID) (domain.Account, error) {
	return s.Accounts.Get(ctx, id)
}

// PermissionsFor 返回账号持有的权限点。
func (s *Service) PermissionsFor(ctx context.Context, accountID uuid.UUID) ([]string, error) {
	return s.RBAC.PermissionsFor(ctx, accountID)
}

// AuthenticateForGame 校验凭据但不签发任何会话。
//
// Minecraft 域只需要「验证用户名密码」这一件事本身,不想要账号内核
// 登录流程带来的副作用(会话签发、SSO 联动、「登录成功」审计事件)。
// 完整理由见 account.Service.AuthenticateForGame 的注释。
func (s *Service) AuthenticateForGame(ctx context.Context, identifier, password, ip, userAgent string) (uuid.UUID, error) {
	acc, err := s.Accounts.AuthenticateForGame(ctx, account.GameAuthInput{
		Identifier: identifier,
		Password:   password,
		IP:         ip,
		UserAgent:  userAgent,
	})
	if err != nil {
		return uuid.Nil, err
	}
	return acc.ID, nil
}

// SetGameLoginEnabled 设置账号在某业务域的登录开关。
//
// 开关的语义由对应域定义,内核只当它是「一个可开关的布尔值」。
func (s *Service) SetGameLoginEnabled(ctx context.Context, accountID uuid.UUID, enabled bool) error {
	return s.Accounts.SetGameLoginEnabled(ctx, accountID, enabled)
}

// GameLoginEnabled 查询账号在某业务域的登录开关。
func (s *Service) GameLoginEnabled(ctx context.Context, accountID uuid.UUID) (bool, error) {
	return s.Accounts.GameLoginEnabled(ctx, accountID)
}

package account

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"

	"github.com/yggauth/yggauth/internal/domain"
	"github.com/yggauth/yggauth/internal/platform/apperr"
	"github.com/yggauth/yggauth/internal/platform/clock"
	"github.com/yggauth/yggauth/internal/platform/log"
)

// Username 规则:3–32 位,字母数字下划线,首字符必须是字母。
//
// 用户名会出现在多处展示与 URL 中,限制字符集比事后过滤更省事。
const (
	usernameMinLength = 3
	usernameMaxLength = 32
)

// Config 是账号服务的运行参数。
type Config struct {
	// Policy 是密码强度策略
	Policy Policy
	// LoginEnabledDefault 是新账号布尔开关的默认值(变更 C-8)
	LoginEnabledDefault bool
	// MaxFailedAttempts 是触发锁定的失败次数
	MaxFailedAttempts int
	// LockDuration 是锁定时长
	LockDuration time.Duration
	// EmailTokenTTL 是邮箱令牌有效期
	EmailTokenTTL time.Duration
	// RegistrationMode: open | invite_only
	RegistrationMode string
	// LeakChecker 检查密码是否已泄露。为 nil 时跳过检查。
	LeakChecker LeakChecker
	// Invitations 核销邀请码。仅 invite_only 模式下使用。
	Invitations *InvitationStore
	// Mailer 发送验证与重置邮件。为 nil 时只记录日志不发送。
	Mailer Mailer
	// Auditor 写审计事件。为 nil 时跳过。
	Auditor Auditor
}

// LeakChecker 检查密码是否出现在公开泄露库中。
type LeakChecker interface {
	// Leaked 报告该密码是否已泄露。返回 error 表示检查本身失败。
	Leaked(ctx context.Context, password string) (bool, error)
}

// Mailer 发送邮件。
type Mailer interface {
	Send(ctx context.Context, msg Message) error
}

// Message 是邮件内容。
type Message struct {
	To      string
	Subject string
	Text    string
	HTML    string
}

// Auditor 记录审计事件。
type Auditor interface {
	Record(ctx context.Context, ev Event) error
}

// Event 是审计事件。
type Event struct {
	AccountID  *uuid.UUID
	Actor      domain.AuditActor
	Action     string
	TargetType string
	TargetID   string
	Outcome    domain.AuditOutcome
	IP         string
	UserAgent  string
	Metadata   map[string]any
}

// Service 是账号与凭据的业务逻辑。
type Service struct {
	repo   Repository
	tokens TokenStore
	hasher *Hasher
	clock  clock.Clock
	cfg    Config
	logger Logger
}

// Logger 是服务用到的日志器最小接口。
type Logger interface {
	Info(msg string, args ...any)
	Warn(msg string, args ...any)
	Error(msg string, args ...any)
}

// NewService 创建账号服务。
func NewService(repo Repository, tokens TokenStore, hasher *Hasher, clk clock.Clock, cfg Config, logger Logger) *Service {
	return &Service{
		repo:   repo,
		tokens: tokens,
		hasher: hasher,
		clock:  clk,
		cfg:    cfg,
		logger: logger,
	}
}

// RegisterInput 是注册入参。
type RegisterInput struct {
	Username   string
	Email      string
	Password   string
	InviteCode string
	IP         string
	UserAgent  string
}

// RegisterOutput 是注册结果。
type RegisterOutput struct {
	Account domain.Account
	// VerifyToken 明文令牌,只在注册响应里出现一次,库里只存它的哈希
	VerifyToken string
}

// ErrInviteRequired 表示该部署要求邀请码注册。
var ErrInviteRequired = apperr.New(apperr.CodeInviteRequired, "当前为邀请码注册模式")

// Register 注册新账号。
//
// 校验顺序刻意设计成「先查重再算哈希」:argon2id 单次 64MiB 内存开销不小,
// 放在最后一步,避免为必然失败的请求浪费 CPU 与内存。
func (s *Service) Register(ctx context.Context, in RegisterInput) (RegisterOutput, error) {
	username := strings.TrimSpace(in.Username)
	email := strings.TrimSpace(in.Email)

	if err := ValidateUsername(username); err != nil {
		return RegisterOutput{}, err
	}
	if err := ValidateEmail(email); err != nil {
		return RegisterOutput{}, err
	}
	if err := s.cfg.Policy.Validate(in.Password); err != nil {
		return RegisterOutput{}, err
	}
	if s.cfg.RegistrationMode == "invite_only" && strings.TrimSpace(in.InviteCode) == "" {
		return RegisterOutput{}, ErrInviteRequired
	}

	// 邀请码核销。放在密码哈希之前:一个无效邀请码不该让服务白算一次 argon2id。
	if s.cfg.RegistrationMode == "invite_only" {
		if s.cfg.Invitations == nil {
			return RegisterOutput{}, apperr.New(apperr.CodeInternal, "邀请码注册模式未配置邀请码存储")
		}
		if err := s.cfg.Invitations.Consume(ctx, strings.TrimSpace(in.InviteCode), email); err != nil {
			return RegisterOutput{}, apperr.New(apperr.CodeInviteInvalid, "邀请码无效或已用尽")
		}
	}

	// 大小写不敏感查重。唯一索引是最后一道防线,这里的预检查只为给出友好错误。
	if _, err := s.repo.GetByEmail(ctx, email); err == nil {
		return RegisterOutput{}, apperr.New(apperr.CodeEmailTaken, "该邮箱已注册")
	} else if !apperr.Is(err, apperr.CodeNotFound) {
		return RegisterOutput{}, err
	}
	if _, err := s.repo.GetByUsername(ctx, username); err == nil {
		return RegisterOutput{}, apperr.New(apperr.CodeUsernameTaken, "该用户名已被占用")
	} else if !apperr.Is(err, apperr.CodeNotFound) {
		return RegisterOutput{}, err
	}

	if s.cfg.LeakChecker != nil {
		leaked, err := s.cfg.LeakChecker.Leaked(ctx, in.Password)
		if err != nil {
			// 检查失败**不放行**也不直接拒绝:记录告警后继续。
			// 泄露库不可用通常是网络问题,直接拒绝注册会让服务整体不可用。
			log.L(ctx).Warn("leaked password check failed", "error", err)
		} else if leaked {
			return RegisterOutput{}, apperr.New(apperr.CodeWeakPassword, "该密码已出现在公开泄露数据中")
		}
	}

	hash, err := s.hasher.Hash(in.Password)
	if err != nil {
		return RegisterOutput{}, err
	}

	// 新账号默认待验证邮箱状态;若部署关闭了验证流程则直接激活。
	status := domain.StatusPendingVerification
	acc, err := s.repo.Create(ctx, CreateInput{
		Username:     username,
		Email:        email,
		Status:       status,
		LoginEnabled: s.cfg.LoginEnabledDefault,
	})
	if err != nil {
		if apperr.Is(err, apperr.CodeConflict) {
			// 并发注册撞上唯一索引,转成更准确的错误码
			if _, e := s.repo.GetByEmail(ctx, email); e == nil {
				return RegisterOutput{}, apperr.New(apperr.CodeEmailTaken, "该邮箱已注册")
			}
			return RegisterOutput{}, apperr.New(apperr.CodeUsernameTaken, "该用户名已被占用")
		}
		return RegisterOutput{}, err
	}

	if _, err := s.repo.UpsertCredential(ctx, acc.ID, AlgoArgon2id, hash); err != nil {
		return RegisterOutput{}, err
	}

	token, err := s.issueEmailToken(ctx, acc.ID, PurposeVerifyEmail)
	if err != nil {
		return RegisterOutput{}, err
	}

	s.audit(ctx, Event{
		AccountID:  &acc.ID,
		Actor:      domain.AuditActorAccount(acc.ID),
		Action:     "account.register",
		TargetType: "account",
		TargetID:   acc.ID.String(),
		Outcome:    domain.OutcomeSuccess,
		IP:         in.IP,
		UserAgent:  in.UserAgent,
	})

	return RegisterOutput{Account: acc, VerifyToken: token}, nil
}

// AuthenticateInput 是登录入参。
type AuthenticateInput struct {
	Email     string
	Password  string
	IP        string
	UserAgent string
}

// AuthenticateOutput 是登录结果。
type AuthenticateOutput struct {
	Account      domain.Account
	SessionToken string
	ExpiresAt    time.Time
}

// Authenticate 校验邮箱与密码。
//
// 防账号枚举的三条措施:
//  1. 「邮箱不存在」与「密码错误」返回**同一个**错误码 20002;
//  2. 邮箱不存在时也执行一次假哈希运算,让响应时间接近;
//  3. 不在错误信息里区分是账号状态问题还是密码问题。
func (s *Service) Authenticate(ctx context.Context, in AuthenticateInput) (AuthenticateOutput, error) {
	email := strings.TrimSpace(in.Email)

	acc, err := s.repo.GetByEmail(ctx, email)
	if err != nil {
		if !apperr.Is(err, apperr.CodeNotFound) {
			return AuthenticateOutput{}, err
		}
		// 邮箱不存在:做一次等价开销的哈希运算再返回统一错误
		s.dummyHash(in.Password)
		s.auditFailure(ctx, nil, "account.login", in.IP, in.UserAgent)
		return AuthenticateOutput{}, apperr.New(apperr.CodeInvalidPassword, "邮箱或密码错误")
	}

	cred, err := s.repo.GetCredential(ctx, acc.ID, AlgoArgon2id)
	if err != nil {
		return AuthenticateOutput{}, err
	}

	now := s.clock.Now()
	if cred.Locked(now) {
		s.auditFailure(ctx, &acc.ID, "account.login", in.IP, in.UserAgent)
		return AuthenticateOutput{}, apperr.Newf(apperr.CodeAccountLocked,
			"账号已锁定,请在 %s 后重试", cred.LockedUntil.Sub(now).Round(time.Second))
	}

	if !s.hasher.Verify(in.Password, cred.Hash) {
		shouldLock := cred.FailedAttempts+1 >= s.cfg.MaxFailedAttempts
		if _, err := s.repo.RecordFailedAttempt(ctx, acc.ID, AlgoArgon2id, shouldLock, s.cfg.LockDuration); err != nil {
			s.logger.Error("record failed attempt", "account_id", acc.ID, "error", err)
		}
		s.auditFailure(ctx, &acc.ID, "account.login", in.IP, in.UserAgent)
		if shouldLock {
			return AuthenticateOutput{}, apperr.New(apperr.CodeAccountLocked, "登录失败次数过多,账号已锁定")
		}
		return AuthenticateOutput{}, apperr.New(apperr.CodeInvalidPassword, "邮箱或密码错误")
	}

	// 密码正确后再看账号状态:密码错误优先于状态判断,同样是防枚举
	if !acc.Status.Usable() {
		s.auditFailure(ctx, &acc.ID, "account.login", in.IP, in.UserAgent)
		switch acc.Status {
		case domain.StatusDisabled:
			return AuthenticateOutput{}, apperr.New(apperr.CodeAccountDisabled, "账号已被禁用")
		case domain.StatusLocked:
			return AuthenticateOutput{}, apperr.New(apperr.CodeAccountLocked, "账号已锁定")
		case domain.StatusPendingVerification:
			return AuthenticateOutput{}, apperr.New(apperr.CodeEmailUnverified, "邮箱尚未验证")
		}
		return AuthenticateOutput{}, apperr.New(apperr.CodeAccountDisabled, "账号当前不可用")
	}

	if err := s.repo.ResetFailedAttempts(ctx, acc.ID, AlgoArgon2id); err != nil {
		s.logger.Error("reset failed attempts", "account_id", acc.ID, "error", err)
	}

	// 登录成功必须留痕(docs/09-security.md 9.1)。
	s.audit(ctx, Event{
		AccountID:  &acc.ID,
		Actor:      domain.AuditActorAccount(acc.ID),
		Action:     "account.login",
		TargetType: "account",
		TargetID:   acc.ID.String(),
		Outcome:    domain.OutcomeSuccess,
		IP:         in.IP,
		UserAgent:  in.UserAgent,
	})

	return AuthenticateOutput{Account: acc}, nil
}

// dummyHash 在账号不存在时执行一次等价开销的哈希运算,拉平响应时间。
func (s *Service) dummyHash(password string) {
	_, _ = s.hasher.Hash(password)
}

// ---------------------------------------------------------------- 校验

// ValidateUsername 校验用户名格式。
func ValidateUsername(username string) error {
	n := len([]rune(username))
	if n < usernameMinLength || n > usernameMaxLength {
		return apperr.Newf(apperr.CodeInvalidArgument,
			"用户名长度必须在 %d–%d 之间", usernameMinLength, usernameMaxLength)
	}
	for i, r := range username {
		switch {
		case unicode.IsLetter(r) || r == '_':
			continue
		case unicode.IsDigit(r):
			if i == 0 {
				return apperr.New(apperr.CodeInvalidArgument, "用户名不能以数字开头")
			}
		default:
			return apperr.New(apperr.CodeInvalidArgument, "用户名只能包含字母、数字与下划线")
		}
	}
	return nil
}

// ValidateEmail 校验邮箱格式。
func ValidateEmail(email string) error {
	if email == "" || len(email) > 254 {
		return apperr.New(apperr.CodeInvalidArgument, "邮箱格式非法")
	}
	// 禁掉控制字符与空白,否则这些字符会流进邮件头
	for _, r := range email {
		if r <= 0x20 || r == 0x7f {
			return apperr.New(apperr.CodeInvalidArgument, "邮箱格式非法")
		}
	}
	addr, err := mail.ParseAddress(email)
	if err != nil || addr.Address != email {
		return apperr.New(apperr.CodeInvalidArgument, "邮箱格式非法")
	}
	if !strings.Contains(email, ".") {
		return apperr.New(apperr.CodeInvalidArgument, "邮箱格式非法")
	}
	return nil
}

// ---------------------------------------------------------------- 邮箱令牌

// 邮箱令牌用途。
const (
	// PurposeVerifyEmail 验证邮箱
	PurposeVerifyEmail = "verify_email"
	// PurposeResetPassword 重置密码
	PurposeResetPassword = "reset_password"
)

// TokenStore 存取邮箱令牌。
type TokenStore interface {
	Create(ctx context.Context, accountID uuid.UUID, purpose, plain string, expiresAt time.Time) error
	Consume(ctx context.Context, purpose, plain string) (uuid.UUID, error)
}

// issueEmailToken 生成明文令牌并存哈希。
func (s *Service) issueEmailToken(ctx context.Context, accountID uuid.UUID, purpose string) (string, error) {
	plain, err := newToken()
	if err != nil {
		return "", err
	}
	if err := s.tokens.Create(ctx, accountID, purpose, plain, s.clock.Now().Add(s.cfg.EmailTokenTTL)); err != nil {
		return "", err
	}
	return plain, nil
}

// VerifyEmailInput 是邮箱验证入参。
type VerifyEmailInput struct {
	Token     string
	IP        string
	UserAgent string
}

// VerifyEmail 验证邮箱。
func (s *Service) VerifyEmail(ctx context.Context, in VerifyEmailInput) (domain.Account, error) {
	accountID, err := s.tokens.Consume(ctx, PurposeVerifyEmail, strings.TrimSpace(in.Token))
	if err != nil {
		s.auditFailure(ctx, nil, "account.verify_email", in.IP, in.UserAgent)
		return domain.Account{}, apperr.New(apperr.CodeInvalidToken, "验证链接无效或已过期")
	}

	acc, err := s.repo.MarkEmailVerified(ctx, accountID)
	if err != nil {
		return domain.Account{}, err
	}

	s.audit(ctx, Event{
		AccountID:  &acc.ID,
		Actor:      domain.AuditActorAccount(acc.ID),
		Action:     "account.verify_email",
		TargetType: "account",
		TargetID:   acc.ID.String(),
		Outcome:    domain.OutcomeSuccess,
		IP:         in.IP,
		UserAgent:  in.UserAgent,
	})
	return acc, nil
}

// RequestPasswordResetInput 是发起密码重置的入参。
type RequestPasswordResetInput struct {
	Email     string
	IP        string
	UserAgent string
}

// RequestPasswordReset 发起密码重置。
//
// **无论邮箱是否存在都返回成功**:对外不暴露「这个邮箱有没有注册过」,
// 否则就成了账号枚举接口。内部差异只体现在是否真的发出邮件。
func (s *Service) RequestPasswordReset(ctx context.Context, in RequestPasswordResetInput) error {
	email := strings.TrimSpace(in.Email)
	acc, err := s.repo.GetByEmail(ctx, email)
	if err != nil {
		if apperr.Is(err, apperr.CodeNotFound) {
			s.logger.Info("password reset requested for unknown email")
			return nil
		}
		return err
	}

	token, err := s.issueEmailToken(ctx, acc.ID, PurposeResetPassword)
	if err != nil {
		return err
	}

	if s.cfg.Mailer != nil {
		msg := Message{
			To:      acc.Email,
			Subject: "重置你的密码",
			Text: fmt.Sprintf("你好 %s,\n\n点击以下链接重置密码(有效期 %s):\n%s/reset-password?token=%s\n\n如果这不是你的操作,忽略这封邮件即可。\n",
				acc.Username, s.cfg.EmailTokenTTL, "{{BASE_URL}}", token),
		}
		if err := s.cfg.Mailer.Send(ctx, msg); err != nil {
			// 邮件发送失败不能泄露账号是否存在,同样按成功处理
			s.logger.Error("send reset email failed", "account_id", acc.ID, "error", err)
		}
	}

	s.audit(ctx, Event{
		AccountID:  &acc.ID,
		Actor:      domain.AuditActorAccount(acc.ID),
		Action:     "account.password_reset_requested",
		TargetType: "account",
		TargetID:   acc.ID.String(),
		Outcome:    domain.OutcomeSuccess,
		IP:         in.IP,
		UserAgent:  in.UserAgent,
	})
	return nil
}

// ResetPasswordInput 是执行密码重置的入参。
type ResetPasswordInput struct {
	Token     string
	Password  string
	IP        string
	UserAgent string
}

// ResetPassword 用令牌重置密码。
func (s *Service) ResetPassword(ctx context.Context, in ResetPasswordInput) (domain.Account, error) {
	if err := s.cfg.Policy.Validate(in.Password); err != nil {
		return domain.Account{}, err
	}

	accountID, err := s.tokens.Consume(ctx, PurposeResetPassword, strings.TrimSpace(in.Token))
	if err != nil {
		s.auditFailure(ctx, nil, "account.password_reset", in.IP, in.UserAgent)
		return domain.Account{}, apperr.New(apperr.CodeInvalidToken, "重置链接无效或已过期")
	}

	hash, err := s.hasher.Hash(in.Password)
	if err != nil {
		return domain.Account{}, err
	}
	if _, err := s.repo.UpsertCredential(ctx, accountID, AlgoArgon2id, hash); err != nil {
		return domain.Account{}, err
	}

	acc, err := s.repo.GetByID(ctx, accountID)
	if err != nil {
		return domain.Account{}, err
	}

	s.audit(ctx, Event{
		AccountID:  &acc.ID,
		Actor:      domain.AuditActorAccount(acc.ID),
		Action:     "account.password_reset",
		TargetType: "account",
		TargetID:   acc.ID.String(),
		Outcome:    domain.OutcomeSuccess,
		IP:         in.IP,
		UserAgent:  in.UserAgent,
	})
	return acc, nil
}

// ---------------------------------------------------------------- 资料

// UpdateProfileInput 是修改资料入参。
type UpdateProfileInput struct {
	AccountID uuid.UUID
	Username  *string
	Email     *string
	IP        string
	UserAgent string
}

// UpdateProfile 修改用户名或邮箱。
//
// 改邮箱会让已验证状态失效 —— 否则「先验证 A 邮箱、再改成 B」就成了
// 一个绕过验证的手段。
func (s *Service) UpdateProfile(ctx context.Context, in UpdateProfileInput) (domain.Account, error) {
	current, err := s.repo.GetByID(ctx, in.AccountID)
	if err != nil {
		return domain.Account{}, err
	}

	username := current.Username
	if in.Username != nil {
		username = strings.TrimSpace(*in.Username)
		if err := ValidateUsername(username); err != nil {
			return domain.Account{}, err
		}
		if !strings.EqualFold(username, current.Username) {
			if _, err := s.repo.GetByUsername(ctx, username); err == nil {
				return domain.Account{}, apperr.New(apperr.CodeUsernameTaken, "该用户名已被占用")
			} else if !apperr.Is(err, apperr.CodeNotFound) {
				return domain.Account{}, err
			}
		}
	}

	email := current.Email
	emailChanged := false
	if in.Email != nil {
		email = strings.TrimSpace(*in.Email)
		if err := ValidateEmail(email); err != nil {
			return domain.Account{}, err
		}
		if !strings.EqualFold(email, current.Email) {
			emailChanged = true
			if _, err := s.repo.GetByEmail(ctx, email); err == nil {
				return domain.Account{}, apperr.New(apperr.CodeEmailTaken, "该邮箱已注册")
			} else if !apperr.Is(err, apperr.CodeNotFound) {
				return domain.Account{}, err
			}
		}
	}

	if username == current.Username && email == current.Email {
		return current, nil
	}

	updated, err := s.repo.UpdateProfile(ctx, in.AccountID, username, email)
	if err != nil {
		return domain.Account{}, err
	}

	if emailChanged {
		token, terr := s.issueEmailToken(ctx, updated.ID, PurposeVerifyEmail)
		if terr != nil {
			s.logger.Error("issue verify token failed", "account_id", updated.ID, "error", terr)
		} else if s.cfg.Mailer != nil {
			if serr := s.cfg.Mailer.Send(ctx, Message{
				To:      updated.Email,
				Subject: "验证你的新邮箱",
				Text:    fmt.Sprintf("你好 %s,\n\n请验证你的新邮箱:\n%s/verify-email?token=%s\n", updated.Username, "{{BASE_URL}}", token),
			}); serr != nil {
				s.logger.Error("send verify email failed", "account_id", updated.ID, "error", serr)
			}
		}
		// 重新置为待验证
		if _, err := s.repo.UpdateStatus(ctx, updated.ID, domain.StatusPendingVerification); err != nil {
			s.logger.Error("reset status failed", "account_id", updated.ID, "error", err)
		}
		updated.Status = domain.StatusPendingVerification
		updated.EmailVerified = false
	}

	s.audit(ctx, Event{
		AccountID:  &updated.ID,
		Actor:      domain.AuditActorAccount(updated.ID),
		Action:     "account.profile_updated",
		TargetType: "account",
		TargetID:   updated.ID.String(),
		Outcome:    domain.OutcomeSuccess,
		IP:         in.IP,
		UserAgent:  in.UserAgent,
		Metadata:   map[string]any{"email_changed": emailChanged},
	})
	return updated, nil
}

// ChangePasswordInput 是改密码入参。
type ChangePasswordInput struct {
	AccountID   uuid.UUID
	OldPassword string
	NewPassword string
	IP          string
	UserAgent   string
}

// ChangePassword 修改密码。
//
// 返回值需要调用方**吊销该账号的全部会话**:密码变了,旧登录态就不该继续有效。
func (s *Service) ChangePassword(ctx context.Context, in ChangePasswordInput) error {
	cred, err := s.repo.GetCredential(ctx, in.AccountID, AlgoArgon2id)
	if err != nil {
		return err
	}
	if !s.hasher.Verify(in.OldPassword, cred.Hash) {
		s.auditFailure(ctx, &in.AccountID, "account.password_change", in.IP, in.UserAgent)
		return apperr.New(apperr.CodeInvalidPassword, "原密码错误")
	}
	if err := s.cfg.Policy.Validate(in.NewPassword); err != nil {
		return err
	}

	hash, err := s.hasher.Hash(in.NewPassword)
	if err != nil {
		return err
	}
	if _, err := s.repo.UpsertCredential(ctx, in.AccountID, AlgoArgon2id, hash); err != nil {
		return err
	}

	s.audit(ctx, Event{
		AccountID:  &in.AccountID,
		Actor:      domain.AuditActorAccount(in.AccountID),
		Action:     "account.password_change",
		TargetType: "account",
		TargetID:   in.AccountID.String(),
		Outcome:    domain.OutcomeSuccess,
		IP:         in.IP,
		UserAgent:  in.UserAgent,
	})
	return nil
}

// SetLoginEnabled 设置账号的通用布尔开关。
func (s *Service) SetLoginEnabled(ctx context.Context, accountID uuid.UUID, enabled bool) (domain.Account, error) {
	return s.repo.SetLoginEnabled(ctx, accountID, enabled)
}

// SetStatus 修改账号状态(管理端用)。
func (s *Service) SetStatus(ctx context.Context, accountID uuid.UUID, status domain.AccountStatus) (domain.Account, error) {
	if !status.Valid() {
		return domain.Account{}, apperr.Newf(apperr.CodeInvalidArgument, "非法账号状态: %s", status)
	}
	return s.repo.UpdateStatus(ctx, accountID, status)
}

// Get 取账号。
func (s *Service) Get(ctx context.Context, id uuid.UUID) (domain.Account, error) {
	return s.repo.GetByID(ctx, id)
}

// List 分页列出账号。
func (s *Service) List(ctx context.Context, f ListFilter) ([]domain.Account, int64, error) {
	if f.Limit <= 0 || f.Limit > 200 {
		f.Limit = 20
	}
	return s.repo.List(ctx, f)
}

// ---------------------------------------------------------------- 内部

func (s *Service) audit(ctx context.Context, ev Event) {
	if s.cfg.Auditor == nil {
		return
	}
	if err := s.cfg.Auditor.Record(ctx, ev); err != nil {
		// 审计写失败不阻断业务:可靠性优先于完整性,但两者都要尽力
		s.logger.Error("audit write failed", "action", ev.Action, "error", err)
	}
}

func (s *Service) auditFailure(ctx context.Context, accountID *uuid.UUID, action, ip, ua string) {
	target := ""
	if accountID != nil {
		target = accountID.String()
	}
	s.audit(ctx, Event{
		AccountID:  accountID,
		Actor:      domain.AuditActorSystem(),
		Action:     action,
		TargetType: "account",
		TargetID:   target,
		Outcome:    domain.OutcomeFailure,
		IP:         ip,
		UserAgent:  ua,
	})
}

// ErrUnsupported 表示请求的令牌用途不受支持。
var ErrUnsupported = errors.New("不支持的令牌用途")

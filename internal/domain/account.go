// Package domain 存放跨域共享的值对象与接口。
//
// 这里放的是**被多个域共同使用**的类型,不是任何单个域的实现。
// 账号内核、授权服务、游戏域都从这里取类型,避免各域各定义一份
// 语义相同但类型不同的 Account。
//
// 域中立性(ADR-010):本包不得出现任何具体业务域的语义。
package domain

import (
	"strings"
	"time"

	"github.com/google/uuid"
)

// AccountStatus 是账号状态。
//
// 状态迁移由账号内核独占管理,其它域只能读不能改 —— 否则「被禁用的账号
// 还能不能发令牌」这种判断会被复制到多处,迟早出现不一致。
type AccountStatus string

// 账号状态取值。
const (
	// StatusPendingVerification 刚注册、邮箱未验证。此时禁止业务登录。
	StatusPendingVerification AccountStatus = "pending_verification"
	// StatusActive 正常可用。
	StatusActive AccountStatus = "active"
	// StatusDisabled 被管理员禁用。
	StatusDisabled AccountStatus = "disabled"
	// StatusLocked 因登录失败次数超限被临时锁定。
	StatusLocked AccountStatus = "locked"
)

// Usable 判断账号当前是否可用于业务登录。
func (s AccountStatus) Usable() bool { return s == StatusActive }

// Valid 判断状态值是否合法。
func (s AccountStatus) Valid() bool {
	switch s {
	case StatusPendingVerification, StatusActive, StatusDisabled, StatusLocked:
		return true
	default:
		return false
	}
}

// String 实现 fmt.Stringer。
func (s AccountStatus) String() string { return string(s) }

// Account 是账号实体。
type Account struct {
	ID            uuid.UUID
	Username      string
	Email         string
	Status        AccountStatus
	EmailVerified bool
	// MCLoginEnabled 是通用布尔开关。
	//
	// 字段名沿用数据库列名,便于对齐;但**内核不理解它的业务语义**,
	// 只当「一个可开关的布尔值」存取。判断「谁可以用它」由对应域负责。
	MCLoginEnabled bool
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// EmailLower 返回小写邮箱,用于大小写不敏感的唯一性判断。
func (a Account) EmailLower() string { return strings.ToLower(a.Email) }

// UsernameLower 返回小写用户名。
func (a Account) UsernameLower() string { return strings.ToLower(a.Username) }

// Session 是登录会话。
//
// ADR-004:会话是**唯一**的用户登录态。授权域与游戏域各有自己的令牌表,
// 不复用本结构。
type Session struct {
	ID            uuid.UUID
	AccountID     uuid.UUID
	SSOSessionID  *uuid.UUID
	CreatedAt     time.Time
	LastSeenAt    time.Time
	ExpiresAt     time.Time
	IdleExpiresAt time.Time
	RevokedAt     *time.Time
	IP            string
	UserAgent     string
	// MustChangePassword 表示本次会话必须先改密码才能继续。
	//
	// 它是凭据上 must_change(真源)在**登录这一刻**的投影:认证中间件
	// 每个请求都要判断这件事,而会话行本来就是必读的,凭据行不是。
	// 改密码会吊销该账号全部会话,所以这份投影不会在旗标清零后残留。
	MustChangePassword bool
}

// Active 判断会话在给定时刻是否仍然有效。
//
// 两个过期条件缺一不可:
//   - IdleExpiresAt 是滑动过期,每次活跃往前推;
//   - ExpiresAt 是绝对过期,**不因活跃而延长**,防止一个令牌永远不过期。
func (s Session) Active(now time.Time) bool {
	if s.RevokedAt != nil {
		return false
	}
	return now.Before(s.IdleExpiresAt) && now.Before(s.ExpiresAt)
}

// Role 是角色。
type Role struct {
	ID          uuid.UUID
	Code        string
	Name        string
	Description string
	IsSystem    bool
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Permission 是权限点。
//
// 权限点形如 `域:资源:动作` 三段式,全库唯一命名空间(ADR-005)。
type Permission struct {
	Code        string
	Description string
}

// Can 判断某权限点是否覆盖目标权限点。
//
// 支持三种通配形式:
//
//	<域>:*           匹配该域下的全部权限点,例如 mydomain:*
//	*:<资源>:<动作>  匹配任意命名空间的同一资源动作
//	<域>:*:<动作>    匹配该域下的同一动作
//
// 另有全局通配 `*`,代表平台管理员(持有全部权限点)。
// 段数不足三段的权限点只做精确匹配 —— 对格式非法的权限点做通配展开,
// 一次配置笔误就可能放大成意外授权。
func Can(granted, want string) bool {
	if granted == want {
		return true
	}
	if granted == "*" {
		return true
	}

	gParts := strings.Split(granted, ":")
	wParts := strings.Split(want, ":")
	if len(wParts) != 3 {
		// 目标权限点格式非法,不做通配展开
		return false
	}
	// <域>:* 形式:段数是 2 而不是 3
	if len(gParts) == 2 && gParts[1] == "*" {
		return gParts[0] == wParts[0]
	}
	if len(gParts) != 3 {
		return false
	}

	for i := range gParts {
		if gParts[i] != "*" && gParts[i] != wParts[i] {
			return false
		}
	}
	return true
}

// Principal 是「已认证主体」,由认证中间件产出并挂到请求上下文。
type Principal struct {
	AccountID uuid.UUID
	Username  string
	Email     string
	SessionID uuid.UUID
	// Permissions 是该主体当前持有的全部权限点(含通配形式)。
	Permissions []string
	// MustChangePassword 是会话行上「首登强制改密」的投影(真源在凭据行,
	// 见迁移 00008)。放进 Principal 是为了:中间件本来就读到了会话行,
	// 顺手带出来,任意 handler 就能把它告诉前端,而不必再查一次凭据。
	MustChangePassword bool
}

// Can 判断主体是否具备目标权限点。
func (p *Principal) Can(permission string) bool {
	for _, granted := range p.Permissions {
		if Can(granted, permission) {
			return true
		}
	}
	return false
}

// AuditOutcome 是审计结果。
type AuditOutcome string

// 审计结果取值。
const (
	OutcomeSuccess AuditOutcome = "success"
	OutcomeFailure AuditOutcome = "failure"
)

// AuditActor 描述「谁」触发了这次操作。
//
// 三种形态:
//   - account     账号本人操作
//   - system      系统内部触发
//   - admin:<uuid> 某个管理员操作
type AuditActor string

// AuditActorAccount 表示账号本人。
func AuditActorAccount(id uuid.UUID) AuditActor { return AuditActor("account:" + id.String()) }

// AuditActorAdmin 表示管理员。
func AuditActorAdmin(id uuid.UUID) AuditActor { return AuditActor("admin:" + id.String()) }

// AuditActorSystem 表示系统。
func AuditActorSystem() AuditActor { return AuditActor("system") }

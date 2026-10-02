package minecraft

import (
	"context"
	"strings"

	"github.com/google/uuid"

	"github.com/yggauth/yggauth/internal/platform/apperr"
)

// accountGateway 把账号内核适配成 MC 域需要的窄接口。
//
// **依赖方向是单向的**:MC 域 → 账号内核。内核完全不知道 MC 域的存在,
// 它的 `mc_login_enabled` 只当「一个可开关的布尔值」存取(见 domain.Account
// 的注释),「谁可以用它」的判断全部落在这里。
//
// 这样拆分还有一个好处:账号内核的登录流程里那些与 MC 无关的副作用
// (会话签发、登录成功审计、SSO 联动)不会在 MC 登录时重复发生一次。
type accountGateway struct {
	// authenticate 由 wire 层注入,内部复用账号内核的凭据校验,
	// 但不复用它的会话签发。
	authenticate func(ctx context.Context, identifier, password string) (uuid.UUID, error)
	loginEnabled func(ctx context.Context, accountID uuid.UUID) (bool, error)
	setEnabled   func(ctx context.Context, accountID uuid.UUID, enabled bool) error
	username     func(ctx context.Context, accountID uuid.UUID) (string, error)
}

// NewAccountGateway 用注入的函数构造网关。
func NewAccountGateway(
	authenticate func(ctx context.Context, identifier, password string) (uuid.UUID, error),
	loginEnabled func(ctx context.Context, accountID uuid.UUID) (bool, error),
	setEnabled func(ctx context.Context, accountID uuid.UUID, enabled bool) error,
	username func(ctx context.Context, accountID uuid.UUID) (string, error),
) AccountGateway {
	return &accountGateway{
		authenticate: authenticate,
		loginEnabled: loginEnabled,
		setEnabled:   setEnabled,
		username:     username,
	}
}

// AuthenticateForMC 校验凭据。
//
// MC 客户端允许用用户名或邮箱登录(identifier 二选一),
// 所以这里先按「像邮箱」判断,再决定查哪一列。
func (g *accountGateway) AuthenticateForMC(ctx context.Context, identifier, password string) (uuid.UUID, error) {
	id := strings.TrimSpace(identifier)
	if id == "" || password == "" {
		return uuid.Nil, apperr.New(apperr.CodeInvalidArgument, "用户名与密码不能为空")
	}
	return g.authenticate(ctx, id, password)
}

func (g *accountGateway) MCLoginEnabled(ctx context.Context, accountID uuid.UUID) (bool, error) {
	return g.loginEnabled(ctx, accountID)
}

func (g *accountGateway) SetMCLoginEnabled(ctx context.Context, accountID uuid.UUID, enabled bool) error {
	return g.setEnabled(ctx, accountID, enabled)
}

func (g *accountGateway) UsernameOf(ctx context.Context, accountID uuid.UUID) (string, error) {
	return g.username(ctx, accountID)
}

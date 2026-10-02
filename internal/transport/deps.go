package transport

import (
	"github.com/yggauth/yggauth/internal/admin"
	"github.com/yggauth/yggauth/internal/identity"
	"github.com/yggauth/yggauth/internal/oidc"
)

// 各业务域的装配依赖。
//
// 当前已装配账号内核(M2);授权服务、游戏域、管理后台随对应里程碑填充。
// 之所以现在就显式列出,是为了让「少装一个域」变成编译期可见的事实(ADR-011),
// 而不是等到运行时才发现某个域没注册。

// IdentityDeps 是账号内核的依赖。
type IdentityDeps struct {
	Service *identity.Service
	Handler *identity.Handler
	Cookie  AuthConfig
}

// OIDCDeps 是授权服务的依赖。
type OIDCDeps struct {
	Server  *oidc.Server
	Handler *oidc.Handler
	SSO     *oidc.SSO
	// Device 是设备码流程服务,用户批准端点用它
	Device *oidc.DeviceService
}

// MCDeps 是游戏域的依赖(M4/M5 填充)。
type MCDeps struct{}

// AdminDeps 是管理后台的依赖。
type AdminDeps struct {
	Service *identity.Service
	Handler *admin.Handler
}

package transport

// 各业务域的装配依赖。
//
// 当前里程碑只占位,随对应业务域的实现逐个填充。之所以现在就显式列出,
// 是为了让「少装一个域」变成编译期可见的事实(ADR-011),
// 而不是等到运行时才发现某个域没注册。

// IdentityDeps 是账号内核的依赖(M2 填充)。
type IdentityDeps struct{}

// OIDCDeps 是授权服务的依赖(M3 填充)。
type OIDCDeps struct{}

// MCDeps 是游戏域的依赖(M4/M5 填充)。
type MCDeps struct{}

// AdminDeps 是管理后台的依赖(M6 填充)。
type AdminDeps struct{}

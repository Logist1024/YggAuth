package transport

import (
	"context"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/yggauth/yggauth/internal/domain"
	"github.com/yggauth/yggauth/internal/identity"
	"github.com/yggauth/yggauth/internal/platform/apperr"
	"github.com/yggauth/yggauth/internal/platform/httpx"
	"github.com/yggauth/yggauth/internal/platform/log"
)

// AuthConfig 是会话认证中间件的配置。
type AuthConfig struct {
	// CookieName 是会话 cookie 名
	CookieName string
	// TrustProxyHeaders 决定取 IP 时是否信任转发头
	TrustProxyHeaders bool
}

// SessionAuthenticator 校验会话令牌。
//
// 定义成接口而不是直接用 *identity.Service,是为了让 transport 层
// 不必知道账号内核的具体类型。
type SessionAuthenticator interface {
	// Authenticate 校验会话令牌。令牌无效时返回 apperr.CodeSessionExpired。
	AuthenticateSession(ctx context.Context, token string) (identity.AuthenticatedSession, error)
	// LookupAccount 按主键取账号。
	LookupAccount(ctx context.Context, id uuid.UUID) (domain.Account, error)
	// PermissionsFor 返回账号权限点。
	PermissionsFor(ctx context.Context, accountID uuid.UUID) ([]string, error)
}

// SessionAuth 中间件解析会话 cookie 或 Bearer 令牌,构造已认证主体。
//
// 两种凭据都接受:
//   - Cookie:终端用户站与管理后台
//   - Bearer:接口调用方(内部工具、脚本)
//
// 主体挂到上下文后,后续的 RequirePermission 才能读到权限点。
func SessionAuth(auth SessionAuthenticator, cfg AuthConfig) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := httpx.ReadCookie(r, cfg.CookieName)
			if token == "" {
				token = httpx.BearerToken(r)
			}
			if token == "" {
				// 没有凭据不算错误:公开接口照样能过,
				// 需要登录的接口另有 RequireAuth 把关。
				next.ServeHTTP(w, r)
				return
			}

			res, err := auth.AuthenticateSession(r.Context(), token)
			if err != nil {
				// 凭据**存在但无效**(例如服务重启后会话被清、
				// 浏览器还留着旧 cookie)不能当作致命错误 —— 它
				// 与「没带凭据」在语义上是一样的:主体未知。
				//
				// 但两者对用户并不一样,所以给下游留一面小旗:
				// 带着死凭据来的请求,RequireAuth 回 20012
				// 「登录态已过期,请重新登录」,空手来的才是 20001。
				//
				// 把它当成未认证继续放行,理由与上面 `token == ""`
				// 分支一致:公开接口(登录/注册)必须能跑,
				// 受保护接口另有 RequireAuth 会基于 nil 主体返回 401。
				//
				// 这里不主动清 cookie —— 登录成功后的 Set-Cookie
				// 会用同名同域覆盖旧值,无须多写一次响应头。
				next.ServeHTTP(w, r.WithContext(httpx.WithRejectedCredential(r.Context())))
				return
			}

			acc, err := auth.LookupAccount(r.Context(), res.Session.AccountID)
			if err != nil {
				httpx.Fail(w, apperr.From(err))
				return
			}

			// 账号在会话创建之后被禁用/删除,登录态必须立刻失效
			if !acc.Status.Usable() {
				httpx.Fail(w, apperr.New(apperr.CodeAccountDisabled, "账号已被禁用"))
				return
			}

			// 首登强制改密:旗标在会话行上(登录时从凭据投的影),在取权限
			// 之前就拦下 —— 必须先改密,后面那些查询一次都不必做。
			if res.Session.MustChangePassword && !mustChangeAllowed(r.URL.Path) {
				httpx.Fail(w, apperr.New(apperr.CodePasswordChangeRequired, "首次登录必须修改密码"))
				return
			}

			codes, err := auth.PermissionsFor(r.Context(), acc.ID)
			if err != nil {
				httpx.Fail(w, apperr.From(err))
				return
			}

			// 权限点变更立即生效:每次请求都重新求值,
			// 不把权限烤进会话里(否则撤权要等会话过期才生效)
			p := &domain.Principal{
				AccountID:   acc.ID,
				Username:    acc.Username,
				Email:       acc.Email,
				SessionID:   res.Session.ID,
				Permissions: codes,
				// 拦截判断已经在上面做过一次了,这里原样带出,
				// 让 /api/auth/session 与 /api/account/ 能把旗标告诉前端。
				MustChangePassword: res.Session.MustChangePassword,
			}
			ctx := httpx.WithPrincipal(r.Context(), p)
			ctx = log.WithFields(ctx, log.KeyAccountID, acc.ID)

			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// mustChangeAllowed 列出「强制改密期间仍可访问」的路径。
//
// 放行三类,都是这条强制流程自身需要的:
//
//	/api/auth/*          登录、会话自举、登出 —— 前端要靠它们知道该跳哪儿
//	/api/account/password 改密本身(不然永远改不成)
//	/api/account/         GET Me:前端渲染顶栏/用户名
//
// 其余一律拦下,包括管理后台的 /api/admin/*:管理员的初始密码也必须先换。
// 白名单按前缀匹配并以 / 结尾收口,避免 `/api/account/` 意外放行
// `/api/account/password` 之外更长的同前缀路径。
func mustChangeAllowed(path string) bool {
	switch {
	case strings.HasPrefix(path, "/api/auth/"):
		return true
	case path == "/api/account/password":
		return true
	case path == "/api/account/", path == "/api/account":
		return true
	default:
		return false
	}
}

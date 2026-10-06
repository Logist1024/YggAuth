package httpx

import (
	"context"
	"net/http"

	"github.com/yggauth/yggauth/internal/domain"
	"github.com/yggauth/yggauth/internal/platform/apperr"
)

// 上下文键。
type ctxKey int

const (
	principalKey ctxKey = iota
	rejectedCredentialKey
)

// WithPrincipal 把已认证主体挂到请求上下文。
func WithPrincipal(ctx context.Context, p *domain.Principal) context.Context {
	return context.WithValue(ctx, principalKey, p)
}

// PrincipalFrom 取出请求上下文里的已认证主体,没有则返回 nil。
//
// 调用方**必须**判空:中间件之外的路径(比如公开接口)本来就没有主体。
func PrincipalFrom(ctx context.Context) *domain.Principal {
	p, ok := ctx.Value(principalKey).(*domain.Principal)
	if !ok {
		return nil
	}
	return p
}

// WithRejectedCredential 标记「请求带了凭据,但凭据已经失效」。
//
// 与「没带凭据」分开,是因为两者对用户是两件事:带着已登出/已过期
// cookie 回来的人,需要听到「登录态已过期,请重新登录」(20012),
// 而不是一句无从下手的「未认证或凭证无效」(20001)。SessionAuth 校验
// 失败时会吞掉错误继续放行(公开接口必须能过),这面小旗因此是下游
// RequireAuth 唯一能分辨「没登录」与「登录态没了」的依据。
func WithRejectedCredential(ctx context.Context) context.Context {
	return context.WithValue(ctx, rejectedCredentialKey, true)
}

// RejectedCredentialFrom 报告该请求是否带着一份已被拒绝的凭据。
func RejectedCredentialFrom(ctx context.Context) bool {
	v, _ := ctx.Value(rejectedCredentialKey).(bool)
	return v
}

// RequireAuth 拒绝没有主体的请求。
//
// 两种拒绝各说各话:带了失效凭据的说「登录态已过期」,
// 空手而来的才说「未认证」。
func RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if PrincipalFrom(r.Context()) == nil {
			if RejectedCredentialFrom(r.Context()) {
				Fail(w, apperr.ErrSessionExpired)
				return
			}
			Fail(w, apperr.ErrUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// RequirePermission 校验主体是否具备指定权限点。
//
// 返回中间件而不是直接包 handler,是为了让调用方能按权限点分组挂路由:
//
//	r.Group(func(r chi.Router) {
//	    r.Use(httpx.RequirePermission("account:read"))
//	    r.Get("/accounts", h.List)
//	})
//
// 这里只是**体验层**的拦截,真正的安全边界永远是服务端自己校验权限,
// 前端路由守卫也拦不住直接输 URL 的请求。
func RequirePermission(permission string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p := PrincipalFrom(r.Context())
			if p == nil {
				// 与 RequireAuth 同一套区分:带了失效凭据来的人
				// 得到「登录态已过期」,空手而来的得到「未认证」。
				if RejectedCredentialFrom(r.Context()) {
					Fail(w, apperr.ErrSessionExpired)
					return
				}
				Fail(w, apperr.ErrUnauthorized)
				return
			}
			if !p.Can(permission) {
				FailCode(w, apperr.CodeForbidden, "缺少权限:"+permission)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// CSRFProtect 校验写操作的同源要求。
//
// 配合 SameSite=Lax Cookie 构成 CSRF 双防线(docs/security.md 3.3)。
func CSRFProtect(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			// 安全方法不改状态,不需要 CSRF 校验
		default:
			if err := CheckSameOrigin(r); err != nil {
				FailCode(w, apperr.CodeForbidden, "请求来源校验失败")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

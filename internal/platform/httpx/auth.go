package httpx

import (
	"context"
	"net/http"

	"github.com/yggauth/yggauth/internal/domain"
	"github.com/yggauth/yggauth/internal/platform/apperr"
)

// 上下文键。
type ctxKey int

const principalKey ctxKey = iota

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

// RequireAuth 拒绝没有主体的请求。
func RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if PrincipalFrom(r.Context()) == nil {
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
// 配合 SameSite=Lax Cookie 构成 CSRF 双防线(docs/09-security.md 3.3)。
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

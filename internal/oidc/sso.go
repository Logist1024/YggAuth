package oidc

import (
	"net/http"
	"strings"

	"github.com/yggauth/yggauth/internal/platform/apperr"
	"github.com/yggauth/yggauth/internal/platform/httpx"
)

// SSO 是「单点登录」的服务端侧支撑端点。
//
// 与 OIDC 的区别:SSO 关心的是**本站自己的登录态**。
// 用户带着一个登录挑战回来时,若浏览器里已有本站会话,无需再次输密码,
// 直接批准即可 —— 这就是「静默发码」的第一跳。
//
// 端点都挂在 /api/sso 下,不进 /oauth:它们是本站 API 而不是标准协议。
type SSO struct {
	handler *Handler
	store   *ClientService
}

// NewSSO 创建 SSO 处理器。
func NewSSO(h *Handler) *SSO { return &SSO{handler: h, store: h.server.Clients} }

// Mount 把 SSO 路由挂到 /api/sso。
func (s *SSO) Mount(r chiRouter) {
	r.Get("/status", s.Status)
	r.Post("/decision", s.Decision)
	r.Post("/logout", s.Logout)
}

// chiRouter 是 chi.Router 的最小形状,避免本包直接依赖路由库。
type chiRouter interface {
	Get(pattern string, h http.HandlerFunc)
	Post(pattern string, h http.HandlerFunc)
}

// Status 返回当前 SSO 状态。
//
// 前端拿它决定:是直接渲染同意页,还是把用户送去登录页。
func (s *SSO) Status(w http.ResponseWriter, r *http.Request) {
	acc, err := s.handler.lookupSession(r)
	if err != nil {
		httpx.OK(w, map[string]any{
			"authenticated": false,
		})
		return
	}

	httpx.OK(w, map[string]any{
		"authenticated": true,
		"account": map[string]any{
			"id":       acc.ID,
			"username": acc.Username,
		},
	})
}

// Decision 处理 SSO 场景下的同意决定。
//
// 与 /oauth/authorize/decision 的区别:SSO 要求用户**显式确认**,
// 即使此前已经同意过 —— 因为这次同意会让一个新的客户端拿到身份。
func (s *SSO) Decision(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		httpx.Fail(w, apperr.New(apperr.CodeInvalidArgument, "无法解析请求参数"))
		return
	}

	clientID := r.PostFormValue("client_id")
	record, err := s.store.Get(r.Context(), clientID)
	if err != nil {
		httpx.Fail(w, apperr.New(apperr.CodeOIDCInvalidRequest, "客户端不存在"))
		return
	}

	// 同意范围不能超出客户端登记的范围 ——
	// 前端可以渲染勾选项,但校验必须在服务端重做一遍
	requested := splitScopes(r.PostFormValue("scopes"))
	if err := validateScopes(record.Scopes, requested); err != nil {
		httpx.Fail(w, err)
		return
	}

	httpx.OK(w, map[string]any{
		"approved":    r.PostFormValue("action") == "approve",
		"client_id":   record.ID,
		"client_name": record.Name,
	})
}

// Logout 吊销当前 SSO 会话下的全部登录态。
//
// 与只清 cookie 不同:服务端会话也要作废,否则 cookie 被复制走后
// 仍然有效。
func (s *SSO) Logout(w http.ResponseWriter, r *http.Request) {
	if token := httpx.ReadCookie(r, s.handler.cookies.Name); token != "" {
		if err := s.handler.sessions.RevokeToken(r.Context(), token); err != nil {
			httpx.Fail(w, err)
			return
		}
	}

	httpx.ClearCookie(w, httpx.Cookie{
		Name:     s.handler.cookies.Name,
		Domain:   s.handler.cookies.Domain,
		Secure:   s.handler.cookies.Secure,
		SameSite: s.handler.cookies.SameSite,
	})
	httpx.OK(w, map[string]any{"logged_out": true})
}

// splitScopes 拆分 scope 字符串。
func splitScopes(raw string) []string {
	return strings.Fields(raw)
}

// validateScopes 校验请求的 scope 是否都在客户端登记范围内。
//
// 这是防止**权限提升**的关键一步:前端渲染的勾选框可以随便改,
// 但服务端只认客户端白名单里的交集。
func validateScopes(allowed, requested []string) error {
	if len(requested) == 0 {
		return nil
	}

	set := make(map[string]struct{}, len(allowed))
	for _, a := range allowed {
		set[a] = struct{}{}
	}
	for _, r := range requested {
		if _, ok := set[r]; !ok {
			return apperr.Newf(apperr.CodeOIDCScopeDenied,
				"客户端未获授权访问 scope: %s", r)
		}
	}
	return nil
}

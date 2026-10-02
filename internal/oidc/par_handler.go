package oidc

import (
	"net/http"

	"github.com/yggauth/yggauth/internal/platform/apperr"
	"github.com/yggauth/yggauth/internal/platform/httpx"
)

// PushedAuthorizationRequest 实现 RFC 9126 的 PAR 端点。
//
// 客户端先把授权请求 POST 到这里,拿到一个短生命周期的 request_uri,
// 再用它去 /oauth/authorize。收益有两个:授权参数不再出现在浏览器
// 地址栏与 referer 里;客户端无法在授权端点偷偷夹带参数。
//
// fosite 提供了 NewPushedAuthorizeRequest / NewPushedAuthorizeResponse,
// 校验与 request_uri 生成都在里面,这里只负责会话注入与输出。
func (h *Handler) PushedAuthorizationRequest(w http.ResponseWriter, r *http.Request) {
	if !h.limiter.allow("par", httpx.ClientIP(r, h.trustProxy)) {
		httpx.Fail(w, apperr.ErrRateLimited)
		return
	}
	if err := r.ParseForm(); err != nil {
		httpx.Fail(w, apperr.New(apperr.CodeInvalidArgument, "无法解析请求参数"))
		return
	}

	// PAR 允许未登录时先推送请求 —— 用户随后才去登录。
	// 但已登录时要带上主体,否则同意记录挂不上。
	sess := NewSession("", "", "", false)
	if acc, err := h.lookupSession(r); err == nil {
		sess = NewSession(acc.ID, acc.Username, acc.Email, true)
	}

	ar, err := h.server.Provider.NewPushedAuthorizeRequest(r.Context(), r)
	if err != nil {
		h.writeOAuthError(w, err)
		return
	}
	setSessionSubject(ar.GetSession(), sess.GetSubject())

	response, err := h.server.Provider.NewPushedAuthorizeResponse(r.Context(), ar, sess)
	if err != nil {
		h.writeOAuthError(w, err)
		return
	}

	// RFC 9126 §2.2:成功时返回 201 + request_uri + expires_in
	h.server.Provider.WritePushedAuthorizeResponse(r.Context(), w, ar, response)
}

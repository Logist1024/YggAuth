package oidc

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ory/fosite"

	"github.com/yggauth/yggauth/internal/oidc/session"
	"github.com/yggauth/yggauth/internal/platform/apperr"
	"github.com/yggauth/yggauth/internal/platform/httpx"
	platformmetrics "github.com/yggauth/yggauth/internal/platform/metrics"
	"github.com/yggauth/yggauth/internal/platform/ratelimit"
)

// stashCookieName 是暂存待确认授权请求的 cookie。
const stashCookieName = "ygg_authz"

// Limiter 包装平台限流器,让本包不必直接依赖 ratelimit 的类型。
type Limiter struct {
	l *ratelimit.Limiter
}

// NewLimiter 创建限流器。
func NewLimiter(l *ratelimit.Limiter) *Limiter {
	if l == nil {
		// 传 nil 时用默认构造,而不是留一个空指针等运行时崩
		l = ratelimit.New(nil)
	}
	return &Limiter{l: l}
}

// allow 判定是否放行,超限时上报指标。
func (li *Limiter) allow(scope, key string) bool {
	rule := ratelimit.Authorize
	if scope == "token" {
		rule = ratelimit.Token
	}
	if li.l.Allow(scope+":"+key, rule.Limit, rule.Window) {
		return true
	}
	platformmetrics.ObserveRateLimited(scope)
	return false
}

// attachSession 把当前登录态写进 fosite 请求。
//
// fosite 不知道「谁在登录」—— 它只看请求参数。这里补上 subject,
// 后续的用户同意、令牌签发全靠它。
func (h *Handler) attachSession(r *http.Request, ar fosite.Requester) error {
	if h.sessions == nil {
		return apperr.ErrUnauthorized
	}

	token := httpx.ReadCookie(r, h.cookies.Name)
	if token == "" {
		return apperr.ErrUnauthorized
	}
	// fosite 解析出来的请求**不带会话** —— 它只在查到同意记录时才填。
	// 这里必须自己挂上,否则下面所有对会话的写入都是空操作。
	ensureSession(ar)

	acc, err := h.sessions.LookupByToken(r.Context(), token)
	if err != nil {
		return apperr.New(apperr.CodeSessionExpired, "登录态已过期,请重新登录")
	}

	if ds, ok := ar.GetSession().(*session.DefaultSession); ok {
		ds.SetProfile(acc.ID, acc.Username, acc.Email, true)
	}
	return nil
}

// stashAuthorizeRequest 把待确认的授权请求写进**带签名**的 cookie。
//
// 存的是**原始查询串**,不是 fosite 的请求对象:
// fosite.Request 里有接口字段(Client、Session),JSON 往返必然失败 ——
// 把一个装不进 JSON 的类型塞进 JSON,是绕过症状而不是解决问题。
//
// 存查询串还有个好处:还原时让 fosite 重新解析一遍,校验逻辑不会被绕过。
//
// 为什么不存服务端:
//   - 保持单进程无状态部署(ADR-007 已接受多副本的限制);
//   - cookie 是 HttpOnly 且带 HMAC,客户端改不了 —— 篡改会在验签时被发现。
func (h *Handler) stashAuthorizeRequest(w http.ResponseWriter, r *http.Request, ar fosite.AuthorizeRequester) {
	query := ar.GetRequestForm().Encode()
	if query == "" {
		return
	}

	encoded := base64.RawURLEncoding.EncodeToString([]byte(query))
	signed := h.sign(encoded)

	httpx.SetCookie(w, httpx.Cookie{
		Name:     stashCookieName,
		Value:    encoded + "." + signed,
		Secure:   h.cookies.Secure,
		SameSite: http.SameSiteLaxMode,
		// 同意页交互通常一两分钟,给足时间但不允许长期挂起
		MaxAge: 300,
	})
	_ = r
}

func (h *Handler) clearStash(w http.ResponseWriter) {
	httpx.ClearCookie(w, httpx.Cookie{
		Name:     stashCookieName,
		Secure:   h.cookies.Secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// restoreAuthorizeRequest 从 cookie 里还原待确认的授权请求。
func (h *Handler) restoreAuthorizeRequest(r *http.Request) (fosite.AuthorizeRequester, error) {
	raw := httpx.ReadCookie(r, stashCookieName)
	if raw == "" {
		return nil, apperr.New(apperr.CodeInvalidArgument, "授权请求已过期,请重新发起登录")
	}

	encoded, signed, found := strings.Cut(raw, ".")
	if !found || !hmac.Equal([]byte(signed), []byte(h.sign(encoded))) {
		return nil, apperr.New(apperr.CodeInvalidArgument, "授权请求校验失败")
	}

	query, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return nil, apperr.New(apperr.CodeInvalidArgument, "授权请求损坏")
	}

	// 用原始查询串重建一个授权请求,让 fosite 重新走一遍完整校验 ——
	// 我们不在这里手工拼请求,那样迟早会漏掉某个校验项。
	synthetic, err := http.NewRequestWithContext(
		r.Context(), http.MethodGet, "/oauth/authorize?"+string(query), nil)
	if err != nil {
		return nil, apperr.New(apperr.CodeInvalidArgument, "授权请求无法解析")
	}
	synthetic.RemoteAddr = r.RemoteAddr

	ar, err := h.server.Provider.NewAuthorizeRequest(r.Context(), synthetic)
	if err != nil {
		return nil, apperr.New(apperr.CodeOIDCInvalidRequest, "授权请求已失效,请重新发起登录")
	}
	return ar, nil
}

// sign 计算载荷的 HMAC。
func (h *Handler) sign(payload string) string {
	mac := hmac.New(sha256.New, h.server.macKey)
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// renderConsent 渲染同意页。
//
// 后端只返回结构化数据,页面由前端渲染 —— 模板不在本包内,
// 也避免把 HTML 拼装逻辑散落到授权流程里。
func (h *Handler) renderConsent(w http.ResponseWriter, ar fosite.AuthorizeRequester) {
	clientName := ar.GetClient().GetID()
	scopes := make([]string, 0, len(ar.GetRequestedScopes()))
	for _, s := range ar.GetRequestedScopes() {
		scopes = append(scopes, s)
	}

	httpx.OK(w, map[string]any{
		"client_id":     ar.GetClient().GetID(),
		"client_name":   clientName,
		"scopes":        scopes,
		"redirect_uri":  ar.GetRedirectURI().String(),
		"state":         ar.GetRequestForm().Get("state"),
		"needs_consent": true,
	})
}

// authenticateClient 校验客户端身份。
//
// 内省与吊销端点必须要求客户端认证,否则任何人都能拿别人的令牌来问。
func (h *Handler) authenticateClient(r *http.Request) error {
	clientID, clientSecret := basicAuth(r)
	if clientID == "" {
		clientID = r.PostFormValue("client_id")
	}
	if clientSecret == "" {
		clientSecret = r.PostFormValue("client_secret")
	}
	if clientID == "" {
		return apperr.New(apperr.CodeOIDCClientAuthFailed, "缺少客户端凭据")
	}

	record, err := h.server.Clients.Get(r.Context(), clientID)
	if err != nil {
		return apperr.New(apperr.CodeOIDCClientAuthFailed, "客户端认证失败")
	}
	if record.Status != "active" {
		return apperr.New(apperr.CodeOIDCClientAuthFailed, "客户端已停用")
	}
	if !h.server.Clients.ValidateSecret(clientSecret, record.SecretHash) {
		return apperr.New(apperr.CodeOIDCClientAuthFailed, "客户端认证失败")
	}
	return nil
}

// basicAuth 解析 Basic 认证头。
func basicAuth(r *http.Request) (id, secret string) {
	raw := r.Header.Get("Authorization")
	if !strings.HasPrefix(strings.ToLower(raw), "basic ") {
		return "", ""
	}
	decoded, err := base64.StdEncoding.DecodeString(raw[len("Basic "):])
	if err != nil {
		return "", ""
	}
	id, secret, _ = strings.Cut(string(decoded), ":")
	return id, secret
}

// validatePostLogoutURI 校验登出后的跳转地址。
//
// 它必须是**已登记客户端**的回调地址。否则这个端点就成了开放重定向:
// 攻击者可以让受害者点了「退出登录」之后落在钓鱼站上。
func (h *Handler) validatePostLogoutURI(r *http.Request, target string) error {
	u, err := urlParse(target)
	if err != nil || u == "" {
		return apperr.New(apperr.CodeInvalidArgument, "跳转地址非法")
	}

	clientID := r.FormValue("client_id")
	if clientID == "" {
		return apperr.New(apperr.CodeInvalidArgument, "缺少 client_id")
	}

	record, err := h.server.Clients.Get(r.Context(), clientID)
	if err != nil {
		return apperr.New(apperr.CodeInvalidArgument, "客户端不存在")
	}
	for _, allowed := range record.RedirectURIs {
		// 完整字符串相等,不做前缀匹配
		if target == allowed {
			return nil
		}
	}
	return apperr.New(apperr.CodeOIDCRedirectMismatch, "跳转地址不在白名单内")
}

// ExpiryNow 返回当前时刻,便于测试注入。
func (s *Server) ExpiryNow() time.Time { return s.Clock.Now() }

var _ = context.Background

// lookupSession 从 SSO cookie 解析出登录账号。
func (h *Handler) lookupSession(r *http.Request) (accountRef, error) {
	if h.sessions == nil {
		return accountRef{}, apperr.ErrUnauthorized
	}
	token := httpx.ReadCookie(r, h.cookies.Name)
	if token == "" {
		return accountRef{}, apperr.ErrUnauthorized
	}
	return h.sessions.LookupByToken(r.Context(), token)
}

// nowForInfo 返回当前时刻,供内省响应的 iat 字段使用。
//
// 内省响应不要求精确的签发时间,给出服务端当前时刻即可 —— 真实签发
// 时间在 access_token 表里有,但 fosite 的 TokenInfo 不暴露它。
func nowForInfo() int64 { return time.Now().Unix() }

// writeOAuthValues 按 RFC 6749 输出错误 JSON。
func writeOAuthValues(w http.ResponseWriter, e *fosite.RFC6749Error) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(statusOfError(e))
	_ = json.NewEncoder(w).Encode(e.ToValues())
}

// setSessionSubject 往会话里写用户主体。
//
// fosite.Session 接口没有 SetSubject(那是 fosite 自己的 DefaultSession 的方法),
// 所以要断言一次。
func setSessionSubject(s fosite.Session, subject string) {
	if ds, ok := s.(*session.DefaultSession); ok {
		ds.SetSubject(subject)
	}
}

// urlParse 解析 URL,失败时返回空串。
func urlParse(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	return u.String(), nil
}

// ensureSession 保证请求上挂着一个可写的会话。
//
// fosite.NewAuthorizeRequest 出来的请求里 Session 是 nil:fosite 自己的
// 流程里,会话由 OpenIDConnect 存储在「查到已同意记录」时填入,而我们
// 必须在查记录**之前**就知道用户是谁(否则查不到该查谁的记录)。
//
// 这里补一个标准会话,后续的 setSessionSubject / setSessionExtra 才有对象。
func ensureSession(ar fosite.Requester) {
	if ar.GetSession() != nil {
		return
	}
	if setter, ok := ar.(interface{ SetSession(fosite.Session) }); ok {
		setter.SetSession(NewSession("", "", "", false))
	}
}

// logInternal 记录内部错误细节。
//
// 客户端只拿到 RFC 6749 的通用错误;真正的因果链必须留在服务端日志里,
// 否则一次 server_error 就是完全的黑盒。
func (h *Handler) logInternal(msg string, err error) {
	if h.logger != nil {
		h.logger.Error(msg, "error", unwrapAll(err))
	}
}

// unwrapAll 把错误摊平成带调试信息的字符串。
//
// fosite 把 handler 的原始错误包进 ErrServerError,只看最外层永远是
// "server_error";%+v 会把 pkg/errors 的 WithDebug 说明也带出来,
// errors.Unwrap 走不到那一层。
func unwrapAll(err error) string { return fmt.Sprintf("%+v", err) }

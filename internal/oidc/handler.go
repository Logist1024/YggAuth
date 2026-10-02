package oidc

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/ory/fosite"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/yggauth/yggauth/internal/platform/apperr"
	"github.com/yggauth/yggauth/internal/platform/httpx"
	platformmetrics "github.com/yggauth/yggauth/internal/platform/metrics"
)

// 授权域的专属指标。
//
// 放在本包而不是平台层:令牌签发是业务语义,平台 metrics 不该知道它。
var (
	tokenIssued = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "yggauth",
		Name:      "oidc_token_total",
		Help:      "OIDC 令牌签发总数",
	}, []string{"grant_type", "token_type"})

	authorizeResult = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "yggauth",
		Name:      "oidc_authorize_total",
		Help:      "OIDC 授权请求总数",
	}, []string{"outcome"})
)

func init() {
	platformmetrics.Registry.MustRegister(tokenIssued, authorizeResult)
	// 预置零值序列,避免刚启动时看板查不到指标
	for _, grant := range []string{"authorization_code", "refresh_token", "client_credentials", "device_code"} {
		for _, tt := range []string{"access_token", "refresh_token", "id_token"} {
			tokenIssued.WithLabelValues(grant, tt)
		}
	}
	authorizeResult.WithLabelValues(platformmetrics.OutcomeSuccess)
	authorizeResult.WithLabelValues(platformmetrics.OutcomeFailure)
}

// Handler 是授权服务的 HTTP 处理器。
type Handler struct {
	server   *Server
	cookies  CookieConfig
	sessions SessionStore
	device   *DeviceService
	limiter  *Limiter
	// trustProxy 决定取 IP 时是否信任转发头
	trustProxy bool
	logger     Logger
}

// CookieConfig 是 SSO 会话 cookie 配置。
type CookieConfig struct {
	Name     string
	Domain   string
	Secure   bool
	SameSite http.SameSite
	MaxAge   int
}

// accountRef 是 SSO 端点需要的账号信息。
type accountRef struct {
	ID       string
	Username string
	Email    string
}

// SessionStore 查询与吊销终端用户登录态。
type SessionStore interface {
	// LookupByToken 按明文令牌返回账号信息。
	LookupByToken(ctx context.Context, token string) (accountRef, error)
	// RevokeToken 按明文令牌吊销会话。
	RevokeToken(ctx context.Context, token string) error
	// IssueSSO 为一次 SSO 登录签发带 SSO 标识的会话。
	IssueSSO(ctx context.Context, accountID, ip, userAgent string) (string, error)
	// RevokeAllBySSO 吊销同一 SSO 会话下的全部登录态。
	RevokeAllBySSO(ctx context.Context, ssoSessionID string) error
}

// Mount 把授权服务的路由挂到 /oauth。
func (h *Handler) Mount(r chi.Router) {
	r.Get("/.well-known/openid-configuration", h.Discovery)
	r.Get("/.well-known/jwks.json", h.JWKS)

	r.Get("/authorize", h.Authorize)
	r.Post("/authorize/decision", h.AuthorizeDecision)
	r.Post("/token", h.Token)

	r.Get("/userinfo", h.UserInfo)
	r.Post("/userinfo", h.UserInfo)

	r.Post("/introspect", h.Introspect)
	r.Post("/revoke", h.Revoke)

	r.Get("/endsession", h.EndSession)
	r.Post("/endsession", h.EndSession)

	r.Post("/par", h.PushedAuthorizationRequest)
	r.Post("/device/auth", h.DeviceAuthorization)
}

// ---------------------------------------------------------------- 发现与 JWKS

// Discovery 返回 OIDC Discovery 文档。
//
// issuer 必须与实际访问域名完全一致 —— 标准客户端就是拿这个值
// 去比对令牌的 iss 声明,不一致会拒绝所有令牌。
func (h *Handler) Discovery(w http.ResponseWriter, _ *http.Request) {
	base := h.server.issuer
	httpx.OK(w, map[string]any{
		"issuer":                                base,
		"authorization_endpoint":                base + "/oauth/authorize",
		"token_endpoint":                        base + "/oauth/token",
		"userinfo_endpoint":                     base + "/oauth/userinfo",
		"jwks_uri":                              base + "/oauth/.well-known/jwks.json",
		"introspection_endpoint":                base + "/oauth/introspect",
		"revocation_endpoint":                   base + "/oauth/revoke",
		"end_session_endpoint":                  base + "/oauth/endsession",
		"pushed_authorization_request_endpoint": base + "/oauth/par",
		"device_authorization_endpoint":         base + "/oauth/device/auth",

		"response_types_supported": []string{"code"},
		"grant_types_supported": []string{
			"authorization_code", "refresh_token", "client_credentials",
			deviceCodeGrantType,
		},
		// OAuth 2.1 只接受 S256。声明 plain 会让客户端以为可以用弱挑战
		"code_challenge_methods_supported":      []string{"S256"},
		"scopes_supported":                      []string{"openid", "profile", "email", "offline_access"},
		"subject_types_supported":               []string{"public"},
		"id_token_signing_alg_values_supported": []string{"RS256"},
		"token_endpoint_auth_methods_supported": []string{
			"client_secret_basic", "client_secret_post",
		},
		"claims_supported": []string{
			"sub", "iss", "aud", "exp", "iat", "auth_time",
			"name", "preferred_username", "email", "email_verified",
		},
	})
}

// JWKS 返回公钥集。
func (h *Handler) JWKS(w http.ResponseWriter, r *http.Request) {
	set, err := h.server.jwks(r.Context())
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=300")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(set)
}

// ---------------------------------------------------------------- 授权

// Authorize 处理授权请求。
//
// 流程:fosite 解析校验 → 补登录态 → 判断是否需要同意 → 发码或渲染同意页。
func (h *Handler) Authorize(w http.ResponseWriter, r *http.Request) {
	if !h.limiter.allow("authorize", httpx.ClientIP(r, h.trustProxy)) {
		httpx.Fail(w, apperr.ErrRateLimited)
		return
	}

	ar, err := h.server.Provider.NewAuthorizeRequest(r.Context(), r)
	if err != nil {
		h.authorizeError(w, ar, err)
		return
	}

	if err := h.attachSession(r, ar); err != nil {
		// 未登录:带回登录页,登录完再回到这里 —— SSO 静默发码的第一跳
		h.redirectToLogin(w, r, ar)
		return
	}

	granted, err := h.hasConsent(r, ar)
	if err != nil {
		h.authorizeError(w, ar, err)
		return
	}
	if granted {
		// 已同意过:静默发码,用户无感知
		h.issueAndRedirect(w, r, ar)
		return
	}

	h.stashAuthorizeRequest(w, r, ar)
	authorizeResult.WithLabelValues(platformmetrics.OutcomeSuccess).Inc()
	h.renderConsent(w, ar)
}

// AuthorizeDecision 处理用户的同意或拒绝。
func (h *Handler) AuthorizeDecision(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		httpx.Fail(w, apperr.New(apperr.CodeInvalidArgument, "无法解析请求参数"))
		return
	}

	ar, err := h.restoreAuthorizeRequest(r)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	// 还原出来的请求没有主体 —— 换码时 id_token 需要 sub。
	// 这里重新读一次登录态,而不是信任 cookie 里存过的值。
	if err := h.attachSession(r, ar); err != nil {
		httpx.Fail(w, apperr.New(apperr.CodeSessionExpired, "登录态已过期,请重新登录"))
		return
	}

	if r.PostFormValue("action") == "deny" {
		authorizeResult.WithLabelValues(platformmetrics.OutcomeFailure).Inc()
		h.authorizeError(w, ar, fosite.ErrAccessDenied)
		return
	}

	// 同意记录入库,下次同一客户端不必再弹同意页
	if err := h.server.Storage.CreateOpenIDConnectSession(r.Context(), ar.GetID(), ar); err != nil {
		h.authorizeError(w, ar, fosite.ErrServerError.WithHint("保存授权同意失败"))
		return
	}

	h.issueAndRedirect(w, r, ar)
}

// issueAndRedirect 生成授权码并把浏览器送回客户端。
func (h *Handler) issueAndRedirect(w http.ResponseWriter, r *http.Request, ar fosite.AuthorizeRequester) {
	response, err := h.server.Provider.NewAuthorizeResponse(r.Context(), ar, ar.GetSession())
	if err != nil {
		h.authorizeError(w, ar, err)
		return
	}

	params := url.Values{}
	for k, vs := range response.GetParameters() {
		for _, v := range vs {
			params.Add(k, v)
		}
	}
	h.clearStash(w)
	h.redirectToClient(w, ar.GetRedirectURI().String(), params)
}

// redirectToLogin 把未登录的用户送去登录页,并保留原始授权请求。
func (h *Handler) redirectToLogin(w http.ResponseWriter, r *http.Request, ar fosite.AuthorizeRequester) {
	authorizeResult.WithLabelValues(platformmetrics.OutcomeFailure).Inc()

	h.stashAuthorizeRequest(w, r, ar)
	target := h.server.issuer + "/login?continue=" + url.QueryEscape(r.URL.RequestURI())
	http.Redirect(w, r, target, http.StatusFound)
}

// hasConsent 判断用户此前是否已同意该客户端。
func (h *Handler) hasConsent(r *http.Request, ar fosite.AuthorizeRequester) (bool, error) {
	if ar.GetSession().GetSubject() == "" {
		return false, nil
	}
	if _, err := h.server.Storage.GetOpenIDConnectSession(r.Context(), ar.GetID(), ar); err != nil {
		// 「查不到同意记录」就是没同意过,属正常路径
		return false, nil //nolint:nilerr
	}
	return true, nil
}

// ---------------------------------------------------------------- 用户信息

// UserInfo 返回当前令牌对应的用户信息。
func (h *Handler) UserInfo(w http.ResponseWriter, r *http.Request) {
	token := httpx.BearerToken(r)
	if token == "" {
		httpx.Fail(w, apperr.New(apperr.CodeUnauthorized, "缺少 Bearer 令牌"))
		return
	}

	info, err := h.introspect(r.Context(), token, fosite.AccessToken)
	if err != nil {
		httpx.FailCode(w, apperr.CodeUnauthorized, "令牌无效")
		return
	}

	httpx.OK(w, map[string]any{
		"sub":                info.GetSession().GetSubject(),
		"preferred_username": info.GetSession().GetUsername(),
		"email":              sessionExtra(info.GetSession(), "email"),
		"email_verified":     sessionExtra(info.GetSession(), "email_verified"),
	})
}

// Introspect 是令牌内省端点(RFC 7662)。
func (h *Handler) Introspect(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		httpx.Fail(w, apperr.New(apperr.CodeInvalidArgument, "无法解析请求参数"))
		return
	}
	token := r.PostFormValue("token")
	if token == "" {
		httpx.Fail(w, apperr.New(apperr.CodeInvalidArgument, "缺少 token 参数"))
		return
	}

	// 内省端点必须要求客户端认证:否则任何人都能拿别人的令牌来问
	if err := h.authenticateClient(r); err != nil {
		httpx.Fail(w, err)
		return
	}

	tokenType := fosite.AccessToken
	if r.PostFormValue("token_type_hint") == "refresh_token" {
		tokenType = fosite.RefreshToken
	}

	info, err := h.introspect(r.Context(), token, tokenType)
	if err != nil {
		// RFC 7662:令牌无效时返回 active=false,而不是错误
		httpx.OK(w, map[string]any{"active": false})
		return
	}

	httpx.OK(w, map[string]any{
		"active":     true,
		"sub":        info.GetSession().GetSubject(),
		"client_id":  info.GetClient().GetID(),
		"scope":      strings.Join(info.GetGrantedScopes(), " "),
		"exp":        info.GetSession().GetExpiresAt(tokenType).Unix(),
		"iat":        nowForInfo(),
		"token_type": "Bearer",
	})
}

// Revoke 吊销令牌(RFC 7009)。
func (h *Handler) Revoke(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		httpx.Fail(w, apperr.New(apperr.CodeInvalidArgument, "无法解析请求参数"))
		return
	}
	if err := h.authenticateClient(r); err != nil {
		httpx.Fail(w, err)
		return
	}

	token := r.PostFormValue("token")
	const reason = "client_revoked"

	if _, err := h.server.Storage.RevokeAccessTokenByHash(r.Context(), token, reason); err != nil {
		httpx.Fail(w, err)
		return
	}
	if _, err := h.server.Storage.RevokeRefreshTokenByHash(r.Context(), token, reason); err != nil {
		httpx.Fail(w, err)
		return
	}

	// RFC 7009:无论令牌是否存在都返回 200,
	// 否则这个端点就成了令牌探测接口
	httpx.NoContent(w)
}

// EndSession 是 RP 发起的登出。
func (h *Handler) EndSession(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		httpx.Fail(w, apperr.New(apperr.CodeInvalidArgument, "无法解析请求参数"))
		return
	}

	// 先吊销本会话的登录态,再把浏览器送回 RP
	if token := httpx.ReadCookie(r, h.cookies.Name); token != "" && h.sessions != nil {
		if err := h.sessions.RevokeToken(r.Context(), token); err != nil {
			httpx.Fail(w, err)
			return
		}
	}
	httpx.ClearCookie(w, httpx.Cookie{
		Name:     h.cookies.Name,
		Domain:   h.cookies.Domain,
		Secure:   h.cookies.Secure,
		SameSite: h.cookies.SameSite,
	})

	if target := r.FormValue("post_logout_redirect_uri"); target != "" {
		// 开放重定向防线:跳转地址必须与已登记客户端的回调地址精确匹配
		if err := h.validatePostLogoutURI(r, target); err != nil {
			httpx.Fail(w, err)
			return
		}
		h.redirectToClient(w, target, r.Form)
		return
	}
	httpx.OK(w, map[string]any{"logged_out": true})
}

// ---------------------------------------------------------------- 令牌

// Token 是令牌端点。
func (h *Handler) Token(w http.ResponseWriter, r *http.Request) {
	if !h.limiter.allow("token", httpx.ClientIP(r, h.trustProxy)) {
		httpx.Fail(w, apperr.ErrRateLimited)
		return
	}
	if err := r.ParseForm(); err != nil {
		httpx.Fail(w, apperr.New(apperr.CodeInvalidArgument, "无法解析请求参数"))
		return
	}

	// 设备码流程 fosite 没有对应 handler,在这里分流到自己的实现
	if r.PostFormValue("grant_type") == deviceCodeGrantType {
		h.deviceToken(w, r)
		return
	}

	// 令牌端点**不要求**浏览器带着 SSO cookie:客户端是服务端到服务端的调用,
	// 手上只有 client_id/client_secret/code,不会有浏览器的会话。
	//
	// 授权码流程的用户身份来自 GetAuthorizeCodeSession —— fosite 会用我们
	// 存进 oidc.authorization_code.session 的内容覆盖这里传入的会话。
	// 所以这里传一个「尽力而为」的会话即可,拿不到登录态不是错误。
	sess := NewSession("", "", "", false)
	if acc, err := h.lookupSession(r); err == nil {
		sess = NewSession(acc.ID, acc.Username, acc.Email, true)
	}

	accessRequest, err := h.server.Provider.NewAccessRequest(r.Context(), r, sess)
	if err != nil {
		h.logInternal("令牌请求校验失败", err)
		h.writeOAuthError(w, err)
		return
	}

	response, err := h.server.Provider.NewAccessResponse(r.Context(), accessRequest)
	if err != nil {
		h.logInternal("令牌响应生成失败", err)
		h.writeOAuthError(w, err)
		return
	}

	// fosite 会把响应直接写进 w。令牌端点**不走**统一响应包 ——
	// RFC 6749 规定它返回 OAuth 标准结构,标准客户端解析不了 {code,message,data}
	h.server.Provider.WriteAccessResponse(r.Context(), w, accessRequest, response)

	grant := r.PostFormValue("grant_type")
	metricsToken(grant, "access_token")
	metricsToken(grant, "refresh_token")
	metricsToken(grant, "id_token")
}

// ---------------------------------------------------------------- 辅助

// metricsToken 上报一次令牌签发。
func metricsToken(grant, tokenType string) {
	tokenIssued.WithLabelValues(grant, tokenType).Inc()
}

// introspect 是 fosite 内省调用的薄封装。
//
// fosite 要求传入一个 session 容器用于还原;标准会话类型即可 ——
// 真实数据在 Storage 里。返回的 AccessRequester 带有完整上下文。
func (h *Handler) introspect(ctx context.Context, token string, tokenType fosite.TokenType) (fosite.AccessRequester, error) {
	s := NewSession("", "", "", false)
	_, requester, err := h.server.Provider.IntrospectToken(ctx, token, tokenType, s)
	if err != nil {
		return nil, err
	}
	return requester, nil
}

// writeOAuthError 按 RFC 6749 输出错误。
func (h *Handler) writeOAuthError(w http.ResponseWriter, err error) {
	rfcErr := toRFCError(err)

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.WriteHeader(statusOfError(rfcErr))
	writeOAuthValues(w, rfcErr)
}

// toRFCError 把任意错误归一成 RFC 6749 错误。
//
// 非 RFC 错误一律退化成 server_error —— 绝不把内部细节带给客户端。
func toRFCError(err error) *fosite.RFC6749Error {
	var rfcErr *fosite.RFC6749Error
	if errors.As(err, &rfcErr) {
		return rfcErr
	}
	return fosite.ErrServerError
}

func statusOfError(e *fosite.RFC6749Error) int {
	if code := e.CodeField; code != 0 {
		return code
	}
	return http.StatusBadRequest
}

// authorizeError 按 OIDC 规范输出错误:回调地址合法就重定向,否则直接渲染。
func (h *Handler) authorizeError(w http.ResponseWriter, ar fosite.AuthorizeRequester, err error) {
	rfcErr := toRFCError(err)
	// 内部细节只进日志,绝不能随重定向发给客户端 —— 那会泄露表名、SQL 片段
	// 甚至密钥长度这类信息。SendDebugMessagesToClients 也是同样的理由关掉的。
	if h.logger != nil && !errors.Is(err, fosite.ErrAccessDenied) {
		h.logger.Error("授权请求失败", "error", unwrapAll(err), "client_id", requestFormValue(ar, "client_id"))
	}

	// 回调地址没通过校验时**不能**重定向 —— 那会把用户送到未登记的地址
	redirect := redirectOf(ar)
	if redirect == nil {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(statusOfError(rfcErr))
		writeOAuthValues(w, rfcErr)
		return
	}

	q := redirect.Query()
	q.Set("error", rfcErr.ErrorField)
	q.Set("error_description", rfcErr.GetDescription())
	if state := requestFormValue(ar, "state"); state != "" {
		q.Set("state", state)
	}
	redirect.RawQuery = q.Encode()
	h.redirectToClient(w, redirect.String(), nil)
}

func redirectOf(ar fosite.AuthorizeRequester) *url.URL {
	if ar == nil {
		return nil
	}
	u := ar.GetRedirectURI()
	if u == nil || u.String() == "" {
		return nil
	}
	return u
}

func requestFormValue(ar fosite.AuthorizeRequester, key string) string {
	if ar == nil {
		return ""
	}
	return ar.GetRequestForm().Get(key)
}

// redirectToClient 把浏览器送回客户端。
func (h *Handler) redirectToClient(w http.ResponseWriter, redirect string, params url.Values) {
	u, err := url.Parse(redirect)
	if err != nil {
		httpx.Fail(w, apperr.New(apperr.CodeInternal, "回调地址非法"))
		return
	}
	if params != nil {
		q := u.Query()
		for k, vs := range params {
			for _, v := range vs {
				q.Set(k, v)
			}
		}
		u.RawQuery = q.Encode()
	}
	w.Header().Set("Location", u.String())
	w.WriteHeader(http.StatusFound)
}

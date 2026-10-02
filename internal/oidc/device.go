package oidc

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/ory/fosite"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/yggauth/yggauth/internal/oidc/session"
	"github.com/yggauth/yggauth/internal/platform/apperr"
	"github.com/yggauth/yggauth/internal/platform/db"
	"github.com/yggauth/yggauth/internal/platform/db/query"
	"github.com/yggauth/yggauth/internal/platform/httpx"
)

// deviceCodeGrantType 是 RFC 8628 的授权类型。
const deviceCodeGrantType = "urn:ietf:params:oauth:grant-type:device_code"

// 设备码流程的状态。
const (
	deviceStatusPending  = "pending"
	deviceStatusApproved = "approved"
	deviceStatusDenied   = "denied"
	deviceStatusConsumed = "consumed"
)

// 设备码流程的度量。
var (
	deviceIssued = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "yggauth",
		Name:      "oidc_device_code_total",
		Help:      "设备码流程各阶段计数",
	}, []string{"stage", "outcome"})

	devicePoll = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "yggauth",
		Name:      "oidc_device_poll_duration_seconds",
		Help:      "设备从发起授权到拿到令牌所需时长",
		Buckets:   []float64{5, 15, 30, 60, 120, 300, 600},
	}, []string{"outcome"})
)

func init() {
	for _, stage := range []string{"authorize", "approve", "deny", "poll"} {
		deviceIssued.WithLabelValues(stage, "success")
		deviceIssued.WithLabelValues(stage, "failure")
	}
}

// DeviceService 实现 RFC 8628 设备授权流程。
//
// 为什么自己实现:fosite 没有 rfc8628 handler。这个流程的特点是
// **两段式** —— 设备拿到 device_code 后反复轮询令牌端点,
// 用户在另一台设备(手机浏览器)上用 user_code 批准。
type DeviceService struct {
	queries *query.Queries
	clock   func() time.Time
	// codeTTL 是设备码有效期
	codeTTL time.Duration
	// interval 是建议的轮询间隔
	interval int
}

// NewDeviceService 创建设备码服务。
func NewDeviceService(pool *db.Pool, clock func() time.Time, codeTTL time.Duration, interval int) *DeviceService {
	return &DeviceService{
		queries:  query.New(pool),
		clock:    clock,
		codeTTL:  codeTTL,
		interval: interval,
	}
}

// DeviceAuthorizationResult 是设备授权端点的响应(RFC 8628 §3.2)。
type DeviceAuthorizationResult struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete,omitempty"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

// Authorize 签发设备码与用户码。
func (s *DeviceService) Authorize(
	ctx context.Context, clientID string, scopes []string, redirectURI, issuer string,
) (DeviceAuthorizationResult, error) {
	deviceCode, err := randomToken()
	if err != nil {
		return DeviceAuthorizationResult{}, err
	}
	userCode, err := randomUserCode()
	if err != nil {
		return DeviceAuthorizationResult{}, err
	}

	if _, err := s.queries.CreateDeviceCode(ctx, query.CreateDeviceCodeParams{
		DeviceCodeHash: hashCode(deviceCode),
		UserCodeHash:   hashCode(userCode),
		ClientID:       clientID,
		Scopes:         scopes,
		Session:        []byte(`{}`),
		Request:        []byte(`{}`),
		ExpiresAt:      s.clock().Add(s.codeTTL),
	}); err != nil {
		return DeviceAuthorizationResult{}, apperr.Newf(apperr.CodeInternal, "签发设备码失败: %v", err)
	}

	deviceIssued.WithLabelValues("authorize", "success").Inc()

	verify := issuer + "/device"
	return DeviceAuthorizationResult{
		DeviceCode:              deviceCode,
		UserCode:                userCode,
		VerificationURI:         verify,
		VerificationURIComplete: verify + "?user_code=" + urlEscape(userCode),
		ExpiresIn:               int(s.codeTTL.Seconds()),
		Interval:                s.interval,
	}, nil
}

// Lookup 用 user_code 查出待批准的设备授权。
func (s *DeviceService) Lookup(ctx context.Context, userCode string) (DeviceCodeView, error) {
	row, err := s.queries.GetDeviceCodeByUserHash(ctx, hashCode(normalizeUserCode(userCode)))
	if err != nil {
		if db.IsNoRows(err) {
			return DeviceCodeView{}, apperr.New(apperr.CodeNotFound, "用户码无效")
		}
		return DeviceCodeView{}, apperr.Newf(apperr.CodeInternal, "查询设备码失败: %v", err)
	}
	if row.ExpiresAt.Before(s.clock()) {
		return DeviceCodeView{}, apperr.New(apperr.CodeInvalidArgument, "用户码已过期")
	}

	return DeviceCodeView{
		ClientID: row.ClientID,
		Scopes:   row.Scopes,
		Status:   row.Status,
		Pending:  row.Status == deviceStatusPending,
	}, nil
}

// DeviceCodeView 是用户批准页面需要的设备授权信息。
type DeviceCodeView struct {
	ClientID string
	Scopes   []string
	Status   string
	Pending  bool
}

// Approve 批准一次设备授权。
func (s *DeviceService) Approve(ctx context.Context, userCode, accountID, username, email string) error {
	row, err := s.queries.GetDeviceCodeByUserHash(ctx, hashCode(normalizeUserCode(userCode)))
	if err != nil {
		if db.IsNoRows(err) {
			return apperr.New(apperr.CodeNotFound, "用户码无效")
		}
		return apperr.Newf(apperr.CodeInternal, "查询设备码失败: %v", err)
	}

	sessionJSON, err := json.Marshal(NewSession(accountID, username, email, true))
	if err != nil {
		return apperr.Newf(apperr.CodeInternal, "序列化会话失败: %v", err)
	}
	if _, err := json.Marshal(struct{}{}); err != nil {
		return apperr.Newf(apperr.CodeInternal, "序列化请求失败: %v", err)
	}

	// WHERE 里带 status='pending',并发批准只有一次成功
	if _, err := s.queries.ApproveDeviceCode(ctx, query.ApproveDeviceCodeParams{
		ID:        row.ID,
		AccountID: uuidParam(accountID),
		Session:   sessionJSON,
		Request:   []byte(`{}`),
	}); err != nil {
		return apperr.Newf(apperr.CodeInternal, "批准设备授权失败: %v", err)
	}
	// 设备页上的「批准」同样构成 OAuth 同意,不落一条同意记录的话,
	// fosite 的 OIDC handler 会因为查不到会话而跳过 id_token 签发 ——
	// 表现是令牌拿到了,却没有身份断言,依赖方只能再调一次 userinfo。
	if _, err := s.queries.UpsertConsent(ctx, query.UpsertConsentParams{
		AccountID: accountUUIDParam(accountID),
		ClientID:  row.ClientID,
		Scopes:    row.Scopes,
		SessionID: row.ID.String(),
		Session:   sessionJSON,
	}); err != nil {
		return apperr.Newf(apperr.CodeInternal, "保存设备授权同意记录失败: %v", err)
	}

	deviceIssued.WithLabelValues("approve", "success").Inc()
	return nil
}

// Deny 拒绝一次设备授权。
func (s *DeviceService) Deny(ctx context.Context, userCode string) error {
	row, err := s.queries.GetDeviceCodeByUserHash(ctx, hashCode(normalizeUserCode(userCode)))
	if err != nil {
		if db.IsNoRows(err) {
			return apperr.New(apperr.CodeNotFound, "用户码无效")
		}
		return apperr.Newf(apperr.CodeInternal, "查询设备码失败: %v", err)
	}
	if _, err := s.queries.DenyDeviceCode(ctx, row.ID); err != nil {
		return apperr.Newf(apperr.CodeInternal, "拒绝设备授权失败: %v", err)
	}

	deviceIssued.WithLabelValues("deny", "success").Inc()
	return nil
}

// PollResult 是轮询结果。
type PollResult struct {
	// Status 是 approved / denied / pending / expired
	Status string
	// Session 在 approved 时携带用户主体
	Session *session.DefaultSession
	// Interval 是建议的下次轮询间隔
	Interval int
}

// Poll 处理设备侧的一次令牌轮询。
func (s *DeviceService) Poll(ctx context.Context, deviceCode, clientID string) (PollResult, error) {
	row, err := s.queries.GetDeviceCodeByDeviceHash(ctx, hashCode(deviceCode))
	if err != nil {
		if db.IsNoRows(err) {
			// 设备码不存在时不告诉客户端「不存在」还是「已过期」——
			// 两种情况下它都无法再换取令牌,返回同一句话更安全
			return PollResult{Status: "expired"}, nil
		}
		return PollResult{}, apperr.Newf(apperr.CodeInternal, "查询设备码失败: %v", err)
	}

	// 设备码必须与申请它的客户端一致,否则就是令牌窃取
	if row.ClientID != clientID {
		return PollResult{}, apperr.New(apperr.CodeOIDCClientAuthFailed, "设备码与客户端不匹配")
	}

	if row.ExpiresAt.Before(s.clock()) {
		devicePoll.WithLabelValues("expired").Observe(row.ExpiresAt.Sub(row.CreatedAt).Seconds())
		return PollResult{Status: "expired"}, nil
	}

	if err := s.queries.TouchDeviceCodePoll(ctx, row.ID); err != nil {
		return PollResult{}, apperr.Newf(apperr.CodeInternal, "更新轮询时间失败: %v", err)
	}

	elapsed := s.clock().Sub(row.CreatedAt).Seconds()

	switch row.Status {
	case deviceStatusApproved:
		// 一次性:换成令牌后立即作废,同一个设备码换第二次会落到 default
		if _, err := s.queries.ConsumeDeviceCode(ctx, row.ID); err != nil {
			return PollResult{}, apperr.Newf(apperr.CodeInternal, "消费设备码失败: %v", err)
		}
		sess := NewSession("", "", "", false)
		if err := json.Unmarshal(row.Session, sess); err != nil {
			return PollResult{}, apperr.Newf(apperr.CodeInternal, "还原会话失败: %v", err)
		}
		devicePoll.WithLabelValues("approved").Observe(elapsed)
		return PollResult{Status: "approved", Session: sess}, nil

	case deviceStatusDenied:
		devicePoll.WithLabelValues("denied").Observe(elapsed)
		return PollResult{Status: "denied"}, nil

	case deviceStatusPending:
		return PollResult{Status: "pending", Interval: s.interval}, nil

	default:
		// consumed:设备码已经用过了
		return PollResult{Status: "expired"}, nil
	}
}

// ---------------------------------------------------------------- HTTP

// DeviceAuthorization 是设备授权端点。
func (h *Handler) DeviceAuthorization(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		httpx.Fail(w, apperr.New(apperr.CodeInvalidArgument, "无法解析请求参数"))
		return
	}

	clientID, _ := basicAuth(r)
	if clientID == "" {
		clientID = r.PostFormValue("client_id")
	}
	if clientID == "" {
		h.writeOAuthError(w, fosite.ErrInvalidClient)
		return
	}

	client, err := h.server.Storage.GetClient(r.Context(), clientID)
	if err != nil {
		h.writeOAuthError(w, fosite.ErrInvalidClient)
		return
	}

	scopes := fosite.Arguments(strings.Fields(r.PostFormValue("scope")))
	result, err := h.device.Authorize(r.Context(), client.GetID(), toTextSlice(scopes),
		r.PostFormValue("redirect_uri"), h.server.issuer)
	if err != nil {
		httpx.Fail(w, err)
		return
	}

	// RFC 8628 的响应**不走**统一响应包 —— 设备端是标准 OAuth 客户端
	writeJSON(w, http.StatusOK, result)
}

// DeviceVerifyPage 返回用户码对应的待批准信息。
func (h *Handler) DeviceVerifyPage(w http.ResponseWriter, r *http.Request) {
	view, err := h.device.Lookup(r.Context(), r.URL.Query().Get("user_code"))
	if err != nil {
		httpx.Fail(w, err)
		return
	}

	httpx.OK(w, map[string]any{
		"client_id": view.ClientID,
		"scopes":    view.Scopes,
		"status":    view.Status,
		"pending":   view.Pending,
	})
}

// DeviceDecision 处理用户对设备授权的批准或拒绝。
func (h *Handler) DeviceDecision(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		httpx.Fail(w, apperr.New(apperr.CodeInvalidArgument, "无法解析请求参数"))
		return
	}

	acc, err := h.lookupSession(r)
	if err != nil {
		httpx.Fail(w, apperr.ErrUnauthorized)
		return
	}

	userCode := r.PostFormValue("user_code")
	approved := r.PostFormValue("action") != "deny"

	if approved {
		err = h.device.Approve(r.Context(), userCode, acc.ID, acc.Username, acc.Email)
	} else {
		err = h.device.Deny(r.Context(), userCode)
	}
	if err != nil {
		httpx.Fail(w, err)
		return
	}

	httpx.OK(w, map[string]any{"approved": approved})
}

// deviceToken 是设备码流程的令牌端点。
//
// 轮询语义按 RFC 8628 §3.5:
//   - 用户还没批准 → authorization_pending
//   - 用户拒绝     → access_denied
//   - 设备码过期   → expired_token
func (h *Handler) deviceToken(w http.ResponseWriter, r *http.Request) {
	clientID, _ := basicAuth(r)
	if clientID == "" {
		clientID = r.PostFormValue("client_id")
	}
	if clientID == "" {
		h.writeOAuthError(w, fosite.ErrInvalidClient)
		return
	}

	result, err := h.device.Poll(r.Context(), r.PostFormValue("device_code"), clientID)
	if err != nil {
		httpx.Fail(w, err)
		return
	}

	switch result.Status {
	case "pending":
		h.writeOAuthError(w, devicePending(result.Interval))
		return
	case "denied":
		h.writeOAuthError(w, fosite.ErrAccessDenied)
		return
	case "expired":
		h.writeOAuthError(w, fosite.ErrTokenExpired)
		return
	}

	// 已批准:走 fosite 的客户端凭证路径换令牌 ——
	// 设备码流程的身份认证就是客户端认证,用户主体已在会话里
	h.issueDeviceTokens(w, r, clientID, result.Session)
}

// issueDeviceTokens 为已批准的设备授权签发令牌。
//
// 实现路径:内部先走一遍授权码流程换出 code,再用 code 换令牌。
//
// 为什么不直接造令牌:授权码流程里的 PKCE 校验、客户端认证、scope 收窄、
// 令牌轮换策略都在 fosite 里,复用它比在业务层重写一遍安全得多。
// 用户主体在设备码批准时就已固化进会话,不会被这里的空表单覆盖。
func (h *Handler) issueDeviceTokens(w http.ResponseWriter, r *http.Request, clientID string, sess *session.DefaultSession) {
	client, err := h.server.Storage.GetClient(r.Context(), clientID)
	if err != nil {
		h.writeOAuthError(w, fosite.ErrInvalidClient)
		return
	}

	scopes := fosite.Arguments(strings.Fields(r.PostFormValue("scope")))
	if len(scopes) == 0 {
		scopes = fosite.Arguments{"openid", "profile"}
	}

	// 内部换码需要一个回调地址。用客户端登记的第一个 ——
	// 它已经通过白名单校验,不会引入新的开放重定向面。
	redirectURI := ""
	if uris := client.GetRedirectURIs(); len(uris) > 0 {
		redirectURI = uris[0]
	}

	// fosite 负责解析并校验这个请求。用户主体不在这条路径上 ——
	// 它在 NewAuthorizeResponse 时才被注入。
	// 设备端未必带 PKCE 挑战。本服务全局强制 PKCE,缺挑战会让 fosite
	// 直接拒绝。这里由授权服务器自建一对挑战/校验值:
	// 挑战进授权请求,校验值随换令牌请求一起提交。
	challenge, verifier := devicePKCEPair(r)

	authorizeReq := deviceAuthorizeRequest(r, clientID, redirectURI, scopes, challenge)
	authorizeReqer, err := h.server.Provider.NewAuthorizeRequest(r.Context(), authorizeReq)
	if err != nil {
		h.writeOAuthError(w, err)
		return
	}

	codeResp, err := h.server.Provider.NewAuthorizeResponse(r.Context(), authorizeReqer, sess)
	if err != nil {
		h.writeOAuthError(w, err)
		return
	}

	code := codeFromResponse(codeResp)
	if code == "" {
		h.writeOAuthError(w, fosite.ErrServerError.WithHint("生成授权码失败"))
		return
	}

	accessReq := deviceTokenExchange(r, clientID, redirectURI, code, verifier)
	accessRequest, err := h.server.Provider.NewAccessRequest(r.Context(), accessReq, sess)
	if err != nil {
		h.writeOAuthError(w, err)
		return
	}
	response, err := h.server.Provider.NewAccessResponse(r.Context(), accessRequest)
	if err != nil {
		h.writeOAuthError(w, err)
		return
	}
	h.server.Provider.WriteAccessResponse(r.Context(), w, accessRequest, response)

	metricsToken(deviceCodeGrantType, "access_token")
	metricsToken(deviceCodeGrantType, "refresh_token")
	deviceIssued.WithLabelValues("poll", "success").Inc()
}

// codeFromResponse 从授权响应里取出授权码。
func codeFromResponse(resp fosite.AuthorizeResponder) string {
	if resp == nil {
		return ""
	}
	return resp.GetParameters().Get("code")
}

// deviceAuthorizeRequest 构造内部使用的授权请求。
func deviceAuthorizeRequest(r *http.Request, clientID, redirectURI string, scopes fosite.Arguments, pkce string) *http.Request {
	form := url.Values{}
	form.Set("client_id", clientID)
	form.Set("response_type", "code")
	form.Set("redirect_uri", redirectURI)
	form.Set("scope", strings.Join(scopes, " "))
	// fosite 要求 state 至少 8 个字符的熵。这里是一次性的内部换码,
	// state 不会出现在任何浏览器地址栏里,但它仍会进入签名与存储,
	// 所以照样用随机值而不是空串 —— 少一个字段就可能被下游当作
	// 「这条请求没有 CSRF 关联」而放行。
	form.Set("state", randomState())
	if pkce != "" {
		form.Set("code_challenge", pkce)
		form.Set("code_challenge_method", "S256")
	}

	// fosite 从 URL 查询串读授权参数,这里构造一个等价的 GET 请求
	req, _ := http.NewRequestWithContext(r.Context(), http.MethodGet, "/oauth/authorize?"+form.Encode(), nil)
	return req
}

// devicePKCE 取出设备端声明的 PKCE 挑战。
func devicePKCE(r *http.Request) string {
	return r.PostFormValue("code_challenge")
}

// deviceTokenExchange 构造换令牌的请求。
func deviceTokenExchange(r *http.Request, clientID, redirectURI, code, verifier string) *http.Request {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("client_id", clientID)
	form.Set("redirect_uri", redirectURI)
	form.Set("code_verifier", verifier)
	if id, secret := basicAuth(r); id != "" {
		form.Set("client_id", id)
		form.Set("client_secret", secret)
	}

	req, _ := http.NewRequestWithContext(r.Context(), http.MethodPost, "/oauth/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return req
}

// devicePending 构造 authorization_pending 错误。
func devicePending(interval int) *fosite.RFC6749Error {
	err := fosite.ErrRequestUnauthorized.WithHint("The client should repeat the access token request.")
	err.ErrorField = "authorization_pending"
	if interval > 0 {
		err.DescriptionField = "polling_interval_seconds=" + itoa(interval)
	}
	return err
}

// randomToken 生成设备码。
//
// 用 256 位随机数 —— 设备码的熵不足就等于把令牌直接送给攻击者。
func randomToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", apperr.Newf(apperr.CodeInternal, "生成设备码失败: %v", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// userCodeAlphabet 去掉了容易混淆的 0/O/1/I/l。
//
// 用户要在另一台设备上手抄这串码,减一个混淆字符就少一类「明明输对了
// 却提示无效」的支持工单。
const userCodeAlphabet = "BCDFGHJKLMNPQRSTVWXZ23456789"

// randomUserCode 生成 8 位用户码,格式 XXXX-XXXX。
func randomUserCode() (string, error) {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", apperr.Newf(apperr.CodeInternal, "生成用户码失败: %v", err)
	}

	var sb strings.Builder
	sb.Grow(9)
	for i, b := range buf {
		if i == 4 {
			sb.WriteByte('-')
		}
		sb.WriteByte(userCodeAlphabet[int(b)%len(userCodeAlphabet)])
	}
	return sb.String(), nil
}

// normalizeUserCode 把用户输入规范成存储时的形式。
//
// 用户很可能输入小写、带空格或没带连字符 —— 这里全部抹平成
// 大写无连字符,再在比较前补回去,避免「码明明对却查不到」。
func normalizeUserCode(code string) string {
	upper := strings.ToUpper(strings.TrimSpace(code))
	var sb strings.Builder
	sb.Grow(9)
	written := 0
	for _, r := range upper {
		if r == '-' || r == ' ' {
			continue
		}
		if written == 4 {
			sb.WriteByte('-')
		}
		sb.WriteRune(r)
		written++
	}
	return sb.String()
}

// urlEscape 是 url.QueryEscape 的别名。
func urlEscape(raw string) string { return url.QueryEscape(raw) }

// uuidParam 把字符串账号 id 转成 SQL 参数。
func uuidParam(id string) pgtype.UUID {
	parsed, err := uuid.Parse(id)
	if err != nil {
		return pgtype.UUID{}
	}
	return pgtype.UUID{Bytes: parsed, Valid: true}
}

// writeJSON 输出标准 JSON 响应。
func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

// itoa 是 strconv.Itoa 的别名。
func itoa(n int) string { return strconv.Itoa(n) }

// devicePKCEPair 决定设备流程用哪一对 PKCE 参数。
//
// 设备端带了挑战就用它自己的(校验值由设备持有);
// 没带就由授权服务器自建 —— 挑战与校验值都在服务端,PKCE 仍会执行,
// 只是防的是「授权码在服务端内部流转过程中被截获」。
func devicePKCEPair(r *http.Request) (challenge, verifier string) {
	if given := devicePKCE(r); given != "" {
		return given, r.PostFormValue("code_verifier")
	}

	verifier = randomState()
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:]), verifier
}

// randomState 生成高熵的随机串。
//
// 长度按 PKCE 对 code_verifier 的下限(43 字符)取:同一个函数也用来
// 生成内部换码的 state,短于 43 会让 fosite 的校验器直接拒绝。
func randomState() string {
	buf := make([]byte, 48)
	if _, err := rand.Read(buf); err != nil {
		// 熵源不可用时用时间戳兜底。设备流程里的这两个值都不会回到
		// 浏览器,它们的作用是满足 fosite 的校验,不是防重放凭证。
		return strconv.FormatInt(time.Now().UnixNano(), 36) + strings.Repeat("0", 16)
	}
	return base64.RawURLEncoding.EncodeToString(buf)
}

// accountUUIDParam 把账号 id 字符串转成 sqlc 需要的 UUID 参数。
//
// 解析失败返回零值 UUID —— 上游已经用 sessionUUID 校验过账号 id 的
// 合法性,这里走到失败分支说明是同一次请求内的异常,宁可让查询落到
// 空结果也不返回半个合法值。
func accountUUIDParam(id string) uuid.UUID {
	parsed, err := uuid.Parse(id)
	if err != nil {
		return uuid.Nil
	}
	return parsed
}

package identity

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/yggauth/yggauth/internal/domain"
	"github.com/yggauth/yggauth/internal/identity/account"
	"github.com/yggauth/yggauth/internal/identity/audit"
	"github.com/yggauth/yggauth/internal/identity/session"
	"github.com/yggauth/yggauth/internal/platform/apperr"
	"github.com/yggauth/yggauth/internal/platform/httpx"
	"github.com/yggauth/yggauth/internal/platform/log"
)

// CookieConfig 是会话 cookie 的设置。
type CookieConfig struct {
	Name     string
	Domain   string
	Secure   bool
	SameSite http.SameSite
	// IdleTTL 决定 cookie 的 MaxAge
	IdleTTL time.Duration
	// TrustProxyHeaders 决定取 IP 时是否信任转发头
	TrustProxyHeaders bool
}

// Handler 是账号内核的 HTTP 处理器。
type Handler struct {
	svc    *Service
	cookie CookieConfig
	base   string
}

// NewHandler 创建账号内核的 HTTP 处理器。
func NewHandler(svc *Service, cookie CookieConfig, publicBaseURL string) *Handler {
	base := strings.TrimRight(publicBaseURL, "/")
	// 这里**不**缓存策略:策略是后台可改的,构造时取一次的话,
	// 管理员在后台把最小长度从 8 调到 12,注册页会一直提示 8
	// 然后被后端 400 —— 改了没用,且没人报错。
	return &Handler{svc: svc, cookie: cookie, base: base}
}

// Mount 把账号内核的路由挂到 r 上。
func (h *Handler) Mount(r chi.Router, requireAuth func(http.Handler) http.Handler) {
	r.Route("/auth", func(r chi.Router) {
		r.Get("/policy", h.Policy)
		r.Post("/register", h.Register)
		r.Post("/login", h.Login)
		r.Post("/password/forgot", h.ForgotPassword)
		r.Post("/password/reset", h.ResetPassword)
		r.Post("/email/verify", h.VerifyEmail)

		r.Group(func(r chi.Router) {
			r.Use(requireAuth)
			r.Post("/logout", h.Logout)
			r.Post("/logout-all", h.LogoutAll)
			r.Get("/session", h.CurrentSession)
			r.Post("/email/resend", h.ResendVerification)
		})
	})

	r.Route("/account", func(r chi.Router) {
		r.Use(requireAuth)
		r.Get("/", h.Me)
		r.Patch("/", h.UpdateMe)
		r.Patch("/password", h.ChangePassword)
		r.Get("/sessions", h.ListSessions)
		r.Delete("/sessions/{sessionID}", h.RevokeSession)
		r.Get("/audit", h.MyAudit)
	})
}

// ---------------------------------------------------------------- 请求体

type registerRequest struct {
	Username   string `json:"username"`
	Email      string `json:"email"`
	Password   string `json:"password"`
	InviteCode string `json:"invite_code"`
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type forgotRequest struct {
	Email string `json:"email"`
}

type resetRequest struct {
	Token    string `json:"token"`
	Password string `json:"password"`
}

type verifyRequest struct {
	Token string `json:"token"`
}

type updateProfileRequest struct {
	Username *string `json:"username"`
	Email    *string `json:"email"`
}

type changePasswordRequest struct {
	OldPassword string `json:"old_password"`
	NewPassword string `json:"new_password"`
}

// ---------------------------------------------------------------- 响应体

type accountView struct {
	ID            string `json:"id"`
	Username      string `json:"username"`
	Email         string `json:"email"`
	Status        string `json:"status"`
	EmailVerified bool   `json:"email_verified"`
	LoginEnabled  bool   `json:"login_enabled"`
	CreatedAt     string `json:"created_at"`
}

func toAccountView(a domain.Account) accountView {
	return accountView{
		ID:            a.ID.String(),
		Username:      a.Username,
		Email:         a.Email,
		Status:        a.Status.String(),
		EmailVerified: a.EmailVerified,
		LoginEnabled:  a.MCLoginEnabled,
		CreatedAt:     a.CreatedAt.UTC().Format(time.RFC3339),
	}
}

// ---------------------------------------------------------------- 处理器

// Register 处理注册。
func (h *Handler) Register(w http.ResponseWriter, r *http.Request) {
	var req registerRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, err)
		return
	}

	out, err := h.svc.Accounts.Register(r.Context(), account.RegisterInput{
		Username:   req.Username,
		Email:      req.Email,
		Password:   req.Password,
		InviteCode: req.InviteCode,
		IP:         httpx.ClientIP(r, h.cookie.TrustProxyHeaders),
		UserAgent:  r.UserAgent(),
	})
	if err != nil {
		httpx.Fail(w, err)
		return
	}

	ObserveAccountCreated()
	body := map[string]any{
		"account": toAccountView(out.Account),
	}
	// 验证链接只在「邮件到不了收件人」的部署里内联回来(见 account.Config.HideVerifyURL)。
	// smtp 下令牌必须只走邮件:同一份令牌再从 HTTP 响应递一遍,
	// 谁替别人注册谁就拿到它,邮箱验证也就形同虚设。
	if out.VerifyURL != "" {
		body["verify_url"] = out.VerifyURL
	}
	httpx.Created(w, body)
}

// Login 处理登录。
func (h *Handler) Login(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, err)
		return
	}

	ip := httpx.ClientIP(r, h.cookie.TrustProxyHeaders)

	auth, err := h.svc.Accounts.Authenticate(r.Context(), account.AuthenticateInput{
		Email:     req.Email,
		Password:  req.Password,
		IP:        ip,
		UserAgent: r.UserAgent(),
	})
	if err != nil {
		ObserveLogin(false)
		httpx.Fail(w, err)
		return
	}

	issued, err := h.svc.Sessions.Issue(r.Context(), auth.Account.ID, ip, r.UserAgent())
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	// 引导流程置的首登强制改密旗标,登录这一刻投到会话上:
	// 之后每个请求由认证中间件读会话行判断,登录响应回给前端立即跳改密页。
	if auth.MustChangePassword {
		if err := h.svc.Sessions.MarkMustChangePassword(r.Context(), issued.SessionID, true); err != nil {
			httpx.Fail(w, err)
			return
		}
	}
	h.setSessionCookie(w, issued)

	ObserveLogin(true)
	h.refreshActiveSessions(r.Context(), auth.Account.ID)

	httpx.OK(w, map[string]any{
		"account":        toAccountView(auth.Account),
		"email_verified": auth.Account.EmailVerified,
		"login_enabled":  auth.Account.MCLoginEnabled,
		"expires_at":     issued.ExpiresAt.UTC().Format(time.RFC3339),
		// 首登强制改密:前端据此直接跳转改密页,不必等第一个请求被拦
		"must_change_password": auth.MustChangePassword,
	})
}

// Logout 处理登出当前会话。
func (h *Handler) Logout(w http.ResponseWriter, r *http.Request) {
	p := httpx.PrincipalFrom(r.Context())
	if p != nil && p.SessionID != uuid.Nil {
		if err := h.svc.Sessions.Revoke(r.Context(), p.SessionID); err != nil {
			httpx.Fail(w, err)
			return
		}
	}
	h.clearSessionCookie(w)
	httpx.OK(w, map[string]any{"logged_out": true})
}

// LogoutAll 处理登出全部会话。
func (h *Handler) LogoutAll(w http.ResponseWriter, r *http.Request) {
	p := httpx.PrincipalFrom(r.Context())
	if p == nil {
		httpx.Fail(w, apperr.ErrUnauthorized)
		return
	}
	if err := h.svc.Sessions.RevokeAll(r.Context(), p.AccountID); err != nil {
		httpx.Fail(w, err)
		return
	}
	h.clearSessionCookie(w)
	httpx.OK(w, map[string]any{"logged_out_all": true})
}

// CurrentSession 返回当前登录态。
func (h *Handler) CurrentSession(w http.ResponseWriter, r *http.Request) {
	p := httpx.PrincipalFrom(r.Context())
	if p == nil {
		httpx.Fail(w, apperr.ErrUnauthorized)
		return
	}
	httpx.OK(w, map[string]any{
		"account": map[string]any{
			"id":       p.AccountID.String(),
			"username": p.Username,
			"email":    p.Email,
		},
		"session_id":  p.SessionID.String(),
		"permissions": p.Permissions,
		// 前端启动时靠这个决定要不要把用户送去改密页:
		// 页面刷新后,登录响应里的同名字段早没了。
		"must_change_password": p.MustChangePassword,
	})
}

// ForgotPassword 处理找回密码请求。
func (h *Handler) ForgotPassword(w http.ResponseWriter, r *http.Request) {
	var req forgotRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, err)
		return
	}

	// 无论邮箱是否存在都返回成功,避免被当成账号枚举接口
	if err := h.svc.Accounts.RequestPasswordReset(r.Context(), account.RequestPasswordResetInput{
		Email:     req.Email,
		IP:        httpx.ClientIP(r, h.cookie.TrustProxyHeaders),
		UserAgent: r.UserAgent(),
	}); err != nil {
		httpx.Fail(w, err)
		return
	}

	httpx.OK(w, map[string]any{
		"message": "如果该邮箱已注册,我们已经向它发送了重置密码邮件",
	})
}

// ResetPassword 处理执行密码重置。
func (h *Handler) ResetPassword(w http.ResponseWriter, r *http.Request) {
	var req resetRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, err)
		return
	}

	if _, err := h.svc.Accounts.ResetPassword(r.Context(), account.ResetPasswordInput{
		Token:     req.Token,
		Password:  req.Password,
		IP:        httpx.ClientIP(r, h.cookie.TrustProxyHeaders),
		UserAgent: r.UserAgent(),
	}); err != nil {
		httpx.Fail(w, err)
		return
	}

	// 密码变了,旧登录态全部失效
	if err := h.svc.Sessions.RevokeAll(r.Context(), mustAccountID(r)); err != nil {
		httpx.Fail(w, err)
		return
	}
	h.clearSessionCookie(w)

	httpx.OK(w, map[string]any{"reset": true})
}

// VerifyEmail 处理邮箱验证。
func (h *Handler) VerifyEmail(w http.ResponseWriter, r *http.Request) {
	var req verifyRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, err)
		return
	}

	acc, err := h.svc.Accounts.VerifyEmail(r.Context(), account.VerifyEmailInput{
		Token:     req.Token,
		IP:        httpx.ClientIP(r, h.cookie.TrustProxyHeaders),
		UserAgent: r.UserAgent(),
	})
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.OK(w, map[string]any{"account": toAccountView(acc)})
}

// ResendVerification 处理重发验证邮件。
func (h *Handler) ResendVerification(w http.ResponseWriter, r *http.Request) {
	p := httpx.PrincipalFrom(r.Context())
	if p == nil {
		httpx.Fail(w, apperr.ErrUnauthorized)
		return
	}

	// 统一返回成功:即便邮箱已验证、或被冷却/每日上限拦下,
	// 也不告诉调用方,避免被用来探测某个账号的验证状态
	if err := h.svc.Accounts.ResendVerification(r.Context(), account.ResendVerificationInput{
		AccountID: p.AccountID,
		IP:        httpx.ClientIP(r, h.cookie.TrustProxyHeaders),
		UserAgent: r.UserAgent(),
	}); err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.OK(w, map[string]any{"sent": true})
}

// Me 返回当前账号信息。
func (h *Handler) Me(w http.ResponseWriter, r *http.Request) {
	p := httpx.PrincipalFrom(r.Context())
	if p == nil {
		httpx.Fail(w, apperr.ErrUnauthorized)
		return
	}
	acc, err := h.svc.Accounts.Get(r.Context(), p.AccountID)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.OK(w, map[string]any{
		"account": toAccountView(acc),
		// 与 /api/auth/session 同源同值(都来自会话行的投影),
		// 账号站启动时读的正是这个接口,两条路都得带上,
		// 否则「刷新页面就绕过强制改密」。
		"must_change_password": p.MustChangePassword,
	})
}

// UpdateMe 修改用户名或邮箱。
func (h *Handler) UpdateMe(w http.ResponseWriter, r *http.Request) {
	p := httpx.PrincipalFrom(r.Context())
	if p == nil {
		httpx.Fail(w, apperr.ErrUnauthorized)
		return
	}

	var req updateProfileRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, err)
		return
	}

	acc, err := h.svc.Accounts.UpdateProfile(r.Context(), account.UpdateProfileInput{
		AccountID: p.AccountID,
		Username:  req.Username,
		Email:     req.Email,
		IP:        httpx.ClientIP(r, h.cookie.TrustProxyHeaders),
		UserAgent: r.UserAgent(),
	})
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.OK(w, map[string]any{"account": toAccountView(acc)})
}

// ChangePassword 修改密码。
func (h *Handler) ChangePassword(w http.ResponseWriter, r *http.Request) {
	p := httpx.PrincipalFrom(r.Context())
	if p == nil {
		httpx.Fail(w, apperr.ErrUnauthorized)
		return
	}

	var req changePasswordRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, err)
		return
	}

	if err := h.svc.Accounts.ChangePassword(r.Context(), account.ChangePasswordInput{
		AccountID:   p.AccountID,
		OldPassword: req.OldPassword,
		NewPassword: req.NewPassword,
		IP:          httpx.ClientIP(r, h.cookie.TrustProxyHeaders),
		UserAgent:   r.UserAgent(),
	}); err != nil {
		httpx.Fail(w, err)
		return
	}

	// 改密码后吊销全部会话:旧登录态继续有效就等于「改密码没生效」
	if err := h.svc.Sessions.RevokeAll(r.Context(), p.AccountID); err != nil {
		httpx.Fail(w, err)
		return
	}
	h.clearSessionCookie(w)

	httpx.OK(w, map[string]any{"changed": true, "relogin_required": true})
}

// sessionView 是会话列表项。
type sessionView struct {
	ID            string `json:"id"`
	Current       bool   `json:"current"`
	IP            string `json:"ip"`
	UserAgent     string `json:"user_agent"`
	CreatedAt     string `json:"created_at"`
	LastSeenAt    string `json:"last_seen_at"`
	ExpiresAt     string `json:"expires_at"`
	IdleExpiresAt string `json:"idle_expires_at"`
}

// ListSessions 列出活跃会话。
func (h *Handler) ListSessions(w http.ResponseWriter, r *http.Request) {
	p := httpx.PrincipalFrom(r.Context())
	if p == nil {
		httpx.Fail(w, apperr.ErrUnauthorized)
		return
	}

	list, err := h.svc.Sessions.ListActive(r.Context(), p.AccountID)
	if err != nil {
		httpx.Fail(w, err)
		return
	}

	out := make([]sessionView, 0, len(list))
	for _, s := range list {
		out = append(out, sessionView{
			ID:            s.ID.String(),
			Current:       s.ID == p.SessionID,
			IP:            s.IP,
			UserAgent:     s.UserAgent,
			CreatedAt:     s.CreatedAt.UTC().Format(time.RFC3339),
			LastSeenAt:    s.LastSeenAt.UTC().Format(time.RFC3339),
			ExpiresAt:     s.ExpiresAt.UTC().Format(time.RFC3339),
			IdleExpiresAt: s.IdleExpiresAt.UTC().Format(time.RFC3339),
		})
	}
	httpx.OK(w, map[string]any{"sessions": out})
}

// RevokeSession 踢掉指定会话。
func (h *Handler) RevokeSession(w http.ResponseWriter, r *http.Request) {
	p := httpx.PrincipalFrom(r.Context())
	if p == nil {
		httpx.Fail(w, apperr.ErrUnauthorized)
		return
	}

	sessionID, err := uuid.Parse(chi.URLParam(r, "sessionID"))
	if err != nil {
		httpx.Fail(w, apperr.New(apperr.CodeInvalidArgument, "会话 ID 非法"))
		return
	}
	if err := h.svc.Sessions.RevokeOne(r.Context(), p.AccountID, sessionID); err != nil {
		httpx.Fail(w, err)
		return
	}
	// 踢掉的是当前会话时,顺手清掉 cookie
	if sessionID == p.SessionID {
		h.clearSessionCookie(w)
	}
	httpx.OK(w, map[string]any{"revoked": true})
}

// MyAudit 返回当前账号自己的操作记录。
func (h *Handler) MyAudit(w http.ResponseWriter, r *http.Request) {
	p := httpx.PrincipalFrom(r.Context())
	if p == nil {
		httpx.Fail(w, apperr.ErrUnauthorized)
		return
	}

	limit := int32(50)
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 200 {
			limit = int32(n)
		}
	}
	offset := int32(0)
	if v := r.URL.Query().Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			offset = int32(n)
		}
	}

	// 只能查自己的记录:account_id 由登录态给出,绝不接受查询参数里的账号 ID
	accID := p.AccountID
	entries, total, err := h.svc.Audit.Search(r.Context(), audit.Filter{
		AccountID: &accID,
		// 动作与结果只是缩小范围,不影响「只看自己」这条硬约束
		Action:  r.URL.Query().Get("action"),
		Outcome: r.URL.Query().Get("outcome"),
		Limit:   limit,
		Offset:  offset,
	})
	if err != nil {
		httpx.Fail(w, err)
		return
	}

	httpx.OK(w, map[string]any{"events": entries, "total": total})
}

// ---------------------------------------------------------------- cookie

func (h *Handler) setSessionCookie(w http.ResponseWriter, issued session.Issued) {
	httpx.SetCookie(w, httpx.Cookie{
		Name:     h.cookie.Name,
		Value:    issued.Token,
		Domain:   h.cookie.Domain,
		Secure:   h.cookie.Secure,
		SameSite: h.cookie.SameSite,
		MaxAge:   int(h.cookie.IdleTTL.Seconds()),
	})
}

func (h *Handler) clearSessionCookie(w http.ResponseWriter) {
	httpx.ClearCookie(w, httpx.Cookie{
		Name:     h.cookie.Name,
		Domain:   h.cookie.Domain,
		Secure:   h.cookie.Secure,
		SameSite: h.cookie.SameSite,
	})
}

func (h *Handler) refreshActiveSessions(ctx context.Context, accountID uuid.UUID) {
	n, err := h.svc.CountActiveSessions(ctx, accountID)
	if err != nil {
		log.L(ctx).Warn("count active sessions failed", "account_id", accountID, "error", err)
		return
	}
	SetActiveSessions(n)
}

func mustAccountID(r *http.Request) uuid.UUID {
	if p := httpx.PrincipalFrom(r.Context()); p != nil {
		return p.AccountID
	}
	return uuid.Nil
}

// Policy 返回前端需要的账号内核约束。
//
// 公开、无需鉴权:注册页在登录之前就要用这些规则。
// 只暴露**约束**,不暴露任何配置细节(存储位置、算法参数)。
func (h *Handler) Policy(w http.ResponseWriter, r *http.Request) {
	// 现读而不是读构造时的快照:这条端点就是为了让前端「不抄规则」,
	// 它自己要是拿了旧规则,抄规则的老问题会以另一种形式回来。
	policy := h.svc.Accounts.Policy(r.Context())
	httpx.OK(w, map[string]any{
		"password_min_length":        policy.MinLength,
		"password_max_length":        policy.MaxLength,
		"password_reject_common":     policy.RejectCommon,
		"username_min_length":        usernameMinLength,
		"username_max_length":        usernameMaxLength,
		"registration_mode":          h.svc.Accounts.RegistrationMode(r.Context()),
		"require_email_verification": true,
	})
}

const (
	usernameMinLength = 3
	usernameMaxLength = 32
)

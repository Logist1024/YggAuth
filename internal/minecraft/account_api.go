package minecraft

import (
	"net/http"

	"github.com/google/uuid"

	"github.com/yggauth/yggauth/internal/platform/apperr"
	"github.com/yggauth/yggauth/internal/platform/httpx"
)

// 账号侧的 MC 管理端点。
//
// 这些是**本站 API**,走统一响应包;与 `/mc/*` 协议端点的响应格式不同。
// 挂在 /api/account/mc 下,复用 transport 里已有的会话认证与 CSRF 中间件。

// AccountAPIHandler 提供账号侧的 MC 管理接口。
type AccountAPIHandler struct {
	svc *Service
}

// NewAccountAPIHandler 创建处理器。
func NewAccountAPIHandler(svc *Service) *AccountAPIHandler {
	return &AccountAPIHandler{svc: svc}
}

// MCMount 是本处理器需要的路由注册能力。
//
// 只声明用得到的两个方法,而不是直接依赖 chi.Router:路由库换了不该
// 波及业务逻辑。
type MCMount interface {
	Get(pattern string, h http.HandlerFunc)
	Patch(pattern string, h http.HandlerFunc)
	Post(pattern string, h http.HandlerFunc)
}

// Mount 挂载路由。
func (h *AccountAPIHandler) Mount(r MCMount) {
	r.Get("/profile", h.Profile)
	r.Get("/names", h.NameHistory)
	r.Get("/login-enabled", h.LoginEnabled)
	r.Patch("/name", h.Rename)
	r.Post("/login-enabled", h.SetLoginEnabled)
}

// mcAccountID 从请求上下文取已登录账号的 id。
//
// 会话认证中间件保证了这里有值;取不到就返回 false 让调用方回 401,
// 而不是继续往下走 —— 宁可明确失败,也不要拿着零值 uuid 去改别人的档案。
func mcAccountID(r *http.Request) (uuid.UUID, bool) {
	p := httpx.PrincipalFrom(r.Context())
	if p == nil || p.AccountID == uuid.Nil {
		return uuid.Nil, false
	}
	return p.AccountID, true
}

// Profile 返回当前账号的 MC 档案。
func (h *AccountAPIHandler) Profile(w http.ResponseWriter, r *http.Request) {
	id, ok := mcAccountID(r)
	if !ok {
		httpx.Fail(w, apperr.ErrUnauthorized)
		return
	}

	profile, err := h.svc.firstProfile(r.Context(), id)
	if err != nil {
		httpx.Fail(w, err)
		return
	}

	// 开关查询失败时按「开启」处理:返回 false 会让前端显示成「已关闭」,
	// 用户再去「打开」一次 —— 这个方向错了,比多显示一个开关更糟。
	enabled := true
	if v, err := h.svc.LoginEnabled(r.Context(), id); err == nil {
		enabled = v
	}

	httpx.OK(w, map[string]any{
		"id":            FormatUUID(profile.UUID),
		"name":          profile.CurrentName,
		"login_enabled": enabled,
	})
}

// NameHistory 返回改名历史。
func (h *AccountAPIHandler) NameHistory(w http.ResponseWriter, r *http.Request) {
	id, ok := mcAccountID(r)
	if !ok {
		httpx.Fail(w, apperr.ErrUnauthorized)
		return
	}

	profiles, err := h.svc.ProfilesByAccount(r.Context(), id)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	// 还没登录过 MC 的账号没有档案,也就没有历史 —— 返回空列表而不是 404。
	if len(profiles) == 0 {
		httpx.OK(w, map[string]any{"items": []NameRecord{}})
		return
	}

	history, err := h.svc.NameHistory(r.Context(), profiles[0].ID)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.OK(w, map[string]any{"items": history})
}

// renameRequest 是改名请求体。
type renameRequest struct {
	NewName string `json:"new_name"`
}

// Rename 改名。
func (h *AccountAPIHandler) Rename(w http.ResponseWriter, r *http.Request) {
	id, ok := mcAccountID(r)
	if !ok {
		httpx.Fail(w, apperr.ErrUnauthorized)
		return
	}

	var req renameRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, err)
		return
	}

	profile, err := h.svc.firstProfile(r.Context(), id)
	if err != nil {
		httpx.Fail(w, err)
		return
	}

	renamed, err := h.svc.Rename(r.Context(), profile.ID, req.NewName)
	if err != nil {
		httpx.Fail(w, err)
		return
	}

	httpx.OK(w, map[string]any{
		"id":   FormatUUID(renamed.UUID),
		"name": renamed.CurrentName,
	})
}

// LoginEnabled 查询 MC 登录开关。
func (h *AccountAPIHandler) LoginEnabled(w http.ResponseWriter, r *http.Request) {
	id, ok := mcAccountID(r)
	if !ok {
		httpx.Fail(w, apperr.ErrUnauthorized)
		return
	}

	enabled, err := h.svc.LoginEnabled(r.Context(), id)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.OK(w, map[string]any{"enabled": enabled})
}

// loginEnabledRequest 是开关请求体。
type loginEnabledRequest struct {
	Enabled bool `json:"enabled"`
}

// SetLoginEnabled 设置 MC 登录开关。
func (h *AccountAPIHandler) SetLoginEnabled(w http.ResponseWriter, r *http.Request) {
	id, ok := mcAccountID(r)
	if !ok {
		httpx.Fail(w, apperr.ErrUnauthorized)
		return
	}

	var req loginEnabledRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, err)
		return
	}

	if err := h.svc.SetLoginEnabled(r.Context(), id, req.Enabled); err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.OK(w, map[string]any{"enabled": req.Enabled})
}

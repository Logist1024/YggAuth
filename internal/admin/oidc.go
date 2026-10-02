package admin

import (
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/yggauth/yggauth/internal/oidc"
	"github.com/yggauth/yggauth/internal/platform/apperr"
	"github.com/yggauth/yggauth/internal/platform/httpx"
)

// OIDC 客户端与签名密钥的后台接口。
//
// 密钥只在创建与轮换的响应里出现一次,之后永远只能取到哈希 ——
// 这是「明文密钥不可二次查看」这条规则的落地点。

// createClientRequest 是登记客户端的入参。
type createClientRequest struct {
	ClientID        string   `json:"client_id"`
	Name            string   `json:"name"`
	Description     string   `json:"description"`
	RedirectURIs    []string `json:"redirect_uris"`
	GrantTypes      []string `json:"grant_types"`
	Scopes          []string `json:"scopes"`
	RequirePKCE     bool     `json:"require_pkce"`
	Public          bool     `json:"public"`
	AccessTokenTTL  string   `json:"access_token_ttl"`
	RefreshTokenTTL string   `json:"refresh_token_ttl"`
	AuthCodeTTL     string   `json:"auth_code_ttl"`
}

// requireOIDC 在授权服务未装配时给出明确错误。
func (h *Handler) requireOIDC() error {
	if h.deps.OIDC == nil {
		return apperr.New(apperr.CodeInternal, "授权服务未装配")
	}
	return nil
}

// ListOIDCClients 分页列出客户端。
func (h *Handler) ListOIDCClients(w http.ResponseWriter, r *http.Request) {
	if err := h.requireOIDC(); err != nil {
		httpx.Fail(w, err)
		return
	}

	status := r.URL.Query().Get("status")
	search := r.URL.Query().Get("search")
	limit := limitParam(r, 20)
	offset := intParam(r, "offset", 0)

	clients, total, err := h.deps.OIDC.List(r.Context(), status, search, int32(limit), offset)
	if err != nil {
		httpx.Fail(w, err)
		return
	}

	// 列表里绝不带密钥哈希:它没有用(校验走 bcrypt),只会多一份可泄露的副本
	views := make([]map[string]any, 0, len(clients))
	for _, c := range clients {
		views = append(views, map[string]any{
			"client_id":         c.ID,
			"name":              c.Name,
			"description":       c.Description,
			"redirect_uris":     c.RedirectURIs,
			"grant_types":       c.GrantTypes,
			"scopes":            c.Scopes,
			"require_pkce":      c.RequirePKCE,
			"public":            c.SecretHash == "",
			"status":            c.Status,
			"access_token_ttl":  c.AccessTokenTTL.String(),
			"refresh_token_ttl": c.RefreshTokenTTL.String(),
			"auth_code_ttl":     c.AuthCodeTTL.String(),
		})
	}

	httpx.OK(w, map[string]any{"items": views, "total": total, "limit": limit, "offset": offset})
}

// CreateOIDCClient 登记客户端。
func (h *Handler) CreateOIDCClient(w http.ResponseWriter, r *http.Request) {
	if err := h.requireOIDC(); err != nil {
		httpx.Fail(w, err)
		return
	}

	var req createClientRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, err)
		return
	}

	rec, err := h.deps.OIDC.Create(r.Context(), oidc.CreateClientInput{
		ClientID:        strings.TrimSpace(req.ClientID),
		Name:            req.Name,
		Description:     req.Description,
		RedirectURIs:    req.RedirectURIs,
		GrantTypes:      req.GrantTypes,
		Scopes:          req.Scopes,
		RequirePKCE:     req.RequirePKCE,
		Public:          req.Public,
		AccessTokenTTL:  parseTTL(req.AccessTokenTTL, time.Hour),
		RefreshTokenTTL: parseTTL(req.RefreshTokenTTL, 30*24*time.Hour),
		AuthCodeTTL:     parseTTL(req.AuthCodeTTL, 60*time.Second),
	})
	if err != nil {
		// 参数校验类的错误(如不合法的回调地址)要原样返回 400,
		// 不能一律吞成 500 —— 前端需要靠业务码给出可操作的提示。
		h.fail(w, "登记客户端失败", err)
		return
	}

	// 明文密钥只在这里出现一次
	httpx.Created(w, map[string]any{
		"client_id":      rec.ID,
		"name":           rec.Name,
		"client_secret":  rec.Secret,
		"redirect_uris":  rec.RedirectURIs,
		"grant_types":    rec.GrantTypes,
		"scopes":         rec.Scopes,
		"require_pkce":   rec.RequirePKCE,
		"secret_warning": "客户端密钥仅在此处显示,请立即保存",
	})
}

// RotateOIDCClientSecret 轮换客户端密钥。
func (h *Handler) RotateOIDCClientSecret(w http.ResponseWriter, r *http.Request) {
	if err := h.requireOIDC(); err != nil {
		httpx.Fail(w, err)
		return
	}

	clientID := chi.URLParam(r, "clientID")
	secret, err := h.deps.OIDC.RotateSecret(r.Context(), clientID)
	if err != nil {
		h.failInternal(w, "轮换客户端密钥失败", err)
		return
	}

	httpx.OK(w, map[string]any{
		"client_id":      clientID,
		"client_secret":  secret,
		"secret_warning": "旧密钥已立即失效,轮换破坏性变更请谨慎执行",
	})
}

// DeleteOIDCClient 删除客户端。
func (h *Handler) DeleteOIDCClient(w http.ResponseWriter, r *http.Request) {
	if err := h.requireOIDC(); err != nil {
		httpx.Fail(w, err)
		return
	}
	if err := h.deps.OIDC.Delete(r.Context(), chi.URLParam(r, "clientID")); err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.NoContent(w)
}

// ListSigningKeys 列出签名密钥。
func (h *Handler) ListSigningKeys(w http.ResponseWriter, r *http.Request) {
	if h.deps.OIDCKeys == nil {
		httpx.Fail(w, apperr.New(apperr.CodeInternal, "密钥管理器未装配"))
		return
	}

	records, err := h.deps.OIDCKeys.List(r.Context())
	if err != nil {
		httpx.Fail(w, err)
		return
	}

	views := make([]map[string]any, 0, len(records))
	for _, rec := range records {
		views = append(views, map[string]any{
			"kid":     rec.Kid,
			"algo":    rec.Algo,
			"status":  string(rec.Status),
			"created": rec.ID.String(),
		})
	}
	httpx.OK(w, map[string]any{"items": views})
}

// RotateSigningKey 轮换签名密钥。
//
// 旧密钥**不删除** —— 已经发出去的令牌还在用它签名,删掉会让所有
// 存量令牌立刻验签失败。它们降级为 retired,继续留在 JWKS 里。
func (h *Handler) RotateSigningKey(w http.ResponseWriter, r *http.Request) {
	if h.deps.OIDCKeys == nil {
		httpx.Fail(w, apperr.New(apperr.CodeInternal, "密钥管理器未装配"))
		return
	}

	rec, err := h.deps.OIDCKeys.Rotate(r.Context())
	if err != nil {
		h.failInternal(w, "轮换签名密钥失败", err)
		return
	}
	httpx.OK(w, map[string]any{"kid": rec.Kid, "status": string(rec.Status)})
}

// parseTTL 解析时长字符串,非法时用 fallback。
func parseTTL(raw string, fallback time.Duration) time.Duration {
	if raw == "" {
		return fallback
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return fallback
	}
	return d
}

// Package admin 提供管理后台的 HTTP 接口。
//
// 它是账号内核的**门面**:账号、角色权限、审计这些能力都由 identity 提供,
// 这里只做参数解析、权限点校验与响应组装,不重复实现业务规则
// (docs/02-architecture.md 第二节)。
package admin

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/yggauth/yggauth/internal/platform/rand"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/yggauth/yggauth/internal/config"
	"github.com/yggauth/yggauth/internal/domain"
	"github.com/yggauth/yggauth/internal/identity"
	"github.com/yggauth/yggauth/internal/identity/account"
	"github.com/yggauth/yggauth/internal/identity/audit"
	"github.com/yggauth/yggauth/internal/identity/rbac"
	"github.com/yggauth/yggauth/internal/platform/apperr"

	"github.com/yggauth/yggauth/internal/platform/db"
	"github.com/yggauth/yggauth/internal/platform/httpx"
)

// 权限点常量。
//
// 与迁移里的 seed 数据一一对应。写错一个字符串就等于把接口敞开或锁死,
// 所以集中在这里声明,而不是散落在路由里。
const (
	PermAccountRead  = "account:read"
	PermAccountWrite = "account:write"
	PermRBACRead     = "rbac:read"
	PermRBACWrite    = "rbac:write"
	PermAuditRead    = "audit:read"
	PermAuditExport  = "audit:export"
	PermSettingRead  = "setting:read"
	PermSettingWrite = "setting:write"
)

// Deps 是后台接口的依赖。
type Deps struct {
	Identity *identity.Service
	Settings *config.SettingStore
	DB       *db.Pool
	// PublicBaseURL 用于拼邀请链接
	PublicBaseURL string
}

// Handler 是管理后台的 HTTP 处理器。
type Handler struct {
	deps Deps
}

// NewHandler 创建后台处理器。
func NewHandler(deps Deps) *Handler { return &Handler{deps: deps} }

// Mount 把后台路由挂到 /api/admin。
func (h *Handler) Mount(r chi.Router) {
	r.Route("/admin", func(r chi.Router) {
		// 后台接口一律要求登录:权限点求值需要一个已认证主体
		r.Use(httpx.RequireAuth)
		r.Get("/me", h.Me)
		r.Get("/menus", h.Menus)
		r.Get("/dashboard", h.Dashboard)

		r.With(httpx.RequirePermission(PermAccountRead)).Get("/accounts", h.ListAccounts)
		r.With(httpx.RequirePermission(PermAccountWrite)).Patch("/accounts/{accountID}", h.UpdateAccount)

		r.With(httpx.RequirePermission(PermRBACRead)).Get("/roles", h.ListRoles)
		r.With(httpx.RequirePermission(PermRBACWrite)).Post("/roles", h.CreateRole)
		r.With(httpx.RequirePermission(PermRBACWrite)).Patch("/roles/{roleID}", h.UpdateRole)
		r.With(httpx.RequirePermission(PermRBACWrite)).Delete("/roles/{roleID}", h.DeleteRole)
		r.With(httpx.RequirePermission(PermRBACWrite)).Post("/roles/grant", h.GrantRole)
		r.With(httpx.RequirePermission(PermRBACWrite)).Post("/roles/revoke", h.RevokeRole)
		r.With(httpx.RequirePermission(PermRBACRead)).Get("/permission-points", h.ListPermissions)

		r.With(httpx.RequirePermission(PermAuditRead)).Get("/audit", h.SearchAudit)
		r.With(httpx.RequirePermission(PermAuditExport)).Get("/audit/export", h.ExportAudit)

		r.With(httpx.RequirePermission(PermRBACWrite)).Get("/invitations", h.ListInvitations)
		r.With(httpx.RequirePermission(PermRBACWrite)).Post("/invitations", h.CreateInvitation)

		r.With(httpx.RequirePermission(PermSettingRead)).Get("/settings", h.GetSettings)
		r.With(httpx.RequirePermission(PermSettingWrite)).Patch("/settings", h.UpdateSetting)
	})
}

// ---------------------------------------------------------------- 自身

// Me 返回当前管理员与权限点。
func (h *Handler) Me(w http.ResponseWriter, r *http.Request) {
	p := httpx.PrincipalFrom(r.Context())
	roles, err := h.deps.Identity.RBAC.ListAccountRoles(r.Context(), p.AccountID)
	if err != nil {
		httpx.Fail(w, err)
		return
	}

	roleViews := make([]map[string]any, 0, len(roles))
	for _, role := range roles {
		roleViews = append(roleViews, map[string]any{
			"id": role.ID.String(), "code": role.Code, "name": role.Name,
		})
	}

	httpx.OK(w, map[string]any{
		"account": map[string]any{
			"id":       p.AccountID.String(),
			"username": p.Username,
			"email":    p.Email,
		},
		"roles":       roleViews,
		"permissions": p.Permissions,
	})
}

// menuItem 是后台菜单项。
type menuItem struct {
	Key        string     `json:"key"`
	Path       string     `json:"path"`
	Title      string     `json:"title"`
	Icon       string     `json:"icon"`
	Permission string     `json:"permission"`
	Children   []menuItem `json:"children,omitempty"`
}

// allMenus 是完整菜单定义。
//
// 后端下发菜单而不是前端硬编码,是为了让「加一个后台模块」
// 不必改前端 —— 菜单的可见性由权限点决定。
var allMenus = []menuItem{
	{Key: "dashboard", Path: "/dashboard", Title: "仪表盘", Icon: "dashboard"},
	{Key: "accounts", Path: "/accounts", Title: "账号管理", Icon: "user", Permission: PermAccountRead},
	{Key: "roles", Path: "/roles", Title: "角色权限", Icon: "safety", Permission: PermRBACRead},
	{Key: "invitations", Path: "/invitations", Title: "邀请管理", Icon: "mail", Permission: PermRBACWrite},
	{Key: "audit", Path: "/audit", Title: "审计日志", Icon: "file-search", Permission: PermAuditRead},
	{Key: "clients", Path: "/clients", Title: "OIDC 客户端", Icon: "api", Permission: "oidc:client:read"},
	{Key: "mc_profiles", Path: "/mc/profiles", Title: "玩家档案", Icon: "idcard", Permission: "minecraft:profile:read"},
	{Key: "mc_textures", Path: "/mc/textures", Title: "材质库", Icon: "picture", Permission: "minecraft:texture:read"},
	{Key: "settings", Path: "/settings", Title: "应用配置", Icon: "setting", Permission: PermSettingRead},
}

// Menus 返回按权限点过滤后的菜单。
func (h *Handler) Menus(w http.ResponseWriter, r *http.Request) {
	p := httpx.PrincipalFrom(r.Context())

	visible := make([]menuItem, 0, len(allMenus))
	for _, m := range allMenus {
		if m.Permission == "" || p.Can(m.Permission) {
			visible = append(visible, m)
		}
	}
	httpx.OK(w, map[string]any{"menus": visible})
}

// Dashboard 返回概览统计。
func (h *Handler) Dashboard(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	accounts, total, err := h.deps.Identity.Accounts.List(ctx, account.ListFilter{Limit: 1})
	if err != nil {
		httpx.Fail(w, err)
		return
	}

	roles, err := h.deps.Identity.RBAC.ListRoles(ctx, "")
	if err != nil {
		httpx.Fail(w, err)
		return
	}

	perms, err := h.deps.Identity.RBAC.ListPermissions(ctx)
	if err != nil {
		httpx.Fail(w, err)
		return
	}

	// 统计事件数用一次轻量查询,不做全表扫描
	var auditCount int64
	if h.deps.DB != nil {
		_ = h.deps.DB.QueryRow(ctx,
			`SELECT count(*) FROM identity.audit_event WHERE occurred_at > now() - interval '24 hours'`,
		).Scan(&auditCount)
	}

	httpx.OK(w, map[string]any{
		"accounts_total":    total,
		"accounts_recent":   len(accounts),
		"roles_total":       len(roles),
		"permissions_total": len(perms),
		"audit_events_24h":  auditCount,
	})
}

// ---------------------------------------------------------------- 账号

type updateAccountRequest struct {
	Status *string `json:"status"`
	Email  *string `json:"email"`
}

// UpdateAccount 修改账号状态或邮箱。
func (h *Handler) UpdateAccount(w http.ResponseWriter, r *http.Request) {
	targetID, err := uuid.Parse(chi.URLParam(r, "accountID"))
	if err != nil {
		httpx.Fail(w, apperr.New(apperr.CodeInvalidArgument, "账号 ID 非法"))
		return
	}

	var req updateAccountRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, err)
		return
	}

	actor := httpx.PrincipalFrom(r.Context())

	if req.Status != nil {
		status := domain.AccountStatus(*req.Status)
		if !status.Valid() {
			httpx.Fail(w, apperr.Newf(apperr.CodeInvalidArgument, "非法账号状态: %s", status))
			return
		}
		// 不允许通过这个接口禁用自己 —— 否则管理员可能把自己锁在门外
		if targetID == actor.AccountID && status != domain.StatusActive {
			httpx.Fail(w, apperr.New(apperr.CodeForbidden, "不能修改自己的账号状态"))
			return
		}
		if _, err := h.deps.Identity.Accounts.SetStatus(r.Context(), targetID, status); err != nil {
			httpx.Fail(w, err)
			return
		}
		h.auditAdmin(r, actor.AccountID, "account.status_changed", "account", targetID.String(),
			map[string]any{"status": string(status)})
	}

	if req.Email != nil {
		updated, err := h.deps.Identity.Accounts.UpdateProfile(r.Context(), account.UpdateProfileInput{
			AccountID: targetID,
			Email:     req.Email,
			IP:        httpx.ClientIP(r, false),
			UserAgent: r.UserAgent(),
		})
		if err != nil {
			httpx.Fail(w, err)
			return
		}
		// 改邮箱后旧登录态不应继续有效
		if err := h.deps.Identity.Sessions.RevokeAll(r.Context(), targetID); err != nil {
			httpx.Fail(w, err)
			return
		}
		h.auditAdmin(r, actor.AccountID, "account.email_changed", "account", targetID.String(), nil)
		httpx.OK(w, map[string]any{"account": accountView(updated)})
		return
	}

	acc, err := h.deps.Identity.Accounts.Get(r.Context(), targetID)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.OK(w, map[string]any{"account": accountView(acc)})
}

// ListAccounts 分页查询账号。
func (h *Handler) ListAccounts(w http.ResponseWriter, r *http.Request) {
	filter := account.ListFilter{
		Status: r.URL.Query().Get("status"),
		Search: r.URL.Query().Get("search"),
		Limit:  intParam(r, "limit", 20),
		Offset: intParam(r, "offset", 0),
	}

	list, total, err := h.deps.Identity.Accounts.List(r.Context(), filter)
	if err != nil {
		httpx.Fail(w, err)
		return
	}

	out := make([]map[string]any, 0, len(list))
	for _, a := range list {
		out = append(out, accountView(a))
	}
	httpx.OK(w, map[string]any{"accounts": out, "total": total})
}

// ---------------------------------------------------------------- 角色权限

// ListRoles 列出角色。
func (h *Handler) ListRoles(w http.ResponseWriter, r *http.Request) {
	roles, err := h.deps.Identity.RBAC.ListRoles(r.Context(), r.URL.Query().Get("search"))
	if err != nil {
		httpx.Fail(w, err)
		return
	}

	out := make([]map[string]any, 0, len(roles))
	for _, role := range roles {
		perms, err := h.deps.Identity.RBAC.ListRolePermissions(r.Context(), role.ID)
		if err != nil {
			httpx.Fail(w, err)
			return
		}
		codes := make([]string, 0, len(perms))
		for _, p := range perms {
			codes = append(codes, p.Code)
		}
		out = append(out, map[string]any{
			"id": role.ID.String(), "code": role.Code, "name": role.Name,
			"description": role.Description, "is_system": role.IsSystem,
			"permissions": codes,
			"created_at":  role.CreatedAt.UTC().Format(time.RFC3339),
		})
	}
	httpx.OK(w, map[string]any{"roles": out})
}

type createRoleRequest struct {
	Code        string   `json:"code"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Permissions []string `json:"permissions"`
}

// CreateRole 创建角色。
func (h *Handler) CreateRole(w http.ResponseWriter, r *http.Request) {
	var req createRoleRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, err)
		return
	}

	role, err := h.deps.Identity.RBAC.CreateRole(r.Context(), rbac.CreateRoleInput{
		Code:        req.Code,
		Name:        req.Name,
		Description: req.Description,
		Permissions: req.Permissions,
	})
	if err != nil {
		httpx.Fail(w, err)
		return
	}

	actor := httpx.PrincipalFrom(r.Context())
	h.auditAdmin(r, actor.AccountID, "rbac.role_created", "role", role.ID.String(),
		map[string]any{"code": role.Code})

	httpx.Created(w, map[string]any{"role": roleView(role)})
}

type updateRoleRequest struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Permissions []string `json:"permissions"`
}

// UpdateRole 修改角色。
//
// Permissions 为空表示「不改权限」,而不是「清空权限」——
// 前端表单通常只提交名称变更,后者会意外清空授权。
func (h *Handler) UpdateRole(w http.ResponseWriter, r *http.Request) {
	roleID, err := uuid.Parse(chi.URLParam(r, "roleID"))
	if err != nil {
		httpx.Fail(w, apperr.New(apperr.CodeInvalidArgument, "角色 ID 非法"))
		return
	}

	var req updateRoleRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, err)
		return
	}

	role, err := h.deps.Identity.RBAC.UpdateRole(r.Context(), roleID, req.Name, req.Description)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	if req.Permissions != nil {
		if err := h.deps.Identity.RBAC.SetPermissions(r.Context(), roleID, req.Permissions); err != nil {
			httpx.Fail(w, err)
			return
		}
	}

	actor := httpx.PrincipalFrom(r.Context())
	h.auditAdmin(r, actor.AccountID, "rbac.role_updated", "role", roleID.String(), nil)

	httpx.OK(w, map[string]any{"role": roleView(role)})
}

// DeleteRole 删除角色。
func (h *Handler) DeleteRole(w http.ResponseWriter, r *http.Request) {
	roleID, err := uuid.Parse(chi.URLParam(r, "roleID"))
	if err != nil {
		httpx.Fail(w, apperr.New(apperr.CodeInvalidArgument, "角色 ID 非法"))
		return
	}
	if err := h.deps.Identity.RBAC.DeleteRole(r.Context(), roleID); err != nil {
		httpx.Fail(w, err)
		return
	}

	actor := httpx.PrincipalFrom(r.Context())
	h.auditAdmin(r, actor.AccountID, "rbac.role_deleted", "role", roleID.String(), nil)

	httpx.OK(w, map[string]any{"deleted": true})
}

type grantRequest struct {
	AccountID string `json:"account_id"`
	RoleID    string `json:"role_id"`
}

// GrantRole 授予角色。
func (h *Handler) GrantRole(w http.ResponseWriter, r *http.Request) {
	accountID, roleID, err := h.auditTargets(w, r)
	if err != nil {
		httpx.Fail(w, err)
		return
	}

	actor := httpx.PrincipalFrom(r.Context())
	if err := h.deps.Identity.RBAC.Grant(r.Context(), accountID, roleID, actor.AccountID); err != nil {
		httpx.Fail(w, err)
		return
	}

	h.auditAdmin(r, actor.AccountID, "rbac.role_granted", "account", accountID.String(),
		map[string]any{"role_id": roleID.String()})
	httpx.OK(w, map[string]any{"granted": true})
}

// RevokeRole 撤销角色。
func (h *Handler) RevokeRole(w http.ResponseWriter, r *http.Request) {
	accountID, roleID, err := h.auditTargets(w, r)
	if err != nil {
		httpx.Fail(w, err)
		return
	}

	actor := httpx.PrincipalFrom(r.Context())
	if err := h.deps.Identity.RBAC.Revoke(r.Context(), accountID, roleID); err != nil {
		httpx.Fail(w, err)
		return
	}

	h.auditAdmin(r, actor.AccountID, "rbac.role_revoked", "account", accountID.String(),
		map[string]any{"role_id": roleID.String()})
	httpx.OK(w, map[string]any{"revoked": true})
}

func (h *Handler) auditTargets(w http.ResponseWriter, r *http.Request) (uuid.UUID, uuid.UUID, error) {
	var req grantRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	accountID, err := uuid.Parse(req.AccountID)
	if err != nil {
		return uuid.Nil, uuid.Nil, apperr.New(apperr.CodeInvalidArgument, "账号 ID 非法")
	}
	roleID, err := uuid.Parse(req.RoleID)
	if err != nil {
		return uuid.Nil, uuid.Nil, apperr.New(apperr.CodeInvalidArgument, "角色 ID 非法")
	}
	return accountID, roleID, nil
}

// ListPermissions 列出全部权限点。
func (h *Handler) ListPermissions(w http.ResponseWriter, r *http.Request) {
	perms, err := h.deps.Identity.RBAC.ListPermissions(r.Context())
	if err != nil {
		httpx.Fail(w, err)
		return
	}

	out := make([]map[string]any, 0, len(perms))
	for _, p := range perms {
		out = append(out, map[string]any{"code": p.Code, "description": p.Description})
	}
	httpx.OK(w, map[string]any{"permissions": out})
}

// ---------------------------------------------------------------- 审计

// SearchAudit 检索审计事件。
func (h *Handler) SearchAudit(w http.ResponseWriter, r *http.Request) {
	filter, err := auditFilter(r)
	if err != nil {
		httpx.Fail(w, err)
		return
	}

	entries, total, err := h.deps.Identity.Audit.Search(r.Context(), filter)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.OK(w, map[string]any{"events": entries, "total": total})
}

// ExportAudit 导出审计事件为 CSV。
func (h *Handler) ExportAudit(w http.ResponseWriter, r *http.Request) {
	filter, err := auditFilter(r)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	filter.Limit = int32(maxOr(limitParam(r, 10000), 100000))

	entries, _, err := h.deps.Identity.Audit.Search(r.Context(), filter)
	if err != nil {
		httpx.Fail(w, err)
		return
	}

	filename := "audit-" + time.Now().UTC().Format("20060102-150405") + ".csv"
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	w.WriteHeader(http.StatusOK)

	if err := h.deps.Identity.Audit.WriteCSV(w, entries); err != nil {
		// 响应头已发出,只能记日志
		return
	}
}

// ---------------------------------------------------------------- 邀请码

type createInvitationRequest struct {
	Code    string `json:"code"`
	Email   string `json:"email"`
	MaxUses int    `json:"max_uses"`
	Days    int    `json:"days"`
}

// ListInvitations 列出邀请码。
func (h *Handler) ListInvitations(w http.ResponseWriter, r *http.Request) {
	if h.deps.DB == nil {
		httpx.Fail(w, apperr.New(apperr.CodeUnavailable, "邀请码功能未启用"))
		return
	}
	rows, err := h.deps.DB.Query(r.Context(), invitationListSQL,
		limitParam(r, 50), intParam(r, "offset", 0))
	if err != nil {
		httpx.Fail(w, apperr.Newf(apperr.CodeInternal, "查询邀请码失败: %v", err))
		return
	}
	defer rows.Close()

	items := make([]map[string]any, 0)
	for rows.Next() {
		var (
			id, code  string
			email     *string
			maxUses   int32
			usedCount int32
			createdAt time.Time
			expiresAt time.Time
			revokedAt *time.Time
			createdBy *string
		)
		if err := rows.Scan(&id, &code, &email, &maxUses, &usedCount,
			&createdAt, &expiresAt, &revokedAt, &createdBy); err != nil {
			httpx.Fail(w, apperr.Newf(apperr.CodeInternal, "读取邀请码失败: %v", err))
			return
		}
		items = append(items, map[string]any{
			"id": id, "code": code, "email": email,
			"max_uses": maxUses, "used_count": usedCount,
			"created_at": createdAt.UTC().Format(time.RFC3339),
			"expires_at": expiresAt.UTC().Format(time.RFC3339),
			"revoked_at": timePtr(revokedAt),
			"created_by": createdBy,
		})
	}

	httpx.OK(w, map[string]any{"invitations": items})
}

// CreateInvitation 创建邀请码。
func (h *Handler) CreateInvitation(w http.ResponseWriter, r *http.Request) {
	var req createInvitationRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, err)
		return
	}

	code := strings.TrimSpace(req.Code)
	if code == "" {
		generated, err := randomCode()
		if err != nil {
			httpx.Fail(w, err)
			return
		}
		code = generated
	}
	if req.MaxUses <= 0 {
		req.MaxUses = 1
	}
	if req.Days <= 0 {
		req.Days = 7
	}

	actor := httpx.PrincipalFrom(r.Context())
	row := h.deps.DB.QueryRow(r.Context(), createInvitationSQL,
		code, nullableText(req.Email), int32(req.MaxUses),
		time.Now().UTC().AddDate(0, 0, req.Days), actor.AccountID)

	var (
		outCode           string
		id                string
		email             *string
		maxUses, used     int32
		createdAt, expiry time.Time
	)
	if err := row.Scan(&id, &outCode, &email, &maxUses, &used, &createdAt, &expiry); err != nil {
		httpx.Fail(w, apperr.Newf(apperr.CodeInternal, "创建邀请码失败: %v", err))
		return
	}

	h.auditAdmin(r, actor.AccountID, "invitation.created", "invitation", id, nil)

	httpx.Created(w, map[string]any{
		"id": id, "code": outCode, "email": email,
		"max_uses": maxUses, "used_count": used,
		"created_at": createdAt.UTC().Format(time.RFC3339),
		"expires_at": expiry.UTC().Format(time.RFC3339),
	})
}

// ---------------------------------------------------------------- 设置

// GetSettings 返回应用配置。
func (h *Handler) GetSettings(w http.ResponseWriter, r *http.Request) {
	if h.deps.Settings == nil {
		httpx.Fail(w, apperr.New(apperr.CodeUnavailable, "配置存储未装配"))
		return
	}
	list, err := h.deps.Settings.List(r.Context())
	if err != nil {
		httpx.Fail(w, err)
		return
	}

	values := map[string]json.RawMessage{}
	for _, s := range list {
		values[s.Key] = s.Value
	}
	httpx.OK(w, map[string]any{"settings": values})
}

type updateSettingsRequest map[string]json.RawMessage

// UpdateSetting 修改应用配置。
func (h *Handler) UpdateSetting(w http.ResponseWriter, r *http.Request) {
	if h.deps.Settings == nil {
		httpx.Fail(w, apperr.New(apperr.CodeUnavailable, "配置存储未装配"))
		return
	}

	var req updateSettingsRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, err)
		return
	}

	actor := httpx.PrincipalFrom(r.Context())
	updated := make(map[string]json.RawMessage, len(req))
	for key, value := range req {
		s, err := h.deps.Settings.Upsert(r.Context(), key, value, &actor.AccountID)
		if err != nil {
			httpx.Fail(w, err)
			return
		}
		updated[s.Key] = s.Value
		h.auditAdmin(r, actor.AccountID, "setting.updated", "setting", key,
			map[string]any{"value": string(value)})
	}

	httpx.OK(w, map[string]any{"settings": updated})
}

// ---------------------------------------------------------------- 辅助

func (h *Handler) auditAdmin(r *http.Request, actorID uuid.UUID, action, targetType, targetID string, meta map[string]any) {
	ev := account.Event{
		AccountID:  &actorID,
		Actor:      domain.AuditActorAdmin(actorID),
		Action:     action,
		TargetType: targetType,
		TargetID:   targetID,
		Outcome:    domain.OutcomeSuccess,
		IP:         httpx.ClientIP(r, false),
		UserAgent:  r.UserAgent(),
		Metadata:   meta,
	}
	// 审计失败只记日志,不阻断管理操作
	if err := h.deps.Identity.Audit.Record(r.Context(), ev); err != nil {
		return
	}
}

func accountView(a domain.Account) map[string]any {
	return map[string]any{
		"id":             a.ID.String(),
		"username":       a.Username,
		"email":          a.Email,
		"status":         a.Status.String(),
		"email_verified": a.EmailVerified,
		"login_enabled":  a.MCLoginEnabled,
		"created_at":     a.CreatedAt.UTC().Format(time.RFC3339),
		"updated_at":     a.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

func roleView(role rbac.Role) map[string]any {
	return map[string]any{
		"id":          role.ID.String(),
		"code":        role.Code,
		"name":        role.Name,
		"description": role.Description,
		"is_system":   role.IsSystem,
	}
}

func auditFilter(r *http.Request) (audit.Filter, error) {
	q := r.URL.Query()
	filter := audit.Filter{
		Action:     q.Get("action"),
		Outcome:    q.Get("outcome"),
		TargetType: q.Get("target_type"),
		TargetID:   q.Get("target_id"),
		Limit:      int32(limitParam(r, 50)),
		Offset:     intParam(r, "offset", 0),
	}

	if v := q.Get("account_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			return filter, apperr.New(apperr.CodeInvalidArgument, "账号 ID 非法")
		}
		filter.AccountID = &id
	}
	if v := q.Get("from"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return filter, apperr.New(apperr.CodeInvalidArgument, "起始时间格式非法")
		}
		filter.From = &t
	}
	if v := q.Get("to"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return filter, apperr.New(apperr.CodeInvalidArgument, "结束时间格式非法")
		}
		filter.To = &t
	}
	return filter, nil
}

func intParam(r *http.Request, name string, fallback int) int32 {
	v := r.URL.Query().Get(name)
	if v == "" {
		return int32(fallback)
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return int32(fallback)
	}
	return int32(n)
}

func limitParam(r *http.Request, fallback int) int {
	return int(intParam(r, "limit", fallback))
}

func maxOr(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func nullableText(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func timePtr(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC().Format(time.RFC3339)
}

// 邀请码直接走两条手写 SQL:它们只被后台用一次,
// 单独为它们生成 sqlc 代码并不划算。
const invitationListSQL = `
SELECT i.id, i.code, i.email, i.max_uses, i.used_count,
       i.created_at, i.expires_at, i.revoked_at, a.username
FROM identity.invitation i
LEFT JOIN identity.account a ON a.id = i.created_by
ORDER BY i.created_at DESC
LIMIT $1 OFFSET $2`

const createInvitationSQL = `
INSERT INTO identity.invitation (code, email, max_uses, expires_at, created_by)
VALUES ($1, $2, $3, $4, $5)
RETURNING id, code, email, max_uses, used_count, created_at, expires_at`

// randomCode 生成一个可读的邀请码。
//
// 用 Crockford 风格字母表:去掉 I/L/O/U,避免人工抄写时与 1/0 混淆。
func randomCode() (string, error) {
	const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	buf, err := rand.Bytes(8)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for i, v := range buf {
		if i > 0 && i%4 == 0 {
			b.WriteByte('-')
		}
		b.WriteByte(alphabet[int(v)%len(alphabet)])
	}
	return b.String(), nil
}

// Package admin 提供管理后台的 HTTP 接口。
//
// 它是账号内核的**门面**:账号、角色权限、审计这些能力都由 identity 提供,
// 这里只做参数解析、权限点校验与响应组装,不重复实现业务规则
// (docs/architecture.md 第二节)。
package admin

import (
	"context"
	"encoding/json"
	"fmt"
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
	"github.com/yggauth/yggauth/internal/oidc"
	"github.com/yggauth/yggauth/internal/platform/apperr"

	"github.com/yggauth/yggauth/internal/platform/db"
	"github.com/yggauth/yggauth/internal/platform/db/query"
	"github.com/yggauth/yggauth/internal/platform/httpx"
	"github.com/yggauth/yggauth/internal/platform/keys"
	"github.com/yggauth/yggauth/internal/platform/mailer"
)

// 权限点常量。
//
// 与迁移里的 seed 数据一一对应。写错一个字符串就等于把接口敞开或锁死,
// 所以集中在这里声明,而不是散落在路由里。
const (
	PermAccountRead     = "account:read"
	PermAccountWrite    = "account:write"
	PermRBACRead        = "rbac:read"
	PermRBACWrite       = "rbac:write"
	PermAuditRead       = "audit:read"
	PermAuditExport     = "audit:export"
	PermSettingRead     = "setting:read"
	PermSettingWrite    = "setting:write"
	PermOIDCClientRead  = "oidc:client:read"
	PermOIDCClientWrite = "oidc:client:write"
	PermMCProfileRead   = "minecraft:profile:read"
	PermMCProfileWrite  = "minecraft:profile:write"
	PermMCTextureRead   = "minecraft:texture:read"
	PermMCTextureWrite  = "minecraft:texture:write"
	PermServerRead      = "minecraft:server:read"
	PermServerWrite     = "minecraft:server:write"
)

// MailReloader 是发件器的热替换入口(docs/configuration.md §7.3)。
//
// 后台只管「配置变了,重建一下」;怎么从设置现值构造发件器由装配处负责。
type MailReloader interface {
	Reload(cfg mailer.Config) error
}

// Deps 是后台接口的依赖。
type Deps struct {
	Identity *identity.Service
	// Settings 是运行时配置快照(带登记表校验与敏感值掩码),
	// 不是裸的 SettingStore —— 裸库表接受任意键任意值。
	Settings *config.Snapshot
	// Mailer 让 `mail.*` 的修改**立刻**生效:只落库不重建的话,
	// 页面显示「保存成功」而发信仍旧按老配置走。
	Mailer MailReloader
	// OIDC 是可选依赖:未装配授权服务时后台的客户端页返回 501
	OIDC *oidc.ClientService
	// OIDCKeys 用于密钥轮换
	OIDCKeys *keys.Manager
	DB       *db.Pool
	// Logger 记录内部错误细节。客户端只拿到通用错误码,
	// 服务端日志是排障时唯一的线索。
	Logger Logger
	// PublicBaseURL 用于拼邀请链接
	PublicBaseURL string
}

// Handler 是管理后台的 HTTP 处理器。
type Handler struct {
	deps   Deps
	logger Logger
}

// NewHandler 创建后台处理器。
func NewHandler(deps Deps) *Handler { return &Handler{deps: deps, logger: deps.Logger} }

// Mount 把后台路由挂到 /api/admin。
func (h *Handler) Mount(r chi.Router) {
	r.Route("/admin", func(r chi.Router) {
		// 后台接口一律要求登录:权限点求值需要一个已认证主体
		r.Use(httpx.RequireAuth)
		r.Get("/me", h.Me)
		r.Get("/menus", h.Menus)
		r.Get("/dashboard", h.Dashboard)

		r.With(httpx.RequirePermission(PermAccountRead)).Get("/accounts", h.ListAccounts)
		r.With(httpx.RequirePermission(PermAccountRead)).Get("/accounts/{accountID}/roles", h.ListAccountRoles)
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
		r.With(httpx.RequirePermission(PermRBACWrite)).Post("/invitations/{invitationID}/revoke", h.RevokeInvitation)

		r.With(httpx.RequirePermission(PermSettingRead)).Get("/settings", h.GetSettings)
		r.With(httpx.RequirePermission(PermOIDCClientRead)).Get("/clients", h.ListOIDCClients)
		r.With(httpx.RequirePermission(PermOIDCClientWrite)).Post("/clients", h.CreateOIDCClient)
		r.With(httpx.RequirePermission(PermOIDCClientWrite)).Post("/clients/{clientID}/rotate-secret", h.RotateOIDCClientSecret)
		r.With(httpx.RequirePermission(PermOIDCClientWrite)).Delete("/clients/{clientID}", h.DeleteOIDCClient)
		r.With(httpx.RequirePermission(PermOIDCClientRead)).Get("/signing-keys", h.ListSigningKeys)
		r.With(httpx.RequirePermission(PermOIDCClientWrite)).Post("/signing-keys/rotate", h.RotateSigningKey)
		r.With(httpx.RequirePermission(PermSettingWrite)).Patch("/settings", h.UpdateSetting)
		// 发测试信用的是**已保存的生效配置**:先保存再测,
		// 否则会出现「测的是草稿、生效的是另一套」这种最费时间的组合。
		r.With(httpx.RequirePermission(PermSettingWrite)).Post("/settings/mail/test", h.TestMail)

		// Minecraft 侧的后台只读列表。菜单里早就有这两项
		// (见 allMenus),此前没挂路由 —— 点进去只能看到占位页。
		r.With(httpx.RequirePermission(PermMCProfileRead)).Get("/mc/profiles", h.ListMCProfiles)
		r.With(httpx.RequirePermission(PermMCTextureRead)).Get("/mc/textures", h.ListMCTextures)
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
//
// Group 只用于界面分组。九项平铺时,「OIDC 客户端」「材质库」这类
// 专有名词夹在「账号管理」中间,新管理员看不出哪些跟自己有关 ——
// 分组标题比给每一项加解释更省地方。
type menuItem struct {
	Key        string     `json:"key"`
	Path       string     `json:"path"`
	Title      string     `json:"title"`
	Icon       string     `json:"icon"`
	Permission string     `json:"permission"`
	Group      string     `json:"group,omitempty"`
	Children   []menuItem `json:"children,omitempty"`
}

// allMenus 是完整菜单定义。
//
// 后端下发菜单而不是前端硬编码,是为了让「加一个后台模块」
// 不必改前端 —— 菜单的可见性由权限点决定。
//
// 顺序即界面顺序。仪表盘不分组(空 Group)由前端置顶,
// 其余按 Group 聚成若干块。
var allMenus = []menuItem{
	{Key: "dashboard", Path: "/dashboard", Title: "仪表盘", Icon: "dashboard"},

	{Key: "accounts", Path: "/accounts", Title: "账号管理", Icon: "user", Group: "账号与权限", Permission: PermAccountRead},
	{Key: "roles", Path: "/roles", Title: "角色权限", Icon: "safety", Group: "账号与权限", Permission: PermRBACRead},
	{Key: "invitations", Path: "/invitations", Title: "邀请管理", Icon: "mail", Group: "账号与权限", Permission: PermRBACWrite},

	{Key: "audit", Path: "/audit", Title: "审计日志", Icon: "file-search", Group: "安全与审计", Permission: PermAuditRead},

	{Key: "clients", Path: "/clients", Title: "OIDC 客户端", Icon: "api", Group: "应用接入", Permission: PermOIDCClientRead},
	{Key: "keys", Path: "/keys", Title: "签名密钥", Icon: "key", Group: "应用接入", Permission: PermOIDCClientRead},

	{Key: "mc_profiles", Path: "/mc/profiles", Title: "玩家档案", Icon: "idcard", Group: "Minecraft", Permission: "minecraft:profile:read"},
	{Key: "mc_textures", Path: "/mc/textures", Title: "材质库", Icon: "picture", Group: "Minecraft", Permission: "minecraft:texture:read"},

	{Key: "settings", Path: "/settings", Title: "应用配置", Icon: "setting", Group: "系统", Permission: PermSettingRead},
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
//
// 每个数字都会直接显示在首页卡片上,所以它必须真的是一次统计。
// 之前的 accounts_recent 取的是 List(Limit: 1) 的返回条数 ——
// 那其实是「有没有账号」,恒为 0 或 1,和「最近注册」没有任何关系。
func (h *Handler) Dashboard(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	_, total, err := h.deps.Identity.Accounts.List(ctx, account.ListFilter{Limit: 1})
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

	// 计数走轻量查询,不做全表扫描。
	// 失败就保持 0:首页少一个数字,比整页报错好。
	var recentAccounts, auditCount, oidcClients, mcServers int64
	if h.deps.DB != nil {
		_ = h.deps.DB.QueryRow(ctx,
			`SELECT count(*) FROM identity.account WHERE created_at > now() - interval '7 days'`,
		).Scan(&recentAccounts)
		_ = h.deps.DB.QueryRow(ctx,
			`SELECT count(*) FROM identity.audit_event WHERE occurred_at > now() - interval '24 hours'`,
		).Scan(&auditCount)
		_ = h.deps.DB.QueryRow(ctx, `SELECT count(*) FROM oidc.client`).Scan(&oidcClients)
		_ = h.deps.DB.QueryRow(ctx, `SELECT count(*) FROM minecraft.server`).Scan(&mcServers)
	}

	httpx.OK(w, map[string]any{
		"accounts_total":     total,
		"accounts_recent":    recentAccounts,
		"roles_total":        len(roles),
		"permissions_total":  len(perms),
		"audit_events_24h":   auditCount,
		"oidc_clients_total": oidcClients,
		"mc_servers_total":   mcServers,
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

// ListAccountRoles 返回某个账号当前拥有的角色。
//
// 授予与撤销走 /roles/grant、/roles/revoke,但「他现在到底有什么角色」
// 也得能看 —— 没有这个端点,详情抽屉只能显示一个空列表,
// 管理员无从判断刚才那下授予是否生效。
//
// 读权限用 account:read 而不是 rbac:read:入口是账号管理页,
// 那里按 account:read 过滤。让只有账号读权限的管理员打开抽屉
// 就撞一个 403,比看不到角色更让人困惑。
func (h *Handler) ListAccountRoles(w http.ResponseWriter, r *http.Request) {
	accountID, err := uuid.Parse(chi.URLParam(r, "accountID"))
	if err != nil {
		httpx.Fail(w, apperr.New(apperr.CodeInvalidArgument, "账号 ID 非法"))
		return
	}

	roles, err := h.deps.Identity.RBAC.ListAccountRoles(r.Context(), accountID)
	if err != nil {
		httpx.Fail(w, err)
		return
	}

	out := make([]map[string]any, 0, len(roles))
	for _, role := range roles {
		out = append(out, map[string]any{
			"id": role.ID.String(), "code": role.Code, "name": role.Name,
		})
	}
	httpx.OK(w, map[string]any{"roles": out})
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

// auditEventDTO 是审计事件的对外形态。
//
// 在 Entry 之外补两个解析出来的名字:Actor 存的是 "account:<uuid>"
// 这类复合串,原样甩到界面上就是两行 UUID —— 占地方、换行难看,
// 而看日志的人真正想知道的是「谁干的」。
type auditEventDTO struct {
	audit.Entry
	// ActorName 是主体对应的用户名。解析不出来时(如 system)为空,
	// 界面回落到显示原始的 Actor 串。
	ActorName string `json:"actor_name,omitempty"`
	// TargetName 是目标为账号时的用户名。
	TargetName string `json:"target_name,omitempty"`
}

// SearchAudit 检索审计事件。
//
// 过滤条件由 auditFilter 统一解析(分页上限、时间窗、动作与结果),
// 这里只负责查、解析主体名并拼回信封。
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

	names := h.resolveAuditNames(r.Context(), entries)

	events := make([]auditEventDTO, 0, len(entries))
	for _, e := range entries {
		dto := auditEventDTO{Entry: e}
		if id, ok := auditActorID(e.Actor); ok {
			dto.ActorName = names[id]
		}
		if e.TargetType == "account" {
			if id, err := uuid.Parse(e.TargetID); err == nil {
				dto.TargetName = names[id]
			}
		}
		events = append(events, dto)
	}

	httpx.OK(w, map[string]any{"events": events, "total": total})
}

// auditActorID 从 "account:<uuid>" / "admin:<uuid>" 里取出账号 ID。
// "system" 或格式不符时返回 false —— 这类主体本来就没有用户名可显示。
func auditActorID(actor string) (uuid.UUID, bool) {
	_, rest, ok := strings.Cut(actor, ":")
	if !ok {
		return uuid.Nil, false
	}
	id, err := uuid.Parse(rest)
	if err != nil {
		return uuid.Nil, false
	}
	return id, true
}

// resolveAuditNames 批量取一页事件涉及到的用户名。
//
// 一次查询解决一屏(默认 50 条),而不是每条事件查一次 ——
// 后者会把一个列表页变成 50 次数据库往返。
//
// 查询失败时返回空表而不是报错:界面回落到显示原始 Actor 串,
// 审计内容本身仍然是完整可用的,不该因为「名字查不到」就整页失败。
func (h *Handler) resolveAuditNames(ctx context.Context, entries []audit.Entry) map[uuid.UUID]string {
	names := map[uuid.UUID]string{}
	if h.deps.DB == nil {
		return names
	}

	seen := make(map[uuid.UUID]struct{}, len(entries)*2)
	ids := make([]uuid.UUID, 0, len(entries)*2)
	add := func(id uuid.UUID) {
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}

	for _, e := range entries {
		if id, ok := auditActorID(e.Actor); ok {
			add(id)
		}
		if e.TargetType == "account" {
			if id, err := uuid.Parse(e.TargetID); err == nil {
				add(id)
			}
		}
	}
	if len(ids) == 0 {
		return names
	}

	rows, err := h.deps.DB.Query(ctx,
		`SELECT id, username FROM identity.account WHERE id = ANY($1::uuid[])`, ids)
	if err != nil {
		return names
	}
	defer rows.Close()

	for rows.Next() {
		var id uuid.UUID
		var name string
		if rows.Scan(&id, &name) == nil {
			names[id] = name
		}
	}
	return names
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

// invitationExistsSQL 判断邀请码是否存在。
const invitationExistsSQL = `SELECT EXISTS(SELECT 1 FROM identity.invitation WHERE id = $1)`

// RevokeInvitation 撤销邀请码。
//
// 撤销是幂等的:已经撤销过的再撤一次同样返回成功,前端重试不该收到
// 一个「状态其实没变」的错误。撤销后该码立即不可再被用于注册
// (核销 SQL 的 WHERE 里带 revoked_at IS NULL)。
// 审计只在真的改了状态那一次写,重复调用不留噪声记录。
func (h *Handler) RevokeInvitation(w http.ResponseWriter, r *http.Request) {
	if h.deps.DB == nil {
		httpx.Fail(w, apperr.New(apperr.CodeUnavailable, "邀请码功能未启用"))
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "invitationID"))
	if err != nil {
		httpx.Fail(w, apperr.New(apperr.CodeInvalidArgument, "邀请码 ID 非法"))
		return
	}

	n, err := query.New(h.deps.DB).RevokeInvitation(r.Context(), id)
	if err != nil {
		httpx.Fail(w, apperr.Newf(apperr.CodeInternal, "撤销邀请码失败: %v", err))
		return
	}
	if n == 0 {
		// 撤销 SQL 带 revoked_at IS NULL 条件,n==0 既可能是「已撤销」,
		// 也可能是「根本不存在」,再查一次才分得清。
		var exists bool
		if err := h.deps.DB.QueryRow(r.Context(), invitationExistsSQL, id).Scan(&exists); err != nil {
			httpx.Fail(w, apperr.Newf(apperr.CodeInternal, "查询邀请码失败: %v", err))
			return
		}
		if !exists {
			httpx.Fail(w, apperr.New(apperr.CodeNotFound, "邀请码不存在"))
			return
		}
		// 已撤销过:幂等成功返回,不补写审计。
		httpx.OK(w, map[string]any{"revoked": true})
		return
	}

	actor := httpx.PrincipalFrom(r.Context())
	h.auditAdmin(r, actor.AccountID, "invitation.revoked", "invitation", id.String(), nil)

	httpx.OK(w, map[string]any{"revoked": true})
}

// ---------------------------------------------------------------- 设置

// GetSettings 返回应用配置与键定义(schema)。
//
// schema 一并下发是刻意的:前端 `SettingsView.vue` 原先手抄了一张 META 表,
// 后端加一个键前端不改就显示不出来。规则只有一份真源(登记表),
// 前端只负责渲染。
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
	httpx.OK(w, map[string]any{"settings": values, "schema": config.Schema()})
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
	if len(req) == 0 {
		httpx.Fail(w, apperr.New(apperr.CodeInvalidArgument, "没有要修改的配置").
			WithMessage("没有要修改的配置"))
		return
	}
	// 先整体校验再写:边写边校验会留下「改了一半」的配置。
	//
	// 校验说明**原样对外**:admin 是知道这条规则的人,只回一句
	// 「参数校验失败」等于让他去猜是哪个键、为什么不行 ——
	// 而注册码 20007 那类对普通用户的兜底文案,对后台并不适用。
	if err := h.deps.Settings.ValidateUpdate(r.Context(), req); err != nil {
		httpx.Fail(w, apperr.New(apperr.CodeInvalidArgument, err.Error()).WithMessage(err.Error()))
		return
	}

	actor := httpx.PrincipalFrom(r.Context())
	updated := make(map[string]json.RawMessage, len(req))
	for key, value := range req {
		// 回显掩码 = 「原样保留」:不写库、不审计 ——
		// 否则每次改别的键都会顺手把 SMTP 密码刷成一串星号。
		// 响应回显掩码本身:值没变,也不用把密文发给浏览器。
		if spec, ok := config.Lookup(key); ok && spec.IsSecret() && string(value) == secretJSON(config.MaskValue) {
			updated[key] = value
			continue
		}

		s, err := h.deps.Settings.Upsert(r.Context(), key, value, &actor.AccountID)
		if err != nil {
			httpx.Fail(w, err)
			return
		}
		updated[s.Key] = s.Value
		h.auditAdmin(r, actor.AccountID, "setting.updated", "setting", key,
			// 敏感键在这里被换成掩码:审计表会被导出、会被后台展示,
			// 明文进去了就等于多了一条泄露路径。
			map[string]any{"value": config.AuditValue(key, value)})
	}

	// mail.* 里影响发件器的键变了 → 就地重建。
	//
	// 只落库不重建 = 「保存成功」但发信还是老配置,
	// 又一次「改了没用」,而且这次连个报错都没有。
	if err := h.reloadMailer(r, req); err != nil {
		httpx.Fail(w, err)
		return
	}

	httpx.OK(w, map[string]any{"settings": updated})
}

// reloadMailer 在本轮改动涉及发件器时用**设置里的现值**重建发件器。
//
// 顺序是「先校验、再落库、最后重建」:校验(ValidateUpdate)把明显的坏值
// 挡在写入之前,所以重建这一步理论上不会失败;真失败了就保留旧配置
// 并把话说明白 —— 发信至少还在按老配置工作。
func (h *Handler) reloadMailer(r *http.Request, req map[string]json.RawMessage) error {
	affects := false
	for key := range req {
		if spec, ok := config.Lookup(key); ok && spec.AffectsSender {
			affects = true
			break
		}
	}
	if !affects {
		return nil
	}
	if h.deps.Mailer == nil {
		return apperr.New(apperr.CodeUnavailable, "发件器未装配,邮件设置无法立即生效")
	}

	cfg, err := h.deps.Settings.MailerConfig(r.Context())
	if err != nil {
		// 解不开 mail.password 这类错误必须报出来:
		// 悄悄按空密码发信,只会得到一句「认证失败」。
		return apperr.New(apperr.CodeInvalidArgument, err.Error()).
			WithMessage("读取邮件配置失败:SMTP 密码解不开(检查主密钥是否换过)")
	}
	if err := h.deps.Mailer.Reload(cfg); err != nil {
		if h.logger != nil {
			h.logger.Error("mail: 重建发件器失败,已保留原配置", "error", err)
		}
		return apperr.New(apperr.CodeInternal, err.Error()).
			WithMessage("邮件设置已保存,但按新配置重建发件器失败:已保留原配置继续发信")
	}
	return nil
}

// TestMail 用**已保存的生效配置**发一封测试信(docs/configuration.md §7.4)。
//
// 没有它,「配了发件邮箱但不知道对不对」的唯一验证方式是等下一个注册用户 ——
// 错误精确到阶段(connect / tls / auth / send),管理员才知道该改什么:
// auth 是账号密码,tls 多半是端口(465 要勾隐式 TLS),connect 是地址。
func (h *Handler) TestMail(w http.ResponseWriter, r *http.Request) {
	if h.deps.Settings == nil {
		httpx.Fail(w, apperr.New(apperr.CodeUnavailable, "配置存储未装配"))
		return
	}

	var req struct {
		To string `json:"to"`
	}
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, err)
		return
	}
	req.To = strings.TrimSpace(req.To)
	if req.To == "" || !strings.Contains(req.To, "@") {
		httpx.Fail(w, apperr.New(apperr.CodeInvalidArgument, "收件邮箱不合法: "+req.To).
			WithMessage("收件邮箱不合法"))
		return
	}

	cfg, err := h.deps.Settings.MailerConfig(r.Context())
	if err != nil {
		if h.logger != nil {
			h.logger.Error("mail: 读取邮件配置失败", "error", err)
		}
		// 细节只进日志:这里多半是 mail.password 解不开(主密钥换过),
		// 对外说清「读不出来」即可,别把内部结构带出去。
		httpx.Fail(w, apperr.New(apperr.CodeUnavailable, err.Error()).
			WithMessage("读取邮件配置失败:SMTP 密码解不开(检查主密钥是否换过)"))
		return
	}

	// 网络操作给独立超时:管理员点了「发送」却卡在浏览器上,
	// 比明确告诉他失败更糟。
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	res := mailer.SendTest(ctx, cfg, req.To)

	actor := httpx.PrincipalFrom(r.Context())
	h.auditAdmin(r, actor.AccountID, "mail.test", "setting", "mail", map[string]any{
		"to":    req.To,
		"ok":    res.OK,
		"stage": string(res.Stage),
	})
	httpx.OK(w, res)
}

// secretJSON 把字符串包成 JSON 字面量。
func secretJSON(v string) string {
	b, err := json.Marshal(v)
	if err != nil {
		return `""`
	}
	return string(b)
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

// Logger 是后台用到的日志器最小接口。
type Logger interface {
	Warn(msg string, args ...any)
	Error(msg string, args ...any)
}

// failInternal 记录内部错误并向客户端返回统一错误包。
//
// 统一错误包对客户端只有「服务器内部错误」一句话,没有它的话
// 一次 500 就是彻底的黑盒。内部细节只进日志,不进响应 ——
// 那会泄露表名、SQL 片段,甚至密钥长度。
func (h *Handler) failInternal(w http.ResponseWriter, msg string, err error) {
	if h.logger != nil {
		h.logger.Error(msg, "error", fmt.Sprintf("%+v", err))
	}
	httpx.Fail(w, apperr.New(apperr.CodeInternal, msg))
}

// fail 记录错误并按原始业务码返回。
//
// 业务错误(参数不合法、冲突、未找到)原样透传 —— 前端靠业务码区分
// 「改一下输入」和「稍后重试」,一律吞成 500 会让用户无从下手。
// 非业务错误才退化成服务器内部错误。
func (h *Handler) fail(w http.ResponseWriter, msg string, err error) {
	if h.logger != nil {
		h.logger.Warn(msg, "error", fmt.Sprintf("%+v", err))
	}
	if apperr.From(err).Code != apperr.CodeInternal {
		httpx.Fail(w, err)
		return
	}
	h.failInternal(w, msg, err)
}

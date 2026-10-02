// Package rbac 提供统一的角色与权限模型(ADR-005)。
//
// 单一命名空间、单一权限点清单。域管理员与平台管理员的区别不在于模型,
// 而在于角色绑定了哪些权限点。
package rbac

import (
	"context"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/yggauth/yggauth/internal/domain"
	"github.com/yggauth/yggauth/internal/platform/apperr"
	"github.com/yggauth/yggauth/internal/platform/db"
	"github.com/yggauth/yggauth/internal/platform/db/query"
)

// Permission 是权限点。
type Permission = domain.Permission

// Role 是角色。
type Role = domain.Role

// Repository 是 RBAC 的数据访问接口。
type Repository interface {
	ListPermissions(ctx context.Context) ([]Permission, error)
	ListRoles(ctx context.Context, search string) ([]Role, error)
	GetRoleByID(ctx context.Context, id uuid.UUID) (Role, error)
	GetRoleByCode(ctx context.Context, code string) (Role, error)
	CreateRole(ctx context.Context, code, name, description string, isSystem bool) (Role, error)
	UpdateRole(ctx context.Context, id uuid.UUID, name, description string) (Role, error)
	DeleteRole(ctx context.Context, id uuid.UUID) error
	SetRolePermissions(ctx context.Context, roleID uuid.UUID, permissions []string) error
	ListRolePermissions(ctx context.Context, roleID uuid.UUID) ([]Permission, error)
	Grant(ctx context.Context, accountID, roleID, grantedBy uuid.UUID) error
	Revoke(ctx context.Context, accountID, roleID uuid.UUID) error
	ListAccountRoles(ctx context.Context, accountID uuid.UUID) ([]Role, error)
	ListAccountPermissions(ctx context.Context, accountID uuid.UUID) ([]string, error)
	CountAccountsWithRole(ctx context.Context, roleID uuid.UUID) (int64, error)
}

// Service 是 RBAC 业务逻辑。
type Service struct {
	repo Repository
}

// NewService 创建 RBAC 服务。
func NewService(repo Repository) *Service { return &Service{repo: repo} }

// ListPermissions 列出全部权限点。
func (s *Service) ListPermissions(ctx context.Context) ([]Permission, error) {
	return s.repo.ListPermissions(ctx)
}

// ListRoles 列出角色。
func (s *Service) ListRoles(ctx context.Context, search string) ([]Role, error) {
	return s.repo.ListRoles(ctx, strings.TrimSpace(search))
}

// GetRole 取角色。
func (s *Service) GetRole(ctx context.Context, id uuid.UUID) (Role, error) {
	return s.repo.GetRoleByID(ctx, id)
}

// CreateRoleInput 是创建角色入参。
type CreateRoleInput struct {
	Code        string
	Name        string
	Description string
	// Permissions 是初始权限点列表
	Permissions []string
}

// CreateRole 创建角色。
func (s *Service) CreateRole(ctx context.Context, in CreateRoleInput) (Role, error) {
	code := strings.TrimSpace(in.Code)
	if code == "" {
		return Role{}, apperr.New(apperr.CodeInvalidArgument, "角色编码不能为空")
	}
	// 角色编码会出现在日志与配置里,限制字符集避免注入与显示混乱
	if !isSafeCode(code) {
		return Role{}, apperr.New(apperr.CodeInvalidArgument, "角色编码只能包含小写字母、数字、下划线与连字符")
	}

	role, err := s.repo.CreateRole(ctx, code, strings.TrimSpace(in.Name), in.Description, false)
	if err != nil {
		if apperr.Is(err, apperr.CodeConflict) {
			return Role{}, apperr.New(apperr.CodeConflict, "角色编码已存在")
		}
		return Role{}, err
	}

	if err := s.SetPermissions(ctx, role.ID, in.Permissions); err != nil {
		return Role{}, err
	}
	return role, nil
}

// UpdateRole 修改角色。
func (s *Service) UpdateRole(ctx context.Context, id uuid.UUID, name, description string) (Role, error) {
	return s.repo.UpdateRole(ctx, id, strings.TrimSpace(name), description)
}

// DeleteRole 删除角色。
//
// 系统内置角色不可删 —— 它们是权限模型的骨架,删掉之后
// 「新账号默认有哪些权限」这个问题就没有答案了。
func (s *Service) DeleteRole(ctx context.Context, id uuid.UUID) error {
	role, err := s.repo.GetRoleByID(ctx, id)
	if err != nil {
		return err
	}
	if role.IsSystem {
		return apperr.New(apperr.CodeForbidden, "系统内置角色不可删除")
	}

	count, err := s.repo.CountAccountsWithRole(ctx, id)
	if err != nil {
		return err
	}
	if count > 0 {
		return apperr.Newf(apperr.CodeConflict, "仍有 %d 个账号持有该角色,先撤权再删除", count)
	}

	if err := s.repo.DeleteRole(ctx, id); err != nil {
		if apperr.Is(err, apperr.CodeNotFound) {
			return apperr.ErrNotFound
		}
		return err
	}
	return nil
}

// SetPermissions 整体替换角色权限点。
//
// 先校验权限点都存在再落库:把不存在的权限点写进 role_permission
// 会让权限求值出现「永远匹配不上」的幽灵条目。
func (s *Service) SetPermissions(ctx context.Context, roleID uuid.UUID, permissions []string) error {
	known, err := s.repo.ListPermissions(ctx)
	if err != nil {
		return err
	}
	valid := make(map[string]struct{}, len(known))
	for _, p := range known {
		valid[p.Code] = struct{}{}
	}

	for _, code := range permissions {
		if _, ok := valid[code]; !ok {
			return apperr.Newf(apperr.CodeInvalidArgument, "权限点不存在: %s", code)
		}
	}
	return s.repo.SetRolePermissions(ctx, roleID, permissions)
}

// ListRolePermissions 列出角色权限点。
func (s *Service) ListRolePermissions(ctx context.Context, roleID uuid.UUID) ([]Permission, error) {
	return s.repo.ListRolePermissions(ctx, roleID)
}

// Grant 授予角色。
func (s *Service) Grant(ctx context.Context, accountID, roleID, grantedBy uuid.UUID) error {
	// 角色必须存在,否则授权会变成一条永远查不出效果的记录
	if _, err := s.repo.GetRoleByID(ctx, roleID); err != nil {
		return err
	}
	return s.repo.Grant(ctx, accountID, roleID, grantedBy)
}

// Revoke 撤销角色。
func (s *Service) Revoke(ctx context.Context, accountID, roleID uuid.UUID) error {
	if err := s.repo.Revoke(ctx, accountID, roleID); err != nil {
		return apperr.Newf(apperr.CodeInternal, "撤销角色失败: %v", err)
	}
	return nil
}

// ListAccountRoles 列出账号持有的角色。
func (s *Service) ListAccountRoles(ctx context.Context, accountID uuid.UUID) ([]Role, error) {
	return s.repo.ListAccountRoles(ctx, accountID)
}

// PermissionsFor 返回账号的权限点集合,已按字典序排好。
//
// 排序是为了让缓存与断言稳定:同样的账号每次拿到同样的顺序。
func (s *Service) PermissionsFor(ctx context.Context, accountID uuid.UUID) ([]string, error) {
	codes, err := s.repo.ListAccountPermissions(ctx, accountID)
	if err != nil {
		return nil, err
	}
	sort.Strings(codes)
	return codes, nil
}

// Can 判断账号是否具备某权限点(含通配符)。
func (s *Service) Can(ctx context.Context, accountID uuid.UUID, permission string) (bool, error) {
	codes, err := s.PermissionsFor(ctx, accountID)
	if err != nil {
		return false, err
	}
	for _, granted := range codes {
		if domain.Can(granted, permission) {
			return true, err
		}
	}
	return false, nil
}

func isSafeCode(code string) bool {
	for _, r := range code {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_', r == '-':
		default:
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------- PG 实现

// PgRepository 是基于 sqlc 的 RBAC 仓储。
type PgRepository struct{ q *query.Queries }

// NewPgRepository 创建 RBAC 仓储。
func NewPgRepository(pool *db.Pool) *PgRepository { return &PgRepository{q: query.New(pool)} }

// ListPermissions 列出全部权限点。
func (r *PgRepository) ListPermissions(ctx context.Context) ([]Permission, error) {
	rows, err := r.q.ListPermissions(ctx)
	if err != nil {
		return nil, apperr.Newf(apperr.CodeInternal, "查询权限点失败: %v", err)
	}
	out := make([]Permission, 0, len(rows))
	for _, row := range rows {
		out = append(out, Permission{Code: row.Code, Description: row.Description})
	}
	return out, nil
}

// ListRoles 列出角色。
func (r *PgRepository) ListRoles(ctx context.Context, search string) ([]Role, error) {
	rows, err := r.q.ListRoles(ctx, nullable(search))
	if err != nil {
		return nil, apperr.Newf(apperr.CodeInternal, "查询角色失败: %v", err)
	}
	return toRoles(rows), nil
}

func toRoles(rows []query.IdentityRole) []Role {
	out := make([]Role, 0, len(rows))
	for _, row := range rows {
		out = append(out, Role{
			ID:          row.ID,
			Code:        row.Code,
			Name:        row.Name,
			Description: row.Description.String,
			IsSystem:    row.IsSystem,
			CreatedAt:   row.CreatedAt,
			UpdatedAt:   row.UpdatedAt,
		})
	}
	return out
}

// GetRoleByID 按主键取角色。
func (r *PgRepository) GetRoleByID(ctx context.Context, id uuid.UUID) (Role, error) {
	row, err := r.q.GetRoleByID(ctx, id)
	if err != nil {
		if db.IsNoRows(err) {
			return Role{}, apperr.ErrNotFound
		}
		return Role{}, apperr.Newf(apperr.CodeInternal, "查询角色失败: %v", err)
	}
	return Role{
		ID: row.ID, Code: row.Code, Name: row.Name,
		Description: row.Description.String, IsSystem: row.IsSystem,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}, nil
}

// GetRoleByCode 按编码取角色。
func (r *PgRepository) GetRoleByCode(ctx context.Context, code string) (Role, error) {
	row, err := r.q.GetRoleByCode(ctx, code)
	if err != nil {
		if db.IsNoRows(err) {
			return Role{}, apperr.ErrNotFound
		}
		return Role{}, apperr.Newf(apperr.CodeInternal, "查询角色失败: %v", err)
	}
	return Role{ID: row.ID, Code: row.Code, Name: row.Name, IsSystem: row.IsSystem}, nil
}

// CreateRole 创建角色。
func (r *PgRepository) CreateRole(ctx context.Context, code, name, description string, isSystem bool) (Role, error) {
	row, err := r.q.CreateRole(ctx, query.CreateRoleParams{
		Code:        code,
		Name:        name,
		Description: pgText(description),
		IsSystem:    isSystem,
	})
	if err != nil {
		if db.IsUniqueViolation(err) {
			return Role{}, apperr.New(apperr.CodeConflict, "角色编码已存在")
		}
		return Role{}, apperr.Newf(apperr.CodeInternal, "创建角色失败: %v", err)
	}
	return Role{
		ID: row.ID, Code: row.Code, Name: row.Name,
		Description: row.Description.String, IsSystem: row.IsSystem,
	}, nil
}

// UpdateRole 修改角色。
func (r *PgRepository) UpdateRole(ctx context.Context, id uuid.UUID, name, description string) (Role, error) {
	row, err := r.q.UpdateRole(ctx, query.UpdateRoleParams{
		ID:          id,
		Name:        name,
		Description: pgText(description),
	})
	if err != nil {
		if db.IsNoRows(err) {
			return Role{}, apperr.ErrNotFound
		}
		return Role{}, apperr.Newf(apperr.CodeInternal, "更新角色失败: %v", err)
	}
	return Role{ID: row.ID, Code: row.Code, Name: row.Name, IsSystem: row.IsSystem}, nil
}

// DeleteRole 删除角色。
func (r *PgRepository) DeleteRole(ctx context.Context, id uuid.UUID) error {
	n, err := r.q.DeleteRole(ctx, id)
	if err != nil {
		return apperr.Newf(apperr.CodeInternal, "删除角色失败: %v", err)
	}
	if n == 0 {
		return apperr.ErrNotFound
	}
	return nil
}

// SetRolePermissions 整体替换权限点。
func (r *PgRepository) SetRolePermissions(ctx context.Context, roleID uuid.UUID, permissions []string) error {
	if err := r.q.SetRolePermissions(ctx, roleID); err != nil {
		return apperr.Newf(apperr.CodeInternal, "清空角色权限失败: %v", err)
	}
	for _, code := range permissions {
		if err := r.q.AddRolePermission(ctx, query.AddRolePermissionParams{
			RoleID:     roleID,
			Permission: code,
		}); err != nil {
			return apperr.Newf(apperr.CodeInternal, "写入角色权限失败: %v", err)
		}
	}
	return nil
}

// ListRolePermissions 列出角色权限点。
func (r *PgRepository) ListRolePermissions(ctx context.Context, roleID uuid.UUID) ([]Permission, error) {
	rows, err := r.q.ListRolePermissions(ctx, roleID)
	if err != nil {
		return nil, apperr.Newf(apperr.CodeInternal, "查询角色权限失败: %v", err)
	}
	out := make([]Permission, 0, len(rows))
	for _, row := range rows {
		out = append(out, Permission{Code: row.Code, Description: row.Description})
	}
	return out, nil
}

// Grant 授予角色。
func (r *PgRepository) Grant(ctx context.Context, accountID, roleID, grantedBy uuid.UUID) error {
	if _, err := r.q.GrantRole(ctx, query.GrantRoleParams{
		AccountID: accountID,
		RoleID:    roleID,
		GrantedBy: pgUUID(grantedBy),
	}); err != nil {
		return apperr.Newf(apperr.CodeInternal, "授予角色失败: %v", err)
	}
	return nil
}

// Revoke 撤销角色。
func (r *PgRepository) Revoke(ctx context.Context, accountID, roleID uuid.UUID) error {
	_, err := r.q.RevokeRole(ctx, query.RevokeRoleParams{AccountID: accountID, RoleID: roleID})
	if err != nil {
		return apperr.Newf(apperr.CodeInternal, "撤销角色失败: %v", err)
	}
	return nil
}

// ListAccountRoles 列出账号角色。
func (r *PgRepository) ListAccountRoles(ctx context.Context, accountID uuid.UUID) ([]Role, error) {
	rows, err := r.q.ListAccountRoles(ctx, accountID)
	if err != nil {
		return nil, apperr.Newf(apperr.CodeInternal, "查询账号角色失败: %v", err)
	}
	return toRoles(rows), nil
}

// ListAccountPermissions 列出账号权限点。
func (r *PgRepository) ListAccountPermissions(ctx context.Context, accountID uuid.UUID) ([]string, error) {
	codes, err := r.q.ListAccountPermissions(ctx, accountID)
	if err != nil {
		return nil, apperr.Newf(apperr.CodeInternal, "查询账号权限失败: %v", err)
	}
	return codes, nil
}

// CountAccountsWithRole 统计持有该角色的账号数。
func (r *PgRepository) CountAccountsWithRole(ctx context.Context, roleID uuid.UUID) (int64, error) {
	n, err := r.q.CountAccountsWithRole(ctx, roleID)
	if err != nil {
		return 0, apperr.Newf(apperr.CodeInternal, "统计角色账号数失败: %v", err)
	}
	return n, nil
}

// pgText 把字符串转成 pgtype.Text。
func pgText(s string) pgtype.Text {
	return pgtype.Text{String: s, Valid: s != ""}
}

// pgUUID 把 UUID 转成 pgtype.UUID。
func pgUUID(id uuid.UUID) pgtype.UUID {
	return pgtype.UUID{Bytes: id, Valid: id != uuid.Nil}
}

// nullable 把空串转成 NULL。
//
// 关键:空串不能当成「匹配空模式」传下去 —— `code LIKE ”` 永远为假,
// 会让「不筛选」的请求返回空列表。
func nullable(s string) pgtype.Text {
	if s == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: "%" + s + "%", Valid: true}
}

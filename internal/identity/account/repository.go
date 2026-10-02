package account

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/yggauth/yggauth/internal/domain"
	"github.com/yggauth/yggauth/internal/platform/apperr"
	"github.com/yggauth/yggauth/internal/platform/db"
	"github.com/yggauth/yggauth/internal/platform/db/query"
)

// Credential 是账号的登录凭据。
type Credential struct {
	ID             uuid.UUID
	AccountID      uuid.UUID
	Algo           Algo
	Hash           string
	FailedAttempts int
	LockedUntil    *time.Time
	ChangedAt      time.Time
}

// Locked 判断凭据当前是否处于锁定状态。
func (c Credential) Locked(now time.Time) bool {
	return c.LockedUntil != nil && now.Before(*c.LockedUntil)
}

// Repository 是账号与凭据的数据访问接口。
//
// 显式声明接口而不是直接暴露 sqlc 的 *query.Queries:
// 业务层依赖接口,测试可以换成替身;sqlc 生成物则是实现细节。
type Repository interface {
	Create(ctx context.Context, in CreateInput) (domain.Account, error)
	GetByID(ctx context.Context, id uuid.UUID) (domain.Account, error)
	GetByEmail(ctx context.Context, email string) (domain.Account, error)
	GetByUsername(ctx context.Context, username string) (domain.Account, error)
	UpdateProfile(ctx context.Context, id uuid.UUID, username, email string) (domain.Account, error)
	UpdateStatus(ctx context.Context, id uuid.UUID, status domain.AccountStatus) (domain.Account, error)
	MarkEmailVerified(ctx context.Context, id uuid.UUID) (domain.Account, error)
	SetLoginEnabled(ctx context.Context, id uuid.UUID, enabled bool) (domain.Account, error)
	List(ctx context.Context, f ListFilter) ([]domain.Account, int64, error)

	UpsertCredential(ctx context.Context, accountID uuid.UUID, algo Algo, hash string) (Credential, error)
	GetCredential(ctx context.Context, accountID uuid.UUID, algo Algo) (Credential, error)
	RecordFailedAttempt(ctx context.Context, accountID uuid.UUID, algo Algo, lock bool, lockFor time.Duration) (Credential, error)
	ResetFailedAttempts(ctx context.Context, accountID uuid.UUID, algo Algo) error
}

// CreateInput 是创建账号的入参。
type CreateInput struct {
	Username string
	Email    string
	Status   domain.AccountStatus
	// LoginEnabled 是新账号的通用布尔开关默认值
	LoginEnabled bool
}

// ListFilter 是账号列表过滤条件。
type ListFilter struct {
	Status string
	Search string
	Limit  int32
	Offset int32
}

// PgRepository 是基于 sqlc 的 PostgreSQL 实现。
type PgRepository struct {
	queries *query.Queries
	pool    *db.Pool
}

// NewPgRepository 创建仓储。
func NewPgRepository(pool *db.Pool) *PgRepository {
	return &PgRepository{queries: query.New(pool), pool: pool}
}

// NewTxRepository 创建绑定到事务的仓储。
func (r *PgRepository) NewTxRepository(tx pgx.Tx) *PgRepository {
	return &PgRepository{queries: r.queries.WithTx(tx), pool: r.pool}
}

func toDomain(row query.IdentityAccount) domain.Account {
	a := domain.Account{
		ID:             row.ID,
		Username:       row.Username,
		Email:          row.Email,
		Status:         domain.AccountStatus(row.Status),
		MCLoginEnabled: row.McLoginEnabled,
		CreatedAt:      row.CreatedAt,
		UpdatedAt:      row.UpdatedAt,
	}
	if row.EmailVerifiedAt.Valid {
		a.EmailVerified = true
	}
	return a
}

func toCredential(row query.IdentityCredential) Credential {
	c := Credential{
		ID:             row.ID,
		AccountID:      row.AccountID,
		Algo:           Algo(row.Algo),
		Hash:           row.Hash,
		FailedAttempts: int(row.FailedAttempts),
		ChangedAt:      row.ChangedAt,
	}
	if row.LockedUntil.Valid {
		t := row.LockedUntil.Time
		c.LockedUntil = &t
	}
	return c
}

// translate 把底层数据库错误翻译成业务错误码。
//
// 409 类语义(邮箱已注册、用户名已存在)靠唯一约束冲突识别 ——
// 唯一约束是最后一道防线,先查后插在并发下必然有窗口。
func translate(err error) error {
	switch {
	case err == nil:
		return nil
	case db.IsNoRows(err):
		return apperr.ErrNotFound
	case db.IsUniqueViolation(err):
		return apperr.New(apperr.CodeConflict, err.Error())
	default:
		return apperr.Newf(apperr.CodeInternal, "数据库操作失败: %v", err)
	}
}

// Create 创建账号。
func (r *PgRepository) Create(ctx context.Context, in CreateInput) (domain.Account, error) {
	row, err := r.queries.CreateAccount(ctx, query.CreateAccountParams{
		Username:       in.Username,
		UsernameLower:  strings.ToLower(in.Username),
		Email:          in.Email,
		Status:         string(in.Status),
		McLoginEnabled: in.LoginEnabled,
	})
	if err != nil {
		return domain.Account{}, translate(err)
	}
	return toDomain(row), nil
}

// GetByID 按主键取账号。
func (r *PgRepository) GetByID(ctx context.Context, id uuid.UUID) (domain.Account, error) {
	row, err := r.queries.GetAccountByID(ctx, id)
	if err != nil {
		return domain.Account{}, translate(err)
	}
	return toDomain(row), nil
}

// GetByEmail 按邮箱取账号(大小写不敏感)。
func (r *PgRepository) GetByEmail(ctx context.Context, email string) (domain.Account, error) {
	row, err := r.queries.GetAccountByEmail(ctx, email)
	if err != nil {
		return domain.Account{}, translate(err)
	}
	return toDomain(row), nil
}

// GetByUsername 按用户名取账号(大小写不敏感)。
func (r *PgRepository) GetByUsername(ctx context.Context, username string) (domain.Account, error) {
	row, err := r.queries.GetAccountByUsername(ctx, strings.ToLower(username))
	if err != nil {
		return domain.Account{}, translate(err)
	}
	return toDomain(row), nil
}

// UpdateProfile 修改用户名与邮箱。
func (r *PgRepository) UpdateProfile(ctx context.Context, id uuid.UUID, username, email string) (domain.Account, error) {
	row, err := r.queries.UpdateAccountProfile(ctx, query.UpdateAccountProfileParams{
		ID:            id,
		Username:      username,
		UsernameLower: strings.ToLower(username),
		Email:         email,
	})
	if err != nil {
		return domain.Account{}, translate(err)
	}
	return toDomain(row), nil
}

// UpdateStatus 修改账号状态。
func (r *PgRepository) UpdateStatus(ctx context.Context, id uuid.UUID, status domain.AccountStatus) (domain.Account, error) {
	row, err := r.queries.UpdateAccountStatus(ctx, query.UpdateAccountStatusParams{
		ID:     id,
		Status: string(status),
	})
	if err != nil {
		return domain.Account{}, translate(err)
	}
	return toDomain(row), nil
}

// MarkEmailVerified 标记邮箱已验证并激活账号。
func (r *PgRepository) MarkEmailVerified(ctx context.Context, id uuid.UUID) (domain.Account, error) {
	row, err := r.queries.SetAccountEmailVerified(ctx, id)
	if err != nil {
		return domain.Account{}, translate(err)
	}
	return toDomain(row), nil
}

// SetLoginEnabled 设置通用布尔开关。
func (r *PgRepository) SetLoginEnabled(ctx context.Context, id uuid.UUID, enabled bool) (domain.Account, error) {
	row, err := r.queries.SetMCLoginEnabled(ctx, query.SetMCLoginEnabledParams{
		ID:             id,
		McLoginEnabled: enabled,
	})
	if err != nil {
		return domain.Account{}, translate(err)
	}
	return toDomain(row), nil
}

// List 分页查询账号。
func (r *PgRepository) List(ctx context.Context, f ListFilter) ([]domain.Account, int64, error) {
	rows, err := r.queries.ListAccounts(ctx, query.ListAccountsParams{
		Status: nullable(f.Status),
		Search: nullable(searchPattern(f.Search)),
		Limit:  f.Limit,
		Offset: f.Offset,
	})
	if err != nil {
		return nil, 0, translate(err)
	}
	total, err := r.queries.CountAccounts(ctx, query.CountAccountsParams{
		Status: nullable(f.Status),
		Search: nullable(searchPattern(f.Search)),
	})
	if err != nil {
		return nil, 0, translate(err)
	}

	out := make([]domain.Account, 0, len(rows))
	for _, row := range rows {
		out = append(out, toDomain(row))
	}
	return out, total, nil
}

// UpsertCredential 写入或更新凭据。
func (r *PgRepository) UpsertCredential(ctx context.Context, accountID uuid.UUID, algo Algo, hash string) (Credential, error) {
	row, err := r.queries.UpsertCredential(ctx, query.UpsertCredentialParams{
		AccountID: accountID,
		Algo:      string(algo),
		Hash:      hash,
		Params:    []byte(`{}`),
	})
	if err != nil {
		return Credential{}, translate(err)
	}
	return toCredential(row), nil
}

// GetCredential 取凭据。
func (r *PgRepository) GetCredential(ctx context.Context, accountID uuid.UUID, algo Algo) (Credential, error) {
	row, err := r.queries.GetCredential(ctx, query.GetCredentialParams{
		AccountID: accountID,
		Algo:      string(algo),
	})
	if err != nil {
		return Credential{}, translate(err)
	}
	return toCredential(row), nil
}

// RecordFailedAttempt 记录一次登录失败,达到阈值时同时锁定。
func (r *PgRepository) RecordFailedAttempt(ctx context.Context, accountID uuid.UUID, algo Algo, lock bool, lockFor time.Duration) (Credential, error) {
	row, err := r.queries.RecordFailedAttempt(ctx, query.RecordFailedAttemptParams{
		AccountID: accountID,
		Algo:      string(algo),
		Lock:      lock,
		LockSecs:  lockFor.Seconds(),
	})
	if err != nil {
		return Credential{}, translate(err)
	}
	return toCredential(row), nil
}

// ResetFailedAttempts 登录成功后清零失败计数。
func (r *PgRepository) ResetFailedAttempts(ctx context.Context, accountID uuid.UUID, algo Algo) error {
	_, err := r.queries.ResetFailedAttempts(ctx, query.ResetFailedAttemptsParams{
		AccountID: accountID,
		Algo:      string(algo),
	})
	return translate(err)
}

// ---------------------------------------------------------------- 辅助

func nullable(s string) pgtype.Text {
	if s == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: s, Valid: true}
}

// searchPattern 把搜索词转成 LIKE 模式。
func searchPattern(s string) string {
	if s == "" {
		return ""
	}
	return "%" + s + "%"
}

// ErrAccountNotFound 表示账号不存在。
var ErrAccountNotFound = errors.New("账号不存在")

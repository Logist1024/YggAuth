package account

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/yggauth/yggauth/internal/platform/apperr"
	"github.com/yggauth/yggauth/internal/platform/db"
	"github.com/yggauth/yggauth/internal/platform/db/query"
)

// tokenBytes 是邮箱令牌的熵。32 字节是会话令牌的同级别要求。
const tokenBytes = 32

// newToken 生成邮箱令牌的明文。
//
// 用 base64url 而不是 hex:同样熵下短 1/3,放进邮件链接更不容易被折行截断。
func newToken() (string, error) {
	buf := make([]byte, tokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", apperr.Newf(apperr.CodeInternal, "生成令牌失败: %v", err)
	}
	return encodeBase64URL(buf), nil
}

// HashToken 返回令牌的 sha256。
//
// 库里只存哈希:数据库泄露时,攻击者拿到的哈希无法直接用于验证邮箱或重置密码。
func HashToken(plain string) []byte {
	sum := sha256.Sum256([]byte(plain))
	return sum[:]
}

// PgTokenStore 是基于 PostgreSQL 的令牌存储。
type PgTokenStore struct {
	queries *query.Queries
}

// NewPgTokenStore 创建令牌存储。
func NewPgTokenStore(pool *db.Pool) *PgTokenStore {
	return &PgTokenStore{queries: query.New(pool)}
}

// Create 存一个新令牌。
//
// 作废同用途的旧令牌:同时只允许一个有效令牌在流通,
// 否则用户点两次「重发」会让前一个链接也还能用。
func (s *PgTokenStore) Create(ctx context.Context, accountID uuid.UUID, purpose, plain string, expiresAt time.Time) error {
	if _, err := s.queries.InvalidateEmailTokens(ctx, query.InvalidateEmailTokensParams{
		AccountID: accountID,
		Purpose:   purpose,
	}); err != nil {
		return apperr.Newf(apperr.CodeInternal, "作废旧令牌失败: %v", err)
	}

	if _, err := s.queries.CreateEmailToken(ctx, query.CreateEmailTokenParams{
		AccountID: accountID,
		TokenHash: HashToken(plain),
		Purpose:   purpose,
		ExpiresAt: expiresAt,
	}); err != nil {
		return apperr.Newf(apperr.CodeInternal, "保存令牌失败: %v", err)
	}
	return nil
}

// Consume 核销令牌并返回所属账号。
//
// UPDATE ... WHERE used_at IS NULL 的写法让「一次性使用」由数据库保证,
// 不存在两个并发请求同时消费同一令牌的可能。
func (s *PgTokenStore) Consume(ctx context.Context, purpose, plain string) (uuid.UUID, error) {
	if plain == "" {
		return uuid.Nil, apperr.New(apperr.CodeInvalidToken, "令牌为空")
	}

	row, err := s.queries.ConsumeEmailToken(ctx, HashToken(plain))
	if err != nil {
		if db.IsNoRows(err) {
			return uuid.Nil, apperr.New(apperr.CodeInvalidToken, "令牌无效或已过期")
		}
		return uuid.Nil, apperr.Newf(apperr.CodeInternal, "核销令牌失败: %v", err)
	}
	if row.Purpose != purpose {
		return uuid.Nil, apperr.New(apperr.CodeInvalidToken, "令牌用途不匹配")
	}
	return row.AccountID, nil
}

// CountSince 统计某用途在时间点之后的签发次数,用于邮件每日上限。
func (s *PgTokenStore) CountSince(ctx context.Context, accountID uuid.UUID, purpose string, since time.Time) (int64, error) {
	n, err := s.queries.CountEmailTokensSince(ctx, query.CountEmailTokensSinceParams{
		AccountID: accountID,
		Purpose:   purpose,
		CreatedAt: since,
	})
	if err != nil {
		return 0, apperr.Newf(apperr.CodeInternal, "统计令牌数量失败: %v", err)
	}
	return n, nil
}

// LastCreatedAt 返回某用途最近一次签发时间,用于重发冷却。
func (s *PgTokenStore) LastCreatedAt(ctx context.Context, accountID uuid.UUID, purpose string) (time.Time, bool, error) {
	row, err := s.queries.GetLastEmailToken(ctx, query.GetLastEmailTokenParams{
		AccountID: accountID,
		Purpose:   purpose,
	})
	if err != nil {
		if db.IsNoRows(err) {
			return time.Time{}, false, nil
		}
		return time.Time{}, false, apperr.Newf(apperr.CodeInternal, "查询最近令牌失败: %v", err)
	}
	return row.CreatedAt, true, nil
}

// encodeBase64URL 用无填充的 base64url 编码,得到的串可以直接放进 URL。
func encodeBase64URL(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// ---------------------------------------------------------------- 邀请码

// InvitationStore 校验并核销邀请码。
type InvitationStore struct{ q *query.Queries }

// NewInvitationStore 创建邀请码存储。
func NewInvitationStore(pool *db.Pool) *InvitationStore {
	return &InvitationStore{q: query.New(pool)}
}

// Consume 校验并核销邀请码。
//
// 核销用一条带条件的 UPDATE 完成:过期、撤销、用尽、邮箱不匹配
// 全部落在 SQL 的 WHERE 里,不存在「先查后写」的竞态窗口。
func (s *InvitationStore) Consume(ctx context.Context, code, email string) error {
	if code == "" {
		return apperr.New(apperr.CodeInviteInvalid, "邀请码为空")
	}
	row, err := s.q.ConsumeInvitation(ctx, query.ConsumeInvitationParams{
		Code: code,
		Email: pgtype.Text{
			String: email,
			Valid:  email != "",
		},
	})
	if err != nil {
		if db.IsNoRows(err) {
			return apperr.New(apperr.CodeInviteInvalid, "邀请码无效或已用尽")
		}
		return apperr.Newf(apperr.CodeInternal, "核销邀请码失败: %v", err)
	}
	_ = row
	return nil
}

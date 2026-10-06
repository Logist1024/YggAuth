package oidc

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/ory/fosite"
	"golang.org/x/crypto/bcrypt"

	"github.com/yggauth/yggauth/internal/platform/apperr"
	"github.com/yggauth/yggauth/internal/platform/db"
	"github.com/yggauth/yggauth/internal/platform/db/query"
)

// Client 是登记在册的 OIDC 客户端。
type Client struct {
	ID           string
	SecretHash   []byte
	RedirectURIs []string
	GrantTypes   []string
	Scopes       []string
	Audience     []string
	// RequirePKCE 为真时该客户端必须使用 PKCE
	RequirePKCE bool
}

// GetID 返回客户端 ID。
func (c *Client) GetID() string { return c.ID }

// GetHashedSecret 返回密钥哈希。
//
// 客户端密钥用 argon2id 哈希存储,fosite 会在校验时用同一算法比对。
// 这里返回的是我们存的哈希本体,fosite 的默认策略支持这种用法。
func (c *Client) GetHashedSecret() []byte { return c.SecretHash }

// GetRedirectURIs 返回白名单回调地址。
func (c *Client) GetRedirectURIs() []string { return c.RedirectURIs }

// GetGrantTypes 返回允许的授权类型。
func (c *Client) GetGrantTypes() fosite.Arguments { return toArgs(c.GrantTypes) }

// GetResponseTypes 返回允许的响应类型组合。
//
// 由 grant_types 推导:授权码流程对应 "code",
// 隐式流程对应 "token"。混在一起时要列出所有组合。
func (c *Client) GetResponseTypes() fosite.Arguments {
	seen := map[string]struct{}{}
	out := fosite.Arguments{}
	for _, grant := range c.GrantTypes {
		var rt string
		switch grant {
		case "authorization_code":
			rt = "code"
		case "implicit":
			rt = "token id_token"
		default:
			// 客户端凭证、设备码这类不需要 response_type
			continue
		}
		if _, dup := seen[rt]; dup {
			continue
		}
		seen[rt] = struct{}{}
		out = append(out, rt)
	}
	return out
}

// GetScopes 返回允许的 scope。
func (c *Client) GetScopes() fosite.Arguments { return toArgs(c.Scopes) }

// IsPublic 返回是否为公开客户端(无密钥,必须走 PKCE)。
func (c *Client) IsPublic() bool { return len(c.SecretHash) == 0 }

// GetAudience 返回受众。
func (c *Client) GetAudience() fosite.Arguments { return toArgs(c.Audience) }

// ValidateRedirectURI 校验回调地址。
//
// **必须是完整字符串相等**,绝不能用前缀匹配 ——
// `https://app.example.com/cb.evil.com` 以前缀方式就能通过 `https://app.example.com/cb`,
// 这类「开放重定向」是 OIDC 实现最常见的漏洞来源(docs/security.md 第四节)。
func (c *Client) ValidateRedirectURI(raw string) error {
	for _, allowed := range c.RedirectURIs {
		if raw == allowed {
			return nil
		}
	}
	return fosite.ErrInvalidRequest.
		WithHint("The 'redirect_uri' parameter does not match any of the OAuth 2.0 Client's pre-registered redirect urls.")
}

// ---------------------------------------------------------------- 客户端管理

// ClientService 提供客户端的登记与维护能力。
type ClientService struct {
	queries *query.Queries
	hasher  *SecretHasher
}

// SecretHasher 生成并校验客户端密钥。
type SecretHasher struct {
	hash func(password string) (string, error)
}

// NewSecretHasher 创建密钥哈希器。
func NewSecretHasher(hash func(password string) (string, error)) *SecretHasher {
	return &SecretHasher{hash: hash}
}

// Generate 生成一个新的客户端密钥明文。
func (h *SecretHasher) Generate() (plain string, hashValue string, err error) {
	buf := make([]byte, 32)
	if _, err := cryptoRandRead(buf); err != nil {
		return "", "", apperr.Newf(apperr.CodeInternal, "生成客户端密钥失败: %v", err)
	}
	plain = "yc_" + base64.RawURLEncoding.EncodeToString(buf)
	hashValue, err = h.hash(plain)
	if err != nil {
		return "", "", err
	}
	return plain, hashValue, nil
}

// Verify 校验客户端密钥。
//
// 必须交给 bcrypt 自己比,不能比较两次哈希的字符串:bcrypt 每次都生成
// 随机盐,同一明文的两次哈希本来就不一样,字符串比较恒为假 ——
// 表现就是「密钥明明正确却被拒」,而且没有任何日志能指出来源。
func (h *SecretHasher) Verify(plain, stored string) bool {
	if plain == "" || stored == "" {
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(stored), []byte(plain)) == nil
}

// NewClientService 创建客户端服务。
func NewClientService(pool *db.Pool, hasher *SecretHasher) *ClientService {
	return &ClientService{queries: query.New(pool), hasher: hasher}
}

// CreateClientInput 是登记客户端的入参。
type CreateClientInput struct {
	ClientID        string
	Name            string
	Description     string
	RedirectURIs    []string
	GrantTypes      []string
	Scopes          []string
	AccessTokenTTL  time.Duration
	RefreshTokenTTL time.Duration
	AuthCodeTTL     time.Duration
	RequirePKCE     bool
	// Public 标记公开客户端:不持有密钥,只能用 PKCE 换令牌。
	// 它与 RequirePKCE 是两件事 —— 保密客户端也可以被要求使用 PKCE。
	Public bool
}

// ClientRecord 是客户端的完整信息(含明文密钥,仅在创建时返回一次)。
type ClientRecord struct {
	ID              string
	Secret          string
	SecretHash      string
	Name            string
	Description     string
	RedirectURIs    []string
	GrantTypes      []string
	Scopes          []string
	RequirePKCE     bool
	AccessTokenTTL  time.Duration
	RefreshTokenTTL time.Duration
	AuthCodeTTL     time.Duration
	Status          string
}

// Create 登记客户端。
func (s *ClientService) Create(ctx context.Context, in CreateClientInput) (ClientRecord, error) {
	id := strings.TrimSpace(in.ClientID)
	if id == "" {
		return ClientRecord{}, apperr.New(apperr.CodeInvalidArgument, "client_id 不能为空")
	}
	if len(in.RedirectURIs) == 0 {
		// 没有回调地址的客户端毫无用处,而且日后加错一次就是开放重定向
		return ClientRecord{}, apperr.New(apperr.CodeInvalidArgument, "至少需要一个回调地址")
	}
	for _, uri := range in.RedirectURIs {
		if !strings.HasPrefix(uri, "https://") && !strings.HasPrefix(uri, "http://localhost") {
			return ClientRecord{}, apperr.Newf(apperr.CodeInvalidArgument,
				"回调地址必须以 https:// 开头(仅本地开发允许 http://localhost): %s", uri)
		}
	}

	// 纯 PKCE 客户端可以没有密钥;其他流程必须有。
	plainSecret, secretHash, err := s.hasher.Generate()
	if err != nil {
		return ClientRecord{}, err
	}
	// 公开客户端(比如纯前端 SPA)不持有密钥,只能用 PKCE。
	//
	// 注意区分「公开」与「要求 PKCE」:一个保密客户端同样可以被要求使用
	// PKCE。把两者混为一谈会让保密客户端被静默降级 —— 它拿不到密钥,
	// 客户端认证会失败,而排查起来极难想到是这个原因。
	var secretValue pgtype.Text
	if !in.Public {
		secretValue = pgtype.Text{String: secretHash, Valid: true}
	}

	row, err := s.queries.CreateClient(ctx, query.CreateClientParams{
		ClientID:             id,
		ClientSecretHash:     secretValue,
		Name:                 in.Name,
		Description:          pgText(in.Description),
		RedirectUris:         in.RedirectURIs,
		GrantTypes:           in.GrantTypes,
		Scopes:               in.Scopes,
		RequirePkce:          in.RequirePKCE,
		AccessTokenTtl:       intervalOf(in.AccessTokenTTL),
		RefreshTokenTtl:      intervalOf(in.RefreshTokenTTL),
		AuthorizationCodeTtl: intervalOf(in.AuthCodeTTL),
	})
	if err != nil {
		if db.IsUniqueViolation(err) {
			return ClientRecord{}, apperr.New(apperr.CodeConflict, "client_id 已存在")
		}
		return ClientRecord{}, apperr.Newf(apperr.CodeInternal, "登记客户端失败: %v", err)
	}

	out := recordFromRow(row)
	if !in.Public {
		out.Secret = plainSecret
	}
	return out, nil
}

// Get 取客户端。
func (s *ClientService) Get(ctx context.Context, clientID string) (ClientRecord, error) {
	row, err := s.queries.GetClient(ctx, clientID)
	if err != nil {
		if db.IsNoRows(err) {
			return ClientRecord{}, apperr.ErrNotFound
		}
		return ClientRecord{}, apperr.Newf(apperr.CodeInternal, "查询客户端失败: %v", err)
	}
	return recordFromRow(row), nil
}

// List 分页列出客户端。
func (s *ClientService) List(ctx context.Context, status, search string, limit, offset int32) ([]ClientRecord, int64, error) {
	rows, err := s.queries.ListClients(ctx, query.ListClientsParams{
		Status: pgText(status),
		Search: pgText(searchPattern(search)),
		Limit:  limit,
		Offset: offset,
	})
	if err != nil {
		return nil, 0, apperr.Newf(apperr.CodeInternal, "查询客户端失败: %v", err)
	}
	total, err := s.queries.CountClients(ctx, query.CountClientsParams{
		Status: pgText(status),
		Search: pgText(searchPattern(search)),
	})
	if err != nil {
		return nil, 0, apperr.Newf(apperr.CodeInternal, "统计客户端失败: %v", err)
	}

	out := make([]ClientRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, recordFromRow(row))
	}
	return out, total, nil
}

// RotateSecret 轮换客户端密钥。旧密钥立即失效。
func (s *ClientService) RotateSecret(ctx context.Context, clientID string) (string, error) {
	plain, hashValue, err := s.hasher.Generate()
	if err != nil {
		return "", err
	}
	if _, err := s.queries.UpdateClientSecret(ctx, query.UpdateClientSecretParams{
		ClientID:         clientID,
		ClientSecretHash: hashValue,
	}); err != nil {
		if db.IsNoRows(err) {
			return "", apperr.ErrNotFound
		}
		return "", apperr.Newf(apperr.CodeInternal, "轮换密钥失败: %v", err)
	}
	return plain, nil
}

// Delete 删除客户端。
func (s *ClientService) Delete(ctx context.Context, clientID string) error {
	if _, err := s.queries.DeleteClient(ctx, clientID); err != nil {
		return apperr.Newf(apperr.CodeInternal, "删除客户端失败: %v", err)
	}
	return nil
}

func recordFromRow(row query.OidcClient) ClientRecord {
	return ClientRecord{
		ID:              row.ClientID,
		SecretHash:      row.ClientSecretHash.String,
		Name:            row.Name,
		Description:     row.Description.String,
		RedirectURIs:    row.RedirectUris,
		GrantTypes:      row.GrantTypes,
		Scopes:          row.Scopes,
		RequirePKCE:     row.RequirePkce,
		AccessTokenTTL:  durationOf(row.AccessTokenTtl),
		RefreshTokenTTL: durationOf(row.RefreshTokenTtl),
		AuthCodeTTL:     durationOf(row.AuthorizationCodeTtl),
		Status:          row.Status,
	}
}

// ValidateSecret 校验客户端密钥。
func (s *ClientService) ValidateSecret(plain, stored string) bool {
	return s.hasher.Verify(plain, stored)
}

func pgText(s string) pgtype.Text {
	if s == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: s, Valid: true}
}

func searchPattern(s string) string {
	if s == "" {
		return ""
	}
	return "%" + s + "%"
}

func intervalOf(d time.Duration) pgtype.Interval {
	if d <= 0 {
		return pgtype.Interval{}
	}
	return pgtype.Interval{Microseconds: d.Microseconds(), Valid: true}
}

func durationOf(i pgtype.Interval) time.Duration {
	if !i.Valid {
		return 0
	}
	return time.Duration(i.Microseconds) * time.Microsecond
}

// 确保接口满足。
var _ fosite.Client = (*Client)(nil)

var (
	_ = uuid.Nil
	_ = sha256.Sum256
)

// cryptoRandRead 是 crypto/rand.Reader 的别名,便于测试替换。
var cryptoRandRead = func(b []byte) (int, error) { return rand.Read(b) }

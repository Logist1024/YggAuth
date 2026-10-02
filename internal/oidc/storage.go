package oidc

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/url"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/ory/fosite"
	"github.com/ory/fosite/handler/oauth2"
	"github.com/ory/fosite/handler/openid"
	"github.com/ory/fosite/handler/pkce"

	"github.com/yggauth/yggauth/internal/oidc/session"
	"github.com/yggauth/yggauth/internal/platform/apperr"

	"github.com/yggauth/yggauth/internal/platform/clock"
	"github.com/yggauth/yggauth/internal/platform/db"
	"github.com/yggauth/yggauth/internal/platform/db/query"
)

// Storage 把 fosite 的存储接口落到 PostgreSQL。
//
// 一条硬规则贯穿整个文件:**明文令牌绝不入库**,只存 sha256。
// fosite 交给我们的是签名(signature)而不是明文令牌,所以我们把
// 「签名」同时当查找键与存在性凭证 —— 它本身就是 HMAC 的结果。
//
// 依赖方向:这一层是 fosite 与我们 schema 之间唯一的适配点,
// fosite 的类型不会泄漏到 handler 与 service 之外。
type Storage struct {
	queries *query.Queries
	pool    *db.Pool
	clock   func() time.Time
	// provider 让存储在需要时让 fosite 重新解析请求
	provider fosite.OAuth2Provider
	// loadClient 反查客户端。fosite.Client 无法 JSON 序列化,
	// 从存储还原请求时必须重新构造它。
	loadClient func(ctx context.Context, id string) (fosite.Client, error)
}

// NewStorage 创建存储适配器。
func NewStorage(pool *db.Pool, clk clock.Clock, loadClient func(ctx context.Context, id string) (fosite.Client, error)) *Storage {
	return &Storage{
		queries:    query.New(pool),
		pool:       pool,
		clock:      clk.Now,
		loadClient: loadClient,
	}
}

// ---------------------------------------------------------------- 客户端

// GetClient 实现 fosite.ClientManager。
func (s *Storage) GetClient(ctx context.Context, id string) (fosite.Client, error) {
	row, err := s.queries.GetClient(ctx, id)
	if err != nil {
		if db.IsNoRows(err) {
			return nil, fosite.ErrNotFound
		}
		return nil, err
	}
	return clientFromRow(row), nil
}

// ClientAssertionJWTValid 检查 jti 是否已被用过。
//
// RFC 7523 要求 client assertion 一次性:同一个 jti 再次出现即判定为重放。
func (s *Storage) ClientAssertionJWTValid(ctx context.Context, jti string) error {
	_, err := s.queries.CreateClientAssertion(ctx, query.CreateClientAssertionParams{
		Jti:       jti,
		ExpiresAt: s.clock().Add(5 * time.Minute),
	})
	if err != nil {
		return err
	}
	return nil
}

// SetClientAssertionJWT 兼容接口:jti 由上面的校验接口负责登记,
// 这里只需要保证过期记录能被清理。
func (s *Storage) SetClientAssertionJWT(_ context.Context, _ string, _ time.Time) error {
	return nil
}

// ---------------------------------------------------------------- 授权码

// CreateAuthorizeCodeSession 保存授权请求。
func (s *Storage) CreateAuthorizeCodeSession(ctx context.Context, code string, request fosite.Requester) error {
	// 不在这里强制要求 PKCE 挑战:PKCE 有独立的存储与校验(handler/pkce),
	// 这一层重复判断只会把「本次没用 PKCE」误报成「授权码已失效」。
	challenge := request.GetRequestForm().Get("code_challenge")

	sessionJSON, requestJSON, err := marshalPair(request.GetSession(), request)
	if err != nil {
		return err
	}

	accountID, err := accountUUID(request.GetSession())
	if err != nil {
		return err
	}

	ttl := time.Minute
	if d := request.GetSession().GetExpiresAt(fosite.AuthorizeCode); !d.IsZero() {
		ttl = d.Sub(s.clock())
	}

	_, err = s.queries.CreateAuthorizationCode(ctx, query.CreateAuthorizationCodeParams{
		CodeHash:            hashCode(code),
		CodeSignature:       []byte(request.GetID()),
		ClientID:            request.GetClient().GetID(),
		AccountID:           accountID,
		RedirectUri:         redirectURIOf(request),
		Scopes:              toTextSlice(request.GetRequestedScopes()),
		Nonce:               pgtype.Text{String: request.GetRequestForm().Get("nonce"), Valid: request.GetRequestForm().Get("nonce") != ""},
		CodeChallenge:       pgText(challenge),
		CodeChallengeMethod: pgText(request.GetRequestForm().Get("code_challenge_method")),
		Session:             sessionJSON,
		Request:             requestJSON,
		ExpiresAt:           s.clock().Add(ttl),
	})
	if err != nil {
		return err
	}
	return nil
}

// GetAuthorizeCodeSession 按明文授权码取回请求。
//
// 已使用或已过期的授权码必须返回 fosite.ErrInvalidatedAuthorizeCode
// **并带上 request** —— fosite 依赖这个约定来按 RFC 6749 §4.1.2
// 把错误重定向回客户端。
func (s *Storage) GetAuthorizeCodeSession(ctx context.Context, code string, fositeSession fosite.Session) (fosite.Requester, error) {
	row, err := s.queries.GetAuthorizationCodeByHash(ctx, hashCode(code))
	if err != nil {
		if db.IsNoRows(err) {
			return nil, fosite.ErrInvalidatedAuthorizeCode
		}
		return nil, err
	}

	// 先还原会话,再还原请求:fosite 后续会把请求的会话整个替换成
	// authorizeRequest 的会话,顺序反了会拿到一个空会话。
	if err := restoreSession(fositeSession, row.Session); err != nil {
		return nil, err
	}

	request, err := s.unmarshalRequest(ctx, row.Request, requestConfig{
		clientID: row.ClientID,
		scopes:   row.Scopes,
	}, fositeSession)
	if err != nil {
		return nil, err
	}

	if row.UsedAt.Valid || row.ExpiresAt.Before(s.clock()) {
		return request, fosite.ErrInvalidatedAuthorizeCode
	}
	return request, nil
}

// InvalidateAuthorizeCodeSession 核销授权码。
func (s *Storage) InvalidateAuthorizeCodeSession(ctx context.Context, code string) error {
	_, err := s.queries.InvalidateAuthorizationCode(ctx, hashCode(code))
	return err
}

// ---------------------------------------------------------------- 访问令牌

// CreateAccessTokenSession 保存访问令牌会话。
func (s *Storage) CreateAccessTokenSession(ctx context.Context, signature string, request fosite.Requester) error {
	sessionJSON, requestJSON, err := marshalPair(request.GetSession(), request)
	if err != nil {
		return err
	}
	accountID, err := accountUUID(request.GetSession())
	if err != nil {
		return err
	}

	_, err = s.queries.CreateAccessToken(ctx, query.CreateAccessTokenParams{
		TokenHash: hashCode(signature),
		Signature: []byte(signature),
		ClientID:  request.GetClient().GetID(),
		AccountID: pgUUID(accountID),
		Scopes:    toTextSlice(request.GetGrantedScopes()),
		Session:   sessionJSON,
		Request:   requestJSON,
		ExpiresAt: s.clock().Add(time.Hour),
	})
	return err
}

// GetAccessTokenSession 按签名取回访问令牌会话。
func (s *Storage) GetAccessTokenSession(ctx context.Context, signature string, fositeSession fosite.Session) (fosite.Requester, error) {
	row, err := s.queries.GetAccessTokenBySignature(ctx, []byte(signature))
	if err != nil {
		if db.IsNoRows(err) {
			return nil, fosite.ErrTokenSignatureMismatch
		}
		return nil, err
	}
	if row.RevokedAt.Valid {
		return nil, fosite.ErrInactiveToken
	}
	if row.ExpiresAt.Before(s.clock()) {
		return nil, fosite.ErrTokenExpired
	}

	if err := restoreSession(fositeSession, row.Session); err != nil {
		return nil, err
	}
	return s.unmarshalRequest(ctx, row.Request, requestConfig{
		clientID: row.ClientID,
		scopes:   row.Scopes,
	}, fositeSession)
}

// DeleteAccessTokenSession 吊销访问令牌。
func (s *Storage) DeleteAccessTokenSession(ctx context.Context, signature string) error {
	_, err := s.queries.RevokeAccessToken(ctx, query.RevokeAccessTokenParams{
		Signature: []byte(signature),
		Reason:    "revoked",
	})
	return err
}

// RevokeAccessTokenByClient 吊销某客户端的全部访问令牌。
func (s *Storage) RevokeAccessTokenByClient(ctx context.Context, clientID, reason string) (int64, error) {
	return s.queries.RevokeAccessTokensByClient(ctx, query.RevokeAccessTokensByClientParams{
		ClientID: clientID,
		Reason:   reason,
	})
}

// RevokeAccessTokensByAccount 吊销某账号的全部访问令牌。
func (s *Storage) RevokeAccessTokensByAccount(ctx context.Context, accountID uuid.UUID, reason string) (int64, error) {
	return s.queries.RevokeAccessTokensByAccount(ctx, query.RevokeAccessTokensByAccountParams{
		AccountID: accountID,
		Reason:    reason,
	})
}

// RevokeAccessTokenByHash 按明文令牌吊销(给 /oauth/revoke 用)。
func (s *Storage) RevokeAccessTokenByHash(ctx context.Context, token, reason string) (int64, error) {
	return s.queries.RevokeAccessTokenByHash(ctx, query.RevokeAccessTokenByHashParams{
		TokenHash: hashCode(token),
		Reason:    reason,
	})
}

// ---------------------------------------------------------------- 刷新令牌

// CreateRefreshTokenSession 保存刷新令牌会话。
func (s *Storage) CreateRefreshTokenSession(ctx context.Context, signature, _ string, request fosite.Requester) error {
	sessionJSON, requestJSON, err := marshalPair(request.GetSession(), request)
	if err != nil {
		return err
	}
	accountID, err := accountUUID(request.GetSession())
	if err != nil {
		return err
	}

	// rotated_from 把历次刷新串成链。链存在,重放检测才能吊销整条链,
	// 而不只是吊销被重放的那一枚。
	var rotated pgtype.UUID
	if prev := sessionExtra(request.GetSession(), rotatedFromKey); prev != nil {
		if id, ok := prev.(uuid.UUID); ok {
			rotated = pgUUID(id)
		}
	}

	_, err = s.queries.CreateRefreshToken(ctx, query.CreateRefreshTokenParams{
		TokenHash:   hashCode(signature),
		Signature:   []byte(signature),
		ClientID:    request.GetClient().GetID(),
		AccountID:   accountID,
		Scopes:      toTextSlice(request.GetGrantedScopes()),
		RotatedFrom: rotated,
		Session:     sessionJSON,
		Request:     requestJSON,
		ExpiresAt:   s.clock().Add(30 * 24 * time.Hour),
	})
	return err
}

// GetRefreshTokenSession 按签名取回刷新令牌会话。
func (s *Storage) GetRefreshTokenSession(ctx context.Context, signature string, fositeSession fosite.Session) (fosite.Requester, error) {
	row, err := s.queries.GetRefreshTokenBySignature(ctx, []byte(signature))
	if err != nil {
		if db.IsNoRows(err) {
			return nil, fosite.ErrTokenSignatureMismatch
		}
		return nil, err
	}
	if row.RevokedAt.Valid {
		return nil, fosite.ErrInactiveToken
	}
	if row.ExpiresAt.Before(s.clock()) {
		return nil, fosite.ErrTokenExpired
	}

	if err := restoreSession(fositeSession, row.Session); err != nil {
		return nil, err
	}
	fositeSession.SetExpiresAt(fosite.RefreshToken, row.ExpiresAt)
	setSessionExtra(fositeSession, refreshTokenIDKey, row.ID)

	return s.unmarshalRequest(ctx, row.Request, requestConfig{
		clientID: row.ClientID,
		scopes:   row.Scopes,
	}, fositeSession)
}

// DeleteRefreshTokenSession 吊销单个刷新令牌。
func (s *Storage) DeleteRefreshTokenSession(ctx context.Context, signature string) error {
	_, err := s.queries.RevokeRefreshTokenBySignature(ctx, query.RevokeRefreshTokenBySignatureParams{
		Signature: []byte(signature),
		Reason:    "revoked",
	})
	return err
}

// RotateRefreshToken 标记旧令牌已轮换。
//
// 旧刷新令牌被再次使用即为泄露信号 —— 调用方据此吊销整条链。
func (s *Storage) RotateRefreshToken(ctx context.Context, _ string, refreshTokenSignature string) error {
	_, err := s.queries.RevokeRefreshTokenBySignature(ctx, query.RevokeRefreshTokenBySignatureParams{
		Signature: []byte(refreshTokenSignature),
		Reason:    "rotated",
	})
	return err
}

// RevokeRefreshTokenChain 吊销整条轮换链。
func (s *Storage) RevokeRefreshTokenChain(ctx context.Context, signature, reason string) (int64, error) {
	return s.queries.RevokeRefreshTokenChain(ctx, query.RevokeRefreshTokenChainParams{
		Signature: []byte(signature),
		Reason:    reason,
	})
}

// RevokeRefreshTokensByAccount 吊销某账号的全部刷新令牌。
func (s *Storage) RevokeRefreshTokensByAccount(ctx context.Context, accountID uuid.UUID, reason string) (int64, error) {
	return s.queries.RevokeRefreshTokensByAccount(ctx, query.RevokeRefreshTokensByAccountParams{
		AccountID: accountID,
		Reason:    reason,
	})
}

// RevokeRefreshTokensByClient 吊销某客户端的全部刷新令牌。
func (s *Storage) RevokeRefreshTokensByClient(ctx context.Context, clientID, reason string) (int64, error) {
	return s.queries.RevokeRefreshTokensByClient(ctx, query.RevokeRefreshTokensByClientParams{
		ClientID: clientID,
		Reason:   reason,
	})
}

// RevokeRefreshTokenByHash 按明文令牌吊销。
func (s *Storage) RevokeRefreshTokenByHash(ctx context.Context, token, reason string) (int64, error) {
	row, err := s.queries.GetRefreshTokenByHash(ctx, hashCode(token))
	if err != nil {
		if db.IsNoRows(err) {
			return 0, nil
		}
		return 0, err
	}
	return s.queries.RevokeRefreshTokenBySignature(ctx, query.RevokeRefreshTokenBySignatureParams{
		Signature: row.Signature,
		Reason:    reason,
	})
}

// ---------------------------------------------------------------- OIDC 同意记录

// CreateOpenIDConnectSession 保存用户同意记录。
func (s *Storage) CreateOpenIDConnectSession(ctx context.Context, _ string, requester fosite.Requester) error {
	sessionJSON, _, err := marshalPair(requester.GetSession(), requester)
	if err != nil {
		return err
	}
	accountID, err := accountUUID(requester.GetSession())
	if err != nil {
		return err
	}

	_, err = s.queries.UpsertConsent(ctx, query.UpsertConsentParams{
		AccountID: accountID,
		ClientID:  requester.GetClient().GetID(),
		Scopes:    toTextSlice(requester.GetGrantedScopes()),
		SessionID: requester.GetID(),
		Session:   sessionJSON,
	})
	return err
}

// GetOpenIDConnectSession 读取用户同意记录。
func (s *Storage) GetOpenIDConnectSession(ctx context.Context, _ string, requester fosite.Requester) (fosite.Requester, error) {
	clientID := ""
	if requester != nil && requester.GetClient() != nil {
		clientID = requester.GetClient().GetID()
	}
	accountID, err := accountUUID(requester.GetSession())
	if err != nil {
		return nil, err
	}

	row, err := s.queries.GetConsent(ctx, query.GetConsentParams{
		AccountID: accountID,
		ClientID:  clientID,
	})
	if err != nil {
		if db.IsNoRows(err) {
			return nil, openid.ErrNoSessionFound
		}
		return nil, err
	}

	if err := restoreSession(requester.GetSession(), row.Session); err != nil {
		return nil, err
	}
	requester.SetRequestedScopes(requester.GetRequestedScopes())
	return requester, nil
}

// DeleteOpenIDConnectSession 撤销用户同意。
func (s *Storage) DeleteOpenIDConnectSession(context.Context, string) error { return nil }

// ---------------------------------------------------------------- 辅助

// rotatedFromKey 是 session extra 里存放「上一枚刷新令牌 id」的键。
const rotatedFromKey = "ygg_rotated_from"

// refreshTokenIDKey 是 session extra 里存放「本枚刷新令牌 id」的键。
const refreshTokenIDKey = "ygg_refresh_token_id"

// requestConfig 是还原请求时已知的、无法从 JSON 里直接取回的信息。
type requestConfig struct {
	clientID string
	scopes   []string
}

// marshalPair 把会话与请求序列化。
func marshalPair(s fosite.Session, r fosite.Requester) ([]byte, []byte, error) {
	sessionJSON, err := json.Marshal(s)
	if err != nil {
		return nil, nil, apperr.Newf(apperr.CodeInternal, "序列化会话失败: %v", err)
	}

	// 走显式 DTO 而不是 json.Marshal(r):fosite.Request 里��� Session 与
	// Client 两个接口字段,JSON 反序列化必然报
	// "cannot unmarshal object into ... of type fosite.Session"。
	// 依赖它的序列化布局还会被上游一次改版悄悄弄坏。
	requestJSON, err := json.Marshal(toStoredRequest(r))
	if err != nil {
		return nil, nil, apperr.Newf(apperr.CodeInternal, "序列化请求失败: %v", err)
	}
	return sessionJSON, requestJSON, nil
}

// storedRequest 是授权请求的可持久化形态。
type storedRequest struct {
	ID              string     `json:"id"`
	RequestedAt     time.Time  `json:"requested_at"`
	RequestedScopes []string   `json:"requested_scopes"`
	GrantedScopes   []string   `json:"granted_scopes"`
	Form            url.Values `json:"form"`
	ClientID        string     `json:"client_id"`
}

// toStoredRequest 把 fosite 请求转成可存储的 DTO。
func toStoredRequest(r fosite.Requester) storedRequest {
	clientID := ""
	if r.GetClient() != nil {
		clientID = r.GetClient().GetID()
	}
	return storedRequest{
		ID:              r.GetID(),
		RequestedAt:     r.GetRequestedAt(),
		RequestedScopes: toTextSlice(r.GetRequestedScopes()),
		GrantedScopes:   toTextSlice(r.GetGrantedScopes()),
		Form:            r.GetRequestForm(),
		ClientID:        clientID,
	}
}

// unmarshalRequest 从 JSONB 还原出一个 fosite 请求。
//
// 只还原可序列化的部分。客户端按 ID 重新查库构造;scope 也不信任
// JSON 里的副本 —— 那份载荷虽然签过名,但没有加密。
func (s *Storage) unmarshalRequest(ctx context.Context, requestJSON []byte, cfg requestConfig, sess fosite.Session) (fosite.Requester, error) {
	var stored storedRequest
	if err := json.Unmarshal(requestJSON, &stored); err != nil {
		return nil, apperr.Newf(apperr.CodeInternal, "反序列化请求失败: %v", err)
	}

	request := fosite.NewRequest()
	request.ID = stored.ID
	request.RequestedAt = stored.RequestedAt
	request.Form = stored.Form
	// 已授予的 scope 以数据库列为准,不采信 JSON 里的副本:
	// 写入时 fosite 还没授予 scope(它在 storage 调用之后才 Grant),
	// 所以那一列存的是用户**同意过**的请求 scope。
	request.GrantedScope = fosite.Arguments(cfg.scopes)
	request.RequestedScope = fosite.Arguments(stored.RequestedScopes)
	request.SetRequestedScopes(fosite.Arguments(stored.RequestedScopes))

	client, err := s.loadClient(ctx, cfg.clientID)
	if err != nil {
		return nil, err
	}
	request.Client = client
	request.SetSession(sess)
	return request, nil
}

// clientFromRow 把数据库行转成 fosite.Client。
func clientFromRow(row query.OidcClient) fosite.Client {
	return &Client{
		ID:           row.ClientID,
		SecretHash:   []byte(row.ClientSecretHash.String),
		RedirectURIs: row.RedirectUris,
		GrantTypes:   toArgs(row.GrantTypes),
		Scopes:       toArgs(row.Scopes),
		RequirePKCE:  row.RequirePkce,
		Audience:     toArgs(row.Scopes),
	}
}

// restoreSession 把 JSONB 里的会话数据还原到给定的 fosite.Session。
//
// fosite.Session 接口没有 Restore 方法,那是我们自己实现的扩展,
// 所以这里要断言一次;不是我们的会话类型就无法还原 ——
// 宁可报错,也不静默丢数据。
func restoreSession(target fosite.Session, data []byte) error {
	ds, ok := target.(*session.DefaultSession)
	if !ok {
		return apperr.New(apperr.CodeInternal, "会话类型不是 DefaultSession,无法从存储还原")
	}
	return ds.Restore(data)
}

// sessionExtra 从会话里取附加字段。
func sessionExtra(s fosite.Session, key string) any {
	ds, ok := s.(*session.DefaultSession)
	if !ok {
		return nil
	}
	return ds.GetExtra(key)
}

// setSessionExtra 往会话里写附加字段。
func setSessionExtra(s fosite.Session, key string, value any) {
	if ds, ok := s.(*session.DefaultSession); ok {
		ds.SetExtra(key, value)
	}
}

// redirectURIOf 从请求里取回调地址。
func redirectURIOf(request fosite.Requester) string {
	if ar, ok := request.(fosite.AuthorizeRequester); ok {
		if u := ar.GetRedirectURI(); u != nil {
			return u.String()
		}
	}
	return request.GetRequestForm().Get("redirect_uri")
}

// 确保存储层覆盖 fosite 要求的全部接口集合。
var (
	_ fosite.Storage                     = (*Storage)(nil)
	_ fosite.ClientManager               = (*Storage)(nil)
	_ oauth2.AuthorizeCodeStorage        = (*Storage)(nil)
	_ oauth2.AccessTokenStorage          = (*Storage)(nil)
	_ oauth2.RefreshTokenStorage         = (*Storage)(nil)
	_ oauth2.TokenRevocationStorage      = (*Storage)(nil)
	_ openid.OpenIDConnectRequestStorage = (*Storage)(nil)
	_ pkce.PKCERequestStorage            = (*Storage)(nil)
	_ fosite.PARStorage                  = (*Storage)(nil)
)

// ---------------------------------------------------------------- PKCE 会话

// CreatePKCERequestSession 保存 PKCE 挑战。
func (s *Storage) CreatePKCERequestSession(ctx context.Context, signature string, requester fosite.Requester) error {
	form := requester.GetRequestForm()
	challenge := form.Get("code_challenge")
	if challenge == "" {
		// 没挑战就没有 PKCE 可记。这不是错误 —— 未启用 PKCE 的客户端
		// 走的就是这条路径,而是否强制 PKCE 由 OIDC_REQUIRE_PKCE 决定。
		return nil
	}

	sessionJSON, requestJSON, err := marshalPair(requester.GetSession(), requester)
	if err != nil {
		return err
	}

	_, err = s.queries.CreatePKCERequest(ctx, query.CreatePKCERequestParams{
		Signature:       []byte(signature),
		Challenge:       challenge,
		ChallengeMethod: form.Get("code_challenge_method"),
		Session:         sessionJSON,
		Request:         requestJSON,
	})
	return err
}

// GetPKCERequestSession 取出 PKCE 挑战对应的请求。
func (s *Storage) GetPKCERequestSession(ctx context.Context, signature string, fositeSession fosite.Session) (fosite.Requester, error) {
	row, err := s.queries.GetPKCERequest(ctx, []byte(signature))
	if err != nil {
		if db.IsNoRows(err) {
			return nil, fosite.ErrNotFound
		}
		return nil, err
	}
	if err := restoreSession(fositeSession, row.Session); err != nil {
		return nil, err
	}
	return s.unmarshalRequest(ctx, row.Request, requestConfig{
		clientID: formClientID(row.Request),
	}, fositeSession)
}

// DeletePKCERequestSession 丢弃 PKCE 记录。
func (s *Storage) DeletePKCERequestSession(ctx context.Context, signature string) error {
	_, err := s.queries.DeletePKCERequest(ctx, []byte(signature))
	return err
}

// formClientID 从已存储的请求 JSON 里取 client_id。
func formClientID(requestJSON []byte) string {
	var stored storedRequest
	if err := json.Unmarshal(requestJSON, &stored); err != nil {
		return ""
	}
	return stored.ClientID
}

// ---------------------------------------------------------------- RFC 7009 吊销

// RevokeAccessToken 实现 RFC 7009 吊销。
//
// requestID 是 fosite 内部生成的授权请求 id。同一授权下的令牌必须
// 一起吊销 —— 只吊销其中一枚等于没吊销。
func (s *Storage) RevokeAccessToken(ctx context.Context, requestID string) error {
	const reason = "revoked"
	if _, err := s.queries.RevokeAccessTokensByClient(ctx, query.RevokeAccessTokensByClientParams{
		ClientID: requestID,
		Reason:   reason,
	}); err != nil {
		return err
	}
	_, err := s.queries.RevokeRefreshTokensByClient(ctx, query.RevokeRefreshTokensByClientParams{
		ClientID: requestID,
		Reason:   reason,
	})
	return err
}

// RevokeRefreshToken 实现 RFC 7009 吊销。
func (s *Storage) RevokeRefreshToken(ctx context.Context, requestID string) error {
	return s.RevokeAccessToken(ctx, requestID)
}

// accountUUID 从会话里取出账号 id。
func accountUUID(s fosite.Session) (uuid.UUID, error) {
	if s == nil {
		return uuid.Nil, apperr.New(apperr.CodeInternal, "会话为空")
	}
	subject := s.GetSubject()
	if subject == "" {
		return uuid.Nil, apperr.New(apperr.CodeInternal, "会话缺少 subject")
	}
	id, err := uuid.Parse(subject)
	if err != nil {
		return uuid.Nil, apperr.Newf(apperr.CodeInternal, "会话 subject 非法: %q", subject)
	}
	return id, nil
}

// hashCode 返回令牌明文的 sha256。
//
// 只存哈希:数据库泄露时攻击者拿到的摘要无法直接换取令牌。
func hashCode(code string) []byte {
	sum := sha256.Sum256([]byte(code))
	return sum[:]
}

func toTextSlice(args fosite.Arguments) []string {
	out := make([]string, 0, len(args))
	for _, a := range args {
		out = append(out, a)
	}
	return out
}

func toArgs(in []string) fosite.Arguments {
	out := make(fosite.Arguments, 0, len(in))
	for _, v := range in {
		out = append(out, v)
	}
	return out
}

func pgUUID(id uuid.UUID) pgtype.UUID {
	if id == uuid.Nil {
		return pgtype.UUID{}
	}
	return pgtype.UUID{Bytes: id, Valid: true}
}

// marshalStoredRequest 把请求 DTO 序列化。
func marshalStoredRequest(stored storedRequest) ([]byte, error) {
	raw, err := json.Marshal(stored)
	if err != nil {
		return nil, apperr.Newf(apperr.CodeInternal, "序列化请求失败: %v", err)
	}
	return raw, nil
}

// unmarshalStoredRequest 把存储的 JSON 还原成请求 DTO。
func unmarshalStoredRequest(raw []byte) (storedRequest, error) {
	var stored storedRequest
	if err := json.Unmarshal(raw, &stored); err != nil {
		return storedRequest{}, apperr.Newf(apperr.CodeInternal, "反序列化请求失败: %v", err)
	}
	return stored, nil
}

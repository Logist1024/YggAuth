package oidc

import (
	"context"

	"github.com/google/uuid"

	"github.com/yggauth/yggauth/internal/domain"
	"github.com/yggauth/yggauth/internal/identity/session"
	"github.com/yggauth/yggauth/internal/platform/apperr"
)

// SessionAdapter 把账号内核的会话服务适配成授权服务需要的 SessionStore。
//
// 依赖方向是单向的:授权域依赖身份域,反过来不成立。身份域完全不知道
// OIDC 的存在 —— 它只管签发与校验登录态。
type SessionAdapter struct {
	sessions *session.Service
	accounts AccountLookup
	cookie   CookieConfig
}

// NewSessionAdapter 创建适配器。
func NewSessionAdapter(svc *session.Service, accounts AccountLookup, cookie CookieConfig) *SessionAdapter {
	return &SessionAdapter{sessions: svc, accounts: accounts, cookie: cookie}
}

// 确保适配器满足接口。
var _ SessionStore = (*SessionAdapter)(nil)

// LookupByToken 按明文令牌返回账号信息。
func (a *SessionAdapter) LookupByToken(ctx context.Context, token string) (accountRef, error) {
	if token == "" {
		return accountRef{}, apperr.ErrUnauthorized
	}

	auth, err := a.sessions.Authenticate(ctx, token)
	if err != nil {
		return accountRef{}, err
	}
	acc, err := a.accounts.LookupAccount(ctx, auth.Session.AccountID)
	if err != nil {
		return accountRef{}, err
	}
	return accountRef{
		ID:       auth.Session.AccountID.String(),
		Username: acc.Username,
		Email:    acc.Email,
	}, nil
}

// RevokeToken 按明文令牌吊销会话。
func (a *SessionAdapter) RevokeToken(ctx context.Context, token string) error {
	auth, err := a.sessions.Authenticate(ctx, token)
	if err != nil {
		// 已经失效的会话在登出场景下不算错误 —— 目标状态已经达成
		if apperr.Is(err, apperr.CodeSessionExpired) {
			return nil
		}
		return err
	}
	return a.sessions.Revoke(ctx, auth.Session.ID)
}

// IssueSSO 为一次 SSO 登录签发带 SSO 标识的会话。
func (a *SessionAdapter) IssueSSO(ctx context.Context, accountID, ip, userAgent string) (string, error) {
	id, err := uuid.Parse(accountID)
	if err != nil {
		return "", apperr.New(apperr.CodeInvalidArgument, "账号 ID 非法")
	}

	issued, err := a.sessions.Issue(ctx, id, ip, userAgent)
	if err != nil {
		return "", err
	}

	// 同一次 SSO 登录下的所有站点会话挂在同一个 SSO 会话 id 上,
	// 这样「一处登出、处处失效」只需要吊销一条链
	ssoID, err := uuid.NewV7()
	if err != nil {
		ssoID = uuid.New()
	}
	if err := a.sessions.LinkSSO(ctx, issued.SessionID, ssoID); err != nil {
		return "", err
	}

	return issued.Token, nil
}

// RevokeAllBySSO 吊销同一 SSO 会话下的全部登录态。
func (a *SessionAdapter) RevokeAllBySSO(ctx context.Context, ssoSessionID string) error {
	id, err := uuid.Parse(ssoSessionID)
	if err != nil {
		return apperr.New(apperr.CodeInvalidArgument, "SSO 会话 ID 非法")
	}
	return a.sessions.RevokeBySSO(ctx, id)
}

// AccountLookup 是查账号所需的最小能力。
type AccountLookup interface {
	// LookupAccount 按 id 查账号
	LookupAccount(ctx context.Context, id uuid.UUID) (domain.Account, error)
}

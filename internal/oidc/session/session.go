// Package session 实现 fosite.Session。
//
// 会话内容会被 JSON 序列化后存进数据库,因此**只能包含可序列化的字段**:
// 指针与基础类型可以,接口、函数、channel 不行。
package session

import (
	"encoding/json"
	"time"

	"github.com/ory/fosite"
	"github.com/ory/fosite/handler/openid"
	"github.com/ory/fosite/token/jwt"
)

// DefaultSession 是本项目的 OIDC 会话。
//
// 字段全部导出且可 JSON 序列化,这是它能被存进 oidc.*_token.session
// 列的前提。改字段名会让存量令牌无法还原,等同于一次破坏性变更。
type DefaultSession struct {
	// Username 与 Subject 是 id_token 与 userinfo 的基础声明。
	Username      string `json:"username"`
	Subject       string `json:"subject"`
	Email         string `json:"email"`
	EmailVerified bool   `json:"email_verified"`
	// Claims 是额外的声明,按 OIDC 规范原样进 id_token。
	Claims map[string]any `json:"claims,omitempty"`
	// idClaims 与 idHeaders 供 fosite 的 openid handler 原地填写 id_token。
	// 它们是运行时状态,**不进 JSON** —— 序列化再反序列化会让 fosite
	// 拿到一个空结构体,把已签发的声明覆盖掉。
	idClaims  *jwt.IDTokenClaims `json:"-"`
	idHeaders *jwt.Headers       `json:"-"`

	// ExpiresAtMap 按令牌类型记录各自的过期时间。
	ExpiresAtMap map[fosite.TokenType]time.Time `json:"expires_at"`

	// Username 不再是 subject 时,audience 里会出现它
	Audience []string `json:"audience,omitempty"`
}

// Ensure 实现 fosite.Session 的编译期检查。
var _ fosite.Session = (*DefaultSession)(nil)

// New 返回一个空会话。
func New(subject, username string) *DefaultSession {
	return &DefaultSession{
		Subject:      subject,
		Username:     username,
		ExpiresAtMap: map[fosite.TokenType]time.Time{},
	}
}

// NewWithAccount 按账号信息构造会话。
func NewWithAccount(subject, username, email string, emailVerified bool) *DefaultSession {
	return &DefaultSession{
		Subject:       subject,
		Username:      username,
		Email:         email,
		EmailVerified: emailVerified,
		ExpiresAtMap:  map[fosite.TokenType]time.Time{},
	}
}

// GetExpiresAt 返回指定令牌类型的过期时间。
func (s *DefaultSession) GetExpiresAt(key fosite.TokenType) time.Time {
	if s.ExpiresAtMap == nil {
		return time.Time{}
	}
	if v, ok := s.ExpiresAtMap[key]; ok {
		return v
	}
	return time.Time{}
}

// SetExpiresAt 设置指定令牌类型的过期时间。
func (s *DefaultSession) SetExpiresAt(key fosite.TokenType, exp time.Time) {
	if s.ExpiresAtMap == nil {
		s.ExpiresAtMap = map[fosite.TokenType]time.Time{}
	}
	s.ExpiresAtMap[key] = exp
}

// GetUsername 返回用户名。
func (s *DefaultSession) GetUsername() string { return s.Username }

// GetSubject 返回主体标识。
func (s *DefaultSession) GetSubject() string { return s.Subject }

// SetSubject 设置主体标识。
func (s *DefaultSession) SetSubject(subject string) { s.Subject = subject }

// GetClaims 返回自定义声明。
func (s *DefaultSession) GetClaims() map[string]any {
	if s.Claims == nil {
		return map[string]any{}
	}
	return s.Claims
}

// GetAudience 返回受众。
func (s *DefaultSession) GetAudience() []string { return s.Audience }

// SetAudience 设置受众。
func (s *DefaultSession) SetAudience(aud []string) { s.Audience = aud }

// GetExtra 返回附加字段。
//
// 附加字段用 `ext:<key>` 前缀存取(fosite 的约定)。
// 键名里不能出现点号,否则解析路径会被拆错。
func (s *DefaultSession) GetExtra(key string) any { return s.extra(key) }

// SetExtra 设置附加字段。
func (s *DefaultSession) SetExtra(key string, value any) {
	s.setExtra(key, value)
}

func (s *DefaultSession) extra(key string) any {
	if s.Claims == nil {
		return nil
	}
	return s.Claims[key]
}

func (s *DefaultSession) setExtra(key string, value any) {
	if s.Claims == nil {
		s.Claims = map[string]any{}
	}
	s.Claims[key] = value
}

// Clone 返回一份深拷贝。
func (s *DefaultSession) Clone() fosite.Session {
	c := *s
	if s.ExpiresAtMap != nil {
		c.ExpiresAtMap = make(map[fosite.TokenType]time.Time, len(s.ExpiresAtMap))
		for k, v := range s.ExpiresAtMap {
			c.ExpiresAtMap[k] = v
		}
	}
	if s.Claims != nil {
		c.Claims = make(map[string]any, len(s.Claims))
		for k, v := range s.Claims {
			c.Claims[k] = v
		}
	}
	if s.Audience != nil {
		c.Audience = append([]string(nil), s.Audience...)
	}
	return &c
}

// Restore 用 JSON 数据覆盖当前会话。
func (s *DefaultSession) Restore(data []byte) error {
	return unmarshal(data, s)
}

// unmarshal 是 json.Unmarshal 的薄封装,便于测试替换。
var unmarshal = func(data []byte, v any) error { return json.Unmarshal(data, v) }

// ---------------------------------------------------------------- ID Token

// IDTokenClaims 返回 id_token 的声明集合。
//
// fosite 的 openid handler 会**原地**修改这个结构体再签发,
// 所以必须每次返回同一个指针,不能临时 new 一个。
func (s *DefaultSession) IDTokenClaims() *jwt.IDTokenClaims {
	if s.idClaims == nil {
		// 个人资料声明走 Extra:fosite 会把 Extra 原样并进 id_token,
		// 这也顺带让我们自定义的 Claims 一并带出去。
		s.idClaims = &jwt.IDTokenClaims{
			Subject: s.Subject,
			Extra: map[string]any{
				"name":               s.Username,
				"preferred_username": s.Username,
				"email":              s.Email,
				"email_verified":     s.EmailVerified,
			},
		}
	}
	return s.idClaims
}

// IDTokenHeaders 返回 id_token 的头部。
func (s *DefaultSession) IDTokenHeaders() *jwt.Headers {
	if s.idHeaders == nil {
		s.idHeaders = &jwt.Headers{}
	}
	return s.idHeaders
}

// 确保会话同时满足 fosite 与 openid 的要求。
var _ openid.Session = (*DefaultSession)(nil)

// SetProfile 写入用户身份信息。
//
// 这些字段会进 id_token 与 userinfo。走 extras 存是错的 ——
// GetUsername() 只读 Username 字段,extras 里的同名键不会被取到,
// 于是令牌能正常签发,userinfo 却返回空用户名。
func (s *DefaultSession) SetProfile(subject, username, email string, emailVerified bool) {
	s.Subject = subject
	s.Username = username
	s.Email = email
	s.EmailVerified = emailVerified

	// extras 里同步一份,让 SetExtra/SetSubject 的调用点也能读到
	s.SetExtra("username", username)
	s.SetExtra("email", email)
	s.SetExtra("email_verified", emailVerified)

	// 会话可能被复用(同一进程内多次授权),重建 id_token 声明
	s.idClaims = nil
}

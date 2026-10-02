package httpx

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
)

// Cookie 是设置会话 cookie 的参数。
type Cookie struct {
	Name     string
	Value    string
	Path     string
	Domain   string
	MaxAge   int
	Secure   bool
	HTTPOnly bool
	SameSite http.SameSite
}

// SetCookie 写出一个 cookie。
//
// HTTPOnly 恒为 true:登录态绝不能被 JavaScript 读到,
// 否则一次 XSS 就能把整个会话偷走(docs/09-security.md 3.1)。
func SetCookie(w http.ResponseWriter, c Cookie) {
	path := c.Path
	if path == "" {
		path = "/"
	}
	http.SetCookie(w, &http.Cookie{
		Name:     c.Name,
		Value:    c.Value,
		Path:     path,
		Domain:   c.Domain,
		MaxAge:   c.MaxAge,
		Secure:   c.Secure,
		HttpOnly: true,
		SameSite: c.SameSite,
	})
}

// ClearCookie 让浏览器立即删除 cookie。
//
// MaxAge 置 -1 是告诉浏览器「这个 cookie 已过期」,
// 比只设空值可靠 —— 后者在部分浏览器上会保留一个空值 cookie。
func ClearCookie(w http.ResponseWriter, c Cookie) {
	c.Value = ""
	c.MaxAge = -1
	SetCookie(w, c)
}

// ReadCookie 读取请求里的 cookie 值。
func ReadCookie(r *http.Request, name string) string {
	c, err := r.Cookie(name)
	if err != nil {
		return ""
	}
	return c.Value
}

// BearerToken 从 Authorization 头取 Bearer 令牌。
func BearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if h == "" {
		return ""
	}
	const prefix = "Bearer "
	// 大小写不敏感:规范写的是 Bearer,但有客户端会发 bearer
	if len(h) > len(prefix) && strings.EqualFold(h[:len(prefix)], prefix) {
		return strings.TrimSpace(h[len(prefix):])
	}
	return ""
}

// ErrNoCSRFToken 表示写操作缺少 CSRF 令牌。
var ErrNoCSRFToken = errors.New("缺少 CSRF 令牌")

// CheckSameOrigin 校验写操作的 Origin / Referer。
//
// Cookie 的 SameSite=Lax 已经挡掉了绝大多数跨站请求,
// 这里是第二道防线:有些客户端会把 Origin 置空(此时退回 Referer),
// 两者都没有则拒绝 —— 因为浏览器发起的写操作一定会带其中之一。
func CheckSameOrigin(r *http.Request) error {
	origin := r.Header.Get("Origin")
	if origin != "" && origin != "null" {
		if r.Host != "" && !sameHost(origin, r.Host) {
			return errors.New("跨站请求被拒绝")
		}
		return nil
	}

	referer := r.Header.Get("Referer")
	if referer == "" {
		return ErrNoCSRFToken
	}
	if r.Host != "" && !sameHost(referer, r.Host) {
		return errors.New("跨站请求被拒绝")
	}
	return nil
}

// sameHost 比较 URL 的主机部分与请求的 Host。
func sameHost(rawURL, host string) bool {
	u, err := urlParse(rawURL)
	if err != nil {
		return false
	}
	return strings.EqualFold(u, host)
}

// urlParse 只取 URL 的 host 部分。
func urlParse(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	return u.Host, nil
}

package transport

import (
	"net/http"

	"github.com/yggauth/yggauth/internal/platform/httpx"
)

// PublicConfig 是前端启动时要读的**公开**配置
// (docs/configuration.md §7.2)。
//
// 只放展示与渲染必需的几项:站点名、logo、注册模式、密码最短长度。
// 其余配置(数据库、SMTP、密钥)一概不给 —— 这条端点无需登录。
type PublicConfig struct {
	SiteName                 string `json:"site_name"`
	LogoURL                  string `json:"logo_url"`
	SupportEmail             string `json:"support_email"`
	RegistrationMode         string `json:"registration_mode"`
	PasswordMinLength        int    `json:"password_min_length"`
	RequireEmailVerification bool   `json:"require_email_verification"`
}

// publicConfig 返回站点公开配置。
//
// 站点名称这类展示配置以前是前端硬编码的字符串:后台改了站点名,
// 浏览器标签、后台左栏还是旧的 —— 又一次「改了没用」,而且没有任何报错。
// index.html 里的标题是**构建期**文件,JS 加载后必须用这里的值覆盖它,
// 否则「站点名称」这项设置对 <title> 无效。
func (rt *Router) publicConfig(w http.ResponseWriter, r *http.Request) {
	// 没装配设置存储(纯 env 的测试装配)时退化到 env:
	// 少接一个依赖不该让页面连标题都拿不到。
	fb := rt.publicFallbacks()

	cfg := PublicConfig{
		SiteName:                 fb.siteName,
		LogoURL:                  fb.logoURL,
		SupportEmail:             fb.supportEmail,
		RegistrationMode:         fb.registrationMode,
		PasswordMinLength:        fb.passwordMinLength,
		RequireEmailVerification: true,
	}
	if s := rt.deps.Settings; s != nil {
		ctx := r.Context()
		cfg.SiteName = s.String(ctx, "site.name", fb.siteName)
		cfg.LogoURL = s.String(ctx, "site.logo_url", fb.logoURL)
		cfg.SupportEmail = s.String(ctx, "site.support_email", fb.supportEmail)
		cfg.RegistrationMode = s.String(ctx, "registration.mode", fb.registrationMode)
		cfg.PasswordMinLength = s.Int(ctx, "password.min_length", fb.passwordMinLength)
	}

	httpx.OK(w, cfg)
}

// publicFallbacks 是设置快照缺席时的兜底值,取自 env。
type publicFallbacks struct {
	siteName          string
	logoURL           string
	supportEmail      string
	registrationMode  string
	passwordMinLength int
}

func (rt *Router) publicFallbacks() publicFallbacks {
	fb := publicFallbacks{
		siteName:          "YggAuth",
		registrationMode:  "open",
		passwordMinLength: 8,
	}
	c := rt.deps.Config
	if c == nil {
		return fb
	}
	if c.Mail.FromName != "" {
		fb.siteName = c.Mail.FromName
	}
	if c.Auth.RegistrationMode != "" {
		fb.registrationMode = c.Auth.RegistrationMode
	}
	if c.Auth.PasswordMinLength > 0 {
		fb.passwordMinLength = c.Auth.PasswordMinLength
	}
	return fb
}

package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/yggauth/yggauth/internal/bootstrap"
	"github.com/yggauth/yggauth/internal/config"
	"github.com/yggauth/yggauth/internal/platform/db"
)

// runBootstrap 在迁移之后、监听端口之前执行首启引导(L2)。
//
// 顺序有讲究:
//   - 必须晚于迁移:它要写 identity.* 与 app.setting;
//   - 必须早于任何请求:否则会出现「服务已监听但还没有管理员」的窗口,
//     那期间的注册、登录、后台访问都会得到互相矛盾的结果。
func runBootstrap(ctx context.Context, pool *db.Pool, cfg *config.Config, logger *slog.Logger) error {
	out, err := bootstrap.Ensure(ctx, pool, newHasher(cfg), bootstrap.Options{
		Enabled:      cfg.Bootstrap.Enabled,
		Email:        cfg.Bootstrap.Email,
		Username:     cfg.Bootstrap.Username,
		Password:     cfg.Bootstrap.Password,
		Policy:       newPolicy(cfg),
		LoginEnabled: cfg.Auth.MCLoginDefault,
	})
	if err != nil {
		return fmt.Errorf("首启引导失败: %w", err)
	}

	// 四种结尾 created / skipped / disabled / failed,措辞各不相同:
	// 运维扫一眼日志就能判断「这次启动到底动没动账号」。
	switch out.Kind {
	case bootstrap.KindCreated:
		logger.Info("bootstrap: created default platform admin",
			"username", out.Username,
			"email", out.Email,
			"must_change_password", true)
		// 随机密码只在这里出现一次,且**不进结构化字段**:
		// 字段会被日志采集器索引、长期留存,而这个密码必须随用随弃。
		if out.GeneratedPassword != "" {
			printGeneratedAdmin(out.Username, out.Email, out.GeneratedPassword)
		}
	case bootstrap.KindSkipped:
		logger.Info("bootstrap: skipped, platform admin already exists", "reason", out.Reason)
	case bootstrap.KindDisabled:
		logger.Info("bootstrap: disabled", "reason", out.Reason)
	default:
		// 不可达:Ensure 的失败以 error 返回。留着只为让「四结尾」在一处收口。
		logger.Error("bootstrap: failed", "reason", out.Reason)
		return fmt.Errorf("首启引导失败: %s", out.Reason)
	}

	if out.NextStep != "" {
		logger.Warn("bootstrap: next step", "action", out.NextStep)
	}
	return nil
}

// printGeneratedAdmin 打印一次性横幅。
//
// 用 stderr 而不是结构化日志:这串字符不是「事件」,是给现场的人抄的凭据。
func printGeneratedAdmin(username, email, password string) {
	fmt.Fprintf(os.Stderr, `
============================================================
 首次启动已创建平台管理员 —— 密码只显示这一次,请立即保存
   用户名: %s
   邮  箱: %s
   密  码: %s
 登录后系统会强制修改密码(首登改密)。
============================================================
`, username, email, password)
}

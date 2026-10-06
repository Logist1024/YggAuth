package config

import (
	"fmt"
	"strings"
)

// MailLabels 是同一条邮件约束在两套配置里的**叫法**:
// env 用环境变量名,后台设置用 setting 键名。
//
// 规则只写一份(ValidateMailConfig),叫法由调用方给 ——
// 两套规则各写一份必然漂移:env 里挡住的错,从后台换个写法就放进来了
// (docs/configuration.md §7.3 要求两者共用同一份校验)。
type MailLabels struct {
	Host string
	User string
	From string
}

// EnvMailLabels 是 env 侧的叫法。
var EnvMailLabels = MailLabels{Host: "SMTP_HOST", User: "SMTP_USER", From: "MAILER_FROM"}

// SettingMailLabels 是后台设置侧的叫法。
var SettingMailLabels = MailLabels{Host: "mail.host", User: "mail.user", From: "mail.from"}

// ValidateMailConfig 校验邮件配置的跨字段约束。
//
// transport=smtp 却没有服务器地址或账号,是典型的「保存成功但发信已经坏了」:
// 单看每个键都各自合法,合起来这条路根本走不通,而错误要等到
// 下一个注册用户的验证邮件身上才暴露。
//
// env(config.validate)与后台设置(settings.ValidateUpdate)共用这条函数。
func ValidateMailConfig(transport, from, host, user string, l MailLabels) error {
	if transport == "smtp" {
		if host == "" {
			return fmt.Errorf("%s 未设置(transport=smtp 时必填)", l.Host)
		}
		if user == "" {
			return fmt.Errorf("%s 未设置(transport=smtp 时必填)", l.User)
		}
	}
	if from != "" && !strings.Contains(from, "@") {
		return fmt.Errorf("%s 必须是合法邮箱,当前值: %s", l.From, from)
	}
	return nil
}

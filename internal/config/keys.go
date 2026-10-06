package config

import (
	"encoding/json"
	"fmt"
	"strings"
)

// SettingType 是配置值的类型。
type SettingType string

// SettingType 的取值:整型、布尔、字符串、枚举、敏感值。
// 它们决定一个键在后台渲染成哪种控件、以及怎么校验。
const (
	SettingInt    SettingType = "int"
	SettingBool   SettingType = "bool"
	SettingString SettingType = "string"
	SettingEnum   SettingType = "enum"
	SettingSecret SettingType = "secret"
)

// MaskValue 是敏感配置对外的固定回显。
//
// GET 恒返回它,PATCH 收到它表示「原样保留」——
// 没有这层约定,前端要么只能把密码发回浏览器,要么每改一个别的键
// 就把 SMTP 密码冲成空串。
const MaskValue = "********"

// KeySpec 是一个配置键的登记项。
//
// 键的**规则只登记一次**(docs/configuration.md §6.2):
// 后端按它校验写入,前端按它渲染表单,启动时按它回填 env 种子。
// 任何一份手抄清单都会过期,过期的表现是「后台改了没用」——
// 那正是本方案要消灭的问题。
type KeySpec struct {
	// Key 是 app.setting 的键。
	Key string `json:"key"`
	// Type 决定校验方式与前端控件。
	Type SettingType `json:"type"`
	// Title 是后台展示名。
	Title string `json:"title"`
	// Min/Max 是整型取值范围(含端点)。
	Min int `json:"min,omitempty"`
	Max int `json:"max,omitempty"`
	// Enum 是枚举取值。
	Enum []string `json:"enum,omitempty"`
	// OptionLabels 给枚举值配中文标签。缺省时前端只能显示原始值。
	OptionLabels map[string]string `json:"-"`
	// Unit 是展示单位,例如 "小时"、"秒"、"天"。
	Unit string `json:"unit,omitempty"`
	// Hint 是前端的一句话说明。
	Hint string `json:"hint,omitempty"`
	// Seed 返回该键的 env 种子;返回 nil 表示没有种子
	// (缺行时不回填,例如还没配的 SMTP 密码)。
	Seed func(*Config) json.RawMessage `json:"-"`
	// AffectsSender 表示这个键参与**发件器**的构造
	// (docs/configuration.md §7.3):它一变,后台就要就地重建发件器,
	// 只落库不重建的话,页面显示「保存成功」而发信仍旧按老配置走 ——
	// 又一次「改了没用」,而且这次连个报错都没有。
	AffectsSender bool `json:"-"`
}

// IsSecret 报告该键是否属于敏感值。
func (s KeySpec) IsSecret() bool { return s.Type == SettingSecret }

// registry 是全部可由后台修改的配置键。
//
// 这张表**必须**与 `db/migrations/00004_init_app.sql` 里的种子行一致 ——
// 不一致的表现是「某个键后台能改但改了不生效」或反过来。
// 单元测试 TestRegistryMatchesMigrationSeed 负责盯这条。
var registry = []KeySpec{
	{
		Key: "registration.mode", Type: SettingEnum, Title: "注册模式",
		Enum: []string{"open", "invite_only", "closed"},
		OptionLabels: map[string]string{
			"open": "开放注册", "invite_only": "需要邀请码", "closed": "关闭注册",
		},
		Hint: "closed 会直接拒绝注册;invite_only 需要邀请码",
		Seed: func(c *Config) json.RawMessage { return jsonString(c.Auth.RegistrationMode) },
	},
	{
		Key: "registration.mc_login_default", Type: SettingBool, Title: "新账号默认可 MC 登录",
		Seed: func(c *Config) json.RawMessage { return jsonBool(c.Auth.MCLoginDefault) },
	},
	{
		Key: "password.min_length", Type: SettingInt, Title: "密码最小长度", Min: 8, Max: 128, Unit: "位",
		Seed: func(c *Config) json.RawMessage { return jsonInt(c.Auth.PasswordMinLength) },
	},
	{
		Key: "password.max_length", Type: SettingInt, Title: "密码最大长度", Min: 8, Max: 1024, Unit: "位",
		Seed: func(c *Config) json.RawMessage { return jsonInt(c.Auth.PasswordMaxLength) },
	},
	{
		Key: "password.reject_common", Type: SettingBool, Title: "拒绝常见弱口令",
		Seed: func(c *Config) json.RawMessage { return jsonBool(c.Auth.PasswordRejectCommon) },
	},
	{
		Key: "session.idle_ttl_hours", Type: SettingInt, Title: "会话闲置超时", Min: 1, Max: 8760, Unit: "小时",
		Hint: "超过这个时长没有任何操作就重新登录",
		Seed: func(c *Config) json.RawMessage { return jsonInt(int(c.Auth.SessionIdleTTL.Hours())) },
	},
	{
		Key: "session.max_ttl_hours", Type: SettingInt, Title: "会话最长寿命", Min: 1, Max: 8760, Unit: "小时",
		Hint: "无论有没有操作,到期必须重新登录",
		Seed: func(c *Config) json.RawMessage { return jsonInt(int(c.Auth.SessionMaxTTL.Hours())) },
	},
	{
		Key: "login.max_failed_attempts", Type: SettingInt, Title: "登录失败锁定阈值", Min: 1, Max: 100, Unit: "次",
		Seed: func(c *Config) json.RawMessage { return jsonInt(c.Auth.LoginMaxFailedAttempts) },
	},
	{
		Key: "login.lock_seconds", Type: SettingInt, Title: "账号锁定时长", Min: 0, Max: 86400, Unit: "秒",
		Hint: "0 表示只记失败次数不锁定账号",
		Seed: func(c *Config) json.RawMessage { return jsonInt(int(c.Auth.LoginLockDuration.Seconds())) },
	},
	{
		Key: "mail.verify_cooldown_seconds", Type: SettingInt, Title: "验证邮件重发冷却", Min: 0, Max: 86400, Unit: "秒",
		Seed: func(c *Config) json.RawMessage { return jsonInt(int(c.Mail.VerifyCooldown.Seconds())) },
	},
	{
		Key: "mail.verify_daily_limit", Type: SettingInt, Title: "单账号每日验证邮件上限", Min: 0, Max: 1000, Unit: "封",
		Seed: func(c *Config) json.RawMessage { return jsonInt(c.Mail.VerifyDailyLimit) },
	},
	{
		Key: "mc.name_retention_days", Type: SettingInt, Title: "改名后旧名保留", Min: 0, Max: 3650, Unit: "天",
		Hint: "期间禁止其他人注册这个旧名",
		Seed: func(c *Config) json.RawMessage { return jsonInt(c.MC.NameRetentionDays) },
	},

	// ---- 站点展示(docs/configuration.md 7.1)。这三条以前没有键可写,
	// 「站点名称」要改只能改源码里的字符串 —— 于是它其实是个常量。
	{
		Key: "site.name", Type: SettingString, Title: "站点名称",
		Hint: "浏览器标题、前台页头与后台左栏都用它",
		Seed: func(c *Config) json.RawMessage {
			name := c.Mail.FromName
			if name == "" {
				name = "YggAuth"
			}
			return jsonString(name)
		},
	},
	{
		Key: "site.logo_url", Type: SettingString, Title: "站点 Logo 地址",
		Hint: "留空则不显示",
	},
	{
		Key: "site.support_email", Type: SettingString, Title: "联系邮箱",
		Hint: "显示在登录页底部;留空则不显示",
	},

	// ---- 发件邮箱(docs/configuration.md 7.1)。SMTP 参数进 L1 的正当理由是
	// 「改完不用重启容器」:以前配错一个端口就得重启一次来确认还配错。
	//
	// 字符串键用 strSeed:**空 = 这项没设 = 不种这一行**。种子必须能通过
	// 登记表校验 —— 手搓的测试配置没走 config.Load,枚举是空串、端口是 0,
	// 拿它当种子会让 Sync 整轮失败,一个字段的问题变成所有配置都读不出来。
	{
		Key: "mail.transport", Type: SettingEnum, Title: "邮件传输方式",
		Enum: []string{"console", "smtp"},
		OptionLabels: map[string]string{
			"console": "console(写日志,不真发信)",
			"smtp":    "SMTP 服务器",
		},
		Seed:          strSeed(func(c *Config) string { return c.Mail.Transport }),
		AffectsSender: true,
	},
	{
		Key: "mail.from", Type: SettingString, Title: "发件人地址",
		Hint:          "如 noreply@example.com;必须含 @",
		Seed:          strSeed(func(c *Config) string { return c.Mail.From }),
		AffectsSender: true,
	},
	{
		Key: "mail.from_name", Type: SettingString, Title: "发件人显示名",
		Seed:          strSeed(func(c *Config) string { return c.Mail.FromName }),
		AffectsSender: true,
	},
	{
		Key: "mail.host", Type: SettingString, Title: "SMTP 服务器",
		Hint:          "transport=smtp 时必填",
		Seed:          strSeed(func(c *Config) string { return c.Mail.SMTPHost }),
		AffectsSender: true,
	},
	{
		Key: "mail.port", Type: SettingInt, Title: "SMTP 端口", Min: 1, Max: 65535,
		Seed: func(c *Config) json.RawMessage {
			if c.Mail.SMTPPort == 0 {
				return nil // 没设:0 不在 1–65535 里,当种子会被 Sync 拒掉
			}
			return jsonInt(c.Mail.SMTPPort)
		},
		AffectsSender: true,
	},
	{
		Key: "mail.user", Type: SettingString, Title: "SMTP 用户名",
		Hint:          "transport=smtp 时必填",
		Seed:          strSeed(func(c *Config) string { return c.Mail.SMTPUsername }),
		AffectsSender: true,
	},
	{
		Key: "mail.tls", Type: SettingBool, Title: "隐式 TLS(465 端口)",
		Hint:          "587 端口不勾这个,走明文连接 + STARTTLS",
		Seed:          func(c *Config) json.RawMessage { return jsonBool(c.Mail.SMTPTLS) },
		AffectsSender: true,
	},
	{
		// 敏感键:库里是 `enc:v1:` 开头的密文,对外恒回显 ********
		Key: "mail.password", Type: SettingSecret, Title: "SMTP 密码",
		Hint: "用 KEY_MASTER_SECRET 加密存储,任何接口都读不到明文",
		Seed: func(c *Config) json.RawMessage {
			if c.Mail.SMTPPassword == "" {
				return nil
			}
			return jsonString(c.Mail.SMTPPassword)
		},
		AffectsSender: true,
	},
}

// Lookup 按键取登记项。
func Lookup(key string) (KeySpec, bool) {
	for i := range registry {
		if registry[i].Key == key {
			return registry[i], true
		}
	}
	return KeySpec{}, false
}

// Registry 返回全部登记项(只读副本,防调用方改坏全局表)。
func Registry() []KeySpec {
	out := make([]KeySpec, len(registry))
	copy(out, registry)
	return out
}

// Validate 校验「某个键能不能取某个值」,并返回它的登记项。
//
// 未知键与非法值都返回参数错误(400)。原先 UpdateSetting 接受任意键任意值,
// 前端也承认会写出垃圾键 —— 那种键一旦落库就再也没人认领:
// 改不动、删不掉、也没人读它,还会被 §6.5 的差异日志天天念叨。
func Validate(key string, value json.RawMessage) (KeySpec, error) {
	spec, ok := Lookup(key)
	if !ok {
		return KeySpec{}, fmt.Errorf("未知配置键 %q:只能写入登记过的键", key)
	}
	if !json.Valid(value) {
		return spec, fmt.Errorf("配置值不是合法 JSON")
	}

	switch spec.Type {
	case SettingInt:
		var v float64
		if err := json.Unmarshal(value, &v); err != nil {
			return spec, fmt.Errorf("%s 需要整数", spec.Key)
		}
		if v != float64(int(v)) {
			return spec, fmt.Errorf("%s 需要整数", spec.Key)
		}
		if int(v) < spec.Min || int(v) > spec.Max {
			return spec, fmt.Errorf("%s 取值范围 %d–%d", spec.Key, spec.Min, spec.Max)
		}
	case SettingBool:
		var v bool
		if err := json.Unmarshal(value, &v); err != nil {
			return spec, fmt.Errorf("%s 需要布尔值 true/false", spec.Key)
		}
	case SettingEnum:
		var v string
		if err := json.Unmarshal(value, &v); err != nil {
			return spec, fmt.Errorf("%s 需要字符串", spec.Key)
		}
		if !contains(spec.Enum, v) {
			return spec, fmt.Errorf("%s 只能取 %s", spec.Key, strings.Join(spec.Enum, " / "))
		}
	case SettingSecret:
		var v string
		if err := json.Unmarshal(value, &v); err != nil {
			return spec, fmt.Errorf("%s 需要字符串", spec.Key)
		}
	default:
		var v string
		if err := json.Unmarshal(value, &v); err != nil {
			return spec, fmt.Errorf("%s 需要字符串", spec.Key)
		}
	}
	return spec, nil
}

// CheckPasswordBounds 复核密码长度上下界。
//
// min > max 时**整个注册链路会全灭**:每一个密码都会先撞上
// 「太长」再撞上「太短」,而单键校验对这种情况无能为力 ——
// 两个键各自都在自己的取值范围内。
func CheckPasswordBounds(minimum, maximum int) error {
	if minimum > maximum {
		return fmt.Errorf("password.min_length(%d) 不能大于 password.max_length(%d)", minimum, maximum)
	}
	return nil
}

// Schema 返回下发给前端的键定义。
//
// 前端 `SettingsView.vue` 原先手抄了一张 META 表 —— 后端加一个键,
// 前端不改就显示不出来;规则只有一份真源。
func Schema() []map[string]any {
	out := make([]map[string]any, 0, len(registry))
	for _, s := range registry {
		row := map[string]any{
			"key":   s.Key,
			"type":  string(s.Type),
			"title": s.Title,
		}
		if s.Min != 0 || s.Max != 0 {
			row["min"] = s.Min
			row["max"] = s.Max
		}
		if len(s.Enum) > 0 {
			// 枚举连标签一起下发:让前端去猜 "invite_only" 是什么意思,
			// 等于把一张标签映射表复制到前端 —— 又一份会过期的清单。
			opts := make([]map[string]string, 0, len(s.Enum))
			for _, v := range s.Enum {
				label, ok := s.OptionLabels[v]
				if !ok {
					label = v
				}
				opts = append(opts, map[string]string{"value": v, "label": label})
			}
			row["enum"] = opts
		}
		if s.Unit != "" {
			row["unit"] = s.Unit
		}
		if s.Hint != "" {
			row["hint"] = s.Hint
		}
		out = append(out, row)
	}
	return out
}

func contains(list []string, v string) bool {
	for _, item := range list {
		if item == v {
			return true
		}
	}
	return false
}

func jsonString(v string) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func jsonInt(v int) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func jsonBool(v bool) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

// strSeed 从 env 取一个**非空**字符串当种子;空 = 这项没设,不种这一行。
//
// 为什么不是「空也种进去」:种子必须先过登记表校验。零值 Config
// (测试里手搓的那些,没走 config.Load)会给枚举留下空串、给端口留下 0,
// 拿它当种子会让 Sync 整轮失败 —— 一个字段的问题,变成所有配置都读不出来。
func strSeed(get func(*Config) string) func(*Config) json.RawMessage {
	return func(c *Config) json.RawMessage {
		v := get(c)
		if v == "" {
			return nil
		}
		return jsonString(v)
	}
}

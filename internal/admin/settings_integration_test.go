//go:build integration

package admin_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/yggauth/yggauth/internal/platform/apperr"
	"github.com/yggauth/yggauth/internal/platform/mailer"
)

// TestSettingChangesTakeEffectWithoutRestart 是 docs/configuration.md
// §6.2 的验收用例 1、2、6:**全程不重启**,改设置 → 接口立刻按新值走。
//
// 这条测试盯的正是被修掉的那类 bug —— 「后台改了没用」。
// 旧实现里 `/api/auth/policy` 是构造时取的快照,密码策略也是装配时读的 env,
// 所以改完设置这两个端点会继续返回旧值,而没有任何地方报错。
func TestSettingChangesTakeEffectWithoutRestart(t *testing.T) {
	e := newEnv(t)
	cookie := makeAdmin(t, e)

	readPolicy := func() (minLength int, mode string) {
		t.Helper()
		rec, body := call(t, e.handler, http.MethodGet, "/api/auth/policy", nil, nil)
		require.Equal(t, http.StatusOK, rec.Code)
		var policy struct {
			PasswordMinLength int    `json:"password_min_length"`
			RegistrationMode  string `json:"registration_mode"`
		}
		require.NoError(t, json.Unmarshal(body.Data, &policy))
		return policy.PasswordMinLength, policy.RegistrationMode
	}

	minLength, mode := readPolicy()
	require.Equal(t, 8, minLength, "迁移种子里 password.min_length 应为 8")
	require.Equal(t, "open", mode)

	// ---- 热改密码策略:12 位之后,8 位密码应当被拒
	rec, _ := call(t, e.handler, http.MethodPatch, "/api/admin/settings",
		map[string]any{"password.min_length": 12}, []*http.Cookie{cookie})
	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())

	got, _ := readPolicy()
	require.Equal(t, 12, got, "改完设置,policy 端点必须立即返回新值(未重启)")

	rec, body := call(t, e.handler, http.MethodPost, "/api/auth/register",
		map[string]string{
			"username": "hot_" + uuid.NewString()[:6],
			"email":    "hot-" + uuid.NewString()[:8] + "@example.com",
			"password": "too-short12", // 11 位 < 12
		}, nil)
	require.Equal(t, http.StatusBadRequest, rec.Code, "body=%s", rec.Body.String())
	require.Equal(t, int(apperr.CodeWeakPassword), body.Code)

	// ---- 热关注册:接口本身必须拒绝,藏起前端入口不算关闭
	rec, _ = call(t, e.handler, http.MethodPatch, "/api/admin/settings",
		map[string]any{"registration.mode": "closed"}, []*http.Cookie{cookie})
	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())

	_, mode = readPolicy()
	require.Equal(t, "closed", mode)

	rec, body = call(t, e.handler, http.MethodPost, "/api/auth/register",
		map[string]string{
			"username": "closed_" + uuid.NewString()[:6],
			"email":    "closed-" + uuid.NewString()[:8] + "@example.com",
			"password": "correct-horse-battery",
		}, nil)
	require.Equal(t, http.StatusForbidden, rec.Code, "body=%s", rec.Body.String())
	require.Equal(t, int(apperr.CodeRegistrationClosed), body.Code)
}

// TestSettingsRejectUnknownAndInvalidValues 校验登记表真的把关:
// 未知键、类型错、越界、跨键矛盾一律 400,且**不落库**。
func TestSettingsRejectUnknownAndInvalidValues(t *testing.T) {
	e := newEnv(t)
	cookie := makeAdmin(t, e)

	cases := []struct {
		name string
		req  map[string]any
	}{
		{"未登记的键", map[string]any{"nope.key": 1}},
		{"类型错", map[string]any{"password.min_length": "abc"}},
		{"低于下界", map[string]any{"password.min_length": 3}},
		{"枚举取值非法", map[string]any{"registration.mode": "semi_open"}},
		{"跨键矛盾", map[string]any{"password.min_length": 200}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec, body := call(t, e.handler, http.MethodPatch, "/api/admin/settings",
				tc.req, []*http.Cookie{cookie})
			require.Equal(t, http.StatusBadRequest, rec.Code, "body=%s", rec.Body.String())
			require.Equal(t, int(apperr.CodeInvalidArgument), body.Code)
		})
	}

	// 被拒的写入不该留下痕迹:库里还是迁移种子。
	raw := ""
	require.NoError(t, e.pool.QueryRow(t.Context(),
		`SELECT value::text FROM app.setting WHERE key = 'password.min_length'`).
		Scan(&raw))
	require.JSONEq(t, "8", raw, "校验失败的请求不能落库")
}

// TestSettingSecretNeverLeavesTheServer 是 §6.4 的验收:
// SMTP 密码加密入库、接口只回显掩码、审计里也只有掩码。
func TestSettingSecretNeverLeavesTheServer(t *testing.T) {
	e := newEnv(t)
	cookie := makeAdmin(t, e)
	const secret = "super-secret-smtp-password"

	rec, body := call(t, e.handler, http.MethodPatch, "/api/admin/settings",
		map[string]any{"mail.password": secret}, []*http.Cookie{cookie})
	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())
	var updated struct {
		Settings map[string]json.RawMessage `json:"settings"`
	}
	require.NoError(t, json.Unmarshal(body.Data, &updated))
	require.NotContains(t, string(updated.Settings["mail.password"]), secret,
		"写入响应不能把明文回显回来")

	// GET 只给掩码
	rec, body = call(t, e.handler, http.MethodGet, "/api/admin/settings", nil,
		[]*http.Cookie{cookie})
	require.Equal(t, http.StatusOK, rec.Code)
	var list struct {
		Settings map[string]json.RawMessage `json:"settings"`
	}
	require.NoError(t, json.Unmarshal(body.Data, &list))
	require.JSONEq(t, `"********"`, string(list.Settings["mail.password"]))

	// 库里是密文
	var stored string
	require.NoError(t, e.pool.QueryRow(t.Context(),
		`SELECT value::text FROM app.setting WHERE key = 'mail.password'`).
		Scan(&stored))
	require.True(t, strings.HasPrefix(strings.Trim(stored, `"`), "enc:v1:"),
		"敏感值必须加密入库,实际=%s", stored)

	// 审计里只有掩码
	rec, _ = call(t, e.handler, http.MethodGet, "/api/admin/audit?limit=200", nil,
		[]*http.Cookie{cookie})
	require.Equal(t, http.StatusOK, rec.Code)
	require.NotContains(t, rec.Body.String(), secret, "审计日志不得出现明文密码")
	require.Contains(t, rec.Body.String(), "setting.updated")

	// 原样回显掩码 = 不修改:库里的密文保持不变
	rec, _ = call(t, e.handler, http.MethodPatch, "/api/admin/settings",
		map[string]any{"mail.password": "********"}, []*http.Cookie{cookie})
	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())
	var after string
	require.NoError(t, e.pool.QueryRow(t.Context(),
		`SELECT value::text FROM app.setting WHERE key = 'mail.password'`).
		Scan(&after))
	require.Equal(t, stored, after, "回显掩码不该改写库里的值")

	// 消费方仍能解开它(邮件发送走的就是这条路)
	require.Equal(t, secret, e.settings.SecretString(t.Context(), "mail.password"))
}

// ---------------------------------------------------------------- 发件器热替换

// recordingMailer 是后台的发件器热替换桩:记录每一次重建与收到的配置,
// 也能注入失败,用来验证「重建失败保留旧配置」这条约定。
type recordingMailer struct {
	mu    sync.Mutex
	calls []mailer.Config
	fail  error
}

func (m *recordingMailer) Reload(cfg mailer.Config) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, cfg)
	return m.fail
}

func (m *recordingMailer) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.calls)
}

func (m *recordingMailer) last() mailer.Config {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.calls) == 0 {
		return mailer.Config{}
	}
	return m.calls[len(m.calls)-1]
}

// TestMailCrossKeyValidationAndHotReload 覆盖 docs/configuration.md §7.3:
//
//   - transport=smtp 却没有服务器/账号:每个键单独看都合法,合起来发不了信 → 400,
//     而且**校验失败不许动发件器**(动了就成了「改了一半」);
//   - 三件套齐了 → 200,并**立刻**按新值重建发件器 —— 只落库不重建的话,
//     页面显示「保存成功」而发信仍旧按老配置走,又一次「改了没用」;
//   - 改与发件无关的键(站点名)→ 不触发重建,免得每次改配置都白白换一次实现。
func TestMailCrossKeyValidationAndHotReload(t *testing.T) {
	e := newEnv(t)
	cookie := makeAdmin(t, e)

	rec, body := call(t, e.handler, http.MethodPatch, "/api/admin/settings",
		map[string]any{"mail.transport": "smtp"}, []*http.Cookie{cookie})
	require.Equal(t, http.StatusBadRequest, rec.Code, "body=%s", rec.Body.String())
	require.Equal(t, int(apperr.CodeInvalidArgument), body.Code)
	require.Contains(t, body.Message, "mail.host")
	require.Zero(t, e.mailer.count(), "校验失败不该触发发件器重建")

	rec, body = call(t, e.handler, http.MethodPatch, "/api/admin/settings",
		map[string]any{
			"mail.transport": "smtp",
			"mail.host":      "smtp.example.com",
			"mail.user":      "mailer@example.com",
		}, []*http.Cookie{cookie})
	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())
	require.Equal(t, 1, e.mailer.count(), "mail.* 改了要立刻重建发件器")

	got := e.mailer.last()
	require.Equal(t, "smtp", got.Transport)
	require.Equal(t, "smtp.example.com", got.SMTPHost)
	require.Equal(t, "mailer@example.com", got.Username)
	require.Equal(t, 587, got.SMTPPort, "没配过的键回退到 env 默认值")
	require.Equal(t, "noreply@localhost", got.From)

	// 站点名与发件器无关:不该触发重建
	rec, body = call(t, e.handler, http.MethodPatch, "/api/admin/settings",
		map[string]any{"site.name": "晨星账号"}, []*http.Cookie{cookie})
	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())
	require.Equal(t, 1, e.mailer.count(), "非发件器键不该触发重建")
	require.Equal(t, "晨星账号", e.settings.String(t.Context(), "site.name", ""))

	// GET 回显的是掩码/现值,顺便确认 mail.host 落库了
	rec, body = call(t, e.handler, http.MethodGet, "/api/admin/settings", nil,
		[]*http.Cookie{cookie})
	require.Equal(t, http.StatusOK, rec.Code)
	var list struct {
		Settings map[string]json.RawMessage `json:"settings"`
	}
	require.NoError(t, json.Unmarshal(body.Data, &list))
	require.JSONEq(t, `"smtp.example.com"`, string(list.Settings["mail.host"]))
}

// TestSendTestMailReportsStage 覆盖 docs/configuration.md §7.4:
// 后台能用**已保存的**配置发一封测试信,并把失败钉在具体阶段
// (connect / tls / auth / send)—— 「发送失败」四个字什么也没说。
func TestSendTestMailReportsStage(t *testing.T) {
	e := newEnv(t)
	cookie := makeAdmin(t, e)

	// 收件地址不合法 → 挡在门外,不去连 SMTP
	rec, body := call(t, e.handler, http.MethodPost, "/api/admin/settings/mail/test",
		map[string]any{"to": "not-an-email"}, []*http.Cookie{cookie})
	require.Equal(t, http.StatusBadRequest, rec.Code, "body=%s", rec.Body.String())
	require.Contains(t, body.Message, "收件邮箱")

	// console 模式:不真发信,但流程走通;提醒要写清楚,否则
	// 「成功了」会被读成「收件箱里有信」
	rec, body = call(t, e.handler, http.MethodPost, "/api/admin/settings/mail/test",
		map[string]any{"to": "admin@example.com"}, []*http.Cookie{cookie})
	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())
	var res struct {
		Transport string `json:"transport"`
		Stage     string `json:"stage"`
		OK        bool   `json:"ok"`
		Error     string `json:"error"`
		Note      string `json:"note"`
	}
	require.NoError(t, json.Unmarshal(body.Data, &res), "data=%s", body.Data)
	require.True(t, res.OK, "error=%s", res.Error)
	require.Equal(t, "console", res.Transport)
	require.Equal(t, "send", res.Stage)
	require.Contains(t, res.Note, "console")

	// 谁在什么时候测了发信,要有据可查
	rec, _ = call(t, e.handler, http.MethodGet, "/api/admin/audit?limit=200", nil,
		[]*http.Cookie{cookie})
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "mail.test")
}

//go:build integration

package identity_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/yggauth/yggauth/internal/bootstrap"
	"github.com/yggauth/yggauth/internal/identity/account"
	"github.com/yggauth/yggauth/internal/platform/apperr"
)

// mustChangeData 是登录 / 自举接口里与强制改密相关的字段。
type mustChangeData struct {
	MustChangePassword bool `json:"must_change_password"`
}

// 必须与 newEnv 里的参数一致:引导用这份参数生成哈希,
// env.svc 用它验证 —— 参数不一致,登录就会莫名失败。
func envHasher() *account.Hasher {
	return account.NewHasher(account.Params{
		MemoryKiB: 8 * 1024, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32,
	})
}

func envPolicy() account.Policy {
	return account.Policy{
		MinLength:       8,
		MaxLength:       128,
		RejectCommon:    true,
		CommonPasswords: account.CommonPasswordSet(),
	}
}

// 首登强制改密的完整链路:真源(凭据行) → 投影(会话行) →
// 自举接口把它告诉前端 → 改密把两者都清干净。
//
// 拦截本身(哪些路径被挡、挡成 20013)由 internal/transport 的单元测试覆盖 ——
// 那里能直接断言「下游 handler 没有被执行」;这里验证的是只有真库才能证明的部分。
func TestMustChangePasswordEndToEnd(t *testing.T) {
	e := newEnv(t)
	ctx := t.Context()

	// ---------------------------------------------------------------- 首启引导
	out, err := bootstrap.Ensure(ctx, e.pool, envHasher(), bootstrap.Options{
		Enabled:      true,
		Email:        "admin@example.com",
		Username:     "admin",
		Policy:       envPolicy(),
		LoginEnabled: true,
	})
	require.NoError(t, err)
	require.Equal(t, bootstrap.KindCreated, out.Kind)
	require.NotEmpty(t, out.GeneratedPassword)

	// ---------------------------------------------------------------- 登录
	rec, body := do(t, e.handler, http.MethodPost, "/api/auth/login", map[string]string{
		"email":    "admin@example.com",
		"password": out.GeneratedPassword,
	}, nil)
	require.Equal(t, http.StatusOK, rec.Code, "登录失败: %s", rec.Body.String())
	var loginData mustChangeData
	require.NoError(t, json.Unmarshal(body.Data, &loginData))
	require.True(t, loginData.MustChangePassword, "登录响应必须直接说明「先去改密」")

	cookie := findCookie(rec.Result().Cookies(), "ygg_session")
	require.NotNil(t, cookie, "登录必须下发会话 cookie")
	cookies := []*http.Cookie{cookie}

	// 投影确实落到了会话行上 —— 认证中间件读的就是这一行
	var projected bool
	require.NoError(t, e.pool.QueryRow(ctx, `
		SELECT s.must_change_password
		FROM identity.session s
		ORDER BY s.created_at DESC
		LIMIT 1`).Scan(&projected))
	require.True(t, projected, "会话行上的投影必须落库,否则重新登录就绕过去了")

	// ---------------------------------------------------------------- 自举接口
	// 页面刷新时登录响应早没了,靠这两个接口恢复旗标:
	// 一个给账号站,一个给管理后台(它启动时只能访问放行名单里的接口)。
	rec, body = do(t, e.handler, http.MethodGet, "/api/auth/session", nil, cookies)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var sessionData mustChangeData
	require.NoError(t, json.Unmarshal(body.Data, &sessionData))
	require.True(t, sessionData.MustChangePassword)

	rec, body = do(t, e.handler, http.MethodGet, "/api/account/", nil, cookies)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var meData mustChangeData
	require.NoError(t, json.Unmarshal(body.Data, &meData))
	require.True(t, meData.MustChangePassword)

	// ---------------------------------------------------------------- 改密码
	// 走的正是放行名单里的那个接口 —— 没有它,用户会被自己锁在闸门这一侧。
	const newPassword = "Str0ng-New-Pass!234"
	rec, body = do(t, e.handler, http.MethodPatch, "/api/account/password", map[string]string{
		"old_password": out.GeneratedPassword,
		"new_password": newPassword,
	}, cookies)
	require.Equal(t, http.StatusOK, rec.Code, "改密失败: %s", rec.Body.String())
	require.Equal(t, int(apperr.CodeOK), body.Code)

	// 真源清了
	var mustChange bool
	require.NoError(t, e.pool.QueryRow(ctx, `
		SELECT c.must_change
		FROM identity.credential c
		JOIN identity.account a ON a.id = c.account_id
		WHERE a.username = 'admin' AND c.algo = 'argon2id'`).Scan(&mustChange))
	require.False(t, mustChange, "改密必须清掉凭据行上的真源旗标")

	// 旧会话全部失效:改密码却让旧登录态继续可用,等于「改了没生效」
	rec, _ = do(t, e.handler, http.MethodGet, "/api/account/", nil, cookies)
	require.Equal(t, http.StatusUnauthorized, rec.Code, "改密后旧会话必须立即失效")

	// ---------------------------------------------------------------- 再登录
	rec, body = do(t, e.handler, http.MethodPost, "/api/auth/login", map[string]string{
		"email":    "admin@example.com",
		"password": newPassword,
	}, nil)
	require.Equal(t, http.StatusOK, rec.Code, "新密码登录失败: %s", rec.Body.String())
	var again mustChangeData
	require.NoError(t, json.Unmarshal(body.Data, &again))
	require.False(t, again.MustChangePassword, "改过一次之后不该再被要求改密")

	cookies = []*http.Cookie{findCookie(rec.Result().Cookies(), "ygg_session")}
	rec, body = do(t, e.handler, http.MethodGet, "/api/auth/session", nil, cookies)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NoError(t, json.Unmarshal(body.Data, &again))
	require.False(t, again.MustChangePassword, "会话行上的投影也必须清掉")
}

// 普通注册用户不带旗标:这条新机制不能给正常注册流程添任何麻烦。
func TestNewlyRegisteredAccountDoesNotMustChangePassword(t *testing.T) {
	e := newEnv(t)

	rec, body := do(t, e.handler, http.MethodPost, "/api/auth/register", map[string]string{
		"username": "player_one",
		"email":    "user@example.com",
		"password": "correct-horse-battery",
	}, nil)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	require.Equal(t, int(apperr.CodeOK), body.Code)

	// 先验证邮箱:未验证时登录会以「邮箱尚未验证」409 挡回来,
	// 那样这例就什么都没证明。
	var regData struct {
		VerifyURL string `json:"verify_url"`
	}
	require.NoError(t, json.Unmarshal(body.Data, &regData))
	token := regData.VerifyURL[strings.Index(regData.VerifyURL, "token=")+len("token="):]
	rec, body = do(t, e.handler, http.MethodPost, "/api/auth/email/verify", map[string]string{"token": token}, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	rec, body = do(t, e.handler, http.MethodPost, "/api/auth/login", map[string]string{
		"email":    "user@example.com",
		"password": "correct-horse-battery",
	}, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var data mustChangeData
	require.NoError(t, json.Unmarshal(body.Data, &data))
	require.False(t, data.MustChangePassword, "注册出来的账号不该被要求改密")
}

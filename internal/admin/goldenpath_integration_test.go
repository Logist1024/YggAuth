//go:build integration

package admin_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/yggauth/yggauth/internal/platform/apperr"
)

// TestGoldenPathBackend 是 docs/configuration.md §8 黄金路径的
// **后端部分**(步骤 3/4/5/6)。
//
// 本机没有 Docker daemon,真容器的步骤 1/2/7 由
// `deploy/test/golden-path.sh` 在有 Docker 的机器上跑(脚本里如实标注)。
//
// 为什么把 3→6 串成一条用例而不是沿用各处单测:单测各自都绿,
// 合起来仍可能在中间某一步断掉 —— 「黄金路径」这个词需要一个
// 可执行的定义,而不是四条互不相干的断言。
func TestGoldenPathBackend(t *testing.T) {
	e := newEnv(t)

	// ---- 步骤 3:管理员登录,读得到受保护的后台数据
	cookie := makeAdmin(t, e)
	rec, body := call(t, e.handler, http.MethodGet, "/api/admin/accounts", nil,
		[]*http.Cookie{cookie})
	require.Equal(t, http.StatusOK, rec.Code, "登录后台读账号列表失败: %s", rec.Body.String())
	require.Equal(t, int(apperr.CodeOK), body.Code)

	// ---- 步骤 4:改站点名 → 公开配置立刻是新值,全程不重启
	rec, body = call(t, e.handler, http.MethodPatch, "/api/admin/settings",
		map[string]any{"site.name": "晨星账号"}, []*http.Cookie{cookie})
	require.Equal(t, http.StatusOK, rec.Code, "改站点名失败: %s", rec.Body.String())
	require.Equal(t, "晨星账号", publicConfigOf(t, e.handler)["site_name"],
		"站点名没有立刻生效(前端 <title> 与后台左栏都读这个端点)")

	// ---- 步骤 5:配 mail.* → 就地重建发件器 → 发测试信,阶段全绿
	rec, body = call(t, e.handler, http.MethodPatch, "/api/admin/settings",
		map[string]any{
			"mail.transport": "console",
			"mail.from":      "noreply@example.com",
		}, []*http.Cookie{cookie})
	require.Equal(t, http.StatusOK, rec.Code, "配发件邮箱失败: %s", rec.Body.String())
	require.Equal(t, 1, e.mailer.count(), "mail.* 改了要立刻重建发件器")
	require.Equal(t, "noreply@example.com", e.mailer.last().From,
		"重建用的不是刚保存的值")

	rec, body = call(t, e.handler, http.MethodPost, "/api/admin/settings/mail/test",
		map[string]any{"to": "ops@example.com"}, []*http.Cookie{cookie})
	require.Equal(t, http.StatusOK, rec.Code, "测试信失败: %s", rec.Body.String())
	var mail struct {
		OK    bool   `json:"ok"`
		Stage string `json:"stage"`
		Error string `json:"error"`
	}
	require.NoError(t, json.Unmarshal(body.Data, &mail))
	require.True(t, mail.OK, "测试信没走通(stage=%s): %s", mail.Stage, mail.Error)
	require.Equal(t, "send", mail.Stage)

	// ---- 步骤 6:关闭注册 → 公开配置、策略端点、注册接口三处一致
	rec, body = call(t, e.handler, http.MethodPatch, "/api/admin/settings",
		map[string]any{"registration.mode": "closed"}, []*http.Cookie{cookie})
	require.Equal(t, http.StatusOK, rec.Code, "关注册失败: %s", rec.Body.String())

	require.Equal(t, "closed", publicConfigOf(t, e.handler)["registration_mode"],
		"公开配置没反映关闭状态,登录页拿不到它")
	rec, _ = call(t, e.handler, http.MethodGet, "/api/auth/policy", nil, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"registration_mode":"closed"`)

	rec, body = call(t, e.handler, http.MethodPost, "/api/auth/register", map[string]string{
		"username": "latecomer_" + "golden",
		"email":    "latecomer@example.com",
		"password": "correct-horse-battery",
	}, nil)
	require.Equal(t, http.StatusForbidden, rec.Code, "注册端点没有被拒: %s", rec.Body.String())
	require.Equal(t, 20014, body.Code, "关闭注册要回专用错误码,而不是笼统的 400")

	// 收尾确认:前面改的东西都还在 —— 全程没有重启过
	data := publicConfigOf(t, e.handler)
	require.Equal(t, "晨星账号", data["site_name"])
	require.Equal(t, "closed", data["registration_mode"])
}

// publicConfigOf 读公开配置端点(无需登录)。
func publicConfigOf(t *testing.T, h http.Handler) map[string]any {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/public/config", nil))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var env struct {
		Code int            `json:"code"`
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env), rec.Body.String())
	require.Equal(t, 0, env.Code, rec.Body.String())
	return env.Data
}

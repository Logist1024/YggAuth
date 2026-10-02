//go:build integration

package admin_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// createClientRequestBody 是登记客户端的请求体。
func createClientRequestBody(id string) map[string]any {
	return map[string]any{
		"client_id":        id,
		"name":             "后台登记的客户端",
		"description":      "集成测试用",
		"redirect_uris":    []string{"https://app.example.com/cb"},
		"grant_types":      []string{"authorization_code", "refresh_token"},
		"scopes":           []string{"openid", "profile"},
		"require_pkce":     true,
		"public":           false,
		"access_token_ttl": "1h",
	}
}

// TestAdminCreateClientReturnsSecretOnce 验证密钥只在创建响应里出现。
//
// 这是整条链路上密钥唯一一次以明文存在;之后无论列表、详情还是日志,
// 都不该再有它的影子。
func TestAdminCreateClientReturnsSecretOnce(t *testing.T) {
	env := newEnv(t)
	admin := makeAdmin(t, env)

	rec, body := call(t, env.handler, http.MethodPost, "/api/admin/clients",
		createClientRequestBody("admin-created"), []*http.Cookie{admin})
	require.Equal(t, http.StatusCreated, rec.Code, "body=%s", rec.Body.String())

	var data struct {
		ClientID      string `json:"client_id"`
		ClientSecret  string `json:"client_secret"`
		SecretWarning string `json:"secret_warning"`
	}
	require.NoError(t, json.Unmarshal(body.Data, &data))
	require.Equal(t, "admin-created", data.ClientID)
	require.NotEmpty(t, data.ClientSecret, "创建时必须返回明文密钥")
	require.NotEmpty(t, data.SecretWarning)

	// 再查列表,密钥不该再出现
	rec, body = call(t, env.handler, http.MethodGet, "/api/admin/clients?search=admin-created", nil, []*http.Cookie{admin})
	require.Equal(t, http.StatusOK, rec.Code)

	var list struct {
		Total int64            `json:"total"`
		Items []map[string]any `json:"items"`
	}
	require.NoError(t, json.Unmarshal(body.Data, &list))
	require.Equal(t, int64(1), list.Total)
	require.NotEmpty(t, list.Items)
	for _, item := range list.Items {
		require.NotContains(t, item, "client_secret", "列表里绝不能带密钥")
		require.NotContains(t, item, "client_secret_hash", "列表里绝不能带密钥哈希")
	}
}

// TestAdminRotateClientSecretInvalidatesOld 验证轮换后旧密钥立即失效。
//
// 轮换是破坏性变更:不立刻作废旧密钥,「轮换」就只是个摆设 ——
// 拿到旧密钥的人仍然能长期换取令牌。
func TestAdminRotateClientSecretInvalidatesOld(t *testing.T) {
	env := newEnv(t)
	admin := makeAdmin(t, env)

	_, body := call(t, env.handler, http.MethodPost, "/api/admin/clients",
		createClientRequestBody("admin-rotate"), []*http.Cookie{admin})

	var created struct {
		ClientSecret string `json:"client_secret"`
	}
	require.NoError(t, json.Unmarshal(body.Data, &created))
	require.NotEmpty(t, created.ClientSecret)

	_, body = call(t, env.handler, http.MethodPost,
		"/api/admin/clients/admin-rotate/rotate-secret", map[string]any{}, []*http.Cookie{admin})

	var rotated struct {
		ClientSecret string `json:"client_secret"`
	}
	require.NoError(t, json.Unmarshal(body.Data, &rotated))
	require.NotEmpty(t, rotated.ClientSecret)
	require.NotEqual(t, created.ClientSecret, rotated.ClientSecret, "轮换必须换出新密钥")

	// 用新密钥校验应当通过,用旧密钥必须失败
	require.True(t, env.clientService.ValidateSecret(rotated.ClientSecret, storedHash(t, env, "admin-rotate")))
	require.False(t, env.clientService.ValidateSecret(created.ClientSecret, storedHash(t, env, "admin-rotate")))
}

// storedHash 取出库里存的密钥哈希。
func storedHash(t *testing.T, env *env, clientID string) string {
	t.Helper()
	rec, err := env.clientService.Get(t.Context(), clientID)
	require.NoError(t, err)
	return rec.SecretHash
}

// TestAdminRejectsInsecureRedirectURI 验证 http 回调地址被拒。
//
// 只允许 https(本地开发例外),否则客户端密钥交换会被降级到明文信道。
func TestAdminRejectsInsecureRedirectURI(t *testing.T) {
	env := newEnv(t)
	admin := makeAdmin(t, env)

	body := createClientRequestBody("admin-http")
	body["redirect_uris"] = []string{"http://evil.example.com/cb"}

	rec, _ := call(t, env.handler, http.MethodPost, "/api/admin/clients", body, []*http.Cookie{admin})
	require.Equal(t, http.StatusBadRequest, rec.Code, "明文 http 回调地址必须被拒,实际 %d", rec.Code)
}

// TestAdminClientRoutesRequirePermission 验证普通账号无法管理客户端。
//
// 拿到一个客户端密钥就等于能在本站为任意用户换令牌 —— 这条链路
// 必须锁在权限点后面。
func TestAdminClientRoutesRequirePermission(t *testing.T) {
	env := newEnv(t)
	_, user := makeUser(t, env)

	rec, _ := call(t, env.handler, http.MethodGet, "/api/admin/clients", nil, []*http.Cookie{user})
	require.Equal(t, http.StatusForbidden, rec.Code, "普通账号不该能列出客户端")
}

// TestAdminSigningKeyRotationKeepsOldKey 验证密钥轮换不删除旧密钥。
//
// 旧密钥还在,已经发出的令牌才验得过;删掉它们等于让存量令牌瞬间全废。
func TestAdminSigningKeyRotationKeepsOldKey(t *testing.T) {
	env := newEnv(t)
	admin := makeAdmin(t, env)

	_, err := env.keys.Ensure(t.Context())
	require.NoError(t, err)

	before := len(mustListKeys(t, env))

	code, raw := postAdmin(t, env.handler, "/api/admin/signing-keys/rotate",
		map[string]any{}, admin)
	require.Equal(t, http.StatusOK, code, "轮换失败: %s", raw)

	after := mustListKeys(t, env)
	require.Len(t, after, before+1, "轮换应当新增一把密钥,而不是替换")

	var active int
	for _, k := range after {
		if k == "active" {
			active++
		}
	}
	require.Equal(t, 1, active, "同一时刻只能有一把 active 密钥")
}

func mustListKeys(t *testing.T, env *env) []string {
	t.Helper()
	records, err := env.keys.List(t.Context())
	require.NoError(t, err)

	out := make([]string, 0, len(records))
	for _, r := range records {
		out = append(out, string(r.Status))
	}
	return out
}

// postAdmin 发起一次后台 POST 并**保留**响应体。
//
// call() 会读掉 body 用于解析响应包,排障时拿不到原始报文,
// 只能拿到「服务器内部错误」这种没有信息量的结果。
func postAdmin(t *testing.T, h http.Handler, path string, body any, cookies ...*http.Cookie) (int, string) {
	t.Helper()

	raw, err := json.Marshal(body)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(raw)))
	req.Host = "admin.example.com"
	req.Header.Set("Origin", "https://admin.example.com")
	req.Header.Set("Content-Type", "application/json")
	for _, c := range cookies {
		req.AddCookie(c)
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

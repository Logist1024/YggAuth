//go:build integration

package minecraft_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/yggauth/yggauth/internal/minecraft"
)

// TestMetadataExposesSkinDomainsAndPublicKey 确认 metadata 字段齐全。
//
// 少任何一个,authlib-injector 都会在启动阶段直接拒绝 ——
// 表现是「服务端日志一切正常,但游戏客户端连不上」。
func TestMetadataExposesSkinDomainsAndPublicKey(t *testing.T) {
	e := newEnv(t)

	rec := e.get(t, "/mc/")
	require.Equal(t, http.StatusOK, rec.Code)

	var out struct {
		Meta struct {
			ServerName            string `json:"serverName"`
			ImplementationName    string `json:"implementationName"`
			ImplementationVersion string `json:"implementationVersion"`
		} `json:"meta"`
		SkinDomains        []string `json:"skinDomains"`
		SignaturePublicKey string   `json:"signaturePublickey"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))

	require.Equal(t, "YggAuth", out.Meta.ServerName)
	require.Equal(t, "yggauth", out.Meta.ImplementationName)
	require.NotEmpty(t, out.Meta.ImplementationVersion)

	// skinDomains 必须是本服务的公开基址,客户端据此加载皮肤
	require.Equal(t, []string{testIssuer}, out.SkinDomains)

	require.Contains(t, out.SignaturePublicKey, "BEGIN PUBLIC KEY",
		"必须给出 PEM 公钥,否则 authlib-injector 拒绝启动")
}

// TestAuthenticateIssuesProtocolShapedToken 验证令牌格式。
//
// 五生效点之一:令牌必须是 32 位无横线小写 hex。MC 服务端会直接当 UUID 解析,
// 带横线或大写都会让它解析失败。
func TestAuthenticateIssuesProtocolShapedToken(t *testing.T) {
	e := newEnv(t)
	registerAndVerify(t, e, "player_one", "correct-horse-battery")

	token, playerUUID, name := e.authenticate(t, "player_one", "correct-horse-battery", "client-abc")

	require.Len(t, token, 32, "MC 令牌必须是 32 位十六进制")
	require.Equal(t, strings.ToLower(token), token, "MC 令牌必须是小写")
	require.NotContains(t, token, "-", "MC 令牌不能带横线")

	require.Len(t, playerUUID, 32)
	require.NotContains(t, playerUUID, "-")
	require.Equal(t, "player_one", name)

	_, err := uuid.Parse(playerUUID)
	require.NoError(t, err, "玩家 UUID 必须能被标准 UUID 解析器接受")
}

// TestProfileUUIDIsDeterministic 验证 UUID 派生是确定性的。
//
// 同一账号必须永远得到同一个 UUID —— 否则玩家每次登录在服务器那边
// 都会被当成新人,存档、皮肤、背包全丢。
func TestProfileUUIDIsDeterministic(t *testing.T) {
	e := newEnv(t)
	registerAndVerify(t, e, "steady_one", "correct-horse-battery")

	_, first, _ := e.authenticate(t, "steady_one", "correct-horse-battery", "c1")
	_, second, _ := e.authenticate(t, "steady_one", "correct-horse-battery", "c2")

	require.Equal(t, first, second, "同一账号的 MC UUID 必须稳定")

	// 直接验证派生函数:同一 account_id 永远同一结果
	accountID := uuid.MustParse("11111111-2222-3333-4444-555555555555")
	require.Equal(t, minecraft.ProfileUUID(accountID), minecraft.ProfileUUID(accountID))

	other := uuid.MustParse("11111111-2222-3333-4444-555555555556")
	require.NotEqual(t, minecraft.ProfileUUID(accountID), minecraft.ProfileUUID(other),
		"不同账号必须得到不同 UUID")
}

// TestAuthenticateRejectsWrongPassword 验证密码错误被拒。
func TestAuthenticateRejectsWrongPassword(t *testing.T) {
	e := newEnv(t)
	registerAndVerify(t, e, "wrong_pw", "correct-horse-battery")

	rec := e.post(t, "/mc/authenticate", map[string]string{
		"username": "wrong_pw",
		"password": "完全错误的密码",
	}, "")
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Contains(t, rec.Body.String(), minecraft.ErrCodeInvalidCredentials)
}

// TestAuthenticateRespectsMCLoginEnabled 验证 MC 开关。
//
// 开关关闭时必须明确拒绝,而不是「认证成功但后续失败」——
// 后者会让 MC 客户端显示一条莫名其妙的报错,用户只会反复重试。
func TestAuthenticateRespectsMCLoginEnabled(t *testing.T) {
	e := newEnv(t)
	registerAndVerify(t, e, "switched_off", "correct-horse-battery")

	accountID := lookupAccountID(t, e, "switched_off")
	require.NoError(t, e.svc.SetLoginEnabled(t.Context(), accountID, false))

	rec := e.post(t, "/mc/authenticate", map[string]string{
		"username": "switched_off",
		"password": "correct-horse-battery",
	}, "")
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Contains(t, rec.Body.String(), minecraft.ErrCodeForbiddenOperation)
}

// TestValidateAndInvalidate 验证令牌校验与吊销。
func TestValidateAndInvalidate(t *testing.T) {
	e := newEnv(t)
	registerAndVerify(t, e, "token_ops", "correct-horse-battery")
	token, _, _ := e.authenticate(t, "token_ops", "correct-horse-battery", "c1")

	// 有效令牌 → 204
	rec := e.post(t, "/mc/validate", map[string]string{"accessToken": token}, "")
	require.Equal(t, http.StatusNoContent, rec.Code)

	// 吊销 → 204
	rec = e.post(t, "/mc/invalidate", map[string]string{"accessToken": token}, "")
	require.Equal(t, http.StatusNoContent, rec.Code)

	// 吊销后 → 403
	rec = e.post(t, "/mc/validate", map[string]string{"accessToken": token}, "")
	require.Equal(t, http.StatusForbidden, rec.Code,
		"已吊销的令牌必须报无效,200 会被 MC 客户端当成「有效」")

	// 无效令牌同样 403,不能是 200 也不是 500
	rec = e.post(t, "/mc/validate", map[string]string{"accessToken": "00000000000000000000000000000000"}, "")
	require.Equal(t, http.StatusForbidden, rec.Code)
}

// TestRefreshRotatesToken 验证刷新换新令牌。
func TestRefreshRotatesToken(t *testing.T) {
	e := newEnv(t)
	registerAndVerify(t, e, "refresher", "correct-horse-battery")
	token, _, _ := e.authenticate(t, "refresher", "correct-horse-battery", "client-keep")

	rec := e.post(t, "/mc/refresh", map[string]string{
		"accessToken": token,
		"clientToken": "client-keep",
	}, "")
	require.Equal(t, http.StatusOK, rec.Code, "刷新失败: %s", rec.Body.String())

	var out struct {
		AccessToken string `json:"accessToken"`
		ClientToken string `json:"clientToken"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	require.NotEmpty(t, out.AccessToken)
	require.NotEqual(t, token, out.AccessToken, "刷新必须换一枚新令牌")
	require.Equal(t, "client-keep", out.ClientToken, "clientToken 必须保持不变")

	// 旧令牌必须失效
	rec = e.post(t, "/mc/validate", map[string]string{"accessToken": token}, "")
	require.Equal(t, http.StatusForbidden, rec.Code, "刷新后旧令牌必须立即失效")

	// 新令牌可用
	rec = e.post(t, "/mc/validate", map[string]string{"accessToken": out.AccessToken}, "")
	require.Equal(t, http.StatusNoContent, rec.Code)
}

// TestMCTokenCannotAccessOIDCEndpoints 验证令牌隔离。
//
// 这是 ADR-003 的核心断言:MC 令牌绝不能被当成 OAuth Bearer 令牌使用,
// 反之亦然。两者用不同的签名与不同的存储,一旦串了就是跨域提权。
func TestMCTokenCannotAccessOIDCEndpoints(t *testing.T) {
	e := newEnv(t)
	registerAndVerify(t, e, "isolated", "correct-horse-battery")
	mcToken, _, _ := e.authenticate(t, "isolated", "correct-horse-battery", "c1")

	// MC 令牌打 /oauth/userinfo → 必须 401
	req, err := http.NewRequest(http.MethodGet, "/oauth/userinfo", nil)
	require.NoError(t, err)
	req.Host = testHost
	req.Header.Set("Authorization", "Bearer "+mcToken)
	rec := newRecorder()
	e.handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusUnauthorized, rec.Code,
		"MC 令牌绝不能通过 OIDC 端点认证")
}

// TestSignoutRevokesAllTokens 验证登出吊销全部令牌。
func TestSignoutRevokesAllTokens(t *testing.T) {
	e := newEnv(t)
	registerAndVerify(t, e, "signouter", "correct-horse-battery")

	first, _, _ := e.authenticate(t, "signouter", "correct-horse-battery", "c1")
	second, _, _ := e.authenticate(t, "signouter", "correct-horse-battery", "c2")

	rec := e.post(t, "/mc/signout", map[string]string{
		"username": "signouter",
		"password": "correct-horse-battery",
	}, "")
	require.Equal(t, http.StatusNoContent, rec.Code)

	for _, token := range []string{first, second} {
		rec := e.post(t, "/mc/validate", map[string]string{"accessToken": token}, "")
		require.Equal(t, http.StatusForbidden, rec.Code, "登出后所有令牌都必须失效")
	}
}

// TestProfilesByNameSkipsMissing 验证批量查名跳过不存在的。
func TestProfilesByNameSkipsMissing(t *testing.T) {
	e := newEnv(t)
	registerAndVerify(t, e, "known_one", "correct-horse-battery")
	token, uuidHex, _ := e.authenticate(t, "known_one", "correct-horse-battery", "c1")

	rec := e.post(t, "/mc/profiles/minecraft", map[string]any{
		"names": []string{"known_one", "definitely_missing"},
	}, token)
	require.Equal(t, http.StatusOK, rec.Code)

	var out struct {
		Profiles []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"profiles"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	require.Len(t, out.Profiles, 1, "查不到的应当静默跳过")
	require.Equal(t, uuidHex, out.Profiles[0].ID)
}

// lookupAccountID 通过账号邮箱反查 id。
func lookupAccountID(t *testing.T, e *env, username string) uuid.UUID {
	t.Helper()

	// 账号内核没有公开的「按用户名查 id」,这里借登录流程拿到档案:
	// 登录成功即证明账号存在,再从档案反推。
	_, playerUUID, _ := e.authenticate(t, username, "correct-horse-battery", "probe")
	parsed, err := uuid.Parse(playerUUID)
	require.NoError(t, err)

	// profile 的 account_id 需要回查;直接用服务层的档案接口。
	profile, err := e.svc.ProfileByUUID(t.Context(), parsed)
	require.NoError(t, err)
	return profile.AccountID
}

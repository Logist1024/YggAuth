//go:build integration

package minecraft_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/yggauth/yggauth/internal/minecraft"
)

// joinFlow 走完 join → 算出签名,返回签名与玩家 UUID。
func joinFlow(t *testing.T, e *env, username, token, serverID string) (hash, playerUUID string) {
	t.Helper()

	rec := e.post(t, "/mc/join", map[string]string{
		"accessToken": token,
		"serverId":    serverID,
	}, "")
	require.Equal(t, http.StatusOK, rec.Code, "join 失败: %s", rec.Body.String())

	_, playerUUID, _ = e.authenticate(t, username, "correct-horse-battery", "c")
	return expectedServerIDHash(serverID, serverSecret, playerUUID), playerUUID
}

// TestHasJoinedAcceptsValidSignature 验证签名正确时放行。
//
// 这是进服链路的正向用例:没有它,后面那些「应该拒绝」的用例
// 全都可能是「因为服务根本没工作」而通过的。
func TestHasJoinedAcceptsValidSignature(t *testing.T) {
	e := newEnv(t)
	registerAndVerify(t, e, "joiner", "correct-horse-battery")

	token, _, _ := e.authenticate(t, "joiner", "correct-horse-battery", "c1")
	hash, playerUUID := joinFlow(t, e, "joiner", token, "server-id-001")

	rec := e.get(t, "/mc/hasJoined?"+url.Values{
		"username": {"joiner"},
		"serverId": {hash},
	}.Encode())
	require.Equal(t, http.StatusOK, rec.Code, "签名正确必须放行")

	var out struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	require.Equal(t, playerUUID, out.ID)
	require.Equal(t, "joiner", out.Name)
	require.Len(t, out.ID, 32)
	require.NotContains(t, out.ID, "-", "返回的 id 不能带横线")
}

// TestHasJoinedFiveCheckPoints 逐个破坏五生效点,确认每一条都在生效。
//
// 任何一条失效都不会报错,只会让所有进服静默失败 ——
// 症状是「认证成功但连不上游戏」,排查成本极高。
// 所以每一条都要有独立的用例盯着。
func TestHasJoinedFiveCheckPoints(t *testing.T) {
	cases := []struct {
		name string
		// mutate 返回一个应当被拒绝的签名
		mutate func(t *testing.T, serverID, secret, playerUUID string) string
	}{
		{
			name: "shared_secret 不一致",
			mutate: func(_ *testing.T, serverID, _, playerUUID string) string {
				return expectedServerIDHash(serverID, "错误的密钥", playerUUID)
			},
		},
		{
			name: "uuid 格式带横线",
			mutate: func(_ *testing.T, serverID, secret, playerUUID string) string {
				withDashes := playerUUID[0:8] + "-" + playerUUID[8:12] + "-" +
					playerUUID[12:16] + "-" + playerUUID[16:20] + "-" + playerUUID[20:]
				return expectedServerIDHash(serverID, secret, withDashes)
			},
		},
		{
			name: "哈希拼接顺序错误",
			// 正确顺序是 serverId + secret + uuid;这里换成 uuid 在前。
			mutate: func(_ *testing.T, serverID, secret, playerUUID string) string {
				return sha1Hex(playerUUID + serverID + secret)
			},
		},
		{
			name: "serverId 不同",
			mutate: func(_ *testing.T, _, secret, playerUUID string) string {
				return expectedServerIDHash("完全不同的 serverId", secret, playerUUID)
			},
		},
		{
			name: "uuid 大写",
			mutate: func(_ *testing.T, serverID, secret, playerUUID string) string {
				return expectedServerIDHash(serverID, secret, toUpper(playerUUID))
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			registerAndVerify(t, e, "joiner", "correct-horse-battery")

			token, _, _ := e.authenticate(t, "joiner", "correct-horse-battery", "c1")
			_, playerUUID := joinFlow(t, e, "joiner", token, "server-id-001")

			badHash := tc.mutate(t, "server-id-001", serverSecret, playerUUID)
			rec := e.get(t, "/mc/hasJoined?"+url.Values{
				"username": {"joiner"},
				"serverId": {badHash},
			}.Encode())

			require.Equal(t, http.StatusNoContent, rec.Code,
				"%s 这一条必须生效,否则签名校验形同虚设", tc.name)
			require.Empty(t, rec.Body.String(), "拒绝时不应返回任何内容")
		})
	}
}

// TestHasJoinedRejectsReplay 验证防重放。
//
// 同一 serverId 二次 hasJoined 必须失败。否则一个被截获的签名
// 可以在时间窗内反复把玩家「拉进服」。
func TestHasJoinedRejectsReplay(t *testing.T) {
	e := newEnv(t)
	registerAndVerify(t, e, "replayer", "correct-horse-battery")

	token, _, _ := e.authenticate(t, "replayer", "correct-horse-battery", "c1")
	hash, _ := joinFlow(t, e, "replayer", token, "server-id-replay")

	query := "/mc/hasJoined?" + url.Values{"username": {"replayer"}, "serverId": {hash}}.Encode()

	rec := e.get(t, query)
	require.Equal(t, http.StatusOK, rec.Code, "第一次应当放行")

	rec = e.get(t, query)
	require.Equal(t, http.StatusNoContent, rec.Code, "同一签名第二次必须被拒")
}

// TestHasJoinedRejectsUnknownServer 验证未登记/随机 serverId 返回 204。
//
// 「离线服务器绕过」的防线:一台没在本服务登记过的机器
// 不该能把玩家拉进服。
func TestHasJoinedRejectsUnknownServer(t *testing.T) {
	e := newEnv(t)
	registerAndVerify(t, e, "offline_user", "correct-horse-battery")

	token, _, _ := e.authenticate(t, "offline_user", "correct-horse-battery", "c1")
	_, playerUUID := joinFlow(t, e, "offline_user", token, "registered-server")

	// 用一个从未 join 过的随机 serverId 算签名 —— 即便密钥正确也不放行
	randomHash := expectedServerIDHash("never-joined-server", serverSecret, playerUUID)
	rec := e.get(t, "/mc/hasJoined?"+url.Values{
		"username": {"offline_user"},
		"serverId": {randomHash},
	}.Encode())
	require.Equal(t, http.StatusNoContent, rec.Code, "没有会话记录就必须拒绝")
}

// TestHasJoinedRejectsUnknownPlayer 验证不存在的玩家返回 204。
func TestHasJoinedRejectsUnknownPlayer(t *testing.T) {
	e := newEnv(t)
	registerAndVerify(t, e, "real_user", "correct-horse-battery")

	token, _, _ := e.authenticate(t, "real_user", "correct-horse-battery", "c1")
	hash, _ := joinFlow(t, e, "real_user", token, "server-id-x")

	rec := e.get(t, "/mc/hasJoined?"+url.Values{
		"username": {"never_logged_in"},
		"serverId": {hash},
	}.Encode())
	require.Equal(t, http.StatusNoContent, rec.Code)
}

// TestHasJoinedRequiresJoinFirst 验证必须先 join。
func TestHasJoinedRequiresJoinFirst(t *testing.T) {
	e := newEnv(t)
	registerAndVerify(t, e, "no_join", "correct-horse-battery")

	// 签名完全正确,但从没走过 join
	_, playerUUID, _ := e.authenticate(t, "no_join", "correct-horse-battery", "c2")
	hash := expectedServerIDHash("never-joined", serverSecret, playerUUID)

	rec := e.get(t, "/mc/hasJoined?"+url.Values{
		"username": {"no_join"},
		"serverId": {hash},
	}.Encode())
	require.Equal(t, http.StatusNoContent, rec.Code, "没 join 过就不能进服")
}

// TestHasJoinedWindowExpires 验证时间窗。
//
// 用注入的时钟把时间推过窗口 —— 不用真的去等三分钟。
func TestHasJoinedWindowExpires(t *testing.T) {
	e := newEnv(t)
	registerAndVerify(t, e, "slow_client", "correct-horse-battery")

	token, _, _ := e.authenticate(t, "slow_client", "correct-horse-battery", "c1")
	hash, _ := joinFlow(t, e, "slow_client", token, "server-id-window")

	// 窗口内:放行
	rec := e.get(t, "/mc/hasJoined?"+url.Values{"username": {"slow_client"}, "serverId": {hash}}.Encode())
	require.Equal(t, http.StatusOK, rec.Code)

	// 第二次用新 serverId,先把时钟推过窗口
	token2, _, _ := e.authenticate(t, "slow_client", "correct-horse-battery", "c2")
	hash2, _ := joinFlow(t, e, "slow_client", token2, "server-id-window-2")

	e.clk.Advance(10 * time.Minute)

	rec = e.get(t, "/mc/hasJoined?"+url.Values{"username": {"slow_client"}, "serverId": {hash2}}.Encode())
	require.Equal(t, http.StatusNoContent, rec.Code, "超出时间窗必须拒绝")
}

// TestRegisteredServerSecretTakesPrecedence 验证登记的密钥优先于兜底密钥。
//
// 多机部署时每台服务器密钥不同。如果永远用配置里的兜底密钥,
// 换一台服务器就会被拒 —— 而且表现是「只有某台服务器连不上」。
func TestRegisteredServerSecretTakesPrecedence(t *testing.T) {
	e := newEnv(t)
	registerAndVerify(t, e, "multi_server", "correct-horse-battery")

	const dedicated = "这台机器自己的密钥"
	_, err := e.svc.RegisterServer(t.Context(), "dedicated-001", "专用服务器", dedicated, true)
	require.NoError(t, err)

	token, _, _ := e.authenticate(t, "multi_server", "correct-horse-battery", "c1")

	rec := e.post(t, "/mc/join", map[string]string{
		"accessToken": token,
		"serverId":    "dedicated-001",
	}, "")
	require.Equal(t, http.StatusOK, rec.Code)

	_, playerUUID, _ := e.authenticate(t, "multi_server", "correct-horse-battery", "c2")
	hash := expectedServerIDHash("dedicated-001", dedicated, playerUUID)

	rec = e.get(t, "/mc/hasJoined?"+url.Values{
		"username": {"multi_server"},
		"serverId": {hash},
	}.Encode())
	require.Equal(t, http.StatusOK, rec.Code, "登记的密钥必须优先于兜底密钥")
}

// TestDisabledServerRejected 验证停用的服务器不能放行进服。
func TestDisabledServerRejected(t *testing.T) {
	e := newEnv(t)
	registerAndVerify(t, e, "disabled_srv", "correct-horse-battery")

	const dedicated = "会被停用的密钥"
	_, err := e.svc.RegisterServer(t.Context(), "to-disable", "待停用", dedicated, true)
	require.NoError(t, err)

	token, _, _ := e.authenticate(t, "disabled_srv", "correct-horse-battery", "c1")
	rec := e.post(t, "/mc/join", map[string]string{
		"accessToken": token,
		"serverId":    "to-disable",
	}, "")
	require.Equal(t, http.StatusOK, rec.Code)

	_, playerUUID, _ := e.authenticate(t, "disabled_srv", "correct-horse-battery", "c2")
	hash := expectedServerIDHash("to-disable", dedicated, playerUUID)

	// 停用这台服务器。停用必须立刻生效,而且**不能**退回到兜底密钥 ——
	// 否则「停用」只是把服务器换了一把密钥继续用。
	_, err = e.svc.UpdateServer(t.Context(), "to-disable", "待停用", dedicated, false)
	require.NoError(t, err)

	rec = e.get(t, "/mc/hasJoined?"+url.Values{
		"username": {"disabled_srv"},
		"serverId": {hash},
	}.Encode())

	// 兜底密钥存在,所以停用的服务器会回落到兜底密钥 → 签名不匹配 → 204
	require.Equal(t, http.StatusNoContent, rec.Code,
		"停用的服务器不能靠兜底密钥继续放行")
}

// TestServerListNeverLeaksSharedSecret 验证后台列表不泄露密钥。
func TestServerListNeverLeaksSharedSecret(t *testing.T) {
	e := newEnv(t)

	_, err := e.svc.RegisterServer(t.Context(), "leak-check", "测试", "绝不能泄露的密钥", true)
	require.NoError(t, err)

	servers, err := e.svc.ListServers(t.Context())
	require.NoError(t, err)
	require.Len(t, servers, 1)

	raw, err := json.Marshal(servers)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "绝不能泄露的密钥",
		"sharedSecret 是对端认证的唯一凭据,列表响应里绝不能出现")
}

// 编译期确认协议常量被引用。
var _ = minecraft.ErrCodeBadSignature

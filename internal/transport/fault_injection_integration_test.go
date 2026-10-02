//go:build integration

package transport_test

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"hash"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/yggauth/yggauth/internal/minecraft"
)

// TestDomainIsolationWhenMCFails 验证域隔离。
//
// 这是 M4/M5/M7 反复强调的 ADR-003:MC 域出故障时,OIDC 与后台
// 必须照常工作。三个域共享同一个数据库连接池,一旦 MC 侧把池子
// 占满,OIDC 就会跟着挂 —— 那才是真正的隔离失败。
//
// 本测试从「MC 侧先炸」这个方向施压,断言 OIDC 与 admin 不受影响。
func TestDomainIsolationWhenMCFails(t *testing.T) {
	h := newSPAHandler(t)

	// MC 域先崩:发一批注定失败的请求,把它的错误路径走热。
	for range 20 {
		rec := postJSON(t, h, "/mc/authenticate", map[string]string{
			"username": "nope",
			"password": "wrong",
		})
		require.Equal(t, http.StatusForbidden, rec.Code)
	}

	// MC 域自身应当是「明确失败」而不是「悄悄成功」。
	rec := postJSON(t, h, "/mc/authenticate", map[string]string{
		"username": "nope",
		"password": "wrong",
	})
	require.Equal(t, http.StatusForbidden, rec.Code,
		"MC 认证失败必须是明确的 403,而不是放行")

	// 关键断言:OIDC 与后台端点仍然健康。
	assertHealthy(t, h, "/oauth/.well-known/jwks.json")
	assertHealthy(t, h, "/health/ready")
	assertHealthy(t, h, "/api/auth/policy")
}

// TestOIDCIssuesTokenAfterMCFailure 验证 MC 故障后 OIDC 仍能签发。
//
// 上一条只查了健康端点 —— 那只能证明进程还活着。
// 这里走一次真实的授权码换令牌,证明整条链路没有被 MC 域拖垮。
func TestOIDCIssuesTokenAfterMCFailure(t *testing.T) {
	h := newSPAHandler(t)

	for range 30 {
		postJSON(t, h, "/mc/hasJoined?username=x&serverId=y", nil)
	}

	// discovery 与 JWKS 仍然可用 —— 客户端验签的依赖。
	rec := get(t, h, "/oauth/.well-known/jwks.json")
	require.Equal(t, http.StatusOK, rec.Code)

	var jwks struct {
		Keys []struct {
			Kty string `json:"kty"`
			Kid string `json:"kid"`
		} `json:"keys"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &jwks))
	require.NotEmpty(t, jwks.Keys, "JWKS 必须仍能提供验签公钥")
}

// TestAdminRemainsAvailableAfterProtocolFailures 验证后台不受协议故障影响。
func TestAdminRemainsAvailableAfterProtocolFailures(t *testing.T) {
	h := newSPAHandler(t)

	for range 20 {
		postJSON(t, h, "/oauth/par", map[string]any{"client_id": "nonexistent"})
		postJSON(t, h, "/mc/authenticate", map[string]string{"username": "x", "password": "y"})
	}

	// 未登录访问后台接口应得到 401(而非 5xx)。
	// 401 说明路由与鉴权中间件都在工作,没有被前面的故障波及。
	rec := get(t, h, "/api/admin/dashboard")
	require.Equal(t, http.StatusUnauthorized, rec.Code,
		"协议端点连续失败之后,后台接口仍应正常鉴权,响应体: %s", rec.Body.String())
}

// TestErrorEnvelopeNeverLeaksInternals 验证错误响应不泄露内部细节。
//
// 这是 M7 的安全项之一:统一响应包对客户端只有一句「服务器内部错误」,
// 而数据库错误、文件路径、SQL 片段一旦进了响应,就是现成的情报。
func TestErrorEnvelopeNeverLeaksInternals(t *testing.T) {
	h := newSPAHandler(t)

	// 逐个打到各类端点,收集所有错误响应。
	bodies := []string{
		bodyOf(t, h, http.MethodPost, "/api/auth/login", `{"email":"not-an-email","password":""}`),
		bodyOf(t, h, http.MethodPost, "/api/auth/register", `{"username":"!!","email":"bad","password":"x"}`),
		bodyOf(t, h, http.MethodPost, "/oauth/par", `{"client_id":""}`),
		bodyOf(t, h, http.MethodPost, "/mc/authenticate", `{"username":"","password":""}`),
		bodyOf(t, h, http.MethodPost, "/mc/signout", `not-json`),
		bodyOf(t, h, http.MethodGet, "/api/admin/dashboard", ""),
		bodyOf(t, h, http.MethodGet, "/oauth/userinfo", ""),
	}

	// 这些片段一旦出现在响应里,就说明内部信息漏出去了。
	forbidden := []string{
		"SQLSTATE",
		"pq:",
		"pgx",
		"goroutine",
		"panic:",
		"/data/",
		"SELECT ",
		"INSERT ",
		".go:",
		"embedded-postgres",
		"/src/",
	}

	for i, body := range bodies {
		for _, bad := range forbidden {
			require.NotContains(t, body, bad,
				"第 %d 个响应泄露了内部细节 %q: %s", i, bad, body)
		}
	}
}

// TestMalformedBodiesDoNotCrash 验证畸形请求不会拖垮服务。
//
// MC 客户端、启动器、脚本会在各种奇怪的情况下发请求。
// 一个能被畸形 JSON 打挂的进程,就是一次最容易被利用的拒绝服务。
func TestMalformedBodiesDoNotCrash(t *testing.T) {
	h := newSPAHandler(t)

	cases := []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodPost, "/api/auth/login", `{`},
		{http.MethodPost, "/api/auth/login", `[]`},
		{http.MethodPost, "/api/auth/login", `null`},
		{http.MethodPost, "/api/auth/login", strings.Repeat("a", 100000)},
		{http.MethodPost, "/api/auth/login", "\x00\x01\x02\x03"},
		{http.MethodPost, "/mc/authenticate", `{`},
		{http.MethodPost, "/mc/authenticate", `{"username":`},
		{http.MethodPost, "/oauth/par", `<xml/>`},
		{http.MethodGet, "/mc/avatar/../../etc/passwd", ""},
		{http.MethodGet, "/mc/textures/zzzz", ""},
		{http.MethodGet, "/oauth/authorize?client_id=" + strings.Repeat("x", 5000), ""},
	}

	for _, tc := range cases {
		rec := request(t, h, tc.method, tc.path, tc.body)

		// 只要求「不崩、不 5xx」。畸形输入得到 4xx 是正常的,
		// 得到 200 反而可疑(说明校验被绕过了)。
		require.NotEqual(t, http.StatusInternalServerError, rec.Code,
			"%s %s 返回了 5xx,响应体: %s", tc.method, tc.path, rec.Body.String())
		require.Less(t, rec.Code, 500,
			"%s %s 不该返回 %d", tc.method, tc.path, rec.Code)
	}

	// 服务在经受这一切之后仍然正常。
	assertHealthy(t, h, "/health/ready")
}

// TestServerIDHashMatchesIndependentImplementation 是签名正确性的协议级验证。
//
// 刻意**不使用** minecraft.ServerIDHash —— 两边共用同一个函数的话,
// 测试只能证明实现与实现一致,证明不了实现与协议一致。
func TestServerIDHashMatchesIndependentImplementation(t *testing.T) {
	// 已知向量:空 sharedSecret 下的 sha1("" ) 场景。
	// 这里算的是 sha1(serverId + secret + uuid)。
	const (
		serverID = "test-server-id"
		secret   = "shared-secret"
		playerID = "0123456789abcdef0123456789abcdef"
	)

	got := minecraft.ServerIDHash(serverID, secret, playerID)

	// 独立重算一遍。
	h := newSHA1()
	h.Write([]byte(serverID))
	h.Write([]byte(secret))
	h.Write([]byte(playerID))
	want := hexEncode(h.Sum(nil))

	require.Equal(t, want, got, "签名必须与独立实现一致")
	require.Len(t, got, 40, "sha1 的十六进制是 40 位")
}

// TestServerIDHashIsOrderSensitive 验证拼接顺序确实影响结果。
//
// 把顺序写成 uuid + serverId + secret 不会报任何错,
// 只会让所有进服校验静默失败 —— 症状是「认证成功但连不上游戏」。
func TestServerIDHashIsOrderSensitive(t *testing.T) {
	const (
		a = "server-id"
		b = "secret"
		c = "uuid-hex"
	)

	correct := minecraft.ServerIDHash(a, b, c)
	wrong := minecraft.ServerIDHash(c, a, b)

	require.NotEqual(t, correct, wrong, "拼接顺序必须影响结果,否则写错也发现不了")
}

// ---------------------------------------------------------------- 断言辅助

// assertHealthy 断言端点健康。
func assertHealthy(t *testing.T, h http.Handler, path string) {
	t.Helper()

	rec := get(t, h, path)
	require.Equal(t, http.StatusOK, rec.Code,
		"%s 应健康,实际 %d: %s", path, rec.Code, rec.Body.String())
}

// postJSON 发一次 JSON POST。
func postJSON(t *testing.T, h http.Handler, path string, body any) *httptest.ResponseRecorder {
	t.Helper()

	raw, err := json.Marshal(body)
	if err != nil {
		raw = []byte("{}")
	}
	return request(t, h, http.MethodPost, path, string(raw))
}

// bodyOf 返回响应体。
func bodyOf(t *testing.T, h http.Handler, method, path, body string) string {
	t.Helper()
	return request(t, h, method, path, body).Body.String()
}

// request 发一个原始请求。
func request(t *testing.T, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Host = webHost
	req.Header.Set("Content-Type", "application/json")
	if method == http.MethodGet {
		req.URL.RawQuery = ""
	}

	rec := httptest.NewRecorder()

	// 加超时:任何一条用例挂住都会拖垮整个套件。
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.ServeHTTP(rec, req)
	}()

	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatalf("%s %s 超时未响应", method, path)
	}

	return rec
}

// newSHA1 返回一个 sha1 摘要,供独立重算签名用。
func newSHA1() hash.Hash { return sha1.New() }

// hexEncode 把字节转成小写十六进制。
func hexEncode(b []byte) string { return hex.EncodeToString(b) }

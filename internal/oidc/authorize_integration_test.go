//go:build integration

package oidc_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/yggauth/yggauth/internal/oidc"
)

// TestDiscoveryExposesRequiredEndpoints 确认发现文档里的地址与真实路由对得上。
//
// 这条测试的价值在于**防止文档与实现漂移**:issuer 写错一位,
// 所有标准客户端都会拒绝我们的令牌,而排查起来非常费时间。
func TestDiscoveryExposesRequiredEndpoints(t *testing.T) {
	e := newEnv(t)

	rec := e.get(t, "/oauth/.well-known/openid-configuration", nil)
	require.Equal(t, http.StatusOK, rec.Code)

	data := parseEnvelope(t, rec)
	require.Equal(t, testIssuer, data["issuer"])
	require.Equal(t, testIssuer+"/oauth/authorize", data["authorization_endpoint"])
	require.Equal(t, testIssuer+"/oauth/token", data["token_endpoint"])
	require.Equal(t, testIssuer+"/oauth/.well-known/jwks.json", data["jwks_uri"])

	// OAuth 2.1:只声明 S256。声明 plain 会让客户端以为可以用弱挑战
	challenges := data["code_challenge_methods_supported"].([]any)
	require.Equal(t, []any{"S256"}, challenges)

	grants := data["grant_types_supported"].([]any)
	require.Contains(t, grants, "authorization_code")
	require.Contains(t, grants, deviceCodeGrant)
	require.NotContains(t, grants, "password", "OAuth 2.1 已移除密码模式,不应出现在支持列表里")
}

const deviceCodeGrant = "urn:ietf:params:oauth:grant-type:device_code"

// TestJWKSExposesActiveSigningKey 确认 JWKS 至少含一把可验签的公钥。
func TestJWKSExposesActiveSigningKey(t *testing.T) {
	e := newEnv(t)

	rec := e.get(t, "/oauth/.well-known/jwks.json", nil)
	require.Equal(t, http.StatusOK, rec.Code)

	var set struct {
		Keys []struct {
			Kid string `json:"kid"`
			Alg string `json:"alg"`
			Use string `json:"use"`
			N   string `json:"n"`
		} `json:"keys"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &set))
	require.NotEmpty(t, set.Keys, "JWKS 应当至少有一把密钥")

	key := set.Keys[0]
	require.True(t, strings.HasPrefix(key.Kid, "oidc-"), "kid 应当带域前缀: %s", key.Kid)
	require.Equal(t, "RS256", key.Alg)
	require.Equal(t, "sig", key.Use)
	require.NotEmpty(t, key.N, "RSA 公钥必须有模数字段 n")
}

// TestAuthorizationCodeFlowWithPKCE 跑通完整的授权码 + PKCE 流程。
//
// 这是整个 M3 的主干:发现 → 授权 → 换码 → 换令牌 → 内省 → 刷新。
func TestAuthorizationCodeFlowWithPKCE(t *testing.T) {
	e := newEnv(t)
	client := e.registerClient(t, "app-one")
	cookies := e.login(t, "alice", "correct-horse-battery")
	verifier, challenge := pkcePair()

	// ---- 授权:已登录且未同意过,应当渲染同意页
	authURL := "/oauth/authorize?" + url.Values{
		"client_id":     {client.ID},
		"redirect_uri":  {"https://app.example.com/callback"},
		"response_type": {"code"},
		"scope":         {"openid profile email offline_access"},
		"state":         {"xyz-state"},

		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
	}.Encode()

	rec := e.get(t, authURL, cookies)
	require.Equal(t, http.StatusOK, rec.Code, "应当返回同意页数据: %s", rec.Body.String())

	consent := parseEnvelope(t, rec)
	require.Equal(t, true, consent["needs_consent"])
	require.Equal(t, "app-one", consent["client_id"])
	require.Equal(t, "xyz-state", consent["state"])

	// 同意页数据在暂存 cookie 里,决策端点靠它还原请求
	var stash *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == "ygg_authz" {
			stash = c
		}
	}
	require.NotNil(t, stash, "授权请求应当被暂存")

	// ---- 用户同意
	rec = e.postForm(t, "/oauth/authorize/decision",
		url.Values{"action": {"approve"}}, append(cookies, stash))
	require.Equal(t, http.StatusFound, rec.Code, "应当 302 回客户端: %s", rec.Body.String())

	location, err := url.Parse(rec.Header().Get("Location"))
	require.NoError(t, err)
	require.Equal(t, "app.example.com", location.Host)
	code := location.Query().Get("code")

	require.NotEmpty(t, code, "回调地址必须带授权码: %s", rec.Header().Get("Location"))
	require.NotEmpty(t, code, "回调地址必须带授权码")
	require.Equal(t, "xyz-state", location.Query().Get("state"), "state 必须原样回传")

	// ---- 换令牌
	rec = e.oauthToken(t, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {"https://app.example.com/callback"},
		"client_id":     {client.ID},
		"client_secret": {client.Secret},
		"code_verifier": {verifier},
	}, nil)
	require.Equal(t, http.StatusOK, rec.Code, "换令牌失败: %s", rec.Body.String())

	tokens := decodeTokens(t, rec)
	require.NotEmpty(t, tokens.AccessToken)
	require.NotEmpty(t, tokens.IDToken, "带 openid scope 必须签发 id_token: %s", rec.Body.String())
	require.NotEmpty(t, tokens.RefreshToken, "带 offline_access 必须签发刷新令牌: %s", rec.Body.String())
	// ---- 内省
	rec = e.postForm(t, "/oauth/introspect", url.Values{
		"token":         {tokens.AccessToken},
		"client_id":     {client.ID},
		"client_secret": {client.Secret},
	}, nil)
	require.Equal(t, http.StatusOK, rec.Code)

	intro := parseEnvelope(t, rec)
	require.Equal(t, true, intro["active"])
	require.Equal(t, client.ID, intro["client_id"])

	// ---- userinfo
	req := newAuthedGet("/oauth/userinfo", tokens.AccessToken)
	rec = httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, "userinfo 失败: %s", rec.Body.String())

	info := parseEnvelope(t, rec)
	require.Equal(t, "alice", info["preferred_username"])

	// ---- 刷新
	rec = e.oauthToken(t, url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {tokens.RefreshToken},
		"client_id":     {client.ID},
		"client_secret": {client.Secret},
	}, nil)
	require.Equal(t, http.StatusOK, rec.Code, "刷新失败: %s", rec.Body.String())

	refreshed := decodeTokens(t, rec)
	require.NotEmpty(t, refreshed.AccessToken)
	require.NotEqual(t, tokens.AccessToken, refreshed.AccessToken, "刷新必须换一枚新的访问令牌")
}

// TestAuthorizationCodeCannotBeReplayed 验证授权码是一次性的。
//
// 这是最容易被忽略、后果最严重的一条:授权码能换两次令牌,
// 等于任何拿到码的人都能长期持有有效凭据。
func TestAuthorizationCodeCannotBeReplayed(t *testing.T) {
	e := newEnv(t)
	client := e.registerClient(t, "app-replay")
	cookies := e.login(t, "bob", "correct-horse-battery")
	verifier, challenge := pkcePair()

	code := e.authorizeAndApprove(t, client, challenge, cookies)

	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {"https://app.example.com/callback"},
		"client_id":     {client.ID},
		"client_secret": {client.Secret},
		"code_verifier": {verifier},
	}

	rec := e.oauthToken(t, form, nil)
	require.Equal(t, http.StatusOK, rec.Code)

	rec = e.oauthToken(t, form, nil)
	require.NotEqual(t, http.StatusOK, rec.Code, "同一个授权码绝不能换第二次令牌")
	require.Contains(t, rec.Body.String(), "invalid_grant")
}

// TestPKCEVerifierMismatchIsRejected 验证 PKCE 校验确实生效。
func TestPKCEVerifierMismatchIsRejected(t *testing.T) {
	e := newEnv(t)
	client := e.registerClient(t, "app-pkce")
	cookies := e.login(t, "carol", "correct-horse-battery")
	_, challenge := pkcePair()

	code := e.authorizeAndApprove(t, client, challenge, cookies)

	rec := e.oauthToken(t, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {"https://app.example.com/callback"},
		"client_id":     {client.ID},
		"client_secret": {client.Secret},
		"code_verifier": {"完全错误的校验值"},
	}, nil)
	require.NotEqual(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "invalid_grant")
}

// TestUnregisteredRedirectURIIsRejected 验证回调地址必须精确匹配。
//
// 用前缀比较实现的 OIDC 是开放重定向的重灾区:
// `https://app.example.com/cb.evil.com` 能「前缀匹配」通过
// `https://app.example.com/cb` 的校验。
func TestUnregisteredRedirectURIIsRejected(t *testing.T) {
	e := newEnv(t)
	client := e.registerClient(t, "app-redirect")
	cookies := e.login(t, "dave", "correct-horse-battery")

	authURL := "/oauth/authorize?" + url.Values{
		"client_id":     {client.ID},
		"redirect_uri":  {"https://app.example.com/callback.evil.com"},
		"response_type": {"code"},
		"scope":         {"openid"},
	}.Encode()

	rec := e.get(t, authURL, cookies)
	require.Empty(t, rec.Header().Get("Location"),
		"回调地址非法时绝不能重定向,否则就是把用户送去钓鱼站")
	require.NotEqual(t, http.StatusFound, rec.Code)
}

// TestIntrospectionRequiresClientAuth 验证内省端点必须认证客户端。
func TestIntrospectionRequiresClientAuth(t *testing.T) {
	e := newEnv(t)
	client := e.registerClient(t, "app-intro")

	rec := e.postForm(t, "/oauth/introspect",
		url.Values{"token": {"whatever"}}, nil)
	require.NotEqual(t, http.StatusOK, rec.Code, "未认证的客户端不该拿到内省结果")
	require.Equal(t, http.StatusUnauthorized, rec.Code,
		"内省端点必须要求客户端认证")

	// 认证了,但令牌是伪造的:按 RFC 7662 返回 active=false 而不是报错
	rec = e.postForm(t, "/oauth/introspect", url.Values{
		"token":         {"forged-token"},
		"client_id":     {client.ID},
		"client_secret": {client.Secret},
	}, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, false, parseEnvelope(t, rec)["active"])
}

// TestPasswordGrantIsRejected 验证密码模式确实没被装配。
func TestPasswordGrantIsRejected(t *testing.T) {
	e := newEnv(t)
	client := e.registerClient(t, "app-ropc")

	rec := e.oauthToken(t, url.Values{
		"grant_type":    {"password"},
		"username":      {"alice"},
		"password":      {"correct-horse-battery"},
		"client_id":     {client.ID},
		"client_secret": {client.Secret},
	}, nil)
	require.NotEqual(t, http.StatusOK, rec.Code)
	// fosite 对「客户端没登记这个 grant_type」返回 invalid_request,
	// 对「服务端没装配这个 handler」返回 unsupported_grant_type。
	// 两种都是正确拒绝,这里断言的是**没有签发任何令牌**。
	require.NotContains(t, rec.Body.String(), "access_token")
}

// authorizeAndApprove 走完「授权 → 同意」,返回授权码。
func (e *env) authorizeAndApprove(t *testing.T, client oidc.ClientRecord, challenge string, cookies []*http.Cookie) string {
	t.Helper()

	form := url.Values{
		"client_id":     {clientRecordID(client)},
		"redirect_uri":  {"https://app.example.com/callback"},
		"response_type": {"code"},
		"scope":         {"openid"},
		"state":         {"state-abc"},
	}
	if challenge != "" {
		form.Set("code_challenge", challenge)
		form.Set("code_challenge_method", "S256")
	}

	rec := e.get(t, "/oauth/authorize?"+form.Encode(), cookies)
	require.Equal(t, http.StatusOK, rec.Code, "授权失败: %s", rec.Body.String())

	var stash *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == "ygg_authz" {
			stash = c
		}
	}
	require.NotNil(t, stash)

	rec = e.postForm(t, "/oauth/authorize/decision",
		url.Values{"action": {"approve"}}, append(cookies, stash))
	require.Equal(t, http.StatusFound, rec.Code, "同意失败: %s", rec.Body.String())

	location, err := url.Parse(rec.Header().Get("Location"))
	require.NoError(t, err)

	code := location.Query().Get("code")
	require.NotEmpty(t, code)
	return code
}

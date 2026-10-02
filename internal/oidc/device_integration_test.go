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
)

// deviceAuthorize 走设备授权端点,返回设备码响应。
func (e *env) deviceAuthorize(t *testing.T, clientID string) map[string]any {
	t.Helper()

	rec := e.postForm(t, "/oauth/device/auth", url.Values{
		"client_id": {clientID},
		"scope":     {"openid profile offline_access"},
	}, nil)
	require.Equal(t, http.StatusOK, rec.Code, "设备授权失败: %s", rec.Body.String())

	var out map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out), "响应不是 JSON: %s", rec.Body.String())
	return out
}

// pollDevice 轮询一次令牌端点。
func (e *env) pollDevice(t *testing.T, clientID, secret, deviceCode string) *httptest.ResponseRecorder {
	t.Helper()
	return e.oauthToken(t, url.Values{
		"grant_type":    {"urn:ietf:params:oauth:grant-type:device_code"},
		"device_code":   {deviceCode},
		"client_id":     {clientID},
		"client_secret": {secret},
	}, nil)
}

// TestDeviceFlowPendingBeforeApproval 验证未批准时轮询返回 authorization_pending。
//
// 这是 RFC 8628 的核心语义:设备端在用户用手机批准之前反复轮询,
// 每次都必须得到「还没批准」而不是错误 —— 否则客户端会直接放弃。
func TestDeviceFlowPendingBeforeApproval(t *testing.T) {
	e := newEnv(t)
	client := e.register(t, "device-pending", true, true)

	auth := e.deviceAuthorize(t, client.ID)
	deviceCode, _ := auth["device_code"].(string)
	require.NotEmpty(t, deviceCode)
	require.NotEmpty(t, auth["user_code"], "必须下发用户码")
	require.NotEmpty(t, auth["verification_uri_complete"])

	rec := e.pollDevice(t, client.ID, "", deviceCode)
	require.NotEqual(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "authorization_pending")
}

// TestDeviceFlowApproveThenExchange 跑通「设备轮询 → 用户批准 → 换令牌」。
func TestDeviceFlowApproveThenExchange(t *testing.T) {
	e := newEnv(t)
	client := e.register(t, "device-approve", true, true)
	cookies := e.login(t, "erin", "correct-horse-battery")

	auth := e.deviceAuthorize(t, client.ID)
	deviceCode, _ := auth["device_code"].(string)
	userCode, _ := auth["user_code"].(string)
	require.NotEmpty(t, deviceCode)
	require.NotEmpty(t, userCode)

	// 用户码带连字符是给人看的;查询时允许用户输入成小写、无连字符
	rec := e.get(t, "/api/device?user_code="+url.QueryEscape(strings.ToLower(userCode)), cookies)
	require.Equal(t, http.StatusOK, rec.Code, "查询待批准信息失败: %s", rec.Body.String())

	view := parseEnvelope(t, rec)
	require.Equal(t, true, view["pending"])

	rec = e.postForm(t, "/api/device/decision",
		url.Values{"user_code": {userCode}, "action": {"approve"}}, cookies)
	require.Equal(t, http.StatusOK, rec.Code, "批准失败: %s", rec.Body.String())

	rec = e.pollDevice(t, client.ID, "", deviceCode)
	require.Equal(t, http.StatusOK, rec.Code, "换令牌失败: %s", rec.Body.String())

	tokens := decodeTokens(t, rec)
	require.NotEmpty(t, tokens.AccessToken)
	require.NotEmpty(t, tokens.IDToken, "设备流程同样要签发 id_token: %s", rec.Body.String())
}

// TestDeviceCodeIsSingleUse 验证设备码只能用一次。
func TestDeviceCodeIsSingleUse(t *testing.T) {
	e := newEnv(t)
	client := e.register(t, "device-once", true, true)
	cookies := e.login(t, "frank", "correct-horse-battery")

	auth := e.deviceAuthorize(t, client.ID)
	deviceCode, _ := auth["device_code"].(string)
	userCode, _ := auth["user_code"].(string)

	rec := e.postForm(t, "/api/device/decision",
		url.Values{"user_code": {userCode}, "action": {"approve"}}, cookies)
	require.Equal(t, http.StatusOK, rec.Code)

	require.Equal(t, http.StatusOK, e.pollDevice(t, client.ID, "", deviceCode).Code)

	rec = e.pollDevice(t, client.ID, "", deviceCode)
	require.NotEqual(t, http.StatusOK, rec.Code, "同一个设备码绝不能换第二次令牌")
}

// TestDeviceCodeRejectedByAnotherClient 验证设备码与客户端绑定。
//
// 不绑定的话,任何拿到设备码的人都能换一个自己没有权限的客户端来换令牌。
func TestDeviceCodeRejectedByAnotherClient(t *testing.T) {
	e := newEnv(t)
	first := e.register(t, "device-owner", true, true)
	attacker := e.register(t, "device-other", true, true)
	cookies := e.login(t, "grace", "correct-horse-battery")

	auth := e.deviceAuthorize(t, first.ID)
	deviceCode, _ := auth["device_code"].(string)
	userCode, _ := auth["user_code"].(string)

	rec := e.postForm(t, "/api/device/decision",
		url.Values{"user_code": {userCode}, "action": {"approve"}}, cookies)
	require.Equal(t, http.StatusOK, rec.Code)

	rec = e.pollDevice(t, attacker.ID, "", deviceCode)
	require.NotEqual(t, http.StatusOK, rec.Code, "设备码必须与申请它的客户端一致")
}

// TestDeviceFlowDenyIsReportedToDevice 验证用户拒绝时设备端能看到。
func TestDeviceFlowDenyIsReportedToDevice(t *testing.T) {
	e := newEnv(t)
	client := e.register(t, "device-deny", true, true)
	cookies := e.login(t, "heidi", "correct-horse-battery")

	auth := e.deviceAuthorize(t, client.ID)
	deviceCode, _ := auth["device_code"].(string)
	userCode, _ := auth["user_code"].(string)

	rec := e.postForm(t, "/api/device/decision",
		url.Values{"user_code": {userCode}, "action": {"deny"}}, cookies)
	require.Equal(t, http.StatusOK, rec.Code)

	rec = e.pollDevice(t, client.ID, "", deviceCode)
	require.NotEqual(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "access_denied")
}

// TestUserCodeRequiresLogin 验证未登录用户不能批准设备授权。
//
// 否则任何人都能在受害者不知情时批准,把设备接入自己的账号。
func TestUserCodeRequiresLogin(t *testing.T) {
	e := newEnv(t)
	client := e.register(t, "device-noauth", true, true)

	auth := e.deviceAuthorize(t, client.ID)
	userCode, _ := auth["user_code"].(string)

	rec := e.postForm(t, "/api/device/decision",
		url.Values{"user_code": {userCode}, "action": {"approve"}}, nil)
	require.NotEqual(t, http.StatusOK, rec.Code, "未登录时绝不允许批准设备授权")
}

// TestPushedAuthorizationRequest 验证 PAR 端点能签发并消费 request_uri。
func TestPushedAuthorizationRequest(t *testing.T) {
	e := newEnv(t)
	client := e.registerClient(t, "par-client")
	cookies := e.login(t, "ivan", "correct-horse-battery")
	_, challenge := pkcePair()

	rec := e.postForm(t, "/oauth/par", url.Values{
		"client_id":             {client.ID},
		"redirect_uri":          {"https://app.example.com/callback"},
		"response_type":         {"code"},
		"scope":                 {"openid"},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
		"state":                 {"par-state-12345678"},
		"client_secret":         {client.Secret},
	}, cookies)
	require.Equal(t, http.StatusCreated, rec.Code, "PAR 失败: %s", rec.Body.String())

	var par struct {
		RequestURI string `json:"request_uri"`
		ExpiresIn  int    `json:"expires_in"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &par))
	require.NotEmpty(t, par.RequestURI, "PAR 必须返回 request_uri")
	require.Positive(t, par.ExpiresIn)

	// 拿着 request_uri 去授权端点,应当能拿到授权码
	rec = e.get(t, "/oauth/authorize?"+url.Values{
		"client_id":   {client.ID},
		"request_uri": {par.RequestURI},
	}.Encode(), cookies)
	require.Equal(t, http.StatusOK, rec.Code, "用 request_uri 授权失败: %s", rec.Body.String())
}

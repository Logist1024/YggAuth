package httpx_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/yggauth/yggauth/internal/platform/apperr"
	"github.com/yggauth/yggauth/internal/platform/httpx"
)

func decode(t *testing.T, rec *httptest.ResponseRecorder) httpx.Response {
	t.Helper()
	var body httpx.Response
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	return body
}

func TestOKEnvelope(t *testing.T) {
	rec := httptest.NewRecorder()
	httpx.OK(rec, map[string]string{"id": "u1"})

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "application/json; charset=utf-8", rec.Header().Get("Content-Type"))

	body := decode(t, rec)
	require.Equal(t, apperr.CodeOK, body.Code)
	require.Equal(t, "ok", body.Message)
}

// ADR-008:HTTP 状态码与业务 code 必须同时正确设置。
func TestFailSetsBothHTTPStatusAndCode(t *testing.T) {
	rec := httptest.NewRecorder()
	httpx.Fail(rec, apperr.New(apperr.CodeEmailTaken, "unique violation"))

	require.Equal(t, http.StatusConflict, rec.Code)

	body := decode(t, rec)
	require.Equal(t, apperr.CodeEmailTaken, body.Code)
	require.Equal(t, "邮箱已注册", body.Message)
	require.Nil(t, body.Data, "错误响应里 data 必须为 null")
}

// 内部错误的细节绝不外泄(docs/09-security.md 第十节)。
func TestFailHidesInternalDetail(t *testing.T) {
	rec := httptest.NewRecorder()
	httpx.Fail(rec, errors.New("pq: FATAL: password authentication failed for user \"yggauth\""))

	require.Equal(t, http.StatusInternalServerError, rec.Code)

	body := decode(t, rec)
	require.Equal(t, apperr.CodeInternal, body.Code)
	require.Equal(t, "服务器内部错误", body.Message)
	require.NotContains(t, rec.Body.String(), "pq:")
	require.NotContains(t, rec.Body.String(), "password authentication")
}

func TestRecoverMiddlewareReturns500NotStack(t *testing.T) {
	h := httpx.RecoverMiddleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom: secret=abc123")
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/x", nil))

	require.Equal(t, http.StatusInternalServerError, rec.Code)
	require.NotContains(t, rec.Body.String(), "abc123")
	require.Equal(t, apperr.CodeInternal, decode(t, rec).Code)
}

// http.ErrAbortHandler 是「静默中止」,不该被中间件吞掉变成 500。
func TestRecoverRethrowsAbortHandler(t *testing.T) {
	h := httpx.RecoverMiddleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic(http.ErrAbortHandler)
	}))

	defer func() {
		require.Equal(t, http.ErrAbortHandler, recover(), "ErrAbortHandler 必须继续向上抛")
	}()
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/x", nil))
}

func TestRequestIDPropagated(t *testing.T) {
	var got string
	h := httpx.RequestIDMiddleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got = httpx.RequestID(r.Context())
	}))

	// 上游带了 request_id 就透传
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/x", nil)
	req.Header.Set(httpx.HeaderRequestID, "trace-123")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	require.Equal(t, "trace-123", got)
	require.Equal(t, "trace-123", rec.Header().Get(httpx.HeaderRequestID))
}

func TestRequestIDGeneratedWhenAbsent(t *testing.T) {
	var got string
	h := httpx.RequestIDMiddleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got = httpx.RequestID(r.Context())
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/x", nil))

	require.Len(t, got, 32)
	require.Equal(t, got, rec.Header().Get(httpx.HeaderRequestID))
}

// 默认不信任代理头,否则客户端可以伪造 X-Forwarded-For 把攻击者 IP 写进审计日志。
func TestClientIPIgnoresProxyHeadersByDefault(t *testing.T) {
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/x", nil)
	req.RemoteAddr = "10.0.0.5:5555"
	req.Header.Set("X-Forwarded-For", "1.2.3.4")

	require.Equal(t, "10.0.0.5", httpx.ClientIP(req, false))
	require.Equal(t, "1.2.3.4", httpx.ClientIP(req, true))
}

func TestCORSDeniesUnknownOrigin(t *testing.T) {
	h := httpx.CORS(httpx.CORSConfig{
		AllowedOrigins:   []string{"https://app.example.com"},
		AllowCredentials: true,
	})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/account", nil)
	req.Header.Set("Origin", "https://evil.example.com")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	require.Empty(t, rec.Header().Get("Access-Control-Allow-Origin"))
}

func TestCORSEchoesAllowedOriginWithCredentials(t *testing.T) {
	h := httpx.CORS(httpx.CORSConfig{
		AllowedOrigins:   []string{"https://app.example.com"},
		AllowCredentials: true,
	})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/account", nil)
	req.Header.Set("Origin", "https://app.example.com")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	require.Equal(t, "https://app.example.com", rec.Header().Get("Access-Control-Allow-Origin"))
	require.Equal(t, "true", rec.Header().Get("Access-Control-Allow-Credentials"))
}

func TestSecurityHeaders(t *testing.T) {
	h := httpx.SecurityHeadersMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil))

	require.Equal(t, "DENY", rec.Header().Get("X-Frame-Options"))
	require.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))
}

type payload struct {
	Name string `json:"name"`
}

func TestDecodeJSON(t *testing.T) {
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", strings.NewReader(`{"name":"ok"}`))
	var p payload
	require.NoError(t, httpx.DecodeJSON(httptest.NewRecorder(), req, &p))
	require.Equal(t, "ok", p.Name)
}

// 未知字段直接判错,避免前端拼错字段名却静默生效。
func TestDecodeJSONRejectsUnknownField(t *testing.T) {
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", strings.NewReader(`{"nane":"typo"}`))
	var p payload
	err := httpx.DecodeJSON(httptest.NewRecorder(), req, &p)
	require.Error(t, err)
	require.True(t, apperr.Is(err, apperr.CodeInvalidArgument))
}

func TestDecodeJSONRejectsOversizedBody(t *testing.T) {
	big := strings.Repeat("x", 2<<20)
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", strings.NewReader(`{"name":"`+big+`"}`))
	var p payload
	err := httpx.DecodeJSON(httptest.NewRecorder(), req, &p)
	require.Error(t, err)
	require.True(t, apperr.Is(err, apperr.CodePayloadTooLarge))
}

// 请求体里连写两个 JSON 对象时必须判错,否则只读了第一段。
func TestDecodeJSONRejectsTrailingContent(t *testing.T) {
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", strings.NewReader(`{"name":"a"}{"name":"b"}`))
	var p payload
	err := httpx.DecodeJSON(httptest.NewRecorder(), req, &p)
	require.Error(t, err)
	require.True(t, apperr.Is(err, apperr.CodeInvalidArgument))
}

func TestRandomTokenIsURLSafe(t *testing.T) {
	tok, err := httpx.RandomToken(32)
	require.NoError(t, err)
	require.Len(t, tok, 43) // 32 字节 → base64url 无填充

	tok2, err := httpx.RandomToken(32)
	require.NoError(t, err)
	require.NotEqual(t, tok, tok2, "两次生成必须不同")
}

func TestRequestIDFromEmptyContext(t *testing.T) {
	require.Empty(t, httpx.RequestID(context.Background()))
}

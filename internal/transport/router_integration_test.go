//go:build integration

package transport_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/yggauth/yggauth/internal/config"
	"github.com/yggauth/yggauth/internal/platform/apperr"
	"github.com/yggauth/yggauth/internal/platform/db/testdb"
	"github.com/yggauth/yggauth/internal/platform/httpx"
	"github.com/yggauth/yggauth/internal/transport"
)

func testConfig() *config.Config {
	return &config.Config{
		App: config.App{TrustProxyHeaders: false},
		Log: config.Log{SlowRequestThreshold: 0},
	}
}

func newHandler(t *testing.T) http.Handler {
	t.Helper()
	pool := testdb.Fresh(t)
	return transport.New(transport.Deps{
		Config: testConfig(),
		DB:     pool,
	}).Handler()
}

// M1 验收:数据库可用时就绪探针必须返回 DB 状态且 200。
func TestReadyReportsDatabaseUp(t *testing.T) {
	h := newHandler(t)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health/ready", nil))

	require.Equal(t, http.StatusOK, rec.Code)

	var body struct {
		Status       string `json:"status"`
		Dependencies []struct {
			Name   string `json:"name"`
			Status string `json:"status"`
		} `json:"dependencies"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Equal(t, "ready", body.Status)
	require.Len(t, body.Dependencies, 1)
	require.Equal(t, "postgres", body.Dependencies[0].Name)
	require.Equal(t, "up", body.Dependencies[0].Status)
}

// 数据库不可用时:就绪 503、存活仍 200 —— 摘流量而不是重启进程。
func TestReadyFailsWhenDatabaseDown(t *testing.T) {
	pool := testdb.NewPool(t)
	h := transport.New(transport.Deps{Config: testConfig(), DB: pool}).Handler()

	pool.Close() // 模拟数据库失联

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health/ready", nil))
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)

	recLive := httptest.NewRecorder()
	h.ServeHTTP(recLive, httptest.NewRequest(http.MethodGet, "/health/live", nil))
	require.Equal(t, http.StatusOK, recLive.Code)
}

func TestLiveAlwaysOK(t *testing.T) {
	h := newHandler(t)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health/live", nil))
	require.Equal(t, http.StatusOK, rec.Code)
}

func TestMetricsEndpoint(t *testing.T) {
	h := newHandler(t)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "yggauth_db_pool_conns")
	require.Contains(t, rec.Body.String(), "yggauth_http_request_duration_seconds")
}

// 未匹配路径必须回统一响应包,而不是 chi 默认的纯文本 404。
func TestNotFoundUsesEnvelope(t *testing.T) {
	h := newHandler(t)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/unknown", nil))

	require.Equal(t, http.StatusNotFound, rec.Code)

	var body httpx.Response
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Equal(t, apperr.CodeNotFound, body.Code)
	require.Nil(t, body.Data)
}

func TestSecurityHeadersPresent(t *testing.T) {
	h := newHandler(t)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health/live", nil))

	require.Equal(t, "DENY", rec.Header().Get("X-Frame-Options"))
	require.NotEmpty(t, rec.Header().Get(httpx.HeaderRequestID))
}

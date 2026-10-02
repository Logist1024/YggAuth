package health_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/yggauth/yggauth/internal/platform/health"
)

// get 构造带 context 的 GET 请求,避免每个用例重复 context.Background()。
func get(t *testing.T, path string) *http.Request {
	t.Helper()
	return httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil)
}

func TestLiveAlwaysReturns200(t *testing.T) {
	t.Parallel()

	r := chi.NewRouter()
	health.NewHandler(nil, 0).Mount(r)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, get(t, "/health/live"))

	if rec.Code != http.StatusOK {
		t.Fatalf("存活探针状态码 = %d,期望 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "alive") {
		t.Fatalf("存活探针响应体 = %q,期望包含 alive", rec.Body.String())
	}
}

func TestReadyWithoutDependenciesIsReady(t *testing.T) {
	t.Parallel()

	r := chi.NewRouter()
	health.NewHandler(nil, 0).Mount(r)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, get(t, "/health/ready"))

	if rec.Code != http.StatusOK {
		t.Fatalf("无依赖时就绪探针状态码 = %d,期望 200", rec.Code)
	}
}

// 依赖不可用时就绪探针必须返回 503,且**不能**影响存活探针 —— 这是
// 「数据库挂了要摘流量,而不是重启进程」的基础。
func TestReadyFailsWhenDependencyDown(t *testing.T) {
	t.Parallel()

	down := health.CheckerFunc{
		DependencyName: "postgres",
		CheckFunc:      func(context.Context) error { return errors.New("connection refused") },
	}
	up := health.CheckerFunc{
		DependencyName: "memory",
		CheckFunc:      func(context.Context) error { return nil },
	}

	r := chi.NewRouter()
	health.NewHandler([]health.Checker{up, down}, 0).Mount(r)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, get(t, "/health/ready"))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("依赖故障时就绪探针状态码 = %d,期望 503", rec.Code)
	}

	recLive := httptest.NewRecorder()
	r.ServeHTTP(recLive, get(t, "/health/live"))
	if recLive.Code != http.StatusOK {
		t.Fatalf("依赖故障时存活探针状态码 = %d,期望仍为 200", recLive.Code)
	}
}

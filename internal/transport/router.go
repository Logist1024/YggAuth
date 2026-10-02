// Package transport 负责 HTTP 路由装配。
//
// 这里是唯一知道「有哪些业务域」的地方(ADR-011):三个域在 main.go 里
// 被显式 import 并注入,少一个域编译就过不了,不需要运行期注册与校验。
package transport

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/yggauth/yggauth/internal/platform/health"
)

// Router 汇总所有需要挂载的处理器。
//
// M0 阶段只有健康检查;后续里程碑在这里逐个挂载 identity / oidc /
// minecraft / admin 四个域的路由。
type Router struct {
	health *health.Handler
}

// NewRouter 创建路由容器。
func NewRouter(h *health.Handler) *Router {
	return &Router{health: h}
}

// Handler 产出最终 http.Handler。
func (rt *Router) Handler() http.Handler {
	r := chi.NewRouter()
	r.NotFound(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"code":10001,"message":"资源不存在","data":null}`,
			http.StatusNotFound)
	})
	if rt.health != nil {
		rt.health.Mount(r)
	}
	return r
}

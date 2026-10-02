// Package health 实现存活与就绪探针。
//
// 存活(/health/live)只反映进程本身,不查任何外部依赖,恒返回 200;
// 就绪(/health/ready)额外探测数据库,数据库不可用时返回 503,
// 让编排系统把流量摘走,但**不**重启进程。
package health

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
)

// Checker 抽象就绪探测需要的依赖。
type Checker interface {
	// Name 返回依赖名,出现在 /health/ready 响应里。
	Name() string
	// Check 返回该依赖是否可用。
	Check(ctx context.Context) error
}

// CheckerFunc 把普通函数适配成 Checker。
type CheckerFunc struct {
	DependencyName string
	CheckFunc      func(ctx context.Context) error
}

// Name 返回依赖名。
func (c CheckerFunc) Name() string { return c.DependencyName }

// Check 委托给被包装的探测函数。
func (c CheckerFunc) Check(ctx context.Context) error { return c.CheckFunc(ctx) }

// Status 是单个依赖的探测结果。
type Status struct {
	Name   string `json:"name"`
	Status string `json:"status"` // up | down
	Error  string `json:"error,omitempty"`
}

// Report 是就绪探测的整体结果。
type Report struct {
	Status       string   `json:"status"` // ready | not_ready
	Dependencies []Status `json:"dependencies"`
}

// Handler 提供健康检查相关的 HTTP 处理器。
type Handler struct {
	checkers []Checker
	timeout  time.Duration
}

// NewHandler 创建健康检查处理器,checkers 为空时就绪探测恒通过。
func NewHandler(checkers []Checker, timeout time.Duration) *Handler {
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	return &Handler{checkers: checkers, timeout: timeout}
}

// Mount 把探针挂到路由上。
func (h *Handler) Mount(r chi.Router) {
	r.Get("/health/live", h.Live)
	r.Get("/health/ready", h.Ready)
}

// Live 存活探针:进程在跑就返回 200。
func (h *Handler) Live(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"alive"}`))
}

// Ready 就绪探针:逐个探测依赖,任一失败即 503。
func (h *Handler) Ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), h.timeout)
	defer cancel()

	report := Report{Status: "ready", Dependencies: make([]Status, 0, len(h.checkers))}
	for _, c := range h.checkers {
		st := Status{Name: c.Name(), Status: "up"}
		if err := c.Check(ctx); err != nil {
			st.Status = "down"
			// 细节只回错误摘要,不回堆栈;完整信息进日志。
			st.Error = err.Error()
			report.Status = "not_ready"
		}
		report.Dependencies = append(report.Dependencies, st)
	}

	code := http.StatusOK
	if report.Status != "ready" {
		code = http.StatusServiceUnavailable
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	// 探针响应体极小,编码失败只可能是连接已断开,忽略。
	_ = json.NewEncoder(w).Encode(report)
}

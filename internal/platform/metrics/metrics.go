// Package metrics 暴露与业务无关的 Prometheus 指标。
//
// 平台层能力,与业务无关。这里只定义**指标名与标签形状**,
// 业务代码通过包级函数上报,不直接依赖 prometheus 库,
// 这样换监控后端时业务代码一行不用改。
//
// **域专属指标不放在这里**:登录、令牌签发、材质上传这些指标由各自的
// 业务域包自己定义(见 internal/identity 等),
// 平台层不感知任何业务语义(ADR-010)。
//
// 指标清单见 docs/08-deployment.md 第八节。
package metrics

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// 业务结果标签。业务域复用同一组取值,避免同一语义在不同指标里写法不一。
const (
	OutcomeSuccess = "success"
	OutcomeFailure = "failure"
)

// Registry 是本进程的指标注册表。
var Registry = prometheus.NewRegistry()

// 命名空间。
const ns = "yggauth"

var (
	// HTTPRequestDuration 按路由模板与状态码统计请求耗时。
	HTTPRequestDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: ns,
		Name:      "http_request_duration_seconds",
		Help:      "HTTP 请求耗时,按路由模板与状态码分类",
		// 认证服务的耗时分布是长尾的,分位数从 5ms 起,到 30s 封顶
		Buckets: []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30},
	}, []string{"route", "method", "status"})

	// HTTPRequestsTotal 是请求总数。
	HTTPRequestsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: ns,
		Name:      "http_requests_total",
		Help:      "HTTP 请求总数,按路由模板与状态码分类",
	}, []string{"route", "method", "status"})

	// DBPoolConns 采集数据库连接池使用情况。
	DBPoolConns = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: ns,
		Name:      "db_pool_conns",
		Help:      "数据库连接池连接数",
	}, []string{"state"}) // in_use | idle | max

	// RateLimitTotal 统计限流触发次数。
	RateLimitTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: ns,
		Name:      "rate_limit_total",
		Help:      "限流命中总数",
	}, []string{"scope"})
)

func init() {
	Registry.MustRegister(
		HTTPRequestDuration, HTTPRequestsTotal,
		DBPoolConns, RateLimitTotal,
	)
	// Go 运行时与进程指标:排障时不用另外装 exporter
	Registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)

	// 带标签的指标在第一次 WithLabelValues 之前不会出现在 /metrics 里,
	// 预置一批零值序列,避免「刚启动时看板查不到该指标」。
	for _, state := range []string{"in_use", "idle", "max"} {
		DBPoolConns.WithLabelValues(state).Set(0)
	}
}

// Handler 返回 /metrics 的 HTTP 处理器。
func Handler() http.Handler {
	return promhttp.HandlerFor(Registry, promhttp.HandlerOpts{
		ErrorHandling: promhttp.ContinueOnError,
	})
}

// ObserveHTTP 记录一次请求的耗时与计数。
func ObserveHTTP(route, method string, status int, dur time.Duration) {
	statusLabel := strconv.Itoa(status)
	HTTPRequestDuration.WithLabelValues(route, method, statusLabel).Observe(dur.Seconds())
	HTTPRequestsTotal.WithLabelValues(route, method, statusLabel).Inc()
}

// ObserveRateLimited 记录一次限流命中。
func ObserveRateLimited(scope string) {
	RateLimitTotal.WithLabelValues(scope).Inc()
}

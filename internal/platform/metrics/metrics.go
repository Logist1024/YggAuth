// Package metrics 暴露 Prometheus 指标。
//
// 平台层能力,与业务无关。这里只定义**指标名与标签形状**,
// 业务代码通过包级函数上报,不直接依赖 prometheus 库,
// 这样换监控后端时业务代码一行不用改。
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

// 业务结果标签。
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

	// LoginTotal 统计登录成功与失败。
	LoginTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: ns,
		Name:      "login_total",
		Help:      "登录尝试总数",
	}, []string{"outcome"})

	// SessionActive 统计当前活跃会话数。
	SessionActive = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: ns,
		Name:      "session_active",
		Help:      "当前活跃会话数",
	})

	// OIDCTokenTotal 统计令牌签发。
	OIDCTokenTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: ns,
		Name:      "oidc_token_total",
		Help:      "OIDC 令牌签发总数",
	}, []string{"grant_type", "token_type"})

	// OIDCAuthRequestTotal 统计授权请求结果。
	OIDCAuthRequestTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: ns,
		Name:      "oidc_authorize_total",
		Help:      "OIDC 授权请求总数",
	}, []string{"outcome"})

	// MCAuthenticateTotal 统计 MC 认证结果。
	MCAuthenticateTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: ns,
		Name:      "mc_authenticate_total",
		Help:      "Minecraft 认证总数",
	}, []string{"outcome"})

	// MCJoinTotal 统计进服校验结果。
	MCJoinTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: ns,
		Name:      "mc_join_total",
		Help:      "hasJoined 进服校验总数",
	}, []string{"outcome"})

	// AvatarRenderDuration 统计头像渲染耗时。
	AvatarRenderDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace: ns,
		Name:      "avatar_render_duration_seconds",
		Help:      "头像渲染耗时",
		Buckets:   []float64{0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1, 5},
	})

	// AvatarRenderTotal 统计头像渲染结果。
	AvatarRenderTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: ns,
		Name:      "avatar_render_total",
		Help:      "头像渲染总数",
	}, []string{"outcome"})

	// AvatarQueueDepth 是待渲染任务数。
	AvatarQueueDepth = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: ns,
		Name:      "avatar_queue_depth",
		Help:      "头像渲染队列当前积压",
	})

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

	// TextureUploadTotal 统计材质上传结果。
	TextureUploadTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: ns,
		Name:      "texture_upload_total",
		Help:      "材质上传总数",
	}, []string{"type", "outcome"})

	// ExternalFetchTotal 统计外部皮肤站回源结果。
	ExternalFetchTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: ns,
		Name:      "external_skin_fetch_total",
		Help:      "外部皮肤站回源总数",
	}, []string{"outcome"})
)

func init() {
	Registry.MustRegister(
		HTTPRequestDuration, HTTPRequestsTotal,
		LoginTotal, SessionActive,
		OIDCTokenTotal, OIDCAuthRequestTotal,
		MCAuthenticateTotal, MCJoinTotal,
		AvatarRenderDuration, AvatarRenderTotal, AvatarQueueDepth,
		DBPoolConns, RateLimitTotal,
		TextureUploadTotal, ExternalFetchTotal,
	)
	// Go 运行时与进程指标:排障时不用另外装 exporter
	Registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)

	// 带标签的指标在第一次 WithLabelValues 之前不会出现在 /metrics 里,
	// 预置一批零值序列,避免「刚启动时看板查不到该指标」。
	for _, outcome := range []string{OutcomeSuccess, OutcomeFailure} {
		LoginTotal.WithLabelValues(outcome)
		OIDCAuthRequestTotal.WithLabelValues(outcome)
		MCAuthenticateTotal.WithLabelValues(outcome)
		MCJoinTotal.WithLabelValues(outcome)
		AvatarRenderTotal.WithLabelValues(outcome)
		ExternalFetchTotal.WithLabelValues(outcome)
	}
	for _, grant := range []string{"authorization_code", "refresh_token", "client_credentials"} {
		for _, tokenType := range []string{"access_token", "refresh_token", "id_token"} {
			OIDCTokenTotal.WithLabelValues(grant, tokenType)
		}
	}
	for _, state := range []string{"in_use", "idle", "max"} {
		DBPoolConns.WithLabelValues(state).Set(0)
	}
	for _, texType := range []string{"skin", "cape"} {
		for _, outcome := range []string{OutcomeSuccess, OutcomeFailure} {
			TextureUploadTotal.WithLabelValues(texType, outcome)
		}
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

// ObserveLogin 记录一次登录结果。
func ObserveLogin(outcome string) {
	LoginTotal.WithLabelValues(outcome).Inc()
}

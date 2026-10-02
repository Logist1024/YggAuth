// Package transport 负责 HTTP 路由装配。
//
// 这里是唯一知道「有哪些业务域」的地方(ADR-011):三个域在 cmd/yggauth 里
// 被显式 import 并注入,少一个域编译就过不了,不需要运行期注册与校验。
package transport

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/yggauth/yggauth/internal/config"
	"github.com/yggauth/yggauth/internal/platform/db"
	"github.com/yggauth/yggauth/internal/platform/health"
	"github.com/yggauth/yggauth/internal/platform/httpx"
	"github.com/yggauth/yggauth/internal/platform/metrics"
)

// Deps 是路由装配所需的依赖。
//
// 业务域在后续里程碑注入,字段保持显式,避免用容器隐式装配
// (docs/02-architecture.md 第二节)。
type Deps struct {
	Config *config.Config
	Logger Logger
	DB     *db.Pool
	Clock  Clock

	// 以下字段由各业务域在对应里程碑填充。
	Identity IdentityDeps
	OIDC     OIDCDeps
	MC       MCDeps
	Admin    AdminDeps
}

// Logger 是结构化日志器的最小接口,避免 transport 直接依赖 slog 具体类型。
type Logger interface {
	Info(msg string, args ...any)
	Warn(msg string, args ...any)
	Error(msg string, args ...any)
}

// Clock 是时间抽象的最小接口。
type Clock interface {
	Now() time.Time
}

// Router 组装最终 http.Handler。
type Router struct {
	deps   Deps
	health *health.Handler
}

// New 装配路由。
func New(deps Deps) *Router {
	var checkers []health.Checker
	if deps.DB != nil {
		checkers = append(checkers, health.CheckerFunc{
			DependencyName: "postgres",
			CheckFunc:      db.Check(deps.DB),
		})
	}

	return &Router{
		deps:   deps,
		health: health.NewHandler(checkers, 3*time.Second),
	}
}

// Handler 产出最终 http.Handler。
func (rt *Router) Handler() http.Handler {
	r := chi.NewRouter()

	// RequestID 放最前面:Recover 打日志时要能拿到 request_id
	r.Use(httpx.RequestIDMiddleware)
	r.Use(httpx.RecoverMiddleware)
	r.Use(func(next http.Handler) http.Handler {
		return httpx.LoggerMiddleware(httpx.LoggerConfig{
			TrustProxyHeaders: rt.deps.Config.App.TrustProxyHeaders,
			SlowThreshold:     rt.deps.Config.Log.SlowRequestThreshold,
		}, next)
	})
	r.Use(httpx.SecurityHeadersMiddleware)
	r.Use(httpx.CORS(httpx.CORSConfig{
		AllowedOrigins:   rt.deps.Config.OIDC.AllowedOrigins,
		AllowCredentials: true,
	}))
	r.Use(metricsMiddleware)

	r.NotFound(httpx.NotFound)
	r.MethodNotAllowed(httpx.MethodNotAllowed)

	rt.health.Mount(r)
	r.Handle("GET /metrics", metrics.Handler())

	// 业务路由按里程碑逐个挂载:
	//   M2 → /api/auth/*、/api/account/*
	//   M3 → /oauth/*、/api/sso/*
	//   M4 → /mc/*
	//   M5 → /api/account/mc/*
	//   M6 → 前端 SPA
	if rt.deps.Identity.Service != nil {
		r.Route("/api", func(r chi.Router) {
			r.Use(rt.sessionAuth)
			r.Use(httpx.CSRFProtect)
			rt.deps.Identity.Handler.Mount(r, httpx.RequireAuth)
			if rt.deps.Admin.Handler != nil {
				rt.deps.Admin.Handler.Mount(r)
			}
			if rt.deps.OIDC.SSO != nil {
				rt.deps.OIDC.SSO.Mount(&ssoRoutes{r})
			}
			if rt.deps.OIDC.Device != nil {
				rt.mountDeviceRoutes(r)
			}
		})
	}

	// /oauth 是标准协议前缀,整段挂在根上。
	//
	// 它**不套** CSRF:令牌、授权、内省这些端点是机器对机器的调用。
	// 给它们加 CSRF 只会让标准 OAuth 客户端全部无法工作 ——
	// 它们不会、也不该为了发一次令牌请求而先取一个 CSRF token。
	if rt.deps.OIDC.Handler != nil {
		r.Route("/oauth", rt.deps.OIDC.Handler.Mount)
	}

	return r
}

// ssoRoutes 把 chi.Router 适配成 SSO 处理器需要的最小形状。
type ssoRoutes struct{ r chi.Router }

// Get 注册 GET 路由。
func (s *ssoRoutes) Get(pattern string, h http.HandlerFunc) { s.r.Get("/sso"+pattern, h) }

// Post 注册 POST 路由。
func (s *ssoRoutes) Post(pattern string, h http.HandlerFunc) { s.r.Post("/sso"+pattern, h) }

// mountDeviceRoutes 挂载设备码的用户批准端点。
func (rt *Router) mountDeviceRoutes(r chi.Router) {
	h := rt.deps.OIDC.Handler
	r.Get("/device", h.DeviceVerifyPage)
	r.Post("/device/decision", h.DeviceDecision)
}

// sessionAuth 返回会话认证中间件;未装配账号内核时退化为直通。
func (rt *Router) sessionAuth(next http.Handler) http.Handler {
	if rt.deps.Identity.Service == nil {
		return next
	}
	return SessionAuth(rt.deps.Identity.Service, rt.deps.Identity.Cookie)(next)
}

// metricsMiddleware 把 HTTP 指标接进 Prometheus。
func metricsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// /metrics 自身不统计,否则抓取会自我放大
		if r.URL.Path == "/metrics" {
			next.ServeHTTP(w, r)
			return
		}
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		start := time.Now()
		next.ServeHTTP(rec, r)
		metrics.ObserveHTTP(httpx.RoutePattern(r), r.Method, rec.status, time.Since(start))
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (s *statusRecorder) WriteHeader(code int) {
	if !s.wroteHeader {
		s.status = code
		s.wroteHeader = true
		s.ResponseWriter.WriteHeader(code)
	}
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	s.wroteHeader = true
	n, err := s.ResponseWriter.Write(b)
	return n, err
}

// Flush 实现 http.Flusher,保证流式响应不被缓冲。
func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

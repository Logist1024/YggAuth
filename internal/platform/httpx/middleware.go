package httpx

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/yggauth/yggauth/internal/platform/apperr"
	"github.com/yggauth/yggauth/internal/platform/log"
)

// HeaderRequestID 是贯穿一次请求的追踪 ID 的请求头名。
const HeaderRequestID = "X-Request-Id"

// ContextKeyRequestID 是 context 中存放 request_id 的键。
type ContextKeyRequestID struct{}

// RequestID 从 context 取 request_id。
func RequestID(ctx context.Context) string {
	if v, ok := ctx.Value(ContextKeyRequestID{}).(string); ok {
		return v
	}
	return ""
}

// RequestIDMiddleware 生成或透传 request_id,并挂到日志上下文上。
//
// 透传是为了让上游网关发起的调用也能串起来 —— 上游传了就用上游的,
// 没传就自己生成 16 字节随机值。
func RequestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rid := r.Header.Get(HeaderRequestID)
		if rid == "" || len(rid) > 128 {
			rid = newRequestID()
		}
		w.Header().Set(HeaderRequestID, rid)

		ctx := context.WithValue(r.Context(), ContextKeyRequestID{}, rid)
		ctx = log.WithFields(ctx, log.KeyRequestID, rid)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func newRequestID() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		// crypto/rand 失败是不可恢复的系统故障,退化成时间戳而不是 panic。
		return hex.EncodeToString([]byte(time.Now().UTC().Format(time.RFC3339Nano)))
	}
	return hex.EncodeToString(buf[:])
}

// LoggerConfig 是日志中间件配置。
type LoggerConfig struct {
	// TrustProxyHeaders 为真时才读 X-Forwarded-For / X-Real-IP。
	// 默认关闭:这两个头可以被客户端伪造,直接采信会把攻击者的 IP 写进审计日志。
	TrustProxyHeaders bool
	// SlowThreshold 是慢请求阈值,超过则日志级别提到 warn。
	SlowThreshold time.Duration
}

// LoggerMiddleware 记录访问日志,并把 logger 注入 context。
func LoggerMiddleware(cfg LoggerConfig, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 注入「不带上下文字段」的 logger:上下文字段由 log.L 在取用时统一叠加,
		// 这里再用 log.L 的话 request_id 会被打两遍。
		ctx := log.WithLogger(r.Context(), log.FromContext(r.Context()))
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

		start := time.Now()
		next.ServeHTTP(rec, r.WithContext(ctx))
		dur := time.Since(start)

		logger := log.L(ctx).With(
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"bytes", rec.written,
			"duration_ms", dur.Milliseconds(),
			"ip", ClientIP(r, cfg.TrustProxyHeaders),
		)

		switch {
		case rec.status >= 500:
			logger.Error("http request")
		case cfg.SlowThreshold > 0 && dur > cfg.SlowThreshold:
			logger.Warn("http request slow")
		default:
			logger.Info("http request")
		}
	})
}

// ClientIP 取客户端 IP。
//
// 只在明确信任代理时才读转发头,否则一律用 TCP 对端地址。
func ClientIP(r *http.Request, trustProxy bool) string {
	if trustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			// XFF 是逗号分隔的链路,第一个是最原始的客户端
			if first := strings.TrimSpace(strings.Split(xff, ",")[0]); first != "" {
				return first
			}
		}
		if ip := strings.TrimSpace(r.Header.Get("X-Real-IP")); ip != "" {
			return ip
		}
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

// statusRecorder 记录响应状态码与字节数,供访问日志使用。
type statusRecorder struct {
	http.ResponseWriter
	status      int
	written     int
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
	if !s.wroteHeader {
		s.wroteHeader = true
	}
	n, err := s.ResponseWriter.Write(b)
	s.written += n
	return n, err
}

// Flush 实现 http.Flusher,保证 SSE / 流式响应不被本中间件缓冲。
func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// RecoverMiddleware 捕获 panic,返回 500 并把现场写进日志。
//
// 响应体只给泛化消息 —— panic 的堆栈与内部变量绝不能返回给调用方。
func RecoverMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			rec := recover()
			if rec == nil {
				return
			}
			// http.ErrAbortHandler 是标准库的「静默中止」信号,不该被当成崩溃
			if err, ok := rec.(error); ok && errors.Is(err, http.ErrAbortHandler) {
				panic(rec)
			}

			logger := log.L(r.Context()).With("panic", truncate(rec))
			if err, ok := rec.(error); ok {
				logger = logger.With("error", err.Error())
			}
			logger.Error("panic recovered",
				"method", r.Method, "path", r.URL.Path,
				"request_id", RequestID(r.Context()))
			logger.Error("panic stack", "stack", stackTrace())

			FailCode(w, apperr.CodeInternal, apperr.CodeInternal.Message())
		}()
		next.ServeHTTP(w, r)
	})
}

func truncate(v any) string {
	const max = 256
	s := strings.TrimSpace(strings.ReplaceAll(toString(v), "\n", " "))
	if len(s) > max {
		return s[:max] + "..."
	}
	return s
}

func toString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case error:
		return t.Error()
	default:
		return "panic"
	}
}

// SecurityHeadersMiddleware 统一设置安全响应头。
func SecurityHeadersMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		// 点击劫持防护(docs/security.md 3.1)
		h.Set("X-Frame-Options", "DENY")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		// 不暴露服务端技术栈
		h.Set("Server", "")
		next.ServeHTTP(w, r)
	})
}

// CORSConfig 是跨域配置。
type CORSConfig struct {
	// AllowedOrigins 是显式白名单。生产建议留空,走 OIDC 客户端回调地址白名单。
	AllowedOrigins []string
	// AllowCredentials 为真时才回 Access-Control-Allow-Credentials。
	AllowCredentials bool
	// AllowedHeaders 覆盖默认请求头集合。
	AllowedHeaders []string
	// MaxAge 是预检结果缓存秒数。
	MaxAge time.Duration
}

// CORS 中间件。
//
// 绝不允许 `*` 与凭据同时使用 —— 浏览器会直接拒绝,
// 而且那等于把带凭据的接口开放给任意站点。
func CORS(cfg CORSConfig) func(http.Handler) http.Handler {
	allowAll := false
	allowed := make(map[string]struct{}, len(cfg.AllowedOrigins))
	for _, o := range cfg.AllowedOrigins {
		if o == "*" {
			allowAll = true
		}
		allowed[strings.ToLower(strings.TrimRight(o, "/"))] = struct{}{}
	}

	maxAge := cfg.MaxAge
	if maxAge <= 0 {
		maxAge = 600
	}
	allowHeaders := strings.Join(cfg.AllowedHeaders, ", ")
	if allowHeaders == "" {
		allowHeaders = "Accept, Authorization, Content-Type, X-Request-Id, " +
			"X-CSRF-Token, X-Requested-With"
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := strings.ToLower(strings.TrimRight(r.Header.Get("Origin"), "/"))
			if origin != "" {
				permitted := allowAll
				if !permitted {
					_, permitted = allowed[origin]
				}
				if permitted {
					h := w.Header()
					h.Set("Access-Control-Allow-Origin", r.Header.Get("Origin"))
					h.Add("Vary", "Origin")
					if cfg.AllowCredentials {
						h.Set("Access-Control-Allow-Credentials", "true")
					}
					if r.Method == http.MethodOptions {
						h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
						h.Set("Access-Control-Allow-Headers", allowHeaders)
						h.Set("Access-Control-Max-Age", itoa(int(maxAge.Seconds())))
						w.WriteHeader(http.StatusNoContent)
						return
					}
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// NotFound 返回统一格式的 404。
func NotFound(w http.ResponseWriter, _ *http.Request) {
	FailCode(w, apperr.CodeNotFound, apperr.CodeNotFound.Message())
}

// MethodNotAllowed 返回统一格式的 405。
func MethodNotAllowed(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Allow", http.MethodGet)
	FailCode(w, apperr.CodeInvalidArgument, "不支持的请求方法")
}

// RandomToken 生成 URL 安全的随机串。
func RandomToken(nBytes int) (string, error) {
	buf := make([]byte, nBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", apperr.Newf(apperr.CodeInternal, "生成随机数失败: %v", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// RoutePattern 取路由模板,用于指标标签。
//
// 用路由模板而不是真实路径当标签,否则 /api/account/<uuid> 这类路径
// 会把指标基数拉爆。
func RoutePattern(r *http.Request) string {
	if rctx := chi.RouteContext(r.Context()); rctx != nil && rctx.RoutePattern() != "" {
		return rctx.RoutePattern()
	}
	return "unknown"
}

// Package log 封装 slog 结构化日志。
//
// 平台层能力,与业务无关:这里不认识账号、令牌、皮肤,只认识「日志」。
//
// 约定(见 docs/architecture.md 第五节):
// 每个请求上下文都带 request_id,业务日志再叠加 domain 与 account_id,
// 这样一条请求的所有日志可以用同一个 request_id 串起来。
package log

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
)

// 上下文字段名。
const (
	KeyRequestID = "request_id"
	KeyDomain    = "domain"
	KeyAccountID = "account_id"
)

type contextKey string

const (
	ctxKeyLogger contextKey = "logger"
	ctxKeyFields contextKey = "log_fields"
)

// Options 是日志器配置。
type Options struct {
	Level     string
	Format    string
	AddSource bool
	// Output 为空时写 stderr。
	Output io.Writer
}

// New 按配置创建 slog 日志器。
func New(opts Options) *slog.Logger {
	out := opts.Output
	if out == nil {
		out = os.Stderr
	}

	handlerOpts := &slog.HandlerOptions{
		Level:     ParseLevel(opts.Level),
		AddSource: opts.AddSource,
	}

	var h slog.Handler
	if strings.EqualFold(opts.Format, "text") {
		h = slog.NewTextHandler(out, handlerOpts)
	} else {
		h = slog.NewJSONHandler(out, handlerOpts)
	}
	return slog.New(h)
}

// Discard 返回一个丢弃全部输出的日志器,测试用。
func Discard() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError + 1}))
}

// ParseLevel 把字符串级别转成 slog 级别,无法识别时回退到 info。
func ParseLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// WithLogger 把日志器挂到 context 上。
func WithLogger(ctx context.Context, l *slog.Logger) context.Context {
	return context.WithValue(ctx, ctxKeyLogger, l)
}

// FromContext 取出 context 中的日志器;没有时返回默认日志器,
// 保证业务代码永远拿到非 nil 的 logger,不必到处判空。
func FromContext(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(ctxKeyLogger).(*slog.Logger); ok && l != nil {
		return l
	}
	return slog.Default()
}

// fields 在 context 里累积的固定字段。
type fields map[string]any

// WithFields 往 context 上叠加固定日志字段(如 request_id、domain、account_id)。
//
// 字段会叠加到该 context 派生的**每一条**日志上。
func WithFields(ctx context.Context, kv ...any) context.Context {
	fs, _ := ctx.Value(ctxKeyFields).(fields)
	if fs == nil {
		fs = fields{}
	} else {
		// 复制一份,避免改动兄弟 context 共享的 map
		copied := make(fields, len(fs)+len(kv)/2)
		for k, v := range fs {
			copied[k] = v
		}
		fs = copied
	}
	for i := 0; i+1 < len(kv); i += 2 {
		key, ok := kv[i].(string)
		if !ok {
			continue
		}
		fs[key] = kv[i+1]
	}
	return context.WithValue(ctx, ctxKeyFields, fs)
}

// L 返回带全部上下文字段的日志器。
func L(ctx context.Context) *slog.Logger {
	l := FromContext(ctx)
	if fs, ok := ctx.Value(ctxKeyFields).(fields); ok && len(fs) > 0 {
		return l.With(fsToArgs(fs)...)
	}
	return l
}

func fsToArgs(fs fields) []any {
	out := make([]any, 0, len(fs)*2)
	for k, v := range fs {
		out = append(out, k, v)
	}
	return out
}

// Redacted 标记一个值在日志中必须脱敏。
//
// 用法:log.Info("...", "token", log.Redact(token)) —— 打印日志的人一眼能看出
// 这里有敏感值,而不是不小心把明文写进日志。
type Redacted string

// String 实现 fmt.Stringer,输出固定掩码而不是原值。
func (r Redacted) String() string { return "[REDACTED]" }

// LogValue 实现 slog.LogValuer。
func (r Redacted) LogValue() slog.Value { return slog.StringValue("[REDACTED]") }

// SensitiveKeys 是默认脱敏的字段名(大小写不敏感)。
//
// 命中这些 key 的日志字段会被替换成掩码,防止「不小心」把密码、
// 令牌、密钥写进日志(docs/security.md 6.3)。
var SensitiveKeys = []string{
	"password", "new_password", "old_password", "token", "access_token",
	"refresh_token", "id_token", "client_secret", "secret", "private_key",
	"cookie", "authorization", "key_master_secret", "session_token",
}

// IsSensitiveKey 判断字段名是否需要脱敏。
func IsSensitiveKey(key string) bool {
	k := strings.ToLower(key)
	for _, s := range SensitiveKeys {
		if k == s || strings.Contains(k, s) {
			return true
		}
	}
	return false
}

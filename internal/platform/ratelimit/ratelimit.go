// Package ratelimit 提供进程内限流。
//
// 平台层能力,与业务无关。认证接口的限流是安全措施而不是性能措施
// (docs/security.md 2.3),所以实现刻意保守:宁可少放过,
// 也不要在攻击流量下把验证逻辑打穿。
//
// 选型说明:用的是**进程内**计数。多实例部署时每个实例各限各的,
// 实际阈值会放大到「实例数 × 配置值」。这在当前规模下可接受 ——
// 真要精确就得引入 Redis,那会给「单进程单容器」的部署形态平添依赖。
// 需要更严格时,把 Limiter 换成共享存储实现即可,调用方不用改。
package ratelimit

import (
	"sync"
	"time"
)

// Limiter 是限流器。
type Limiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	// now 可注入,便于测试
	now func() time.Time
	// maxBuckets 是桶数量上限,防止攻击者用随机键把内存打满
	maxBuckets int
	// idleTTL 是多久清理一次没用到的桶
	idleTTL time.Duration
}

type bucket struct {
	count  int
	reset  time.Time
	lastAt time.Time
}

// New 创建限流器。
func New(now func() time.Time) *Limiter {
	if now == nil {
		now = time.Now
	}
	return &Limiter{
		buckets:    map[string]*bucket{},
		now:        now,
		maxBuckets: 100000,
		idleTTL:    10 * time.Minute,
	}
}

// Allow 判断是否放行,并在放行时计数 +1。
//
// 固定窗口计数:实现简单、内存有界。代价是窗口边界可能出现
// 「短时间 2 倍流量」,对登录这种接口可以接受。
func (l *Limiter) Allow(key string, limit int, window time.Duration) bool {
	if limit <= 0 || window <= 0 {
		return true
	}

	now := l.now()

	l.mu.Lock()
	defer l.mu.Unlock()

	b, ok := l.buckets[key]
	if !ok {
		if len(l.buckets) >= l.maxBuckets {
			l.evictLocked(now)
		}
		b = &bucket{reset: now.Add(window)}
		l.buckets[key] = b
	}

	b.lastAt = now
	if now.After(b.reset) {
		// 窗口已过,重新计数
		b.count = 0
		b.reset = now.Add(window)
	}
	if b.count >= limit {
		return false
	}
	b.count++
	return true
}

// Remaining 返回窗口内还剩多少次额度。
func (l *Limiter) Remaining(key string, limit int, window time.Duration) int {
	now := l.now()

	l.mu.Lock()
	defer l.mu.Unlock()

	b, ok := l.buckets[key]
	if !ok {
		return limit
	}
	if now.After(b.reset) {
		return limit
	}
	left := limit - b.count
	if left < 0 {
		return 0
	}
	return left
}

// Reset 清空某个键的计数。
func (l *Limiter) Reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.buckets, key)
}

// evictLocked 在桶数量超限时清理闲置桶。只在持有锁时调用。
func (l *Limiter) evictLocked(now time.Time) {
	threshold := now.Add(-l.idleTTL)
	for k, b := range l.buckets {
		if b.lastAt.Before(threshold) {
			delete(l.buckets, k)
		}
	}
	// 清理后仍然超限(全是活跃桶):丢掉最老的那个,
	// 让攻击者无法通过构造大量新键把内存吃光。
	if len(l.buckets) >= l.maxBuckets {
		var oldestKey string
		var oldest time.Time
		for k, b := range l.buckets {
			if oldestKey == "" || b.lastAt.Before(oldest) {
				oldestKey, oldest = k, b.lastAt
			}
		}
		if oldestKey != "" {
			delete(l.buckets, oldestKey)
		}
	}
}

// Scope 是一组限流规则。
//
// 阈值全部取自 docs/api.md 第七节。
type Scope struct {
	// Limit 是窗口内允许的次数
	Limit int
	// Window 是窗口长度
	Window time.Duration
}

// 预置的限流规则。
var (
	// Login 登录:5 次/分钟,键为 IP + 邮箱
	Login = Scope{Limit: 5, Window: time.Minute}
	// PasswordReset 找回密码请求:3 次/小时,键为 IP
	PasswordReset = Scope{Limit: 3, Window: time.Hour}
	// MailResend 邮件重发:1 次/60 秒,键为账号
	MailResendCooldown = Scope{Limit: 1, Window: 60 * time.Second}
	// MailDaily 邮件每日上限:5 次/天,键为账号
	MailDaily = Scope{Limit: 5, Window: 24 * time.Hour}
	// Authorize 授权端点:60 次/分钟,键为 IP
	Authorize = Scope{Limit: 60, Window: time.Minute}
	// Token 令牌端点:120 次/分钟,键为 IP
	Token = Scope{Limit: 120, Window: time.Minute}
	// MC 认证:60 次/分钟,键为 IP
	MC = Scope{Limit: 60, Window: time.Minute}
	// 材质上传:10 次/小时,键为账号
	TextureUpload = Scope{Limit: 10, Window: time.Hour}
	// 材质下载:300 次/分钟,键为 IP
	TextureDownload = Scope{Limit: 300, Window: time.Minute}
)

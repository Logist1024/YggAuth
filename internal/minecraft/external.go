package minecraft

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/yggauth/yggauth/internal/platform/apperr"
)

// ExternalFetcher 从外部皮肤站拉取材质。
//
// 单独成型的理由:外部站是**别人的**服务,随时可能慢、挂、改接口。
// 它必须被完全隔离在熔断器后面 —— 一旦外部站开始超时,请求线程
// 会被逐个拖住 5 秒,几秒内整个服务的皮肤请求就全堵死了。
//
// 本期只有一个实现(远程皮肤站 API),但接口留在这里:
// 将来接 Mojang 官方 API、LotionsDOS、LittleSkin 各自差异不小。
type ExternalFetcher interface {
	// Fetch 拉取指定用户在指定类型的纹理。
	Fetch(ctx context.Context, username string, kind TextureType) ([]byte, error)
	// Enabled 报告是否启用。关闭时调用方直接跳过,一次请求都不发。
	Enabled() bool
}

// ExternalOptions 是回源客户端的构造参数。
type ExternalOptions struct {
	// Enabled 对应 MC_SKIN_EXTERNAL
	Enabled bool
	// BaseURL 是外部皮肤站的基址
	BaseURL string
	// Timeout 是单次调用超时
	Timeout time.Duration
	// FailureThreshold 是熔断阈值:连续失败达该次数后短路
	FailureThreshold int
	// ResetTimeout 是熔断半开前的等待时长
	ResetTimeout time.Duration
	Client       *http.Client
	Logger       Logger
}

// breaker 是极简熔断器。
//
// 三态:closed(正常)→ open(短路)→ half-open(放一个探��过去)。
// 只数连续失败数 —— 一个请求失败就断路会让系统一有抖动就拒绝服务,
// 而这些请求本来就是「失败就降级」的可有可无路径。
type breaker struct {
	mu           sync.Mutex
	failures     int
	openedAt     time.Time
	threshold    int
	resetTimeout time.Duration
}

// allow 判断当前是否放行。
func (b *breaker) allow(now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.failures < b.threshold {
		return true
	}
	if now.Sub(b.openedAt) >= b.resetTimeout {
		// 半开:放一个探针过去。成功后由 record 复位。
		return true
	}
	return false
}

func (b *breaker) recordSuccess() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.failures = 0
}

func (b *breaker) recordFailure(now time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.failures++
	b.openedAt = now
}

// RemoteFetcher 从远程皮肤站回源。
type RemoteFetcher struct {
	opts ExternalOptions
	brk  breaker
	now  func() time.Time
}

// NewRemoteFetcher 创建回源客户端。
func NewRemoteFetcher(opts ExternalOptions) *RemoteFetcher {
	if opts.Timeout <= 0 {
		opts.Timeout = 5 * time.Second
	}
	if opts.FailureThreshold <= 0 {
		opts.FailureThreshold = 5
	}
	if opts.ResetTimeout <= 0 {
		opts.ResetTimeout = time.Minute
	}
	if opts.Client == nil {
		opts.Client = &http.Client{Timeout: opts.Timeout}
	}

	return &RemoteFetcher{
		opts: opts,
		brk:  breaker{threshold: opts.FailureThreshold, resetTimeout: opts.ResetTimeout},
		now:  time.Now,
	}
}

// Enabled 报告是否启用。
func (f *RemoteFetcher) Enabled() bool {
	return f.opts.Enabled && f.opts.BaseURL != ""
}

// Fetch 拉取材质。
func (f *RemoteFetcher) Fetch(ctx context.Context, username string, kind TextureType) ([]byte, error) {
	if !f.Enabled() {
		return nil, apperr.New(apperr.CodeNotFound, "外部回源未启用")
	}

	// 熔断打开时直接降级,**不发起请求**。
	// 这样一次外部站故障的开销是零,而不是每次一个 5 秒超时。
	if !f.brk.allow(f.now()) {
		return nil, apperr.New(apperr.CodeNotFound, "外部皮肤站熔断中")
	}

	endpoint, err := f.endpoint(username, kind)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, f.opts.Timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		f.brk.recordFailure(f.now())
		return nil, apperr.Newf(apperr.CodeInternal, "构造外部请求失败: %v", err)
	}
	// 声明真实 UA。多数皮肤站会拒绝空 UA,结果是「永远拉不到」。
	req.Header.Set("User-Agent", "YggAuth/1.0 (+skin-fetch)")
	req.Header.Set("Accept", "image/png")

	resp, err := f.opts.Client.Do(req)
	if err != nil {
		f.brk.recordFailure(f.now())
		f.logf("外部皮肤站请求失败", "url", endpoint, "error", err)
		return nil, apperr.Newf(apperr.CodeNotFound, "外部皮肤站不可用")
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		f.brk.recordFailure(f.now())
		return nil, apperr.Newf(apperr.CodeNotFound, "外部皮肤站返回 %d", resp.StatusCode)
	}

	data, err := readLimited(resp.Body, f.MaxSize())
	if err != nil {
		f.brk.recordFailure(f.now())
		return nil, apperr.Newf(apperr.CodeNotFound, "读取外部材质失败")
	}

	f.brk.recordSuccess()
	return data, nil
}

func (f *RemoteFetcher) logf(msg string, args ...any) {
	if f.opts.Logger != nil {
		f.opts.Logger.Warn(msg, args...)
	}
}

// MaxSize 是外部材质的体积上限。
//
// 与上传上限一致:回源进来的内容会走和上传完全相同的校验,
// 一个 100MB 的响应体不该先被完整读进内存再被尺寸检查拒绝。
func (f *RemoteFetcher) MaxSize() int64 { return 2 << 20 }

// endpoint 构造外部皮肤站 URL。
func (f *RemoteFetcher) endpoint(username string, kind TextureType) (string, error) {
	base, err := url.Parse(f.opts.BaseURL)
	if err != nil {
		return "", apperr.Newf(apperr.CodeInternal, "外部皮肤站地址非法: %v", err)
	}
	base.Path = fmt.Sprintf("%s/%s/%s.png", base.Path, username, string(kind))
	return base.String(), nil
}

// readLimited 读取至多 limit 字节。
//
// 多读一个字节用来判断是否超限:直接读满 limit 再比较会漏掉
// 「正好等于上限」和「超了一个字节」之间的区别。
func readLimited(r io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, apperr.New(apperr.CodeInvalidArgument, "外部材质超过体积上限")
	}
	return data, nil
}

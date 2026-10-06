package mailer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/yggauth/yggauth/internal/identity/account"
)

// Hot 是可热替换的发件器(docs/configuration.md §7.3)。
//
// 启动时用初始配置建一次;后台改 `mail.*` 后调 Reload,用**设置里的现值**
// 重建并原子替换。于是「改完 SMTP 设置」不再等于「改完还得重启容器」。
type Hot struct {
	mu     sync.RWMutex
	sender Sender
	cfg    Config
	logger *slog.Logger
	build  func(Config) (Sender, error)
}

// NewHot 用初始配置建一个热发件器。
func NewHot(cfg Config, logger *slog.Logger) *Hot {
	if logger == nil {
		logger = slog.Default()
	}
	h := &Hot{logger: logger}
	h.build = func(c Config) (Sender, error) { return New(c, h.logger), nil }
	// 构建只是选个实现(console 或 smtp),不会失败;
	// 真正会坏的环节(端口、密码)由后台的测试信端点逐阶段暴露。
	h.sender, _ = h.build(cfg)
	h.cfg = cfg
	return h
}

// Send 用当前生效的发件器发信。
func (h *Hot) Send(ctx context.Context, msg account.Message) error {
	h.mu.RLock()
	s := h.sender
	h.mu.RUnlock()
	return s.Send(ctx, msg)
}

// Reload 用新配置重建发件器并原子替换。
//
// 重建失败**保留旧的**:发信至少还在按老配置工作,而错误会被原样返回给
// 管理员 —— 「保存成功但发信已经坏了」且毫无提示,是这条链路上最坏的结果。
func (h *Hot) Reload(cfg Config) error {
	next, err := h.build(cfg)
	if err != nil {
		return fmt.Errorf("重建发件器失败,已保留原配置: %w", err)
	}
	h.mu.Lock()
	h.sender, h.cfg = next, cfg
	h.mu.Unlock()
	return nil
}

// Config 返回当前生效的配置。
func (h *Hot) Config() Config {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.cfg
}

// TestResult 是一封测试信的阶段化结果(docs/configuration.md §7.4)。
type TestResult struct {
	Transport string `json:"transport"`
	Stage     Stage  `json:"stage"`
	OK        bool   `json:"ok"`
	Error     string `json:"error,omitempty"`
	// Note 是给操作者的一句提醒,例如 console 模式根本不会投递
	Note string `json:"note,omitempty"`
}

// SendTest 用**已保存的生效配置**发一封测试信。
//
// 必须先保存再测:拿草稿去测、生效的是另一套配置,会出现
// 「测的时候好好的、真发的时候是坏的」这种最费时间的组合。
func SendTest(ctx context.Context, cfg Config, to string) TestResult {
	res := TestResult{Transport: cfg.Transport, Stage: StageConnect}

	msg := account.Message{
		To:      to,
		Subject: "YggAuth 发件配置测试",
		Text: "这是一封来自 YggAuth 后台的测试邮件。\n" +
			"如果你收到了它,说明后台保存的发件配置可以正常投递。\n",
	}

	if err := New(cfg, slog.Default()).Send(ctx, msg); err != nil {
		res.Stage = StageOf(err)
		res.Error = stageMessage(err)
		return res
	}

	res.OK = true
	res.Stage = StageSend
	if cfg.Transport != "smtp" {
		res.Note = "当前 transport=console:邮件只写进日志,不会真的投递到收件箱"
	}
	return res
}

// stageMessage 取给管理员看的错误正文:阶段已经单列,这里给根因。
func stageMessage(err error) string {
	var se *StageError
	if errors.As(err, &se) {
		return se.Err.Error()
	}
	return err.Error()
}

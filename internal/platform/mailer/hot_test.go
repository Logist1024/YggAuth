package mailer

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/yggauth/yggauth/internal/identity/account"
)

// TestHotReloadSwapsSender 验证热重建真的换了发件器:
// 重建后 Config() 是新值,而且**发信路径**也换到了新实现
// (对着一个必然拒绝的目标发信,必须报错 —— 否则等于什么都没验证)。
func TestHotReloadSwapsSender(t *testing.T) {
	h := NewHot(Config{Transport: "console", From: "noreply@localhost"}, nil)
	if got := h.Config().Transport; got != "console" {
		t.Fatalf("初值 = %q, want console", got)
	}

	// 127.0.0.1:1 必然拒绝连接:不碰真实网络,也不会等十秒
	if err := h.Reload(Config{
		Transport: "smtp", SMTPHost: "127.0.0.1", SMTPPort: 1,
		Timeout: time.Second, From: "noreply@localhost",
	}); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if got := h.Config().SMTPHost; got != "127.0.0.1" {
		t.Fatalf("重建后 host = %q", got)
	}

	err := h.Send(context.Background(), account.Message{
		To: "admin@example.com", Subject: "t", Text: "t",
	})
	if err == nil {
		t.Fatal("发信仍走旧的 console 实现(日志发送器不可能报错),重建没有生效")
	}
}

// TestHotReloadFailureKeepsOldSender 验证「重建失败保留旧配置」:
// 发信至少还要按老配置工作,并且错误原样交给调用方
// (docs/configuration.md §7.3)。
func TestHotReloadFailureKeepsOldSender(t *testing.T) {
	h := NewHot(Config{Transport: "console"}, nil)
	boom := errors.New("配置不合要求")
	h.build = func(Config) (Sender, error) { return nil, boom }

	err := h.Reload(Config{Transport: "smtp"})
	if err == nil {
		t.Fatal("重建失败必须报错,不能静默")
	}
	if !errors.Is(err, boom) {
		t.Fatalf("错误没有带上根因: %v", err)
	}
	if got := h.Config().Transport; got != "console" {
		t.Fatalf("重建失败却换掉了原配置: %q", got)
	}
}

// TestSendStageConnect 失败必须钉住阶段:「发送失败」四个字什么也没说,
// 看到 connect 才知道是地址/网络,tls 是端口配错(465 要勾隐式 TLS),
// auth 才是账号密码(docs/configuration.md §7.4)。
func TestSendStageConnect(t *testing.T) {
	err := (realSMTP{}).send(context.Background(), Config{
		Transport: "smtp", SMTPHost: "127.0.0.1", SMTPPort: 1, Timeout: time.Second,
	}, "noreply@localhost", "admin@example.com", []byte("Subject: t\r\n\r\nbody"))

	if err == nil {
		t.Fatal("127.0.0.1:1 必然拒绝连接,不该成功")
	}
	var se *StageError
	if !errors.As(err, &se) {
		t.Fatalf("错误没有阶段信息: %v", err)
	}
	if se.Stage != StageConnect {
		t.Fatalf("阶段 = %q, want connect", se.Stage)
	}
	if StageOf(err) != StageConnect {
		t.Fatalf("StageOf = %q", StageOf(err))
	}
	if !strings.Contains(err.Error(), "connect 阶段失败") {
		t.Fatalf("错误文案要带阶段: %v", err)
	}
}

// TestSendTestConsoleOk 验证 console 模式的测试信:流程走通、
// 但必须提醒「不会真的投递」—— 否则「成功了」会被读成「收件箱里有信」。
func TestSendTestConsoleOk(t *testing.T) {
	res := SendTest(context.Background(), Config{
		Transport: "console", From: "noreply@localhost",
	}, "admin@example.com")

	if !res.OK {
		t.Fatalf("console 不该失败: %+v", res)
	}
	if res.Stage != StageSend {
		t.Fatalf("stage = %q, want send", res.Stage)
	}
	if res.Transport != "console" {
		t.Fatalf("transport = %q", res.Transport)
	}
	if !strings.Contains(res.Note, "console") {
		t.Fatalf("要提醒 console 不真发信: %q", res.Note)
	}
}

// TestSendTestSmtpFailsWithStage 验证 SMTP 测试信把失败钉在阶段上。
func TestSendTestSmtpFailsWithStage(t *testing.T) {
	res := SendTest(context.Background(), Config{
		Transport: "smtp", SMTPHost: "127.0.0.1", SMTPPort: 1,
		Timeout: time.Second, From: "noreply@localhost",
	}, "admin@example.com")

	if res.OK {
		t.Fatalf("连不上 SMTP 却报成功: %+v", res)
	}
	if res.Stage != StageConnect {
		t.Fatalf("stage = %q, want connect", res.Stage)
	}
	if res.Error == "" {
		t.Fatal("失败要给出可读的错误正文")
	}
}

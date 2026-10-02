// Package mailer 实现邮件发送。
//
// 平台层能力,与业务无关。两种传输方式:
//   - console:把邮件内容写进日志,开发与无邮件服务的部署用它
//   - smtp:标准 SMTP,支持 STARTTLS 与隐式 TLS
//
// 安全约束(docs/09-security.md 6.3):日志里**不打印邮件正文**。
// 正文含重置令牌,写进日志等于把令牌泄露给任何能看到日志的人。
// console 模式只打收件人与主题。
package mailer

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"log/slog"
	"net"
	"net/smtp"
	"strings"
	"sync"
	"time"

	"github.com/yggauth/yggauth/internal/identity/account"
)

// Config 是邮件发送配置。
type Config struct {
	// Transport: console | smtp
	Transport string
	From      string
	FromName  string

	SMTPHost string
	SMTPPort int
	Username string
	Password string
	// UseTLS 表示连上就用隐式 TLS(通常 465 端口);
	// 否则先明文连接再 STARTTLS(通常 587 端口)
	UseTLS bool
	// Timeout 是单封邮件的网络超时
	Timeout time.Duration
}

// Sender 发送邮件。
type Sender interface {
	Send(ctx context.Context, msg account.Message) error
}

// Console 把邮件记进日志,不真正发送。
type Console struct {
	logger *slog.Logger
}

// NewConsole 创建日志发送器。
func NewConsole(logger *slog.Logger) *Console {
	if logger == nil {
		logger = slog.Default()
	}
	return &Console{logger: logger}
}

// Send 只记元信息,**不记录正文**。
func (c *Console) Send(_ context.Context, msg account.Message) error {
	c.logger.Info("mail queued",
		"transport", "console",
		"to", msg.To,
		"subject", msg.Subject,
		// 正文含一次性令牌,写进日志等于泄露。开发时需要看内容,
		// 请自行把 msg.Text 打到终端而不是打到日志管道。
		"body_logged", false,
	)
	return nil
}

// SMTP 通过 SMTP 服务器发送。
type SMTP struct {
	cfg    Config
	sender smtpSender
	// mu 保护 sender 的懒初始化
	mu sync.Mutex
}

// smtpSender 抽象 SMTP 交互,便于测试替换。
type smtpSender interface {
	send(ctx context.Context, cfg Config, from, to string, msg []byte) error
	close() error
}

type realSMTP struct{}

// send 通过 net/smtp 发信。
func (realSMTP) send(ctx context.Context, cfg Config, from, to string, msg []byte) error {
	addr := net.JoinHostPort(cfg.SMTPHost, fmt.Sprintf("%d", cfg.SMTPPort))

	dialer := &net.Dialer{Timeout: cfg.Timeout}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("连接 SMTP 服务器失败: %w", err)
	}
	_ = conn.SetDeadline(time.Now().Add(cfg.Timeout))

	if cfg.UseTLS {
		tlsConn := tls.Client(conn, &tls.Config{
			ServerName: cfg.SMTPHost,
			MinVersion: tls.VersionTLS12,
		})
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			_ = conn.Close()
			return fmt.Errorf("SMTP TLS 握手失败: %w", err)
		}
		conn = tlsConn
	}

	c, err := smtp.NewClient(conn, cfg.SMTPHost)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("初始化 SMTP 客户端失败: %w", err)
	}
	defer func() { _ = c.Close() }()

	if !cfg.UseTLS {
		// 隐式 TLS 之外,现代 SMTP 一律先 STARTTLS 再认证凭据
		if ok, _ := c.Extension("STARTTLS"); ok {
			if err := c.StartTLS(&tls.Config{
				ServerName: cfg.SMTPHost,
				MinVersion: tls.VersionTLS12,
			}); err != nil {
				return fmt.Errorf("SMTP STARTTLS 失败: %w", err)
			}
		}
	}

	if cfg.Username != "" {
		if ok, _ := c.Extension("AUTH"); ok {
			if err := c.Auth(smtp.PlainAuth("", cfg.Username, cfg.Password, cfg.SMTPHost)); err != nil {
				return fmt.Errorf("SMTP 认证失败: %w", err)
			}
		}
	}

	if err := c.Mail(from); err != nil {
		return fmt.Errorf("SMTP MAIL FROM 失败: %w", err)
	}
	if err := c.Rcpt(to); err != nil {
		return fmt.Errorf("SMTP RCPT TO 失败: %w", err)
	}

	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("SMTP DATA 失败: %w", err)
	}
	if _, err := w.Write(msg); err != nil {
		_ = w.Close()
		return fmt.Errorf("写入邮件正文失败: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("结束邮件正文失败: %w", err)
	}
	return c.Quit()
}

func (realSMTP) close() error { return nil }

// NewSMTP 创建 SMTP 发送器。
func NewSMTP(cfg Config) *SMTP {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 10 * time.Second
	}
	return &SMTP{cfg: cfg, sender: realSMTP{}}
}

// New 创建发送器。
func New(cfg Config, logger *slog.Logger) Sender {
	switch strings.ToLower(cfg.Transport) {
	case "smtp":
		return NewSMTP(cfg)
	default:
		return NewConsole(logger)
	}
}

// Send 发一封邮件。
func (s *SMTP) Send(ctx context.Context, msg account.Message) error {
	raw, err := s.buildMessage(msg)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sender.send(ctx, s.cfg, s.cfg.From, msg.To, raw)
}

// buildMessage 拼出 RFC 5322 邮件。
func (s *SMTP) buildMessage(msg account.Message) ([]byte, error) {
	subject := encodeHeader(msg.Subject)
	from := s.cfg.From
	if s.cfg.FromName != "" {
		from = fmt.Sprintf("%s <%s>", encodeHeader(s.cfg.FromName), s.cfg.From)
	}

	var b strings.Builder
	b.WriteString("From: " + from + "\r\n")
	b.WriteString("To: " + msg.To + "\r\n")
	b.WriteString("Subject: " + subject + "\r\n")
	b.WriteString("Date: " + time.Now().UTC().Format(time.RFC1123Z) + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	b.WriteString("\r\n")

	body := msg.Text
	if body == "" {
		body = stripTags(msg.HTML)
	}
	b.WriteString(body)
	b.WriteString("\r\n")
	return []byte(b.String()), nil
}

// encodeHeader 编码含非 ASCII 字符的头字段。
func encodeHeader(s string) string {
	for i := range s {
		if s[i] > 127 {
			return "=?UTF-8?B?" + base64Encode(s) + "?="
		}
	}
	return s
}

// stripTags 粗略去掉 HTML 标签,作为纯文本兜底。
func stripTags(html string) string {
	var b strings.Builder
	depth := 0
	for _, r := range html {
		switch r {
		case '<':
			depth++
		case '>':
			if depth > 0 {
				depth--
			}
		default:
			if depth == 0 {
				b.WriteRune(r)
			}
		}
	}
	return b.String()
}

// base64Encode 做标准 base64 编码。
func base64Encode(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

// Command yggauth 是 YggAuth 的进程入口。
//
// 子命令:
//
//	serve       启动 HTTP 服务(默认)
//	migrate     执行数据库迁移
//	healthcheck 供容器探针调用,探测 /health/ready
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/yggauth/yggauth/internal/platform/health"
	"github.com/yggauth/yggauth/internal/transport"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "FATAL %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	// 无子命令时默认 serve,方便 `docker run yggauth` 直接起服务。
	cmd := "serve"
	if len(os.Args) > 1 && len(os.Args[1]) > 0 && os.Args[1][0] != '-' {
		cmd = os.Args[1]
		os.Args = append(os.Args[:1], os.Args[2:]...)
	}

	switch cmd {
	case "serve":
		return runServe(os.Args[1:])
	case "migrate":
		return runMigrate(os.Args[1:])
	case "healthcheck":
		return runHealthcheck(os.Args[1:])
	case "version":
		fmt.Println(versionString())
		return nil
	default:
		return fmt.Errorf("未知子命令 %q,可用:serve / migrate / healthcheck / version", cmd)
	}
}

const (
	readTimeout     = 15 * time.Second
	writeTimeout    = 30 * time.Second
	idleTimeout     = 60 * time.Second
	shutdownTimeout = 20 * time.Second
)

func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	host := fs.String("host", envOr("APP_HOST", "0.0.0.0"), "监听地址")
	port := fs.Int("port", envIntOr("APP_PORT", 3000), "监听端口")
	if err := fs.Parse(args); err != nil {
		return err
	}

	// M0:空服务,只有探针。M1 起在这里接配置、日志与数据库。
	rt := transport.NewRouter(health.NewHandler(nil, 0))

	addr := fmt.Sprintf("%s:%d", *host, *port)
	srv := &http.Server{
		Addr:              addr,
		Handler:           rt.Handler(),
		ReadHeaderTimeout: readTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		fmt.Printf("yggauth listening on %s\n", addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		fmt.Println("shutting down...")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}

func runMigrate(args []string) error {
	// M1 接入 goose 后实现。
	return errors.New("migrate 子命令将在 M1 实现")
}

func runHealthcheck(args []string) error {
	fs := flag.NewFlagSet("healthcheck", flag.ContinueOnError)
	host := fs.String("host", envOr("APP_HOST", "127.0.0.1"), "服务地址")
	port := fs.Int("port", envIntOr("APP_PORT", 3000), "服务端口")
	if err := fs.Parse(args); err != nil {
		return err
	}

	url := fmt.Sprintf("http://%s:%d/health/ready", *host, *port)
	client := &http.Client{Timeout: 5 * time.Second}

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("构造健康检查请求失败: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("健康检查失败: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("服务未就绪,状态码 %d", resp.StatusCode)
	}
	return nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envIntOr(key string, fallback int) int {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	var n int
	if _, err := fmt.Sscanf(v, "%d", &n); err != nil {
		return fallback
	}
	return n
}

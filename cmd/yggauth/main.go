// Command yggauth 是 YggAuth 的进程入口。
//
// 子命令:
//
//	serve       启动 HTTP 服务(默认)
//	migrate     执行数据库迁移(up / down / status)
//	admin       运维子命令:create 手工创建平台管理员
//	healthcheck 供容器探针调用,探测 /health/ready
//	version     打印版本信息
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/yggauth/yggauth/internal/config"
	"github.com/yggauth/yggauth/internal/platform/db"
	"github.com/yggauth/yggauth/internal/platform/log"
	"github.com/yggauth/yggauth/internal/platform/metrics"
	"github.com/yggauth/yggauth/internal/transport"
)

const (
	readTimeout     = 15 * time.Second
	writeTimeout    = 30 * time.Second
	idleTimeout     = 60 * time.Second
	shutdownTimeout = 20 * time.Second
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
	if len(os.Args) > 1 && os.Args[1][0] != '-' {
		cmd = os.Args[1]
		os.Args = append(os.Args[:1], os.Args[2:]...)
	}

	switch cmd {
	case "serve":
		return runServe(os.Args[1:])
	case "migrate":
		return runMigrate(os.Args[1:])
	case "admin":
		return runAdmin(os.Args[1:])
	case "healthcheck":
		return runHealthcheck(os.Args[1:])
	case "version":
		fmt.Println(versionString())
		return nil
	default:
		return fmt.Errorf("未知子命令 %q,可用:serve / migrate / admin / healthcheck / version", cmd)
	}
}

func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	host := fs.String("host", "", "覆盖 APP_HOST")
	port := fs.Int("port", 0, "覆盖 APP_PORT")
	if err := fs.Parse(args); err != nil {
		return err
	}

	// 配置校验失败必须直接退出。带着错误配置继续启动,
	// 只会在更晚的地方以更难排查的方式失败。
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if *host != "" {
		cfg.App.Host = *host
	}
	if *port != 0 {
		cfg.App.Port = *port
	}

	logger := log.New(log.Options{
		Level:     cfg.Log.Level,
		Format:    cfg.Log.Format,
		AddSource: cfg.Log.AddSource,
	})

	// 启动日志只打配置摘要,敏感项脱敏(docs/security.md 6.3)
	for k, v := range cfg.Redacted() {
		logger.Info("config", "key", k, "value", v)
	}
	logger.Info("starting yggauth",
		"version", versionString(),
		"go", runtime.Version(),
		"pid", os.Getpid(),
	)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pool, err := openDatabase(ctx, cfg, logger)
	if err != nil {
		return err
	}
	defer pool.Close()
	startPoolCollector(pool, logger)

	if cfg.DB.AutoMigrate {
		if err := runMigrations(ctx, pool, cfg, logger); err != nil {
			return err
		}
	}

	// 首启引导:迁移之后、监听之前(理由见 runBootstrap 注释)
	if err := runBootstrap(ctx, pool, cfg, logger); err != nil {
		return err
	}

	rt := transport.New(buildDeps(cfg, logger, pool))

	srv := &http.Server{
		Addr:              cfg.App.Addr(),
		Handler:           rt.Handler(),
		ReadHeaderTimeout: readTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("http server listening", "addr", srv.Addr)
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
		logger.Info("shutdown signal received, draining connections")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.App.ShutdownTimeout)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("优雅退出失败: %w", err)
		}
		logger.Info("shutdown complete")
		return nil
	}
}

func openDatabase(ctx context.Context, cfg *config.Config, logger *slog.Logger) (*db.Pool, error) {
	pool, err := db.Open(ctx, db.Config{
		Host:            cfg.DB.Host,
		Port:            cfg.DB.Port,
		User:            cfg.DB.User,
		Password:        cfg.DB.Password,
		Database:        cfg.DB.Name,
		SSLMode:         cfg.DB.SSLMode,
		MaxConns:        int32(cfg.DB.MaxConns),
		MinConns:        int32(cfg.DB.MinConns),
		MaxConnLifetime: cfg.DB.MaxConnLifetime,
		MaxConnIdleTime: cfg.DB.MaxConnIdleTime,
		ConnectTimeout:  cfg.DB.ConnectTimeout,
	}, logger)
	if err != nil {
		return nil, fmt.Errorf("数据库连接失败: %w", err)
	}
	return pool, nil
}

func runMigrations(ctx context.Context, pool *db.Pool, cfg *config.Config, logger *slog.Logger) error {
	m, err := db.NewMigrator(pool, cfg.App.MigrationsDir, logger)
	if err != nil {
		return err
	}
	defer m.Close()

	if err := m.Up(ctx); err != nil {
		return err
	}
	return nil
}

// startPoolCollector 周期上报连接池指标。
func startPoolCollector(pool *db.Pool, logger *slog.Logger) {
	report := func() {
		s := db.PoolStats(pool)
		metrics.DBPoolConns.WithLabelValues("in_use").Set(float64(s.AcquiredConns))
		metrics.DBPoolConns.WithLabelValues("idle").Set(float64(s.IdleConns))
		metrics.DBPoolConns.WithLabelValues("max").Set(float64(s.MaxConns))
	}
	report()

	go func() {
		t := time.NewTicker(15 * time.Second)
		defer t.Stop()
		for range t.C {
			report()
		}
	}()
	logger.Info("db pool collector started", "interval", "15s")
}

func runMigrate(args []string) error {
	fs := flag.NewFlagSet("migrate", flag.ContinueOnError)
	action := fs.String("to", "up", "up | down | status")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	logger := log.New(log.Options{Level: cfg.Log.Level, Format: cfg.Log.Format})

	ctx := context.Background()
	pool, err := openDatabase(ctx, cfg, logger)
	if err != nil {
		return err
	}
	defer pool.Close()

	m, err := db.NewMigrator(pool, cfg.App.MigrationsDir, logger)
	if err != nil {
		return err
	}
	defer m.Close()

	switch *action {
	case "up":
		return m.Up(ctx)
	case "down":
		return m.Down(ctx)
	case "status":
		lines, err := m.Status(ctx)
		if err != nil {
			return err
		}
		for _, l := range lines {
			fmt.Println(l)
		}
		return nil
	default:
		return fmt.Errorf("未知迁移动作 %q,可用:up / down / status", *action)
	}
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

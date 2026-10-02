//go:build integration

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/yggauth/yggauth/internal/platform/db/testdb"
)

// freePortPort 返回一个空闲端口,避免并行跑测试时端口冲突。
func freePortPort(t *testing.T) int {
	t.Helper()
	var lc net.ListenConfig
	l, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port
}

// serveEnv 铺一份能启动服务的环境变量。
func serveEnv(t *testing.T, port int) {
	t.Helper()
	_, dbPort, dbUser, dbPassword, _ := testdb.Connect(t).DSNParts()

	t.Setenv("APP_PUBLIC_DOMAIN", "auth.example.com")
	t.Setenv("PUBLIC_BASE_URL", "https://auth.example.com")
	t.Setenv("DB_HOST", "127.0.0.1")
	t.Setenv("DB_PORT", fmt.Sprintf("%d", dbPort))
	t.Setenv("DB_NAME", "yggauth_smoke")
	t.Setenv("DB_USER", dbUser)
	t.Setenv("DB_PASSWORD", dbPassword)
	t.Setenv("KEY_MASTER_SECRET", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	t.Setenv("SSO_COOKIE_SECURE", "true")
	t.Setenv("LOG_LEVEL", "warn")
	t.Setenv("DATA_DIR", t.TempDir())
}

// M1 验收:非法配置必须启动即失败,并明确指出缺了哪一项。
func TestServeFailsFastOnInvalidConfig(t *testing.T) {
	// 故意不给必填项
	for _, k := range []string{
		"APP_PUBLIC_DOMAIN", "PUBLIC_BASE_URL", "DB_PASSWORD", "KEY_MASTER_SECRET",
	} {
		t.Setenv(k, "")
		require.NoError(t, os.Unsetenv(k))
	}

	err := runServe(nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "APP_PUBLIC_DOMAIN 未设置")
	require.Contains(t, err.Error(), "KEY_MASTER_SECRET 未设置")
}

// M1 验收:能迁移、能起服务、/health/ready 返回 DB 状态、/metrics 可用。
func TestServeBootstrapsWithRealDatabase(t *testing.T) {
	port := freePortPort(t)
	serveEnv(t, port)

	// 在独立库上跑迁移,避免污染其他用例共享的库
	// 在独立库上跑迁移,避免污染其他用例共享的库
	pool := testdb.NewPool(t)
	ctx := t.Context()
	_, err := pool.Exec(ctx, `CREATE DATABASE yggauth_smoke`)
	require.NoError(t, err)

	go func() {
		// 服务会一直跑到测试进程结束,这里忽略返回
		_ = runServe([]string{"--host", "127.0.0.1", "--port", fmt.Sprintf("%d", port)})
	}()

	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	client := &http.Client{Timeout: 3 * time.Second}

	// 等待服务完成迁移并就绪
	waitReady(t, client, base)

	// 存活探针
	rec := doGet(t, client, base+"/health/live")
	require.Equal(t, http.StatusOK, rec.StatusCode)

	// 就绪探针:数据库 up
	rec = doGet(t, client, base+"/health/ready")
	require.Equal(t, http.StatusOK, rec.StatusCode)

	body, err := io.ReadAll(rec.Body)
	require.NoError(t, err)

	var ready struct {
		Status       string `json:"status"`
		Dependencies []struct {
			Name   string `json:"name"`
			Status string `json:"status"`
		} `json:"dependencies"`
	}
	require.NoError(t, json.Unmarshal(body, &ready))
	require.Equal(t, "ready", ready.Status)
	require.Len(t, ready.Dependencies, 1)
	require.Equal(t, "postgres", ready.Dependencies[0].Name)
	require.Equal(t, "up", ready.Dependencies[0].Status)

	// 指标端点
	rec = doGet(t, client, base+"/metrics")
	require.Equal(t, http.StatusOK, rec.StatusCode)

	// 迁移确实建了四个 schema
	smokePool := testdb.NewPoolFor(t, "yggauth_smoke")
	var schemaCount int
	err = smokePool.QueryRow(ctx, `
		SELECT count(*) FROM information_schema.schemata
		WHERE schema_name IN ('identity','oidc','minecraft','app')`).Scan(&schemaCount)
	require.NoError(t, err)
	require.Equal(t, 4, schemaCount)
}

func waitReady(t *testing.T, client *http.Client, base string) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, base+"/health/ready", nil)
		if err != nil {
			lastErr = err
		} else {
			resp, derr := client.Do(req)
			if derr == nil {
				_ = resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					return
				}
				lastErr = fmt.Errorf("状态码 %d", resp.StatusCode)
			} else {
				lastErr = derr
			}
		}
		time.Sleep(300 * time.Millisecond)
	}
	t.Fatalf("服务在 60 秒内未就绪: %v", lastErr)
}

func doGet(t *testing.T, client *http.Client, url string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	require.NoError(t, err)
	resp, err := client.Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

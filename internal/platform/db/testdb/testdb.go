// Package testdb 为集成测试提供真实的 PostgreSQL 实例。
//
// 用 embedded-postgres 在本机拉起一个真正的 PostgreSQL 16 进程(非 root 也能跑),
// 这样 sqlc 生成的 SQL、goose 迁移、约束与索引都能被真实验证,
// 而不是只对着 mock 猜行为。
//
// 只被 _test.go 引用,不会进生产二进制。
package testdb

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	neturl "net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/yggauth/yggauth/internal/platform/db"
)

// 进程内共享一个实例:拉起 PostgreSQL 约需数秒,每个包各起一个太慢。
var (
	sharedOnce sync.Once
	shared     *Instance
	sharedErr  error
)

// Instance 是一套可用的测试数据库。
type Instance struct {
	DSN      string
	Pool     *db.Pool
	Database string
}

// Connect 返回进程内共享的测试数据库,首次调用时会真实拉起 PostgreSQL 16。
func Connect(t *testing.T) *Instance {
	t.Helper()

	sharedOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		shared, sharedErr = start(ctx)
	})
	if sharedErr != nil {
		t.Fatalf("启动嵌入式 PostgreSQL 失败: %v", sharedErr)
	}
	return shared
}

func start(ctx context.Context) (*Instance, error) {
	root, err := os.MkdirTemp("", "yggauth-pg-*")
	if err != nil {
		return nil, fmt.Errorf("创建临时目录失败: %w", err)
	}

	// 端口用 0 让内核分配空闲端口,避免并行跑测试时互相抢占。
	port := freePort()

	pg := embeddedpostgres.NewDatabase(embeddedpostgres.DefaultConfig().
		Version(embeddedpostgres.V16).
		Port(port).
		Database("yggauth_test").
		Username("yggauth_test").
		Password("yggauth_test").
		RuntimePath(filepath.Join(root, "run")).
		DataPath(filepath.Join(root, "data")).
		BinariesPath(binDir()).
		StartTimeout(3 * time.Minute).
		Logger(io.Discard))

	if err := pg.Start(); err != nil {
		// embedded-postgres 首次运行会从 Maven 中央仓库下载 PostgreSQL 二进制,
		// 失败最常见的原因是网络不可达,这里把临时目录留着方便排查。
		return nil, fmt.Errorf("拉起 PostgreSQL 失败(临时目录 %s 保留): %w", root, err)
	}

	dsn := fmt.Sprintf("postgres://yggauth_test:yggauth_test@127.0.0.1:%d/yggauth_test?sslmode=disable", port)
	pool, err := db.Open(ctx, db.Config{DSN: dsn}, nil)
	if err != nil {
		_ = pg.Stop()
		return nil, err
	}

	inst := &Instance{DSN: dsn, Pool: pool, Database: "yggauth_test"}
	cleanupFns = append(cleanupFns, func() {
		pool.Close()
		_ = pg.Stop()
		_ = os.RemoveAll(root)
	})
	return inst, nil
}

// cleanupFns 保存已启动实例的清理动作,由 StopShared 调用。
var cleanupFns []func()

// StopShared 关闭共享实例。由 TestMain 调用。
func StopShared() {
	for i := len(cleanupFns) - 1; i >= 0; i-- {
		cleanupFns[i]()
	}
	cleanupFns = nil
}

// Fresh 返回一个「干净」的数据库:重新跑一遍全部迁移。
//
// 每个测试自己调一次,保证用例之间互不污染 —— 比共享一个被写脏的库可靠得多。
func Fresh(t *testing.T) *Pool {
	t.Helper()

	inst := Connect(t)
	ctx := t.Context()

	m, err := db.NewMigrator(inst.Pool, "", discardLogger())
	if err != nil {
		t.Fatalf("初始化迁移器失败: %v", err)
	}
	defer m.Close()

	// 先全量回滚再全量上迁移,得到与生产初始状态一致的库。
	if err := dropAll(ctx, inst.Pool); err != nil {
		t.Fatalf("清理数据库失败: %v", err)
	}
	if err := m.Up(ctx); err != nil {
		t.Fatalf("执行迁移失败: %v", err)
	}
	return inst.Pool
}

// Pool 是测试用的连接池类型。
type Pool = pgxpool.Pool

// dropAll 删除四个业务 schema 与 goose 版本表,让迁移从零开始。
func dropAll(ctx context.Context, pool *Pool) error {
	_, err := pool.Exec(ctx, `
		DROP SCHEMA IF EXISTS identity CASCADE;
		DROP SCHEMA IF EXISTS oidc CASCADE;
		DROP SCHEMA IF EXISTS minecraft CASCADE;
		DROP SCHEMA IF EXISTS app CASCADE;
		DROP SCHEMA IF EXISTS public CASCADE;
		CREATE SCHEMA public;
		DROP TABLE IF EXISTS goose_db_version;
	`)
	return err
}

// freePort 让内核分配一个空闲端口,避免并行跑测试时互相抢占。
func freePort() uint32 {
	var lc net.ListenConfig
	l, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		// 兜底:拿不到端口就用一个不太可能冲突的高位端口。
		return 55432
	}
	defer func() { _ = l.Close() }()
	addr, ok := l.Addr().(*net.TCPAddr)
	if !ok {
		return 55432
	}
	return uint32(addr.Port)
}

// discardLogger 返回一个丢弃全部输出的 logger,避免迁移日志刷屏测试输出。
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// NewPool 基于共享实例再开一个独立连接池。
//
// 用于「故意把连接池关掉」这类测试:关掉独立池不会影响共享实例,
// 否则后面所有用例会一起失败。
func NewPool(t *testing.T) *db.Pool {
	t.Helper()
	inst := Connect(t)
	pool, err := db.Open(t.Context(), db.Config{DSN: inst.DSN}, nil)
	if err != nil {
		t.Fatalf("创建测试连接池失败: %v", err)
	}
	return pool
}

// binDir 返回 PostgreSQL 二进制的存放目录。
//
// 用固定的、可写的目录而不是每次新建临时目录:二进制只需要下载一次,
// 后续跑测试直接复用,省掉每次几秒到几十秒的下载。
func binDir() string {
	base := os.Getenv("YGG_TEST_PG_BIN_DIR")
	if base == "" {
		cache, err := os.UserCacheDir()
		if err != nil {
			cache = os.TempDir()
		}
		base = filepath.Join(cache, "yggauth-test-pg")
	}
	_ = os.MkdirAll(base, 0o755)
	return base
}

// DSNParts 从 DSN 中拆出连接参数,便于测试拼装自己的连接串。
func (i *Instance) DSNParts() (host string, port int, user, password, database string) {
	u, err := neturl.Parse(i.DSN)
	if err != nil {
		return "127.0.0.1", 5432, "", "", i.Database
	}
	n, convErr := strconv.Atoi(u.Port())
	if convErr != nil {
		n = 5432
	}
	pw, _ := u.User.Password()
	return u.Hostname(), n, u.User.Username(), pw, strings.TrimPrefix(u.Path, "/")
}

// NewPoolFor 在共享实例的指定库里开一个新连接池。
func NewPoolFor(t *testing.T, database string) *db.Pool {
	t.Helper()
	host, port, user, password, _ := Connect(t).DSNParts()
	pool, err := db.Open(t.Context(), db.Config{
		Host: host, Port: port, User: user, Password: password,
		Database: database, SSLMode: "disable",
	}, nil)
	if err != nil {
		t.Fatalf("连接测试库 %s 失败: %v", database, err)
	}
	return pool
}

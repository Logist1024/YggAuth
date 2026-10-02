// Package db 封装 PostgreSQL 连接池与事务辅助。
//
// 平台层能力,与业务无关:这里不认识账号、令牌、皮肤,只认识「连接」与「事务」。
package db

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Pool 是本项目使用的连接池类型,起别名只是为了业务层少写一个包名。
type Pool = pgxpool.Pool

// defaultConnectTimeout 是未配置 DB_CONNECT_TIMEOUT 时的建连超时。
const defaultConnectTimeout = 10 * time.Second

// Config 是连接池配置。
type Config struct {
	Host            string
	Port            int
	User            string
	Password        string
	Database        string
	SSLMode         string
	MaxConns        int32
	MinConns        int32
	MaxConnLifetime time.Duration
	MaxConnIdleTime time.Duration
	ConnectTimeout  time.Duration
	// 连接串覆盖。设置后忽略上面所有字段,便于测试注入。
	DSN string
}

// DSN 拼出 PostgreSQL 连接串。
func (c Config) buildDSN() string {
	if c.DSN != "" {
		return c.DSN
	}
	ssl := c.SSLMode
	if ssl == "" {
		ssl = "disable"
	}
	timeout := c.ConnectTimeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	// password 走配置值直拼而不是 URL 转义,避免含特殊字符的密码被错误解析。
	return fmt.Sprintf(
		"host=%s port=%d user=%s password=%s dbname=%s sslmode=%s connect_timeout=%d",
		c.Host, c.Port, c.User, c.Password, c.Database, ssl, int(timeout.Seconds()),
	)
}

// Open 建立连接池并做一次连通性自检。
//
// 连接池的 MaxConns 默认按 CPU 核数 × 4 推断(见 docs/02-architecture.md 第六节),
// 生产环境建议显式配置,避免容器 CPU limit 与宿主机核数不一致时估偏。
func Open(ctx context.Context, cfg Config, logger *slog.Logger) (*Pool, error) {
	// 未显式配置时补一个默认超时。否则 context.WithTimeout(ctx, 0)
	// 会让探测在建立连接之前就立刻超时。
	if cfg.ConnectTimeout <= 0 {
		cfg.ConnectTimeout = defaultConnectTimeout
	}

	pcfg, err := pgxpool.ParseConfig(cfg.buildDSN())
	if err != nil {
		return nil, fmt.Errorf("解析数据库连接串失败: %w", err)
	}

	if cfg.MaxConns > 0 {
		pcfg.MaxConns = cfg.MaxConns
	}
	if cfg.MinConns > 0 {
		pcfg.MinConns = cfg.MinConns
	}
	if cfg.MaxConnLifetime > 0 {
		pcfg.MaxConnLifetime = cfg.MaxConnLifetime
	}
	if cfg.MaxConnIdleTime > 0 {
		pcfg.MaxConnIdleTime = cfg.MaxConnIdleTime
	}
	pcfg.ConnConfig.ConnectTimeout = cfg.ConnectTimeout

	pool, err := pgxpool.NewWithConfig(ctx, pcfg)
	if err != nil {
		return nil, fmt.Errorf("创建数据库连接池失败: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, cfg.ConnectTimeout)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("数据库连通性检查失败: %w", err)
	}

	if logger != nil {
		logger.Info("database connected",
			"host", cfg.Host, "port", cfg.Port, "database", cfg.Database,
			"max_conns", pcfg.MaxConns)
	}
	return pool, nil
}

// String 输出连接池快照的可读形式。
func (s Stats) String() string {
	return fmt.Sprintf("total=%d idle=%d acquired=%d max=%d",
		s.TotalConns, s.IdleConns, s.AcquiredConns, s.MaxConns)
}

// Stats 是连接池快照,用于 /metrics 上报。
type Stats struct {
	TotalConns    int32
	IdleConns     int32
	AcquiredConns int32
	MaxConns      int32
}

// PoolStats 采集连接池统计。
func PoolStats(pool *Pool) Stats {
	s := pool.Stat()
	return Stats{
		TotalConns:    s.TotalConns(),
		IdleConns:     s.IdleConns(),
		AcquiredConns: s.AcquiredConns(),
		MaxConns:      s.MaxConns(),
	}
}

// Check 把连接池适配成健康探针。
//
// 不给 Pool 加方法 —— 它是外部包类型,Go 不允许在别处为其定义方法。
// 用这个适配器包一层即可满足 health.Checker 接口。
func Check(pool *Pool) func(ctx context.Context) error {
	return pool.Ping
}

// InTx 在事务里执行 fn,fn 返回错误或 panic 时回滚。
//
// 这是全项目唯一允许写 BEGIN/COMMIT 的地方,业务代码不得自行开事务,
// 避免事务边界散落各处导致嵌套与死锁。
func InTx(ctx context.Context, pool *Pool, opts pgx.TxOptions, fn func(pgx.Tx) error) error {
	tx, err := pool.BeginTx(ctx, opts)
	if err != nil {
		return fmt.Errorf("开启事务失败: %w", err)
	}

	//nolint:errcheck // 提交失败时事务一定已结束,Close 只为释放连接
	defer func() { _ = tx.Rollback(ctx) }()

	if err := fn(tx); err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("提交事务失败: %w", err)
	}
	return nil
}

// IsNoRows 判断错误是否为「查询无结果」。仓储层用它区分 not found 与真实故障,
// 避免把正常路径上的空结果当成异常上报。
func IsNoRows(err error) bool { return errors.Is(err, pgx.ErrNoRows) }

// IsUniqueViolation 判断错误是否为唯一约束冲突。
// 409 类错误(邮箱已注册、用户名已存在)靠它与业务错误码对应。
func IsUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

package db

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	dbfs "github.com/yggauth/yggauth/db"
)

// MigrationsDir 是嵌入模式下迁移文件在 embed.FS 中的目录名。
const MigrationsDir = "migrations"

// Migrator 封装 goose 迁移操作。
//
// 用 goose.Provider 而不是 goose 包级函数(Up/Down/Status),因为包级函数
// 操作全局状态,并发跑集成测试时会互相污染。
type Migrator struct {
	provider *goose.Provider
	logger   *slog.Logger
}

// NewMigrator 构造迁移器。
//
// dir 非空时从磁盘目录读取迁移文件(运维改 SQL 调试用),
// 否则从编进二进制的 embed.FS 读取。
func NewMigrator(pool *Pool, dir string, logger *slog.Logger) (*Migrator, error) {
	var (
		fsys fs.FS
		err  error
	)
	switch {
	case dir != "":
		abs, aerr := filepath.Abs(dir)
		if aerr != nil {
			return nil, fmt.Errorf("解析迁移目录失败: %w", aerr)
		}
		if _, serr := os.Stat(abs); serr != nil {
			return nil, fmt.Errorf("迁移目录不可用 %q: %w", abs, serr)
		}
		fsys = os.DirFS(abs)
	default:
		// embed.FS 以 db/ 为根,goose 需要以迁移目录本身为根
		sub, serr := fs.Sub(dbfs.FS, MigrationsDir)
		if serr != nil {
			return nil, fmt.Errorf("定位内嵌迁移目录失败: %w", serr)
		}
		fsys = sub
	}

	sqlDB := stdlib.OpenDBFromPool(pool)
	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, fsys)
	if err != nil {
		return nil, fmt.Errorf("初始化迁移器失败: %w", err)
	}
	return &Migrator{provider: provider, logger: logger}, nil
}

// Up 执行所有未应用的迁移。
func (m *Migrator) Up(ctx context.Context) error {
	results, err := m.provider.Up(ctx)
	if err != nil {
		return fmt.Errorf("数据库迁移失败: %w", err)
	}
	for _, r := range results {
		if r.Source == nil {
			continue
		}
		m.logger.Info("migration applied",
			"version", r.Source.Version, "direction", r.Direction, "duration", r.Duration.String())
	}
	return nil
}

// Down 回滚一步。生产环境需谨慎,先在预发验证。
func (m *Migrator) Down(ctx context.Context) error {
	if _, err := m.provider.Down(ctx); err != nil {
		return fmt.Errorf("数据库回滚失败: %w", err)
	}
	return nil
}

// Status 返回当前各迁移的状态。
func (m *Migrator) Status(ctx context.Context) ([]string, error) {
	statuses, err := m.provider.Status(ctx)
	if err != nil {
		return nil, fmt.Errorf("查询迁移状态失败: %w", err)
	}
	out := make([]string, 0, len(statuses))
	for _, s := range statuses {
		if s.Source == nil {
			continue
		}
		out = append(out, fmt.Sprintf("%s\t%v", s.Source.Path, s.State))
	}
	return out, nil
}

// Version 返回当前数据库的迁移版本。
func (m *Migrator) Version(ctx context.Context) (int64, error) {
	return m.provider.GetDBVersion(ctx)
}

// Close 释放迁移器持有的 database/sql 句柄。
func (m *Migrator) Close() {
	if m.provider != nil {
		_ = m.provider.Close()
	}
}

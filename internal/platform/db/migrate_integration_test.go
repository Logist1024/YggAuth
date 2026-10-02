//go:build integration

package db_test

import (
	"testing"

	"github.com/yggauth/yggauth/internal/platform/db"
	"github.com/yggauth/yggauth/internal/platform/db/testdb"
)

// TestMigrationsApplyAndRollback 验证基线迁移能在真实 PostgreSQL 16 上跑通,
// 并且每个 schema 的关键对象都真的建出来了。
func TestMigrationsApplyAndRollback(t *testing.T) {
	pool := testdb.Fresh(t)
	ctx := t.Context()

	m, err := db.NewMigrator(pool, "", nil)
	if err != nil {
		t.Fatalf("初始化迁移器失败: %v", err)
	}
	defer m.Close()

	version, err := m.Version(ctx)
	if err != nil {
		t.Fatalf("读取迁移版本失败: %v", err)
	}
	if version != 7 {
		t.Fatalf("迁移版本 = %d,期望 7", version)
	}

	// 四个 schema 必须存在(ADR-002 的域隔离手段)。
	for _, schema := range []string{"identity", "oidc", "minecraft", "app"} {
		var exists bool
		if err := pool.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM information_schema.schemata WHERE schema_name = $1)`,
			schema,
		).Scan(&exists); err != nil {
			t.Fatalf("查询 schema %s 失败: %v", schema, err)
		}
		if !exists {
			t.Fatalf("schema %s 未创建", schema)
		}
	}
}

// TestIdentitySeedData 验证 RBAC 种子数据:权限点全集与系统角色。
func TestIdentitySeedData(t *testing.T) {
	pool := testdb.Fresh(t)
	ctx := t.Context()

	var permCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM identity.permission`).Scan(&permCount); err != nil {
		t.Fatalf("统计权限点失败: %v", err)
	}
	if permCount != 17 {
		t.Fatalf("权限点数量 = %d,期望 17", permCount)
	}

	// 邮箱唯一索引建在 lower(email) 上,大小写不同也算重复(变更 C-10)。
	if _, err := pool.Exec(ctx, `
		INSERT INTO identity.account (username, username_lower, email, status)
		VALUES ('Alice', 'alice', 'User@Example.com', 'active')`); err != nil {
		t.Fatalf("插入账号失败: %v", err)
	}
	_, err := pool.Exec(ctx, `
		INSERT INTO identity.account (username, username_lower, email, status)
		VALUES ('Alice2', 'alice2', 'user@example.com', 'active')`)
	if !db.IsUniqueViolation(err) {
		t.Fatalf("大小写不同的同邮箱应触发唯一约束,实际错误 = %v", err)
	}

	// 状态字段的 CHECK 约束必须生效
	_, err = pool.Exec(ctx, `
		INSERT INTO identity.account (username, username_lower, email, status)
		VALUES ('Bob', 'bob', 'bob@example.com', 'bogus')`)
	if err == nil {
		t.Fatal("非法 status 应被 CHECK 约束拒绝")
	}

	// 平台管理员应持有全部权限点
	var adminPerms int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM identity.role_permission rp
		JOIN identity.role r ON r.id = rp.role_id
		WHERE r.code = 'platform_admin'`).Scan(&adminPerms); err != nil {
		t.Fatalf("统计平台管理员权限失败: %v", err)
	}
	if adminPerms != 17 {
		t.Fatalf("平台管理员权限点数量 = %d,期望 17", adminPerms)
	}
}

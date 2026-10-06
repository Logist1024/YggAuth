//go:build integration

package bootstrap_test

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/yggauth/yggauth/internal/bootstrap"
	"github.com/yggauth/yggauth/internal/identity/account"
	"github.com/yggauth/yggauth/internal/platform/db/query"
	"github.com/yggauth/yggauth/internal/platform/db/testdb"
)

// 测试用 argon2 参数刻意取最小值:这里验证的是流程与契约,
// 不是哈希强度(强度有专门的 unit test)。
func testHasher() *account.Hasher {
	return account.NewHasher(account.Params{
		MemoryKiB:   8 * 1024,
		Iterations:  1,
		Parallelism: 1,
		SaltLength:  16,
		KeyLength:   32,
	})
}

func testPolicy() account.Policy {
	return account.Policy{
		MinLength:       8,
		MaxLength:       128,
		RejectCommon:    true,
		CommonPasswords: account.CommonPasswordSet(),
	}
}

func baseOpts() bootstrap.Options {
	return bootstrap.Options{
		Enabled:      true,
		Email:        "admin@example.com",
		Username:     "admin",
		Policy:       testPolicy(),
		LoginEnabled: true,
	}
}

// 首启引导的核心契约:建号一次、再跑就跳、库里的四件事都发生。
func TestEnsureCreatesAdminExactlyOnce(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Fresh(t)
	hasher := testHasher()

	out, err := bootstrap.Ensure(ctx, pool, hasher, baseOpts())
	require.NoError(t, err)
	require.Equal(t, bootstrap.KindCreated, out.Kind)
	require.NotEmpty(t, out.GeneratedPassword, "没给密码时必须随机生成一个")
	require.Empty(t, out.NextStep)

	q := query.New(pool)

	// 1. 账号存在、active、邮箱已验证(否则登录会被 pending 挡住)
	acc, err := q.GetAccountByUsername(ctx, "admin")
	require.NoError(t, err)
	require.Equal(t, "active", acc.Status)
	require.True(t, acc.EmailVerifiedAt.Valid, "管理员邮箱必须直接标记已验证")
	require.Equal(t, "admin@example.com", acc.Email)

	// 2. 拿到 platform_admin 角色,且持有者正好一个
	role, err := q.GetRoleByCode(ctx, bootstrap.AdminRole)
	require.NoError(t, err)
	n, err := q.CountAccountsWithRole(ctx, role.ID)
	require.NoError(t, err)
	require.EqualValues(t, 1, n)

	// 3. 凭据:密码确实是生成的那一个,且要求首登改密
	cred, err := q.GetCredential(ctx, query.GetCredentialParams{AccountID: acc.ID, Algo: "argon2id"})
	require.NoError(t, err)
	require.True(t, hasher.Verify(out.GeneratedPassword, cred.Hash), "生成的密码必须能验证通过")
	require.True(t, cred.MustChange, "首启引导必须要求首登改密")

	// 4. system.installed_at 已写入且是合法时间
	st, err := q.GetSetting(ctx, bootstrap.InstalledAtKey)
	require.NoError(t, err)
	var installedAt string
	require.NoError(t, json.Unmarshal(st.Value, &installedAt))
	_, err = time.Parse(time.RFC3339, installedAt)
	require.NoError(t, err, "installed_at 必须是 RFC3339 时间")

	// 5. 审计留痕:system.bootstrap 成功一次
	var audits int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM identity.audit_event WHERE action = 'system.bootstrap' AND outcome = 'success'`,
	).Scan(&audits))
	require.Equal(t, 1, audits)

	// 幂等:第二次启动只 skip,不建号、不覆盖、不重发密码
	out2, err := bootstrap.Ensure(ctx, pool, hasher, baseOpts())
	require.NoError(t, err)
	require.Equal(t, bootstrap.KindSkipped, out2.Kind)
	require.Empty(t, out2.GeneratedPassword)

	var accounts int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM identity.account`).Scan(&accounts))
	require.Equal(t, 1, accounts)

	// installed_at 也没被第二次覆盖
	st2, err := q.GetSetting(ctx, bootstrap.InstalledAtKey)
	require.NoError(t, err)
	require.Equal(t, st.Value, st2.Value)
}

// ADMIN_BOOTSTRAP=false:什么都不做,但要给出可执行的下一步。
func TestEnsureDisabledDoesNothing(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Fresh(t)

	opts := baseOpts()
	opts.Enabled = false
	out, err := bootstrap.Ensure(ctx, pool, testHasher(), opts)
	require.NoError(t, err)
	require.Equal(t, bootstrap.KindDisabled, out.Kind)
	require.Contains(t, out.NextStep, "admin create")

	var accounts int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM identity.account`).Scan(&accounts))
	require.Zero(t, accounts)
}

// 引导密码不满足策略必须启动失败:「建好了但登不进去」是最难排查的事故。
func TestEnsureRejectsWeakPassword(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Fresh(t)

	opts := baseOpts()
	opts.Password = "123"
	_, err := bootstrap.Ensure(ctx, pool, testHasher(), opts)
	require.Error(t, err)
	require.Contains(t, err.Error(), "密码不满足策略")

	var accounts int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM identity.account`).Scan(&accounts))
	require.Zero(t, accounts, "校验失败必须在建号之前,不能留下半成品")
}

// 管理员删光之后不会自动补号(包注释第 2 条),补号只能显式发起。
func TestEnsureDoesNotResurrectDeletedAdmin(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Fresh(t)

	_, err := bootstrap.Ensure(ctx, pool, testHasher(), baseOpts())
	require.NoError(t, err)

	// 把管理员连同角色授予一起删掉,模拟「误删到一个不剩」
	_, err = pool.Exec(ctx, `DELETE FROM identity.account`)
	require.NoError(t, err)

	out, err := bootstrap.Ensure(ctx, pool, testHasher(), baseOpts())
	require.NoError(t, err)
	require.Equal(t, bootstrap.KindSkipped, out.Kind,
		"删光后重启不应静默重建,否则「删不掉」")

	// 显式动作才能补回来
	out2, err := bootstrap.Create(ctx, pool, testHasher(), baseOpts())
	require.NoError(t, err)
	require.Equal(t, bootstrap.KindCreated, out2.Kind)
}

// `yggauth admin create` 走的同一套事务:能建第二个管理员,
// 同一身份重复创建被唯一索引挡住,不会产生两份。
func TestCreateIsExplicitAndConflictSafe(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Fresh(t)

	first, err := bootstrap.Ensure(ctx, pool, testHasher(), baseOpts())
	require.NoError(t, err)
	require.Equal(t, bootstrap.KindCreated, first.Kind)

	// 不同身份:正常创建,并且显式要求首登改密(CLI 的 -must-change 默认 true)
	opts := baseOpts()
	opts.Email = "ops@example.com"
	opts.Username = "ops"
	opts.MustChange = true
	second, err := bootstrap.Create(ctx, pool, testHasher(), opts)
	require.NoError(t, err)
	require.Equal(t, bootstrap.KindCreated, second.Kind)

	q := query.New(pool)
	acc, err := q.GetAccountByUsername(ctx, "ops")
	require.NoError(t, err)
	cred, err := q.GetCredential(ctx, query.GetCredentialParams{AccountID: acc.ID, Algo: "argon2id"})
	require.NoError(t, err)
	require.True(t, cred.MustChange)

	// 同一身份再来一次:唯一索引兜底
	third, err := bootstrap.Create(ctx, pool, testHasher(), opts)
	require.NoError(t, err)
	require.Equal(t, bootstrap.KindSkipped, third.Kind, "重复身份必须被挡住,而不是改掉旧号的密码")

	var accounts int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM identity.account`).Scan(&accounts))
	require.EqualValues(t, 2, accounts)
}

// 并发首启(多副本同时跑迁移后的第一次启动)只能建出一个管理员。
func TestEnsureConcurrentStartsCreateOneAdmin(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Fresh(t)

	var wg sync.WaitGroup
	outcomes := make([]bootstrap.Outcome, 2)
	errs := make([]error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			outcomes[i], errs[i] = bootstrap.Ensure(ctx, pool, testHasher(), baseOpts())
		}(i)
	}
	wg.Wait()

	created := 0
	for i := range outcomes {
		require.NoError(t, errs[i])
		if outcomes[i].Kind == bootstrap.KindCreated {
			created++
		}
	}
	require.Equal(t, 1, created, "并发首启只能有一个实例真的建号")

	var accounts int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM identity.account`).Scan(&accounts))
	require.EqualValues(t, 1, accounts)
}

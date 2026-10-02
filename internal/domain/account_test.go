package domain_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/yggauth/yggauth/internal/domain"
)

// 权限求值是后台安全的基础,通配规则一旦写错就是越权。
func TestCanExactMatch(t *testing.T) {
	t.Parallel()

	require.True(t, domain.Can("account:read", "account:read"))
	require.False(t, domain.Can("account:read", "account:write"))
	require.False(t, domain.Can("account:read", "audit:read"))
}

func TestCanNamespaceWildcard(t *testing.T) {
	t.Parallel()

	// 域通配:持有该域全部权限
	require.True(t, domain.Can("minecraft:*", "minecraft:profile:read"))
	require.True(t, domain.Can("minecraft:*", "minecraft:texture:write"))
	// 不能跨域
	require.False(t, domain.Can("minecraft:*", "oidc:client:read"))
	require.False(t, domain.Can("minecraft:*", "account:read"))
}

func TestCanSegmentWildcard(t *testing.T) {
	t.Parallel()

	require.True(t, domain.Can("*:profile:read", "minecraft:profile:read"))
	require.True(t, domain.Can("*:profile:read", "oidc:profile:read"))
	require.False(t, domain.Can("*:profile:read", "minecraft:profile:write"))

	// 中间段通配
	require.True(t, domain.Can("minecraft:*:read", "minecraft:profile:read"))
	require.True(t, domain.Can("minecraft:*:read", "minecraft:server:read"))
	require.False(t, domain.Can("minecraft:*:read", "minecraft:profile:write"))
}

func TestCanFullWildcard(t *testing.T) {
	t.Parallel()

	require.True(t, domain.Can("*", "account:read"))
	require.True(t, domain.Can("*", "minecraft:texture:write"))
}

// 非三段式的权限点只允许精确匹配,不做通配展开 ——
// 否则一个拼错的配置可能意外获得全部权限。
func TestCanNonThreeSegmentRequiresExact(t *testing.T) {
	t.Parallel()

	require.True(t, domain.Can("admin", "admin"))
	require.False(t, domain.Can("admin", "admin:read"))
}

func TestPrincipalCan(t *testing.T) {
	t.Parallel()

	p := &domain.Principal{
		Permissions: []string{"account:read", "minecraft:*"},
	}

	require.True(t, p.Can("account:read"))
	require.True(t, p.Can("minecraft:texture:write"))
	require.False(t, p.Can("account:write"))
	require.False(t, p.Can("oidc:client:read"))

	// 全通配代表平台管理员,持有全部权限点
	require.True(t, domain.Can("*", "anything:at:all"))

	// 没有任何权限点时一律拒绝
	empty := &domain.Principal{}
	require.False(t, empty.Can("account:read"))
}

// ---------------------------------------------------------------- 状态

func TestAccountStatusUsable(t *testing.T) {
	t.Parallel()

	require.True(t, domain.StatusActive.Usable())
	require.False(t, domain.StatusDisabled.Usable())
	require.False(t, domain.StatusLocked.Usable())
	require.False(t, domain.StatusPendingVerification.Usable())

	require.True(t, domain.AccountStatus("active").Valid())
	require.False(t, domain.AccountStatus("bogus").Valid())
}

// ---------------------------------------------------------------- 会话

// 会话必须同时满足「未吊销 + 未空闲超时 + 未绝对超时」才算有效。
func TestSessionActive(t *testing.T) {
	t.Parallel()

	now := timeAt(100)
	sess := domain.Session{
		ExpiresAt:     timeAt(200),
		IdleExpiresAt: timeAt(150),
	}

	require.True(t, sess.Active(now))

	// 空闲超时
	require.False(t, sess.Active(timeAt(160)))
	// 绝对超时(即使刚刚活跃过)
	require.False(t, sess.Active(timeAt(210)))
	// 恰好等于过期时刻即失效
	require.False(t, sess.Active(timeAt(150)))

	// 已吊销
	revoked := sess
	tv := timeAt(120)
	revoked.RevokedAt = &tv
	require.False(t, revoked.Active(now))
}

// timeAt 返回一个确定时刻,避免测试依赖真实时间。
func timeAt(sec int) time.Time {
	return time.Unix(int64(sec), 0).UTC()
}

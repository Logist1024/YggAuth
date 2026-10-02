package account_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/yggauth/yggauth/internal/identity/account"
	"github.com/yggauth/yggauth/internal/platform/apperr"
)

// 变更 C-1 把密码长度从 6–18 改为 8–128,边界值必须逐一验证。
// 7 位曾经合法、8 位现在才合法、129 位必须被拒 —— 任何一边松动都会
// 造成「旧版能注册、新版不能」的兼容事故。
func TestPasswordPolicyBoundaries(t *testing.T) {
	t.Parallel()

	p := account.Policy{
		MinLength: 8,
		MaxLength: 128,
		// 关掉弱口令库,这里只测长度边界
		RejectCommon: false,
	}

	cases := []struct {
		name     string
		password string
		wantErr  bool
	}{
		{"7 位应被拒", strings.Repeat("a", 7), true},
		{"8 位应通过", strings.Repeat("a", 8), false},
		{"128 位应通过", strings.Repeat("a", 128), false},
		{"129 位应被拒", strings.Repeat("a", 129), true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := p.Validate(tc.password)
			if tc.wantErr {
				require.Error(t, err)
				require.True(t, apperr.Is(err, apperr.CodeWeakPassword),
					"应返回 20007 密码不符合策略,实际: %v", err)
				return
			}
			require.NoError(t, err)
		})
	}
}

// 变更 C-2:不再强制大小写+数字组合,只拒绝弱口令库里的常见密码。
func TestPasswordPolicyDoesNotForceComplexity(t *testing.T) {
	t.Parallel()

	p := account.Policy{MinLength: 8, MaxLength: 128, RejectCommon: false}

	// 全小写、无数字、无符号 —— 旧版会拒绝,新版通过
	require.NoError(t, p.Validate("abcdefgh"))
	// 全大写
	require.NoError(t, p.Validate("ABCDEFGH"))
	// 无任何组合但足够长
	require.NoError(t, p.Validate("qqqqqqqq"))
}

func TestPasswordPolicyRejectsCommonPasswords(t *testing.T) {
	t.Parallel()

	p := account.Policy{
		MinLength:       8,
		MaxLength:       128,
		RejectCommon:    true,
		CommonPasswords: account.CommonPasswordSet(),
	}

	for _, weak := range []string{"password", "password123", "12345678", "qwertyuiop", "minecraft"} {
		require.Error(t, p.Validate(weak), "弱口令 %q 应被拒绝", weak)
	}

	// 大小写不敏感:PassWord 与 password 同样是弱口令
	require.Error(t, p.Validate("PassWord"))
	require.NoError(t, p.Validate("x7Fq#2Lp9"))
}

// 长度按字符数而非字节数计算:中文密码不应该因为字节多就被误判超长。
func TestPasswordPolicyCountsRunes(t *testing.T) {
	t.Parallel()

	p := account.Policy{MinLength: 8, MaxLength: 128}
	// 8 个中文字符 = 24 字节,按字节算会误判超长
	require.NoError(t, p.Validate("密码强度够长的九个"))
	require.Error(t, p.Validate("短密码"))
}

// ---------------------------------------------------------------- 哈希

// argon2id 默认参数是 64MiB 内存,单测里调小否则跑得慢;
// 格式与算法必须与生产完全一致。
func fastHasher() *account.Hasher {
	return account.NewHasher(account.Params{
		MemoryKiB:   8 * 1024,
		Iterations:  1,
		Parallelism: 1,
		SaltLength:  16,
		KeyLength:   32,
	})
}

func TestHashVerifyRoundTrip(t *testing.T) {
	t.Parallel()

	h := fastHasher()
	hash, err := h.Hash("correct-horse-battery")
	require.NoError(t, err)

	require.True(t, h.Verify("correct-horse-battery", hash))
	require.False(t, h.Verify("wrong-password", hash))
	require.False(t, h.Verify("", hash))
}

func TestHashUsesSaltedArgon2id(t *testing.T) {
	t.Parallel()

	h := fastHasher()
	a, err := h.Hash("same-password")
	require.NoError(t, err)
	b, err := h.Hash("same-password")
	require.NoError(t, err)

	// 每次都应生成不同哈希(独立随机盐)
	require.NotEqual(t, a, b)
	// 但都能校验通过
	require.True(t, h.Verify("same-password", a))
	require.True(t, h.Verify("same-password", b))
}

func TestHashFormatIsPHC(t *testing.T) {
	t.Parallel()

	h := fastHasher()
	hash, err := h.Hash("whatever")
	require.NoError(t, err)

	parts := strings.Split(hash, "$")
	require.Len(t, parts, 6, "PHC 格式应有 6 段")
	require.Equal(t, "argon2id", parts[1])
	require.Equal(t, "v=19", parts[2])
	require.Contains(t, parts[3], "m=")
	require.Contains(t, parts[3], "t=")
	require.Contains(t, parts[3], "p=")
}

// 参数内嵌在哈希串里:调参之后老密码仍要能校验,
// 否则一次调参会让全站用户被迫重置密码。
func TestVerifyUsesStoredParamsNotDefaults(t *testing.T) {
	t.Parallel()

	weak := account.NewHasher(account.Params{
		MemoryKiB: 8 * 1024, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32,
	})
	strong := account.NewHasher(account.Params{
		MemoryKiB: 32 * 1024, Iterations: 3, Parallelism: 2, SaltLength: 16, KeyLength: 32,
	})

	hash, err := weak.Hash("upgrade-me")
	require.NoError(t, err)

	// 用新参数创建的校验器仍能校验老哈希
	require.True(t, strong.Verify("upgrade-me", hash))
	require.False(t, strong.Verify("nope", hash))
}

func TestVerifyRejectsMalformedHash(t *testing.T) {
	t.Parallel()

	h := fastHasher()
	for _, bad := range []string{
		"",
		"not-a-hash",
		"$argon2id$v=19$m=8192,t=1,p=1$only-five-parts",
		"$argon2i$v=19$m=8192,t=1,p=1$c2FsdA$aGFzaA", // 算法不匹配
		"$argon2id$m=8192,t=1,p=1$c2FsdA$aGFzaA",     // 缺版本号
		"$argon2id$v=19$m=abc,t=1,p=1$c2FsdA$aGFzaA", // 参数值非法
	} {
		require.False(t, h.Verify("password", bad), "非法哈希 %q 不应通过校验", bad)
	}
}

// ---------------------------------------------------------------- 用户名与邮箱

func TestValidateUsername(t *testing.T) {
	t.Parallel()

	valid := []string{"abc", "player_one", "PlayerOne", "a_1", strings.Repeat("u", 32)}
	for _, name := range valid {
		require.NoError(t, account.ValidateUsername(name), "%q 应合法", name)
	}

	invalid := []string{
		"",                      // 空
		"ab",                    // 太短
		strings.Repeat("u", 33), // 太长
		"1abc",                  // 数字开头
		"user-name",             // 连字符不允许
		"user name",             // 空格
		"用户",                    // 非 ASCII 字母被拒(与旧版行为一致,避免同形异码问题)
		"user@name",
	}
	for _, name := range invalid {
		require.Error(t, account.ValidateUsername(name), "%q 应非法", name)
	}
}

func TestValidateEmail(t *testing.T) {
	t.Parallel()

	valid := []string{
		"user@example.com",
		"first.last+tag@example.co.uk",
		"a@b.io",
	}
	for _, e := range valid {
		require.NoError(t, account.ValidateEmail(e), "%q 应合法", e)
	}

	invalid := []string{
		"",
		"not-an-email",
		"user@",
		"@example.com",
		"user@example",               // 没有点
		"user name@x.com",            // 含空格
		"user@x.com\r\nBcc: x@y.com", // CR/LF 头注入
	}
	for _, e := range invalid {
		require.Error(t, account.ValidateEmail(e), "%q 应非法", e)
	}
}

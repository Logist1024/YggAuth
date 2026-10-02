package rand_test

import (
	"encoding/hex"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/yggauth/yggauth/internal/platform/rand"
)

// 会话令牌一旦可预测,等于把账号交出去 —— 必须有足够熵。
func TestTokenHasEnoughEntropy(t *testing.T) {
	tok, err := rand.Token()
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(tok), 43, "32 字节熵的 base64url 应为 43 字符")

	seen := make(map[string]struct{}, 1000)
	for range 1000 {
		tok, err := rand.Token()
		require.NoError(t, err)
		_, dup := seen[tok]
		require.False(t, dup, "令牌出现重复")
		seen[tok] = struct{}{}
	}
}

// MC 协议要求 accessToken 必须是 32 位无横线小写 hex,服务端会直接解析它。
func TestUUIDTokenFormat(t *testing.T) {
	tok, err := rand.UUIDToken()
	require.NoError(t, err)
	require.Len(t, tok, 32)
	require.NotContains(t, tok, "-")
	require.Equal(t, strings.ToLower(tok), tok)

	_, err = hex.DecodeString(tok)
	require.NoError(t, err, "必须是合法 hex")
}

// 人工抄写的用户码要排除易混淆字符(0/O、1/I/L)。
func TestUserCodeAvoidsAmbiguousCharacters(t *testing.T) {
	code, err := rand.UserCode(2, 4)
	require.NoError(t, err)
	require.Equal(t, 4+1+4, len(code), "格式应为 XXXX-XXXX")
	require.Equal(t, 1, strings.Count(code, "-"))

	alphabet := "BCDFGHJKLMNPQRSTVWXZ23456789"
	for _, r := range strings.ReplaceAll(code, "-", "") {
		require.Contains(t, alphabet, string(r), "用户码不应包含易混淆字符 %q", r)
	}
}

func TestHexFormat(t *testing.T) {
	h, err := rand.Hex(16)
	require.NoError(t, err)
	require.Len(t, h, 32)
	require.Equal(t, strings.ToLower(h), h)
}

func TestRejectsNonPositiveSize(t *testing.T) {
	_, err := rand.Bytes(0)
	require.Error(t, err)
	_, err = rand.Bytes(-1)
	require.Error(t, err)
}

func TestURLSafeHasNoUnsafeChars(t *testing.T) {
	tok, err := rand.URLSafe(16)
	require.NoError(t, err)
	require.NotContains(t, tok, "+")
	require.NotContains(t, tok, "/")
	require.NotContains(t, tok, "=")
}

package apperr_test

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/yggauth/yggauth/internal/platform/apperr"
)

// 错误码与 HTTP 状态的映射是前端与运维的共同契约,改错会让前端判断失准。
func TestCodeHTTPStatusMapping(t *testing.T) {
	cases := []struct {
		code   apperr.Code
		status int
	}{
		{apperr.CodeOK, http.StatusOK},
		{apperr.CodeInvalidArgument, http.StatusBadRequest},
		{apperr.CodeRateLimited, http.StatusTooManyRequests},
		{apperr.CodeInternal, http.StatusInternalServerError},
		{apperr.CodeInvalidPassword, http.StatusUnauthorized},
		{apperr.CodeAccountDisabled, http.StatusForbidden},
		{apperr.CodeEmailTaken, http.StatusConflict},
		{apperr.CodeUsernameTaken, http.StatusConflict},
		{apperr.CodeAccountLocked, http.StatusTooManyRequests},
		{apperr.CodeWeakPassword, http.StatusBadRequest},
		{apperr.CodeInvalidToken, http.StatusBadRequest},
		{apperr.CodeEmailUnverified, http.StatusConflict},
		{apperr.CodeForbidden, http.StatusForbidden},
		{apperr.CodeOIDCRedirectMismatch, http.StatusBadRequest},
		{apperr.CodeMCLoginDisabled, http.StatusForbidden},
		{apperr.CodeTextureTooLarge, http.StatusRequestEntityTooLarge},
		{apperr.CodeMCTokenInvalid, http.StatusUnauthorized},
	}
	for _, tc := range cases {
		require.Equal(t, tc.status, tc.code.HTTPStatus(), "错误码 %d 的 HTTP 状态不符", tc.code)
	}
}

// 每个错误码都必须登记默认消息与状态码,否则会静默退化成 500。
func TestAllCodesHaveMetadata(t *testing.T) {
	for _, c := range apperr.AllCodes() {
		require.True(t, c.Known(), "错误码 %d 未登记元数据", c)
		require.NotEmpty(t, c.Message(), "错误码 %d 缺少默认消息", c)
		if c == apperr.CodeOK {
			continue // 成功码的 HTTP 状态本来就是 200
		}
		require.GreaterOrEqual(t, c.HTTPStatus(), 400, "错误码 %d 的 HTTP 状态异常", c)
	}
}

// 区间划分是错误码体系的骨架(1xxxx 通用 / 2xxxx 账号 / 3xxxx 权限 /
// 4xxxx OIDC / 5xxxx 游戏域),被打破会让后续排查失去方向。
func TestCodeRanges(t *testing.T) {
	ranges := []struct {
		name  string
		lo    int
		hi    int
		codes []apperr.Code
	}{
		{"通用", 10000, 19999, []apperr.Code{
			apperr.CodeInvalidArgument, apperr.CodeRateLimited, apperr.CodeInternal,
			apperr.CodeNotFound, apperr.CodeConflict, apperr.CodePayloadTooLarge, apperr.CodeUnavailable,
		}},
		{"账号", 20000, 29999, []apperr.Code{
			apperr.CodeUnauthorized, apperr.CodeInvalidPassword, apperr.CodeAccountDisabled,
			apperr.CodeEmailTaken, apperr.CodeUsernameTaken, apperr.CodeAccountLocked,
			apperr.CodeWeakPassword, apperr.CodeInvalidToken, apperr.CodeEmailUnverified,
			apperr.CodeInviteRequired, apperr.CodeInviteInvalid, apperr.CodeSessionExpired,
		}},
		{"权限", 30000, 39999, []apperr.Code{
			apperr.CodeForbidden, apperr.CodePermissionDenied,
		}},
		{"OIDC", 40000, 49999, []apperr.Code{
			apperr.CodeOIDCInvalidRequest, apperr.CodeOIDCClientAuthFailed,
			apperr.CodeOIDCRedirectMismatch, apperr.CodeOIDCPKCEFailed,
			apperr.CodeOIDCScopeDenied, apperr.CodeOIDCUnsupportedGrant,
		}},
		{"Minecraft", 50000, 59999, []apperr.Code{
			apperr.CodeMCNameInvalid, apperr.CodeMCNameTaken, apperr.CodeMCTokenInvalid,
			apperr.CodeMCLoginDisabled, apperr.CodeTextureInvalid, apperr.CodeTextureTooLarge,
			apperr.CodeMCSignatureError, apperr.CodeMCReadOnly,
		}},
	}
	for _, r := range ranges {
		for _, c := range r.codes {
			require.GreaterOrEqual(t, int(c), r.lo, "%s 错误码 %d 越界", r.name, c)
			require.LessOrEqual(t, int(c), r.hi, "%s 错误码 %d 越界", r.name, c)
		}
	}
}

// 内部错误不能把细节泄露给调用方。
func TestFromWrapsUnknownErrorAsInternal(t *testing.T) {
	internal := errors.New("pq: relation \"identity.account\" does not exist")

	got := apperr.From(internal)
	require.Equal(t, apperr.CodeInternal, got.Code)
	require.Equal(t, "服务器内部错误", got.PublicMessage())
	require.Contains(t, got.Detail, "relation") // 细节留在内部,不进 PublicMessage
	require.Equal(t, http.StatusInternalServerError, got.HTTPStatus())
}

func TestFromExtractsBusinessErrorThroughWrapping(t *testing.T) {
	inner := apperr.New(apperr.CodeEmailTaken, "duplicate key")
	wrapped := fmt.Errorf("注册失败: %w", inner)

	got := apperr.From(wrapped)
	require.Equal(t, apperr.CodeEmailTaken, got.Code)
	require.Equal(t, http.StatusConflict, got.HTTPStatus())
	require.True(t, apperr.Is(wrapped, apperr.CodeEmailTaken))
	require.False(t, apperr.Is(wrapped, apperr.CodeUsernameTaken))
}

func TestWithMessageOverridesPublicText(t *testing.T) {
	e := apperr.New(apperr.CodeInvalidArgument, "字段 password 不合法").WithMessage("密码不符合安全策略")

	require.Equal(t, "密码不符合安全策略", e.PublicMessage())
	require.Equal(t, http.StatusBadRequest, e.HTTPStatus())
	// 内部细节仍然保留,便于排查
	require.Contains(t, e.Detail, "password")
}

func TestNilErrorMapsToOK(t *testing.T) {
	require.Equal(t, apperr.CodeOK, apperr.From(nil).Code)
}

// 登录失败必须统一返回「邮箱或密码错误」,不能因为内部错误码不同
// 让前端区分出「邮箱不存在」还是「密码错误」(防账号枚举)。
func TestInvalidPasswordMessageDoesNotLeakExistence(t *testing.T) {
	require.Equal(t, "邮箱或密码错误", apperr.CodeInvalidPassword.Message())
}

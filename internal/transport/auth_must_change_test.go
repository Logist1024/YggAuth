package transport

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/yggauth/yggauth/internal/domain"
	"github.com/yggauth/yggauth/internal/identity"
	"github.com/yggauth/yggauth/internal/platform/apperr"
)

// fakeAuthenticator 按构造给出的会话与账号应答,专供中间件行为测试。
type fakeAuthenticator struct {
	session domain.Session
	account domain.Account
	perms   []string
}

func (f fakeAuthenticator) AuthenticateSession(context.Context, string) (identity.AuthenticatedSession, error) {
	return identity.AuthenticatedSession{Session: f.session}, nil
}

func (f fakeAuthenticator) LookupAccount(context.Context, uuid.UUID) (domain.Account, error) {
	return f.account, nil
}

func (f fakeAuthenticator) PermissionsFor(context.Context, uuid.UUID) ([]string, error) {
	return f.perms, nil
}

// runThroughAuth 走一次中间件,返回下游是否被调用与响应记录器。
func runThroughAuth(t *testing.T, mustChange bool, path string) (bool, *httptest.ResponseRecorder) {
	t.Helper()

	auth := fakeAuthenticator{
		session: domain.Session{ID: uuid.New(), AccountID: uuid.New(), MustChangePassword: mustChange},
		account: domain.Account{ID: uuid.New(), Username: "admin", Email: "admin@example.com", Status: domain.StatusActive},
		perms:   []string{"*"},
	}

	reached := false
	h := SessionAuth(auth, AuthConfig{CookieName: "ygg_session"})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil)
	req.AddCookie(&http.Cookie{Name: "ygg_session", Value: "tok"})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return reached, rec
}

// 首登强制改密:旗标置位时,除了改密流程自身与自举/登出,其余一律 20013。
func TestSessionAuthBlocksUnlessPasswordChangeAllowed(t *testing.T) {
	cases := []struct {
		path        string
		wantBlocked bool
	}{
		// 放行:改密流程自身 + 前端自举需要的三条
		{"/api/account/password", false},
		{"/api/auth/logout", false},
		{"/api/auth/session", false},
		{"/api/account/", false},

		// 拦下:业务接口、管理后台、授权与游戏域 —— 不改密就走不下去
		{"/api/admin/settings", true},
		{"/api/admin/accounts", true},
		{"/api/account/mc/profile", true},
		{"/api/oidc/clients", true},
	}

	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			reached, rec := runThroughAuth(t, true, tc.path)

			if !tc.wantBlocked {
				require.Equal(t, http.StatusOK, rec.Code, "强制改密期间该路径必须放行: %s", tc.path)
				require.True(t, reached)
				return
			}

			require.Equal(t, http.StatusForbidden, rec.Code, "必须拦截: %s", tc.path)
			require.False(t, reached, "拦截后不应再执行下游 handler")

			var body struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			}
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
			require.Equal(t, int(apperr.CodePasswordChangeRequired), body.Code)
			require.NotEmpty(t, body.Message)
		})
	}
}

// 旗标未置位时一切照旧 —— 保证这条新拦截不打扰正常请求。
func TestSessionAuthPassesWhenNoFlag(t *testing.T) {
	for _, path := range []string{"/api/admin/settings", "/api/account/", "/api/oidc/clients"} {
		reached, rec := runThroughAuth(t, false, path)
		require.Equal(t, http.StatusOK, rec.Code, path)
		require.True(t, reached)
	}
}

// 20013 的语义是「登录有效但必须先改密」,因此是 403 而不是 401:
// 回 401 会让前端把用户踢回登录页,而重新登录根本解决不了这件事。
func TestPasswordChangeRequiredCodeMapsToForbidden(t *testing.T) {
	require.Equal(t, 403, apperr.CodePasswordChangeRequired.HTTPStatus())
	require.Equal(t, "首次登录必须修改密码", apperr.CodePasswordChangeRequired.Message())
}

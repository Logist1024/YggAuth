package session_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/yggauth/yggauth/internal/domain"
	"github.com/yggauth/yggauth/internal/identity/session"
	"github.com/yggauth/yggauth/internal/platform/apperr"
	"github.com/yggauth/yggauth/internal/platform/clock"
)

var testStart = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// fakeRepo 是内存会话仓储,只实现过期与续期相关的行为。
type fakeRepo struct {
	mu       sync.Mutex
	sessions map[string]domain.Session // key: token hash hex
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{sessions: map[string]domain.Session{}}
}

func hashKey(b []byte) string {
	const hexDigits = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, v := range b {
		out[i*2] = hexDigits[v>>4]
		out[i*2+1] = hexDigits[v&0x0f]
	}
	return string(out)
}

func (r *fakeRepo) Create(_ context.Context, in session.CreateInput) (domain.Session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := domain.Session{
		ID:            uuid.New(),
		AccountID:     in.AccountID,
		CreatedAt:     testStart,
		LastSeenAt:    testStart,
		ExpiresAt:     in.ExpiresAt,
		IdleExpiresAt: in.IdleExpiresAt,
		IP:            in.IP,
		UserAgent:     in.UserAgent,
	}
	r.sessions[hashKey(in.TokenHash)] = s
	return s, nil
}

func (r *fakeRepo) GetActive(_ context.Context, h []byte) (domain.Session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.sessions[hashKey(h)]
	if !ok {
		return domain.Session{}, apperr.ErrNotFound
	}
	return s, nil
}

func (r *fakeRepo) GetByID(_ context.Context, id uuid.UUID) (domain.Session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, s := range r.sessions {
		if s.ID == id {
			return s, nil
		}
	}
	return domain.Session{}, apperr.ErrNotFound
}

func (r *fakeRepo) ListActive(_ context.Context, accountID uuid.UUID) ([]domain.Session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]domain.Session, 0, len(r.sessions))
	for _, s := range r.sessions {
		if s.AccountID == accountID {
			out = append(out, s)
		}
	}
	return out, nil
}

func (r *fakeRepo) CountActive(_ context.Context, accountID uuid.UUID) (int64, error) {
	list, _ := r.ListActive(context.Background(), accountID)
	return int64(len(list)), nil
}

func (r *fakeRepo) Touch(_ context.Context, id uuid.UUID, idleExpiresAt time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for k, s := range r.sessions {
		if s.ID == id {
			s.IdleExpiresAt = idleExpiresAt
			s.LastSeenAt = testStart
			r.sessions[k] = s
		}
	}
	return nil
}

func (r *fakeRepo) Revoke(_ context.Context, id uuid.UUID, _ string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := testStart
	for k, s := range r.sessions {
		if s.ID == id {
			s.RevokedAt = &now
			r.sessions[k] = s
		}
	}
	return nil
}

func (r *fakeRepo) RevokeAll(_ context.Context, accountID uuid.UUID, _ string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := testStart
	for k, s := range r.sessions {
		if s.AccountID == accountID {
			s.RevokedAt = &now
			r.sessions[k] = s
		}
	}
	return nil
}

func (r *fakeRepo) LinkSSO(context.Context, uuid.UUID, uuid.UUID) error { return nil }

// SetMustChangePassword 记录登录时投到会话上的强制改密旗标,
// 让上层测试能断言「认证读到的是会话行上的那份」。
func (r *fakeRepo) SetMustChangePassword(_ context.Context, id uuid.UUID, mustChange bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for k, s := range r.sessions {
		if s.ID == id {
			s.MustChangePassword = mustChange
			r.sessions[k] = s
			return nil
		}
	}
	return apperr.ErrNotFound
}

func (r *fakeRepo) RevokeBySSO(context.Context, uuid.UUID, string) error { return nil }

func (r *fakeRepo) DeleteExpired(context.Context, time.Time) (int64, error) { return 0, nil }

func newService(t *testing.T) (*session.Service, *clock.Mock, *fakeRepo) {
	t.Helper()
	repo := newFakeRepo()
	clk := clock.NewMock(testStart)
	svc := session.NewService(repo, clk, session.Config{
		IdleTTL: 7 * 24 * time.Hour,
		MaxTTL:  30 * 24 * time.Hour,
	}, nopLogger{})
	return svc, clk, repo
}

type nopLogger struct{}

func (nopLogger) Info(string, ...any)  {}
func (nopLogger) Warn(string, ...any)  {}
func (nopLogger) Error(string, ...any) {}

// ---------------------------------------------------------------- 测试

func TestIssueReturnsPlainToken(t *testing.T) {
	t.Parallel()

	svc, _, _ := newService(t)
	issued, err := svc.Issue(context.Background(), uuid.New(), "10.0.0.1", "Mozilla/5.0")
	require.NoError(t, err)
	require.NotEmpty(t, issued.Token)
	require.NotEqual(t, issued.Token, hashKey(session.HashToken(issued.Token)),
		"明文令牌不应与它的哈希相同")

	// 两次签发的令牌必须不同
	issued2, err := svc.Issue(context.Background(), uuid.New(), "10.0.0.1", "Mozilla/5.0")
	require.NoError(t, err)
	require.NotEqual(t, issued.Token, issued2.Token)
}

// 绝对过期不因活跃而延长:持续使用也不该让令牌永不过期。
func TestIdleExpirySlidesButMaxTTLDoesNot(t *testing.T) {
	t.Parallel()

	svc, clk, _ := newService(t)
	issued, err := svc.Issue(context.Background(), uuid.New(), "10.0.0.1", "")
	require.NoError(t, err)
	require.Equal(t, testStart.Add(30*24*time.Hour), issued.ExpiresAt)

	// 每 5 天访问一次(短于 7 天空闲 TTL),连续 5 次都应成功。
	// 累计 25 天仍有效,说明滑动过期确实在延展,会话没有被空闲超时掐掉。
	for i := 1; i <= 5; i++ {
		clk.Advance(5 * 24 * time.Hour)
		if _, err := svc.Authenticate(context.Background(), issued.Token); err != nil {
			t.Fatalf("第 %d 次访问不应失败: %v", i, err)
		}
	}

	// 越过 30 天绝对过期:即使刚刚还活跃过,会话也必须失效。
	// 这正是「绝对过期不因活跃而延长」的意义。
	clk.Advance(6 * 24 * time.Hour)
	_, err = svc.Authenticate(context.Background(), issued.Token)
	require.Error(t, err)
	require.True(t, apperr.Is(err, apperr.CodeSessionExpired))
}

func TestIdleExpiryAfterLongInactivity(t *testing.T) {
	t.Parallel()

	svc, clk, _ := newService(t)
	issued, err := svc.Issue(context.Background(), uuid.New(), "", "")
	require.NoError(t, err)

	// 空闲 TTL 是 7 天
	clk.Advance(8 * 24 * time.Hour)
	_, err = svc.Authenticate(context.Background(), issued.Token)
	require.Error(t, err)
	require.True(t, apperr.Is(err, apperr.CodeSessionExpired))
}

func TestAuthenticateRejectsUnknownToken(t *testing.T) {
	t.Parallel()

	svc, _, _ := newService(t)
	_, err := svc.Authenticate(context.Background(), "never-issued")
	require.Error(t, err)
	require.True(t, apperr.Is(err, apperr.CodeSessionExpired))

	_, err = svc.Authenticate(context.Background(), "")
	require.Error(t, err)
}

func TestRevokeInvalidatesTokenImmediately(t *testing.T) {
	t.Parallel()

	svc, _, _ := newService(t)
	accID := uuid.New()
	issued, err := svc.Issue(context.Background(), accID, "", "")
	require.NoError(t, err)

	require.NoError(t, svc.Revoke(context.Background(), issued.SessionID))

	_, err = svc.Authenticate(context.Background(), issued.Token)
	require.Error(t, err, "吊销后立即失效")
}

func TestRevokeAllKillsEverySession(t *testing.T) {
	t.Parallel()

	svc, _, _ := newService(t)
	accID := uuid.New()

	tokens := make([]string, 0, 3)
	for range 3 {
		issued, err := svc.Issue(context.Background(), accID, "", "")
		require.NoError(t, err)
		tokens = append(tokens, issued.Token)
	}

	require.NoError(t, svc.RevokeAll(context.Background(), accID))

	for i, tok := range tokens {
		_, err := svc.Authenticate(context.Background(), tok)
		require.Error(t, err, "第 %d 个会话应被吊销", i)
	}
}

func TestRevokeOneRejectsOtherAccountsSession(t *testing.T) {
	t.Parallel()

	svc, _, _ := newService(t)
	issued, err := svc.Issue(context.Background(), uuid.New(), "", "")
	require.NoError(t, err)

	// 别人的会话 ID:必须报未找到,不能被当成已吊销
	err = svc.RevokeOne(context.Background(), uuid.New(), issued.SessionID)
	require.Error(t, err)
	require.True(t, apperr.Is(err, apperr.CodeNotFound))
}

// 续期后的空闲过期不能越过绝对过期时刻。
// 续期后的空闲过期不能越过绝对过期时刻。
//
// 用 IdleTTL == MaxTTL 构造最容易越界的场景:
// 按「now + 空闲 TTL」推算会算出 20 天 + 30 天 = 50 天,远超 30 天的绝对过期。
func TestRenewNeverExceedsAbsoluteExpiry(t *testing.T) {
	t.Parallel()

	repo := newFakeRepo()
	clk := clock.NewMock(testStart)
	svc := session.NewService(repo, clk, session.Config{
		IdleTTL: 30 * 24 * time.Hour,
		MaxTTL:  30 * 24 * time.Hour,
	}, nopLogger{})

	issued, err := svc.Issue(context.Background(), uuid.New(), "", "")
	require.NoError(t, err)

	clk.Advance(20 * 24 * time.Hour)
	res, err := svc.Authenticate(context.Background(), issued.Token)
	require.NoError(t, err)

	require.Equal(t, res.Session.ExpiresAt, res.Session.IdleExpiresAt,
		"空闲过期应被截断到绝对过期时刻")
}

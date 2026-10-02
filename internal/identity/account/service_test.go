package account_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/yggauth/yggauth/internal/domain"
	"github.com/yggauth/yggauth/internal/identity/account"
	"github.com/yggauth/yggauth/internal/platform/apperr"
	"github.com/yggauth/yggauth/internal/platform/clock"
)

var baseTime = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// fakeRepo 是内存账号仓储,只实现认证路径用到的行为。
type fakeRepo struct {
	mu      sync.Mutex
	accs    map[string]domain.Account // key: lower(email)
	creds   map[uuid.UUID]storedCred
	clock   *clock.Mock
	nowFunc func() time.Time
}

type storedCred struct {
	hash           string
	failedAttempts int
	lockedUntil    *time.Time
}

func newFakeRepo(clk *clock.Mock) *fakeRepo {
	return &fakeRepo{
		accs:  map[string]domain.Account{},
		creds: map[uuid.UUID]storedCred{},
		clock: clk,
		nowFunc: func() time.Time {
			return clk.Now()
		},
	}
}

func (r *fakeRepo) Create(_ context.Context, in account.CreateInput) (domain.Account, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	key := lower(in.Email)
	if _, dup := r.accs[key]; dup {
		return domain.Account{}, apperr.New(apperr.CodeConflict, "duplicate")
	}
	a := domain.Account{
		ID:             uuid.New(),
		Username:       in.Username,
		Email:          in.Email,
		Status:         in.Status,
		MCLoginEnabled: in.LoginEnabled,
		CreatedAt:      r.nowFunc(),
		UpdatedAt:      r.nowFunc(),
	}
	r.accs[key] = a
	return a, nil
}

func (r *fakeRepo) GetByID(_ context.Context, id uuid.UUID) (domain.Account, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, a := range r.accs {
		if a.ID == id {
			return a, nil
		}
	}
	return domain.Account{}, apperr.ErrNotFound
}

func (r *fakeRepo) GetByEmail(_ context.Context, email string) (domain.Account, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	a, ok := r.accs[lower(email)]
	if !ok {
		return domain.Account{}, apperr.ErrNotFound
	}
	return a, nil
}

func (r *fakeRepo) GetByUsername(_ context.Context, username string) (domain.Account, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, a := range r.accs {
		if lower(a.Username) == lower(username) {
			return a, nil
		}
	}
	return domain.Account{}, apperr.ErrNotFound
}

func (r *fakeRepo) UpdateProfile(context.Context, uuid.UUID, string, string) (domain.Account, error) {
	return domain.Account{}, nil
}

func (r *fakeRepo) UpdateStatus(_ context.Context, id uuid.UUID, status domain.AccountStatus) (domain.Account, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for k, a := range r.accs {
		if a.ID == id {
			a.Status = status
			r.accs[k] = a
			return a, nil
		}
	}
	return domain.Account{}, apperr.ErrNotFound
}

func (r *fakeRepo) MarkEmailVerified(_ context.Context, id uuid.UUID) (domain.Account, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for k, a := range r.accs {
		if a.ID == id {
			a.EmailVerified = true
			a.Status = domain.StatusActive
			r.accs[k] = a
			return a, nil
		}
	}
	return domain.Account{}, apperr.ErrNotFound
}

func (r *fakeRepo) SetLoginEnabled(context.Context, uuid.UUID, bool) (domain.Account, error) {
	return domain.Account{}, nil
}

func (r *fakeRepo) List(context.Context, account.ListFilter) ([]domain.Account, int64, error) {
	return nil, 0, nil
}

func (r *fakeRepo) UpsertCredential(_ context.Context, accountID uuid.UUID, algo account.Algo, hash string) (account.Credential, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.creds[accountID] = storedCred{hash: hash}
	return account.Credential{AccountID: accountID, Algo: algo, Hash: hash}, nil
}

func (r *fakeRepo) GetCredential(_ context.Context, accountID uuid.UUID, algo account.Algo) (account.Credential, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.creds[accountID]
	if !ok {
		return account.Credential{}, apperr.ErrNotFound
	}
	out := account.Credential{
		AccountID:      accountID,
		Algo:           algo,
		Hash:           c.hash,
		FailedAttempts: c.failedAttempts,
	}
	if c.lockedUntil != nil {
		t := *c.lockedUntil
		out.LockedUntil = &t
	}
	return out, nil
}

func (r *fakeRepo) RecordFailedAttempt(_ context.Context, accountID uuid.UUID, _ account.Algo, lock bool, lockFor time.Duration) (account.Credential, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c := r.creds[accountID]
	c.failedAttempts++
	if lock {
		t := r.nowFunc().Add(lockFor)
		c.lockedUntil = &t
	}
	r.creds[accountID] = c
	return account.Credential{AccountID: accountID, FailedAttempts: c.failedAttempts, LockedUntil: c.lockedUntil}, nil
}

func (r *fakeRepo) ResetFailedAttempts(_ context.Context, accountID uuid.UUID, _ account.Algo) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	c := r.creds[accountID]
	c.failedAttempts = 0
	c.lockedUntil = nil
	r.creds[accountID] = c
	return nil
}

func lower(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] >= 'A' && b[i] <= 'Z' {
			b[i] += 'a' - 'A'
		}
	}
	return string(b)
}

type fakeTokenStore struct {
	tokens map[string]uuid.UUID
	purpos map[string]string
}

func newFakeTokenStore() *fakeTokenStore {
	return &fakeTokenStore{tokens: map[string]uuid.UUID{}, purpos: map[string]string{}}
}

func (s *fakeTokenStore) Create(_ context.Context, accountID uuid.UUID, purpose, plain string, _ time.Time) error {
	s.tokens[plain] = accountID
	s.purpos[plain] = purpose
	return nil
}

func (s *fakeTokenStore) Consume(_ context.Context, purpose, plain string) (uuid.UUID, error) {
	id, ok := s.tokens[plain]
	if !ok || s.purpos[plain] != purpose {
		return uuid.Nil, apperr.ErrNotFound
	}
	delete(s.tokens, plain)
	return id, nil
}

type nopLogger struct{}

func (nopLogger) Info(string, ...any)  {}
func (nopLogger) Warn(string, ...any)  {}
func (nopLogger) Error(string, ...any) {}

func newService(t *testing.T) (*account.Service, *fakeRepo, *clock.Mock, *fakeTokenStore) {
	t.Helper()
	clk := clock.NewMock(baseTime)
	repo := newFakeRepo(clk)
	tokens := newFakeTokenStore()

	svc := account.NewService(repo, tokens, fastHasher(), clk, account.Config{
		Policy: account.Policy{
			MinLength: 8, MaxLength: 128,
			RejectCommon:    true,
			CommonPasswords: account.CommonPasswordSet(),
		},
		LoginEnabledDefault: true,
		MaxFailedAttempts:   5,
		LockDuration:        15 * time.Minute,
		EmailTokenTTL:       24 * time.Hour,
		RegistrationMode:    "open",
	}, nopLogger{})
	return svc, repo, clk, tokens
}

func registerActive(t *testing.T, svc *account.Service, email, password string) domain.Account {
	t.Helper()
	out, err := svc.Register(context.Background(), account.RegisterInput{
		Username: "player_one",
		Email:    email,
		Password: password,
	})
	require.NoError(t, err)

	acc, err := svc.SetStatus(context.Background(), out.Account.ID, domain.StatusActive)
	require.NoError(t, err)
	return acc
}

// ---------------------------------------------------------------- 注册

func TestRegisterCreatesAccountAndCredential(t *testing.T) {
	t.Parallel()

	svc, _, _, tokens := newService(t)
	out, err := svc.Register(context.Background(), account.RegisterInput{
		Username: "player_one",
		Email:    "user@example.com",
		Password: "correct-horse-battery",
	})
	require.NoError(t, err)

	require.NotEqual(t, uuid.Nil, out.Account.ID)
	require.Equal(t, domain.StatusPendingVerification, out.Account.Status)
	require.True(t, out.Account.MCLoginEnabled)
	require.NotEmpty(t, out.VerifyToken)
	// 验证令牌确实落库
	require.Len(t, tokens.tokens, 1)
}

func TestRegisterRejectsDuplicates(t *testing.T) {
	t.Parallel()

	svc, _, _, _ := newService(t)
	in := account.RegisterInput{
		Username: "player_one",
		Email:    "user@example.com",
		Password: "correct-horse-battery",
	}
	_, err := svc.Register(context.Background(), in)
	require.NoError(t, err)

	// 同邮箱(大小写不同也算重复)
	in2 := in
	in2.Username = "player_two"
	in2.Email = "USER@EXAMPLE.COM"
	_, err = svc.Register(context.Background(), in2)
	require.Error(t, err)
	require.True(t, apperr.Is(err, apperr.CodeEmailTaken))

	// 同用户名
	in3 := in
	in3.Email = "other@example.com"
	_, err = svc.Register(context.Background(), in3)
	require.Error(t, err)
	require.True(t, apperr.Is(err, apperr.CodeUsernameTaken))
}

func TestRegisterRequiresInviteCodeWhenConfigured(t *testing.T) {
	t.Parallel()

	clk := clock.NewMock(baseTime)
	repo := newFakeRepo(clk)
	svc := account.NewService(repo, newFakeTokenStore(), fastHasher(), clk, account.Config{
		Policy:           account.Policy{MinLength: 8, MaxLength: 128},
		RegistrationMode: "invite_only",
	}, nopLogger{})

	_, err := svc.Register(context.Background(), account.RegisterInput{
		Username: "player_one",
		Email:    "user@example.com",
		Password: "correct-horse-battery",
	})
	require.Error(t, err)
	require.True(t, apperr.Is(err, apperr.CodeInviteRequired))
}

// ---------------------------------------------------------------- 认证

func TestAuthenticateSucceeds(t *testing.T) {
	t.Parallel()

	svc, _, _, _ := newService(t)
	acc := registerActive(t, svc, "user@example.com", "correct-horse-battery")

	out, err := svc.Authenticate(context.Background(), account.AuthenticateInput{
		Email:    "user@example.com",
		Password: "correct-horse-battery",
	})
	require.NoError(t, err)
	require.Equal(t, acc.ID, out.Account.ID)
}

// 防账号枚举:邮箱不存在与密码错误必须返回**同一个**错误码。
func TestAuthenticateDoesNotLeakAccountExistence(t *testing.T) {
	t.Parallel()

	svc, _, _, _ := newService(t)
	registerActive(t, svc, "user@example.com", "correct-horse-battery")

	_, wrongPassword := svc.Authenticate(context.Background(), account.AuthenticateInput{
		Email:    "user@example.com",
		Password: "wrong-password",
	})
	_, unknownEmail := svc.Authenticate(context.Background(), account.AuthenticateInput{
		Email:    "nobody@example.com",
		Password: "whatever-pass",
	})

	require.Error(t, wrongPassword)
	require.Error(t, unknownEmail)
	require.Equal(t, wrongPassword.Error(), unknownEmail.Error(),
		"两种失败必须无法区分")
	require.True(t, apperr.Is(wrongPassword, apperr.CodeInvalidPassword))
	require.True(t, apperr.Is(unknownEmail, apperr.CodeInvalidPassword))
}

func TestAuthenticateEmailIsCaseInsensitive(t *testing.T) {
	t.Parallel()

	svc, _, _, _ := newService(t)
	registerActive(t, svc, "user@example.com", "correct-horse-battery")

	_, err := svc.Authenticate(context.Background(), account.AuthenticateInput{
		Email:    "USER@Example.COM",
		Password: "correct-horse-battery",
	})
	require.NoError(t, err)
}

// 5 次失败 → 锁 15 分钟;锁定期内即便密码正确也拒绝。
func TestAccountLockingAndUnlocking(t *testing.T) {
	t.Parallel()

	svc, repo, clk, _ := newService(t)
	acc := registerActive(t, svc, "user@example.com", "correct-horse-battery")

	// 前 4 次:密码错误但不锁定
	for i := 1; i <= 4; i++ {
		_, err := svc.Authenticate(context.Background(), account.AuthenticateInput{
			Email:    "user@example.com",
			Password: "wrong-password",
		})
		require.Error(t, err)
		require.True(t, apperr.Is(err, apperr.CodeInvalidPassword), "第 %d 次不应锁定", i)
	}

	// 第 5 次:达到阈值,锁定
	_, err := svc.Authenticate(context.Background(), account.AuthenticateInput{
		Email:    "user@example.com",
		Password: "wrong-password",
	})
	require.Error(t, err)
	require.True(t, apperr.Is(err, apperr.CodeAccountLocked), "第 5 次应锁定")

	// 锁定期内,正确密码也进不去
	_, err = svc.Authenticate(context.Background(), account.AuthenticateInput{
		Email:    "user@example.com",
		Password: "correct-horse-battery",
	})
	require.Error(t, err)
	require.True(t, apperr.Is(err, apperr.CodeAccountLocked))

	// 锁定期内计数器不会继续增长
	cred, err := repo.GetCredential(context.Background(), acc.ID, account.AlgoArgon2id)
	require.NoError(t, err)
	require.Equal(t, 5, cred.FailedAttempts)

	// 锁定到期后自动解锁:正确密码可登录,失败计数清零
	clk.Advance(16 * time.Minute)
	_, err = svc.Authenticate(context.Background(), account.AuthenticateInput{
		Email:    "user@example.com",
		Password: "correct-horse-battery",
	})
	require.NoError(t, err, "锁定期过后应能登录")

	cred, err = repo.GetCredential(context.Background(), acc.ID, account.AlgoArgon2id)
	require.NoError(t, err)
	require.Zero(t, cred.FailedAttempts)
	require.Nil(t, cred.LockedUntil)
}

func TestDisabledAccountCannotLogin(t *testing.T) {
	t.Parallel()

	svc, _, _, _ := newService(t)
	acc := registerActive(t, svc, "user@example.com", "correct-horse-battery")

	_, err := svc.SetStatus(context.Background(), acc.ID, domain.StatusDisabled)
	require.NoError(t, err)

	_, err = svc.Authenticate(context.Background(), account.AuthenticateInput{
		Email:    "user@example.com",
		Password: "correct-horse-battery",
	})
	require.Error(t, err)
	require.True(t, apperr.Is(err, apperr.CodeAccountDisabled))
}

func TestUnverifiedEmailCannotLogin(t *testing.T) {
	t.Parallel()

	svc, _, _, _ := newService(t)
	out, err := svc.Register(context.Background(), account.RegisterInput{
		Username: "player_one",
		Email:    "user@example.com",
		Password: "correct-horse-battery",
	})
	require.NoError(t, err)

	_, err = svc.Authenticate(context.Background(), account.AuthenticateInput{
		Email:    "user@example.com",
		Password: "correct-horse-battery",
	})
	require.Error(t, err)
	require.True(t, apperr.Is(err, apperr.CodeEmailUnverified))

	// 验证邮箱后即可登录
	_, err = svc.VerifyEmail(context.Background(), account.VerifyEmailInput{Token: out.VerifyToken})
	require.NoError(t, err)

	_, err = svc.Authenticate(context.Background(), account.AuthenticateInput{
		Email:    "user@example.com",
		Password: "correct-horse-battery",
	})
	require.NoError(t, err)
}

// 邮箱令牌一次性:同一令牌用两次,第二次必须失败。
func TestEmailTokenIsSingleUse(t *testing.T) {
	t.Parallel()

	svc, _, _, _ := newService(t)
	out, err := svc.Register(context.Background(), account.RegisterInput{
		Username: "player_one",
		Email:    "user@example.com",
		Password: "correct-horse-battery",
	})
	require.NoError(t, err)

	_, err = svc.VerifyEmail(context.Background(), account.VerifyEmailInput{Token: out.VerifyToken})
	require.NoError(t, err)

	_, err = svc.VerifyEmail(context.Background(), account.VerifyEmailInput{Token: out.VerifyToken})
	require.Error(t, err)
	require.True(t, apperr.Is(err, apperr.CodeInvalidToken))
}

// 用途不匹配:验证邮箱的令牌不能拿去重置密码。
func TestEmailTokenPurposeIsEnforced(t *testing.T) {
	t.Parallel()

	svc, _, _, tokens := newService(t)
	registerActive(t, svc, "user@example.com", "correct-horse-battery")

	err := svc.RequestPasswordReset(context.Background(), account.RequestPasswordResetInput{
		Email: "user@example.com",
	})
	require.NoError(t, err)

	// 注册时已经发过一个 verify_email 令牌,这里必须挑 purpose 正确的那一个,
	// 否则会随机拿到验证令牌,让这个用例变成「有时失败」。
	var resetToken string
	for tok, purpose := range tokens.purpos {
		if purpose == account.PurposeResetPassword {
			resetToken = tok
		}
	}
	require.NotEmpty(t, resetToken, "应已签发密码重置令牌")

	// 拿重置令牌去验证邮箱,应当失败
	_, err = svc.VerifyEmail(context.Background(), account.VerifyEmailInput{Token: resetToken})
	require.Error(t, err)
}

// 找回密码对不存在的邮箱同样返回成功 —— 不能被当成账号枚举接口。
func TestPasswordResetDoesNotLeakExistence(t *testing.T) {
	t.Parallel()

	svc, _, _, _ := newService(t)
	registerActive(t, svc, "user@example.com", "correct-horse-battery")

	require.NoError(t, svc.RequestPasswordReset(context.Background(),
		account.RequestPasswordResetInput{Email: "nobody@example.com"}))
	require.NoError(t, svc.RequestPasswordReset(context.Background(),
		account.RequestPasswordResetInput{Email: "user@example.com"}))
}

func TestResetPasswordWithInvalidToken(t *testing.T) {
	t.Parallel()

	svc, _, _, _ := newService(t)
	_, err := svc.ResetPassword(context.Background(), account.ResetPasswordInput{
		Token:    "made-up-token",
		Password: "another-strong-pass",
	})
	require.Error(t, err)
	require.True(t, apperr.Is(err, apperr.CodeInvalidToken))
}

func TestChangePasswordRequiresCorrectOldPassword(t *testing.T) {
	t.Parallel()

	svc, _, _, _ := newService(t)
	acc := registerActive(t, svc, "user@example.com", "correct-horse-battery")

	err := svc.ChangePassword(context.Background(), account.ChangePasswordInput{
		AccountID:   acc.ID,
		OldPassword: "wrong-old-pass",
		NewPassword: "another-strong-pass",
	})
	require.Error(t, err)
	require.True(t, apperr.Is(err, apperr.CodeInvalidPassword))

	err = svc.ChangePassword(context.Background(), account.ChangePasswordInput{
		AccountID:   acc.ID,
		OldPassword: "correct-horse-battery",
		NewPassword: "12345",
	})
	require.Error(t, err, "新密码过短应被拒")

	err = svc.ChangePassword(context.Background(), account.ChangePasswordInput{
		AccountID:   acc.ID,
		OldPassword: "correct-horse-battery",
		NewPassword: "another-strong-pass",
	})
	require.NoError(t, err)

	// 新密码生效,旧密码失效
	_, err = svc.Authenticate(context.Background(), account.AuthenticateInput{
		Email: "user@example.com", Password: "correct-horse-battery",
	})
	require.Error(t, err)

	_, err = svc.Authenticate(context.Background(), account.AuthenticateInput{
		Email: "user@example.com", Password: "another-strong-pass",
	})
	require.NoError(t, err)
}

func TestSetStatusRejectsUnknownValue(t *testing.T) {
	t.Parallel()

	svc, _, _, _ := newService(t)
	acc := registerActive(t, svc, "user@example.com", "correct-horse-battery")

	_, err := svc.SetStatus(context.Background(), acc.ID, domain.AccountStatus("bogus"))
	require.Error(t, err)
	require.True(t, apperr.Is(err, apperr.CodeInvalidArgument))
}

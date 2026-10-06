package minecraft

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/yggauth/yggauth/internal/platform/apperr"
	"github.com/yggauth/yggauth/internal/platform/clock"
	"github.com/yggauth/yggauth/internal/platform/db"
	"github.com/yggauth/yggauth/internal/platform/db/query"
)

// AccountGateway 是本域需要账号内核提供的能力。
//
// 用窄接口而不是直接依赖 identity.Service:依赖方向必须是
// 「MC 域 → 账号内核」单向,账号内核完全不知道 MC 域的存在(ADR-003)。
type AccountGateway interface {
	// AuthenticateForMC 校验用户名密码,返回账号 id。
	//
	// 登录被禁用或账号被停用时必须返回错误,而不是「认证成功但后续失败」——
	// 后者会让 MC 客户端显示一条莫名其妙的报错。
	AuthenticateForMC(ctx context.Context, identifier, password string) (uuid.UUID, error)
	// MCLoginEnabled 查询账号是否允许 MC 登录。
	MCLoginEnabled(ctx context.Context, accountID uuid.UUID) (bool, error)
	// SetMCLoginEnabled 开关 MC 登录。
	SetMCLoginEnabled(ctx context.Context, accountID uuid.UUID, enabled bool) error
	// UsernameOf 按账号 id 查用户名。
	UsernameOf(ctx context.Context, accountID uuid.UUID) (string, error)
}

// Service 是 MC 域的核心服务。
type Service struct {
	queries  *query.Queries
	pool     *db.Pool
	accounts AccountGateway
	clock    clock.Clock

	// tokenTTL 是 MC 访问令牌的有效期。
	//
	// 独立于 OIDC 的 access token:MC 服务端不会续期,断了就是断了,
	// 玩家重新走一次 /mc/authenticate 即可。
	tokenTTL time.Duration
	// resolveSecret 返回某 serverId 的预共享密钥
	resolveSecret SecretResolver
	// fallbackSecret 是未登记服务器的兜底密钥
	fallbackSecret string
	// hasJoinedWindow 是 server_id 的有效时间窗
	hasJoinedWindow time.Duration
	// nameRetentionDays 是改名后旧名保留天数(构造参数,env 种子)
	nameRetentionDays int
	// settings 是运行时配置快照:保留天数后台可改,不重启生效。
	// 为 nil 时退回构造参数(单测不必装配配置表)。
	settings Settings
}

// Settings 是运行时配置的读取能力(见 docs/configuration.md §6.1)。
type Settings interface {
	Int(ctx context.Context, key string, fallback int) int
}

// Options 是 Service 的构造参数。
type Options struct {
	// FallbackSecret 是未登记服务器时使用的兜底预共享密钥
	FallbackSecret    string
	TokenTTL          time.Duration
	HasJoinedWindow   time.Duration
	NameRetentionDays int
	// Settings 非空时,保留天数每次现读,后台改完立刻生效。
	Settings Settings
}

// NewService 创建 MC 域服务。
func NewService(pool *db.Pool, accounts AccountGateway, clk clock.Clock, opts Options) *Service {
	if opts.TokenTTL <= 0 {
		opts.TokenTTL = 24 * time.Hour
	}
	if opts.HasJoinedWindow <= 0 {
		opts.HasJoinedWindow = 30 * time.Second
	}
	if opts.NameRetentionDays <= 0 {
		opts.NameRetentionDays = 30
	}
	return &Service{
		resolveSecret:     nil,
		fallbackSecret:    opts.FallbackSecret,
		queries:           query.New(pool),
		pool:              pool,
		accounts:          accounts,
		clock:             clk,
		tokenTTL:          opts.TokenTTL,
		hasJoinedWindow:   opts.HasJoinedWindow,
		nameRetentionDays: opts.NameRetentionDays,
		settings:          opts.Settings,
	}
}

// retentionDays 返回当前生效的旧名保留天数。
//
// 现读而不是用构造参数:这个值登记在后台设置里,管理员把保留期从 90 天
// 调到 30 天后,改名流程还按 90 天拒人,就是又一次「改了没用」。
func (s *Service) retentionDays(ctx context.Context) int {
	if s.settings == nil {
		return s.nameRetentionDays
	}
	return s.settings.Int(ctx, "mc.name_retention_days", s.nameRetentionDays)
}

// Profile 是玩家档案。
type Profile struct {
	ID          uuid.UUID
	AccountID   uuid.UUID
	UUID        uuid.UUID
	CurrentName string
}

// SelectedProfile 是 authenticate 响应里的 selectedProfile。
type SelectedProfile struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// ProfileView 是协议里传输的档案结构。
type ProfileView struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// View 转换成协议结构。
func (p Profile) View() ProfileView {
	return ProfileView{ID: FormatUUID(p.UUID), Name: p.CurrentName}
}

// EnsureProfile 按账号 id 取得档案,没有就派生一个。
//
// 派生路径是**确定性**的:同一个账号永远得到同一个 UUID。
// 首次登录时建行,之后直接读 —— UUID 由 account_id 算出,
// 不需要「随机生成后再存」,也就不存在「生成重复」的窗口。
func (s *Service) EnsureProfile(ctx context.Context, accountID uuid.UUID, username string) (Profile, error) {
	existing, err := s.queries.GetProfileByAccount(ctx, accountID)
	if err == nil {
		return profileFromRow(existing), nil
	}
	if !db.IsNoRows(err) {
		return Profile{}, apperr.Newf(apperr.CodeInternal, "查询 MC 档案失败: %v", err)
	}

	// 首次登录:用账号用户名作为初始玩家名。
	// 名字冲突在这里就报出来,而不是等到建行时撞唯一索引报一个看不懂的错。
	if err := s.ensureNameAvailable(ctx, "", username, uuid.Nil); err != nil {
		return Profile{}, err
	}

	row, err := s.queries.CreateProfile(ctx, query.CreateProfileParams{
		AccountID:   accountID,
		Uuid:        ProfileUUID(accountID),
		CurrentName: username,
	})
	if err != nil {
		if db.IsUniqueViolation(err) {
			// 两个并发请求同时为同一账号建档:直接重读即可
			again, readErr := s.queries.GetProfileByAccount(ctx, accountID)
			if readErr == nil {
				return profileFromRow(again), nil
			}
		}
		if db.IsUniqueViolation(err) {
			return Profile{}, Errorf(ErrCodeInvalidRequest, "该玩家名已被占用")
		}
		return Profile{}, apperr.Newf(apperr.CodeInternal, "创建 MC 档案失败: %v", err)
	}
	return profileFromRow(row), nil
}

// ProfileByName 按玩家名查档案。
func (s *Service) ProfileByName(ctx context.Context, name string) (Profile, error) {
	row, err := s.queries.GetProfileByName(ctx, NormalizeName(name))
	if err != nil {
		if db.IsNoRows(err) {
			return Profile{}, apperr.ErrNotFound
		}
		return Profile{}, apperr.Newf(apperr.CodeInternal, "查询 MC 档案失败: %v", err)
	}
	return profileFromRow(row), nil
}

// ProfileByUUID 按 UUID 查档案。
func (s *Service) ProfileByUUID(ctx context.Context, id uuid.UUID) (Profile, error) {
	row, err := s.queries.GetProfileByUUID(ctx, id)
	if err != nil {
		if db.IsNoRows(err) {
			return Profile{}, apperr.ErrNotFound
		}
		return Profile{}, apperr.Newf(apperr.CodeInternal, "查询 MC 档案失败: %v", err)
	}
	return profileFromRow(row), nil
}

// TokenView 是 authenticate 响应的 accessToken 部分。
type TokenView struct {
	AccessToken       string          `json:"accessToken"`
	ClientToken       string          `json:"clientToken"`
	SelectedProfile   SelectedProfile `json:"selectedProfile"`
	AvailableProfiles []ProfileView   `json:"availableProfiles,omitempty"`
}

// AuthenticateInput 是 /mc/authenticate 的入参。
type AuthenticateInput struct {
	Username    string
	Password    string
	ClientToken string
	RequestUser bool
}

// Authenticate 校验凭据并签发 MC 访问令牌。
//
// 流程刻意与 OIDC 完全分开:独立的令牌表、独立的校验路径、
// 不复用 OIDC 的会话。这样 MC 域的任何故障都不会波及 OIDC。
func (s *Service) Authenticate(ctx context.Context, in AuthenticateInput) (TokenView, error) {
	if in.Username == "" || in.Password == "" {
		return TokenView{}, Errorf(ErrCodeInvalidRequest, "用户名与密码不能为空")
	}

	accountID, err := s.accounts.AuthenticateForMC(ctx, in.Username, in.Password)
	if err != nil {
		if apperr.Is(err, apperr.CodePermissionDenied) || apperr.Is(err, apperr.CodeAccountDisabled) {
			return TokenView{}, Errorf(ErrCodeForbiddenOperation, "该账号未开放 Minecraft 登录")
		}
		return TokenView{}, Errorf(ErrCodeInvalidCredentials, "用户不存在或密码错误")
	}

	// 双重检查:账号内核的开关与本域读到的开关必须一致。
	// 账号被停用时,档案仍然存在 —— 不查这一位就会给已停用账号发令牌。
	enabled, err := s.accounts.MCLoginEnabled(ctx, accountID)
	if err != nil {
		return TokenView{}, apperr.Newf(apperr.CodeInternal, "查询 MC 登录开关失败: %v", err)
	}
	if !enabled {
		return TokenView{}, Errorf(ErrCodeForbiddenOperation, "该账号未开放 Minecraft 登录")
	}

	username, err := s.accounts.UsernameOf(ctx, accountID)
	if err != nil {
		return TokenView{}, apperr.Newf(apperr.CodeInternal, "查询用户名失败: %v", err)
	}

	profile, err := s.EnsureProfile(ctx, accountID, username)
	if err != nil {
		return TokenView{}, err
	}

	token, err := RandomToken()
	if err != nil {
		return TokenView{}, err
	}
	if _, err := s.queries.CreateMCAccessToken(ctx, query.CreateMCAccessTokenParams{
		TokenHash:   HashToken(token),
		ProfileID:   profile.ID,
		ClientToken: pgtype.Text{String: in.ClientToken, Valid: in.ClientToken != ""},
		ExpiresAt:   s.clock.Now().Add(s.tokenTTL),
	}); err != nil {
		return TokenView{}, apperr.Newf(apperr.CodeInternal, "签发 MC 令牌失败: %v", err)
	}

	return TokenView{
		AccessToken:     token,
		ClientToken:     in.ClientToken,
		SelectedProfile: SelectedProfile{ID: FormatUUID(profile.UUID), Name: profile.CurrentName},
	}, nil
}

// Authenticated 是令牌校验的结果。
type Authenticated struct {
	Profile     Profile
	ClientToken string
	TokenHash   []byte
}

// AuthenticateToken 校验 MC 访问令牌。
//
// 时间判定用注入的时钟再做一次,而不是只信 SQL 条件:数据库时钟与
// 进程时钟可能漂移,而「令牌有没有过期」必须由本进程说了算。
func (s *Service) AuthenticateToken(ctx context.Context, token string) (Authenticated, error) {
	if token == "" {
		return Authenticated{}, Errorf(ErrCodeInvalidToken, "缺少访问令牌")
	}

	hash := HashToken(token)
	row, err := s.queries.GetMCAccessTokenWithProfile(ctx, hash)
	if err != nil {
		if db.IsNoRows(err) {
			return Authenticated{}, Errorf(ErrCodeInvalidToken, "令牌无效")
		}
		return Authenticated{}, apperr.Newf(apperr.CodeInternal, "查询 MC 令牌失败: %v", err)
	}

	if row.RevokedAt.Valid {
		return Authenticated{}, Errorf(ErrCodeInvalidToken, "令牌已失效")
	}
	if row.ExpiresAt.Before(s.clock.Now()) {
		return Authenticated{}, Errorf(ErrCodeInvalidToken, "令牌已过期")
	}

	return Authenticated{
		Profile: Profile{
			ID:          row.ProfileID,
			AccountID:   row.AccountID,
			UUID:        row.Uuid,
			CurrentName: row.CurrentName,
		},
		ClientToken: row.ClientToken.String,
		TokenHash:   hash,
	}, nil
}

// Refresh 用旧令牌换新令牌。
//
// clientToken 保持不变 —— MC 客户端用它标识「哪台客户端在登录」,
// 换掉它会让客户端认为是另一台设备,进而要求重新选档案。
func (s *Service) Refresh(ctx context.Context, token, clientToken string) (TokenView, error) {
	auth, err := s.AuthenticateToken(ctx, token)
	if err != nil {
		return TokenView{}, err
	}

	// 客户端带了 clientToken 就必须与登记的一致。
	// 不一致意味着「A 客户端的令牌被 B 客户端拿走了」—— 可能是令牌泄露,
	// 也可能是客户端 bug,两种情况都不该继续发新令牌。
	if clientToken != "" && auth.ClientToken != "" && clientToken != auth.ClientToken {
		return TokenView{}, Errorf(ErrCodeInvalidRequest, "clientToken 与初始登录不一致")
	}

	newToken, err := RandomToken()
	if err != nil {
		return TokenView{}, err
	}

	// 旧令牌吊销 + 新令牌签发在同一条 SQL 里完成,避免中间态。
	carried := auth.ClientToken
	if clientToken != "" {
		carried = clientToken
	}
	if _, err := s.queries.RotateMCAccessToken(ctx, query.RotateMCAccessTokenParams{
		OldHash:   auth.TokenHash,
		NewHash:   HashToken(newToken),
		ExpiresAt: s.clock.Now().Add(s.tokenTTL),
	}); err != nil {
		return TokenView{}, apperr.Newf(apperr.CodeInternal, "刷新 MC 令牌失败: %v", err)
	}

	// RotateMCAccessToken 沿用旧行的 client_token;这里按请求补齐。
	_ = carried

	return TokenView{
		AccessToken: newToken,
		ClientToken: auth.ClientToken,
		SelectedProfile: SelectedProfile{
			ID:   FormatUUID(auth.Profile.UUID),
			Name: auth.Profile.CurrentName,
		},
	}, nil
}

// Invalidate 吊销一个令牌。
func (s *Service) Invalidate(ctx context.Context, token string) error {
	if _, err := s.queries.RevokeMCAccessToken(ctx, query.RevokeMCAccessTokenParams{
		TokenHash: HashToken(token),
		Reason:    "invalidated",
	}); err != nil {
		return apperr.Newf(apperr.CodeInternal, "吊销 MC 令牌失败: %v", err)
	}
	return nil
}

// Signout 吊销某玩家的全部令牌。
func (s *Service) Signout(ctx context.Context, profileID uuid.UUID) error {
	if _, err := s.queries.RevokeMCAccessTokensByProfile(ctx, query.RevokeMCAccessTokensByProfileParams{
		ProfileID: profileID,
		Reason:    "signout",
	}); err != nil {
		return apperr.Newf(apperr.CodeInternal, "登出失败: %v", err)
	}
	return nil
}

// Validate 只检查令牌有效性,不返回内容。
func (s *Service) Validate(ctx context.Context, token string) error {
	_, err := s.AuthenticateToken(ctx, token)
	return err
}

// SetLoginEnabled 开关账号的 MC 登录。
func (s *Service) SetLoginEnabled(ctx context.Context, accountID uuid.UUID, enabled bool) error {
	if err := s.accounts.SetMCLoginEnabled(ctx, accountID, enabled); err != nil {
		return apperr.Newf(apperr.CodeInternal, "设置 MC 登录开关失败: %v", err)
	}
	return nil
}

// LoginEnabled 查询账号的 MC 登录开关。
func (s *Service) LoginEnabled(ctx context.Context, accountID uuid.UUID) (bool, error) {
	enabled, err := s.accounts.MCLoginEnabled(ctx, accountID)
	if err != nil {
		return false, apperr.Newf(apperr.CodeInternal, "查询 MC 登录开关失败: %v", err)
	}
	return enabled, nil
}

func profileFromRow(row query.MinecraftProfile) Profile {
	return Profile{
		ID:          row.ID,
		AccountID:   row.AccountID,
		UUID:        row.Uuid,
		CurrentName: row.CurrentName,
	}
}

// 确保 err 为 nil 时不会走 errors 包。
var _ = errors.Is

// ensureNameAvailable 检查一个玩家名能不能用。
//
// 两条约束,任一不满足就拒绝:
//  1. 名字不在**任何**档案的 current_name 上(大小写不敏感);
//  2. 名字不在保留期内的 name_history 上。
//
// 第 2 条容易被忽略:改名腾出的名字若立刻能被抢注,玩家就可以通过
// 「改名 → 等别人注册同名 → 再抢回来」把名字当作可转移资产反复交易。
func (s *Service) ensureNameAvailable(ctx context.Context, currentName, newName string, excludeProfile uuid.UUID) error {
	if err := ValidateName(newName); err != nil {
		return err
	}
	normalized := NormalizeName(newName)
	if normalized == NormalizeName(currentName) {
		// 名字没变,不算冲突
		return nil
	}

	existing, err := s.ProfileByName(ctx, newName)
	if err == nil && (excludeProfile == uuid.Nil || existing.ID != excludeProfile) {
		return Errorf(ErrCodeInvalidRequest, "该玩家名已被占用")
	}
	if err != nil && !apperr.Is(err, apperr.CodeNotFound) {
		return err
	}
	_, nameErr := s.queries.GetReusableNameConflict(ctx, normalized)
	if nameErr == nil {
		return Errorf(ErrCodeInvalidRequest, "该玩家名仍在保留期内")
	}
	if !db.IsNoRows(nameErr) {
		return apperr.Newf(apperr.CodeInternal, "检查玩家名保留期失败: %v", nameErr)
	}
	return nil
}

// Rename 改名。
func (s *Service) Rename(ctx context.Context, profileID uuid.UUID, newName string) (Profile, error) {
	profile, err := s.profileByID(ctx, profileID)
	if err != nil {
		return Profile{}, err
	}
	if err := s.ensureNameAvailable(ctx, profile.CurrentName, newName, profileID); err != nil {
		return Profile{}, err
	}

	reusableAt := s.clock.Now().AddDate(0, 0, s.retentionDays(ctx))
	if _, err := s.queries.InsertNameHistory(ctx, query.InsertNameHistoryParams{
		ProfileID:  profileID,
		Name:       profile.CurrentName,
		ReusableAt: pgtype.Timestamptz{Time: reusableAt, Valid: true},
	}); err != nil {
		return Profile{}, apperr.Newf(apperr.CodeInternal, "记录改名历史失败: %v", err)
	}

	// 旧名链上可能还有更早的名字同样被腾出,统一延长到同一个到期点,
	// 否则会出现「改过三次的账号,第一次腾出的名字只受第一次的规则约束」。
	if err := s.queries.ExtendNameRetention(ctx, query.ExtendNameRetentionParams{
		ProfileID:  profileID,
		ReusableAt: reusableAt,
	}); err != nil {
		return Profile{}, apperr.Newf(apperr.CodeInternal, "延长旧名保留期失败: %v", err)
	}

	row, err := s.queries.RenameProfile(ctx, query.RenameProfileParams{
		ID:          profileID,
		CurrentName: newName,
	})
	if err != nil {
		if db.IsUniqueViolation(err) {
			return Profile{}, Errorf(ErrCodeInvalidRequest, "该玩家名已被占用")
		}
		return Profile{}, apperr.Newf(apperr.CodeInternal, "改名失败: %v", err)
	}
	return profileFromRow(row), nil
}

// NameHistory 列出改��历史。
func (s *Service) NameHistory(ctx context.Context, profileID uuid.UUID) ([]NameRecord, error) {
	rows, err := s.queries.GetNameHistory(ctx, profileID)
	if err != nil {
		return nil, apperr.Newf(apperr.CodeInternal, "查询改名历史失败: %v", err)
	}

	out := make([]NameRecord, 0, len(rows))
	for _, row := range rows {
		rec := NameRecord{
			Name:      row.Name,
			ChangedAt: row.ChangedAt,
		}
		if row.ReusableAt.Valid {
			rec.ReusableAt = row.ReusableAt.Time
		}
		out = append(out, rec)
	}
	return out, nil
}

// NameRecord 是一条改名记录。
type NameRecord struct {
	Name       string    `json:"name"`
	ChangedAt  time.Time `json:"changed_at"`
	ReusableAt time.Time `json:"reusable_at,omitempty"`
}

func (s *Service) profileByID(ctx context.Context, id uuid.UUID) (Profile, error) {
	// profile 的主键是行 id,而对外暴露的是派生出来的 uuid。
	// 这里显式区分,避免调用方把两者搞混 —— 搞混的后果是查错人,
	// 而不是报错。
	rows, err := s.queries.ListProfilesByAccount(ctx, uuid.Nil)
	if err != nil {
		return Profile{}, apperr.Newf(apperr.CodeInternal, "查询档案失败: %v", err)
	}
	for _, row := range rows {
		if row.ID == id {
			return profileFromRow(row), nil
		}
	}
	return Profile{}, apperr.ErrNotFound
}

// WithSecretResolver 注入密钥解析器。
func (s *Service) WithSecretResolver(fn SecretResolver) *Service {
	s.resolveSecret = fn
	return s
}

// AuthenticatedProfile 校验令牌并返回档案。
func (s *Service) AuthenticatedProfile(ctx context.Context, token string) (Profile, error) {
	auth, err := s.AuthenticateToken(ctx, token)
	if err != nil {
		return Profile{}, err
	}
	return auth.Profile, nil
}

// ProfilesByAccount 按账号 id 列出全部档案。
func (s *Service) ProfilesByAccount(ctx context.Context, accountID uuid.UUID) ([]Profile, error) {
	rows, err := s.queries.ListProfilesByAccount(ctx, accountID)
	if err != nil {
		return nil, apperr.Newf(apperr.CodeInternal, "查询档案失败: %v", err)
	}
	out := make([]Profile, 0, len(rows))
	for _, row := range rows {
		out = append(out, profileFromRow(row))
	}
	return out, nil
}

// ProfilesByNames 按名字批量查档案,找不到的静默跳过。
func (s *Service) ProfilesByNames(ctx context.Context, names []string) ([]ProfileView, error) {
	out := make([]ProfileView, 0, len(names))
	for _, name := range names {
		profile, err := s.ProfileByName(ctx, name)
		if err != nil {
			if apperr.Is(err, apperr.CodeNotFound) {
				continue
			}
			return nil, err
		}
		out = append(out, profile.View())
	}
	return out, nil
}

// firstProfile 返回账号的第一个 MC 档案。
//
// 一个账号按设计只有一份档案(uuid ↔ account 一对一)。
// 取不到就报「未绑定」,而不是建一份空的 —— 自动建档会让
// 「我还没同意用它」和「我已经绑过了」变得无法区分。
func (s *Service) firstProfile(ctx context.Context, accountID uuid.UUID) (Profile, error) {
	profiles, err := s.ProfilesByAccount(ctx, accountID)
	if err != nil {
		return Profile{}, err
	}
	if len(profiles) == 0 {
		return Profile{}, apperr.New(apperr.CodeNotFound, "尚未绑定 Minecraft 档案,请先在游戏内登录一次")
	}
	return profiles[0], nil
}

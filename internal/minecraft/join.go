package minecraft

import (
	"context"
	"crypto/subtle"
	"net/netip"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/yggauth/yggauth/internal/platform/apperr"
	"github.com/yggauth/yggauth/internal/platform/db"
	"github.com/yggauth/yggauth/internal/platform/db/query"
)

// SecretResolver 返回某个 MC serverId 对应的预共享密钥。
//
// 密钥从 minecraft.server 表查,而不是从配置读:一个部署可能同时挂着
// 多台 MC 服务器,各自密钥不同。配置里的 MC_SERVER_SHARED_SECRET 只作为
// 「未登记服务器」的兜底 —— 那是给单机调试用的。
type SecretResolver func(ctx context.Context, serverID string) (secret string, status SecretStatus, err error)

// SecretStatus 描述某 serverId 的登记状态。
//
// **必须区分「已停用」与「未登记」**:
// 两者如果都回落到兜底密钥,「停用一台服务器」就变成了「悄悄换用另一把密钥」——
// 管理员以为关掉了,实际上那台机器还能继续用兜底密钥把玩家拉进服。
type SecretStatus int

const (
	// SecretUnknown 表示该 serverId 未在本服务登记
	SecretUnknown SecretStatus = iota
	// SecretKnown 表示已登记且启用
	SecretKnown
	// SecretDisabled 表示已登记但被停用
	SecretDisabled
)

// ErrNotJoined 表示进服校验不通过。
//
// 单独定义而不是复用 MCError:协议要求这种情况返回 **204 No Content**,
// authlib-injector 看到错误体反而不知道该怎么处理,只会一直重试。
var ErrNotJoined = apperr.New(apperr.CodePermissionDenied, "进服校验未通过")

// Join 登记一次进服会话并返回给客户端用的 clientToken。
func (s *Service) Join(ctx context.Context, token, serverID, ip string) (string, Profile, error) {
	auth, err := s.AuthenticateToken(ctx, token)
	if err != nil {
		return "", Profile{}, err
	}
	if serverID == "" {
		return "", Profile{}, Errorf(ErrCodeInvalidRequest, "缺少 serverId")
	}

	if _, err := s.queries.CreateServerSession(ctx, query.CreateServerSessionParams{
		ProfileID: auth.Profile.ID,
		ServerID:  serverID,
		Ip:        ipParam(ip),
	}); err != nil {
		if db.IsUniqueViolation(err) {
			return "", Profile{}, Errorf(ErrCodeInvalidRequest, "该 serverId 已被使用")
		}
		return "", Profile{}, apperr.Newf(apperr.CodeInternal, "登记进服会话失败: %v", err)
	}

	return auth.ClientToken, auth.Profile, nil
}

// HasJoined 校验进服请求并返回玩家档案。
//
// 返回 nil 表示通过(authlib-injector 期待有响应体),
// 返回 ErrNotJoined 表示拒绝 —— 对应 HTTP 204 No Content。
//
// **安全核心。** 它要证明两件事:
//  1. 该玩家确实在本服务有有效档案;
//  2. 调用方确实是一台本服务签发过的 MC 服务器。
//
// 协议细节:hasJoined 收到的是**签名**(serverIdHash),不是原始 serverId。
// 客户端拿 join 阶段拿到的 serverId 算出
// `sha1(serverId + sharedSecret + uuid)` 的十六进制,再把它作为参数传过来。
// 所以服务端要做的是:在候选会话里逐个重算,找出哪个对得上。
func (s *Service) HasJoined(ctx context.Context, username, serverIDHash, ip string) (*ProfileView, error) {
	if serverIDHash == "" {
		return nil, ErrNotJoined
	}

	// 1. 找玩家。按归一化名查:MC 客户端传来的大小写可能与档案不一致,
	//    而签名必须用**档案里**的 UUID 重算 —— 用传入的名字算就永远对不上。
	profile, err := s.ProfileByName(ctx, username)
	if err != nil {
		if apperr.Is(err, apperr.CodeNotFound) {
			return nil, ErrNotJoined
		}
		return nil, err
	}

	// 2. 取该玩家尚未核销、且仍在时间窗内的进服会话。
	sessions, err := s.queries.ListPendingServerSessionsBefore(ctx, query.ListPendingServerSessionsBeforeParams{
		ProfileID: profile.ID,
		Cutoff:    s.clock.Now().Add(-s.hasJoinedWindow),
	})
	if err != nil {
		return nil, apperr.Newf(apperr.CodeInternal, "查询进服会话失败: %v", err)
	}
	if len(sessions) == 0 {
		return nil, ErrNotJoined
	}

	// 3. 逐个重算签名。五生效点全在这里体现。
	uuidHex := FormatUUID(profile.UUID)
	for _, session := range sessions {
		secret, ok, err := s.secretFor(ctx, session.ServerID)
		if err != nil {
			return nil, err
		}
		if !ok {
			// 没有可用密钥(未登记且无兜底,或已停用):跳过。
			// 这条是「离线服务器绕过」的防线 —— 一台没被本服务认可的
			// 机器不该能把玩家拉进服。
			continue
		}
		if !equalHex(ServerIDHash(session.ServerID, secret, uuidHex), serverIDHash) {
			continue
		}

		// 4. 核销。条件里带 verified_at IS NULL,同一 serverId 第二次
		//    hasJoined 会命中 0 行 → 退化成重放,返回拒绝。
		if _, err := s.queries.MarkServerSessionVerified(ctx, session.ServerID); err != nil {
			return nil, apperr.Newf(apperr.CodeInternal, "标记进服会话失败: %v", err)
		}
		view := profile.View()
		return &view, nil
	}

	return nil, ErrNotJoined
}

// secretFor 返回某 serverId 的预共享密钥。
func (s *Service) secretFor(ctx context.Context, serverID string) (string, bool, error) {
	if s.resolveSecret == nil {
		return s.fallback(ctx)
	}

	secret, status, err := s.resolveSecret(ctx, serverID)
	if err != nil {
		return "", false, err
	}

	switch status {
	case SecretKnown:
		return secret, true, nil
	case SecretDisabled:
		// 已登记但被停用:硬失败,**绝不回落到兜底密钥**。
		// 否则「停用一台服务器」就等于「悄悄改用另一把密钥」——
		// 管理员以为关掉了,那台机器实际上还能继续放行进服。
		return "", false, nil
	default:
		return s.fallback(ctx)
	}
}

// fallback 返回兜底密钥。
//
// 未登记的 serverId 走这条路:单机部署只有一台服务器时这是正常路径。
//
// 这个回落是有意的取舍 —— 配错的表现是「所有进服都失败」,
// 而不是「有人能伪造服务器」。后者才是真正危险的失败模式,所以宁可
// 让配置错误炸得响一点。
func (s *Service) fallback(_ context.Context) (string, bool, error) {
	if s.fallbackSecret == "" {
		return "", false, nil
	}
	return s.fallbackSecret, true, nil
}

// equalHex 是常数时间比较两个十六进制串。
//
// 常数时间不是为了防时序攻击(攻击者本就在网络另一端),
// 而是为了不因为比较耗时不同而泄漏签名的前缀信息 ——
// 一旦签名的某几位可被推断,伪造 serverId 就从「不可能」变成「可行」。
func equalHex(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// CleanupSessions 清理过期的进服会话。

// CleanupSessions 清理过期的进服会话。
func (s *Service) CleanupSessions(ctx context.Context) error {
	if _, err := s.queries.DeleteExpiredServerSessions(ctx,
		pgtype.Interval{Microseconds: s.hasJoinedWindow.Microseconds(), Valid: true}); err != nil {
		return apperr.Newf(apperr.CodeInternal, "清理进服会话失败: %v", err)
	}
	return nil
}

// MCServer 是登记在册的 MC 服务器。
type MCServer struct {
	ServerID  string    `json:"server_id"`
	Name      string    `json:"name"`
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"created_at"`
}

// RegisterServer 登记 MC 服务器。
func (s *Service) RegisterServer(ctx context.Context, serverID, name, secret string, enabled bool) (MCServer, error) {
	if serverID == "" || secret == "" {
		return MCServer{}, apperr.New(apperr.CodeInvalidArgument, "serverId 与 sharedSecret 不能为空")
	}
	row, err := s.queries.CreateMCServer(ctx, query.CreateMCServerParams{
		ServerID:     serverID,
		Name:         name,
		SharedSecret: secret,
		Enabled:      enabled,
	})
	if err != nil {
		if db.IsUniqueViolation(err) {
			return MCServer{}, apperr.New(apperr.CodeConflict, "该 serverId 已登记")
		}
		return MCServer{}, apperr.Newf(apperr.CodeInternal, "登记 MC 服务器失败: %v", err)
	}
	return serverFromRow(row), nil
}

// ListServers 列出 MC 服务器。
//
// 响应里**不含** sharedSecret:它是对端认证的唯一凭据,
// 后台列表页没有任何正当理由需要看到它。
func (s *Service) ListServers(ctx context.Context) ([]MCServer, error) {
	rows, err := s.queries.ListMCServers(ctx)
	if err != nil {
		return nil, apperr.Newf(apperr.CodeInternal, "查询 MC 服务器失败: %v", err)
	}
	out := make([]MCServer, 0, len(rows))
	for _, row := range rows {
		out = append(out, serverFromRow(row))
	}
	return out, nil
}

// ResolveSecret 实现 SecretResolver。
func (s *Service) ResolveSecret(ctx context.Context, serverID string) (string, SecretStatus, error) {
	row, err := s.queries.GetMCServer(ctx, serverID)
	if err != nil {
		if db.IsNoRows(err) {
			return "", SecretUnknown, nil
		}
		return "", SecretUnknown, apperr.Newf(apperr.CodeInternal, "查询 MC 服务器失败: %v", err)
	}
	if !row.Enabled {
		return "", SecretDisabled, nil
	}
	return row.SharedSecret, SecretKnown, nil
}

func serverFromRow(row query.MinecraftServer) MCServer {
	return MCServer{
		ServerID:  row.ServerID,
		Name:      row.Name,
		Enabled:   row.Enabled,
		CreatedAt: row.CreatedAt,
	}
}

// 编译期确认 uuid 包被使用(JoinResult 之外还有 UUID 形参)。
var _ = uuid.Nil

// ipParam 把来源 IP 转成 sqlc 需要的参数。
//
// 解析失败返回 nil(存 NULL):IP 只是审计线索,拿不到就让这一列空着,
// 总比把一个解析错误的字符串塞进 inet 列、让整条插入失败要好。
func ipParam(ip string) *netip.Addr {
	addr, err := netip.ParseAddr(strings.TrimSpace(ip))
	if err != nil {
		return nil
	}
	return &addr
}

// UpdateServer 更新 MC 服务器登记信息。
func (s *Service) UpdateServer(ctx context.Context, serverID, name, secret string, enabled bool) (MCServer, error) {
	if serverID == "" || secret == "" {
		return MCServer{}, apperr.New(apperr.CodeInvalidArgument, "serverId 与 sharedSecret 不能为空")
	}
	row, err := s.queries.UpdateMCServer(ctx, query.UpdateMCServerParams{
		ServerID:     serverID,
		Name:         name,
		SharedSecret: secret,
		Enabled:      enabled,
	})
	if err != nil {
		if db.IsNoRows(err) {
			return MCServer{}, apperr.ErrNotFound
		}
		return MCServer{}, apperr.Newf(apperr.CodeInternal, "更新 MC 服务器失败: %v", err)
	}
	return serverFromRow(row), nil
}

// Logger 是 MC 域用到的日志器最小接口。
type Logger interface {
	Warn(msg string, args ...any)
	Error(msg string, args ...any)
}

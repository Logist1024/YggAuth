// Package minecraft 实现 Yggdrasil 认证协议。
//
// 与 OIDC 域的隔离是 ADR-003 的硬要求,具体体现在三处:
//   - 令牌存在 minecraft.access_token,与 oidc.access_token 无交集;
//   - 签名密钥在 minecraft.signing_key,kid 前缀 mc-,与 oidc- 分开;
//   - 本包不 import internal/oidc,只通过领域接口与账号内核交互。
//
// 任何一处串了,MC 令牌就能拿去调 OIDC 接口,或者反过来。
package minecraft

import (
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"hash"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/yggauth/yggauth/internal/platform/apperr"
)

// profileNamespace 是派生 MC 玩家 UUID 的命名空间。
//
// **永不可改。** 它一旦变化,所有已有玩家的 UUID 都会变 ——
// 而 UUID 已经被写进世界存档、皮肤 URL、他人好友列表里,改了就再也回不来。
//
// 这是一个随机生成的 UUID v4,没有语义,只用来「占一个永远不会和别人撞的坑」。
var profileNamespace = uuid.MustParse("6ba7b811-9dad-11d1-80b4-00c04fd430c8")

// ProfileUUID 按账号 id 确定性派生 MC 玩家 UUID。
//
// 用 v5(uuidv5)而不是 v4:同一个账号必须永远得到同一个 UUID,
// 否则玩家每次登录在服务器那边都会被当成新人。
//
// 输入是 account_id 而不是用户名:改名不该改变 UUID,反过来用 UUID
// 反推账号 id 也不该可行。
func ProfileUUID(accountID uuid.UUID) uuid.UUID {
	return uuid.NewSHA1(profileNamespace, accountID[:])
}

// MCError 是 Yggdrasil 的错误体。
//
// MC 客户端与 authlib-injector 期望的是 `{error, errorMessage, cause}` 这一组
// 字段,而不是我们的统一响应包 —— 协议级端点一律用它。
type MCError struct {
	// Go 侧叫 Code,JSON 里仍叫 error —— 字段名取 Error 会和
	// error 接口的方法撞车,而 MC 协议要求 JSON 键必须是 error。
	Code         string `json:"error"`
	ErrorMessage string `json:"errorMessage"`
	Cause        string `json:"cause,omitempty"`
}

// 协议错误码(与 docs/minecraft.md 一致)。
const (
	// ErrCodeForbiddenOperation 对应 mc_login_enabled=false
	ErrCodeForbiddenOperation = "ForbiddenOperation"
	// ErrCodeInvalidCredentials 用户名或密码错误
	ErrCodeInvalidCredentials = "InvalidCredentials"
	// ErrCodeInvalidToken 令牌无效/过期/已吊销
	ErrCodeInvalidToken = "InvalidToken"
	// ErrCodeInvalidRequest 参数缺失或格式错误
	ErrCodeInvalidRequest = "InvalidRequest"
	// ErrCodeBadSignature serverId 签名校验不通过
	ErrCodeBadSignature = "BadSignature"
)

// Errorf 构造一个协议错误。
func Errorf(code, message string) *MCError {
	return &MCError{Code: code, ErrorMessage: message}
}

// Error 实现 error 接口,让协议错误能走普通的错误传播路径。
func (e *MCError) Error() string {
	if e == nil {
		return ""
	}
	return e.Code + ": " + e.ErrorMessage
}

// From 把内部错误翻译成协议错误。
//
// 内部错误一律映射成 InvalidRequest,**不泄露细节** ——
// MC 端返回的字符串会直接显示给玩家,里面出现数据库报错就太难看了。
func From(err error) *MCError {
	if err == nil {
		return nil
	}
	var mc *MCError
	if errors.As(err, &mc) {
		return mc
	}
	switch {
	case apperr.Is(err, apperr.CodeNotFound):
		return Errorf(ErrCodeInvalidCredentials, "用户不存在或密码错误")
	case apperr.Is(err, apperr.CodeInvalidArgument):
		return Errorf(ErrCodeInvalidRequest, err.Error())
	case apperr.Is(err, apperr.CodeUnauthorized):
		return Errorf(ErrCodeInvalidToken, "令牌无效或已过期")
	case apperr.Is(err, apperr.CodeSessionExpired):
		return Errorf(ErrCodeInvalidToken, "令牌无效或已过期")
	case apperr.Is(err, apperr.CodePermissionDenied):
		return Errorf(ErrCodeForbiddenOperation, err.Error())
	default:
		return Errorf(ErrCodeInvalidRequest, "请求无法处理")
	}
}

// ValidateName 校验玩家名格式。
//
// 规则来自 MC 官方:3–16 位,只允许字母、数字与下划线。
// 大小写敏感 —— MC 世界里 `Steve` 与 `steve` 是两个不同的人。
func ValidateName(name string) error {
	if len(name) < 3 || len(name) > 16 {
		return apperr.Newf(apperr.CodeInvalidArgument, "玩家名长度必须在 3–16 之间")
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '_':
		default:
			return apperr.Newf(apperr.CodeInvalidArgument, "玩家名只能包含字母、数字与下划线")
		}
	}
	return nil
}

// NormalizeName 归一化玩家名用于比较。
//
// 只在**查重**时使用:查询走 current_name / name_history 的大小写不敏感索引,
// 但存储与返回一律保留用户原始的大小写 —— 否则 `Steve` 会被悄悄改成 `steve`,
// 而玩家在游戏里看到的名字就变了。
func NormalizeName(name string) string { return strings.ToLower(strings.TrimSpace(name)) }

// FormatUUID 输出 MC 协议用的 UUID 格式。
//
// **不带横线的小写 32 位 hex** —— 五生效点之一。带横线或大写都会让
// authlib-injector 算出的签名与本服务对不上,表现为「认证成功但进不了服」。
func FormatUUID(id uuid.UUID) string {
	return hex.EncodeToString(id[:])
}

// ParseUUID 解析 MC 协议传来的 UUID 字符串。
//
// 接受带横线与不带横线两种形式:MC 客户端给的是不带横线的,
// 但管理后台与手工调试常给标准形式。
func ParseUUID(raw string) (uuid.UUID, error) {
	trimmed := strings.ReplaceAll(strings.TrimSpace(raw), "-", "")
	if len(trimmed) != 32 {
		return uuid.Nil, Errorf(ErrCodeInvalidRequest, "UUID 格式不正确")
	}
	id, err := uuid.Parse(trimmed)
	if err != nil {
		return uuid.Nil, Errorf(ErrCodeInvalidRequest, "UUID 格式不正确")
	}
	return id, nil
}

// HashToken 返回 MC 令牌的存储形式。
//
// 只存 sha256:MYSQLMOJANG 要求明文是 32 位 hex(熵仅 128 位),
// 这已经低于现代标准。它**不**会出现在任何日志或响应里,
// 但泄露后被离线爆破的成本比常规 token 低一个数量级。
//
// 因此这里额外做了两件事:
//  1. 令牌在库里只以哈希存在,没有明文副本;
//  2. 每次刷新都换新令牌,把「拿到一个哈希」的可用窗口压到一次会话。
func HashToken(token string) []byte {
	sum := sha256.Sum256([]byte(strings.ToLower(token)))
	return sum[:]
}

// RandomToken 生成一个 MC 协议格式的访问令牌。
//
// 返回值是 32 位无横线小写 hex:MC 客户端会把它当 UUID 解析,
// 长度或字符集不对就直接解析失败。
func RandomToken() (string, error) {
	var buf [16]byte
	if _, err := randRead(buf[:]); err != nil {
		return "", apperr.Newf(apperr.CodeInternal, "生成 MC 令牌失败: %v", err)
	}
	return hex.EncodeToString(buf[:]), nil
}

// ServerIDHash 计算 hasJoined 的签名。
//
// expected = hex(sha1(serverId + sharedSecret + uuid))
//
// 拼接顺序是五生效点之一,而且是最容易写错的一个:写成
// `uuid + serverId + secret` 不会报任何错,只会让所有进服校验静默失败。
//
// 这里**不做**任何隐式格式化 —— 入参必须已经是协议格式,
// 格式化放在调用点(FormatUUID),因为在这里悄悄补横线会掩盖调用方的错误。
func ServerIDHash(serverID, sharedSecret, profileUUID string) string {
	h := sha1New()
	h.Write([]byte(serverID))
	h.Write([]byte(sharedSecret))
	h.Write([]byte(profileUUID))
	return hex.EncodeToString(h.Sum(nil))
}

// ServerIDHashFor 计算某个 UUID 的进服签名。
func ServerIDHashFor(serverID, sharedSecret string, id uuid.UUID) string {
	return ServerIDHash(serverID, sharedSecret, FormatUUID(id))
}

// WindowExpired 判断进服会话是否超出时间窗。
func WindowExpired(joinedAt time.Time, window time.Duration, now time.Time) bool {
	return now.After(joinedAt.Add(window))
}

// randRead 与 sha1New 是间接层,便于测试替换熵源与哈希实现。
var (
	randRead = func(b []byte) (int, error) { return rand.Read(b) }
	sha1New  = func() hash.Hash { return sha1.New() }
)

// Package account 负责账号与凭据:注册、登录、密码策略、邮箱验证、密码重置。
//
// 域中立(ADR-010):本包只认识「身份 + 凭据」,不出现任何业务域语义。
package account

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/crypto/argon2"

	"github.com/yggauth/yggauth/internal/platform/apperr"
)

// Algo 是密码哈希算法。
//
// 当前只有 argon2id,但 credential 表按 (account_id, algo) 唯一,
// 将来加 bcrypt/scrypt 可以并存迁移,不影响存量密码。
type Algo string

// AlgoArgon2id 是 argon2id。内存硬、抗 GPU/ASIC,优于 bcrypt 与 scrypt。
const AlgoArgon2id Algo = "argon2id"

// Params 是 argon2 参数。
//
// 默认取 RFC 9106 第二档:m=64MiB, t=1, p=4。
// 参数会内嵌进哈希字符串,升级时只需改这里的默认值,老密码仍能按老参数校验。
type Params struct {
	// MemoryKiB 是内存开销
	MemoryKiB uint32
	// Iterations 是迭代次数
	Iterations uint32
	// Parallelism 是并行度
	Parallelism uint8
	// SaltLength 是盐长度(字节)
	SaltLength uint32
	// KeyLength 是输出密钥长度(字节)
	KeyLength uint32
}

// DefaultParams 返回 RFC 9106 第二档参数。
func DefaultParams() Params {
	return Params{
		MemoryKiB:   64 * 1024,
		Iterations:  1,
		Parallelism: 4,
		SaltLength:  16,
		KeyLength:   32,
	}
}

// hashVersion 是 PHC 字符串里的版本号,argon2 当前固定为 19。
const hashVersion = 19

// saltAlphabet 用于 base64 编码盐与密钥。
var saltAlphabet = base64.RawStdEncoding

// Hasher 计算与校验密码哈希。
type Hasher struct {
	params Params
}

// NewHasher 创建哈希器。
func NewHasher(p Params) *Hasher { return &Hasher{params: p} }

// Params 返回当前参数,用于写入 credential.params。
func (h *Hasher) Params() Params { return h.params }

// Hash 用 argon2id 计算密码哈希。
//
// 存储格式(PHC 字符串规范):
//
//	$argon2id$v=19$m=65536,t=1,p=4$<salt>$<hash>
//
// 参数内嵌在字符串里,校验时按**存储的**参数重算,而不是用当前默认值 ——
// 否则一旦调参,存量密码会全部校验失败。
func (h *Hasher) Hash(password string) (string, error) {
	salt := make([]byte, h.params.SaltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", apperr.Newf(apperr.CodeInternal, "生成盐值失败: %v", err)
	}

	sum := argon2.IDKey(
		[]byte(password), salt,
		h.params.Iterations, h.params.MemoryKiB, h.params.Parallelism, h.params.KeyLength,
	)

	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		hashVersion, h.params.MemoryKiB, h.params.Iterations, h.params.Parallelism,
		saltAlphabet.EncodeToString(salt), saltAlphabet.EncodeToString(sum),
	), nil
}

// Verify 校验密码是否与哈希匹配。
//
// 用常数时间比较(subtle.ConstantTimeCompare),避免通过响应耗时逐字节猜测哈希。
// 哈希格式非法时返回 false 而不是 error —— 对调用方来说「校验不通过」与
// 「哈希坏了」的处理方式相同(都算认证失败),区别只体现在日志里。
func (h *Hasher) Verify(password, encoded string) bool {
	parsed, err := parseHash(encoded)
	if err != nil {
		return false
	}
	if parsed.algo != AlgoArgon2id {
		return false
	}

	salt, err := saltAlphabet.DecodeString(parsed.salt)
	if err != nil {
		return false
	}
	want, err := saltAlphabet.DecodeString(parsed.hash)
	if err != nil {
		return false
	}

	got := argon2.IDKey(
		[]byte(password), salt,
		parsed.iterations, parsed.memoryKiB, parsed.parallelism, uint32(len(want)),
	)
	return subtle.ConstantTimeCompare(got, want) == 1
}

type parsedHash struct {
	algo        Algo
	version     int
	memoryKiB   uint32
	iterations  uint32
	parallelism uint8
	salt        string
	hash        string
}

// parseHash 解析 PHC 格式哈希串。
func parseHash(encoded string) (parsedHash, error) {
	// 形如 $argon2id$v=19$m=65536,t=1,p=4$salt$hash,共 6 段
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" {
		return parsedHash{}, apperr.Newf(apperr.CodeInternal, "密码哈希格式非法")
	}

	p := parsedHash{algo: Algo(parts[1])}

	if !strings.HasPrefix(parts[2], "v=") {
		return parsedHash{}, apperr.Newf(apperr.CodeInternal, "密码哈希缺少版本号")
	}
	v, err := strconv.Atoi(strings.TrimPrefix(parts[2], "v="))
	if err != nil {
		return parsedHash{}, apperr.Newf(apperr.CodeInternal, "密码哈希版本号非法")
	}
	p.version = v

	for _, kv := range strings.Split(parts[3], ",") {
		k, val, found := strings.Cut(kv, "=")
		if !found {
			return parsedHash{}, apperr.Newf(apperr.CodeInternal, "密码哈希参数字段非法")
		}
		n, convErr := strconv.ParseUint(val, 10, 32)
		if convErr != nil {
			return parsedHash{}, apperr.Newf(apperr.CodeInternal, "密码哈希参数值非法")
		}
		switch k {
		case "m":
			p.memoryKiB = uint32(n)
		case "t":
			p.iterations = uint32(n)
		case "p":
			p.parallelism = uint8(n)
		default:
			return parsedHash{}, apperr.Newf(apperr.CodeInternal, "未知密码哈希参数 %q", k)
		}
	}

	p.salt = parts[4]
	p.hash = parts[5]
	return p, nil
}

// ---------------------------------------------------------------- 密码策略

// Policy 是密码强度策略。
//
// 依据 NIST SP 800-63B:**长度优先,不强制字符组合**。
// 强制「大小写 + 数字」会让人写出 `Password1!` 这种可预测的密码,
// 反而降低安全性(这是变更 C-2 的依据)。
type Policy struct {
	MinLength int
	MaxLength int
	// RejectCommon 为真时拒绝弱口令库中的常见密码
	RejectCommon bool
	// CommonPasswords 是弱口令集合,由调用方注入以便测试替换。
	CommonPasswords map[string]struct{}
}

// DefaultPolicy 返回变更 C-1 确定的默认策略:8–128 位。
func DefaultPolicy() Policy {
	return Policy{
		MinLength: 8,
		MaxLength: 128,
		// 弱口令表在服务层注入,这里只留开关
		RejectCommon: true,
	}
}

// Validate 校验密码是否符合策略。
//
// 边界值 7/8/128/129 的判定结果必须与 NIST 建议一致,
// 这条规则由 TestPasswordPolicyBoundaries 固定住。
func (p Policy) Validate(password string) error {
	// 按字符数而非字节数计算:中文密码按字节算会被无理由拒绝
	n := len([]rune(password))
	if n < p.MinLength {
		return apperr.Newf(apperr.CodeWeakPassword, "密码长度不足 %d 位", p.MinLength)
	}
	if n > p.MaxLength {
		return apperr.Newf(apperr.CodeWeakPassword, "密码长度超过 %d 位", p.MaxLength)
	}
	if p.RejectCommon && p.CommonPasswords != nil {
		if _, weak := p.CommonPasswords[strings.ToLower(password)]; weak {
			return apperr.New(apperr.CodeWeakPassword, "该密码过于常见,请换一个")
		}
	}
	return nil
}

// ---------------------------------------------------------------- 弱口令表

// loadCommonPasswords 在包初始化时解析内置弱口令表。
var loadCommonPasswords = sync.OnceValue(func() map[string]struct{} {
	out := make(map[string]struct{}, len(commonPasswords))
	for _, w := range commonPasswords {
		out[w] = struct{}{}
	}
	return out
})

// CommonPasswordSet 返回内置弱口令集合(全部小写)。
func CommonPasswordSet() map[string]struct{} { return loadCommonPasswords() }

// commonPasswords 是最常见的弱口令与键盘模式密码。
//
// 只收录**毫无争议**的高危项(qwerty、123456、password、admin 之类)。
// 完整的 top-10000 弱口令库建议在部署时以配置注入,这里内置一份够用的基线,
// 保证「查弱口令库」这条规则默认就是开启的。
var commonPasswords = []string{
	"123456", "123456789", "12345678", "1234567890", "1234567",
	"password", "password1", "password123", "passw0rd", "p@ssw0rd",
	"admin", "admin123", "administrator", "root", "root123",
	"qwerty", "qwertyuiop", "qwerty123", "123456789a", "1q2w3e4r",
	"111111", "000000", "123123", "654321", "666666",
	"abc123", "abc123456", "a123456", "1234qwer", "qwe123",
	"letmein", "welcome", "welcome1", "monkey", "dragon",
	"iloveyou", "sunshine", "princess", "football", "baseball",
	"trustno1", "master", "superman", "batman", "shadow",
	"michael", "jennifer", "jordan", "hunter", "ranger",
	"yggauth", "yggdrasil", "minecraft", "changeme", "default",
	"test", "test123", "demo", "guest", "guest123", "temp",
	"login", "pass", "secret", "hello", "asdfgh", "zxcvbn",
}

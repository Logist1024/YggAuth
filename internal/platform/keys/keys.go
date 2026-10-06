// Package keys 管理签名密钥的生成、加密存储与轮换。
//
// 平台层能力,与业务无关 —— 它只管「密钥」,不关心谁在用它签名。
//
// 私钥用 AES-256-GCM 加密后入库:数据库泄露时,攻击者拿到的密文
// 无法直接使用,还需要 KEY_MASTER_SECRET(docs/security.md 6.1)。
package keys

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"io"
	"strings"

	"github.com/google/uuid"

	"github.com/yggauth/yggauth/internal/platform/apperr"
)

// Status 是密钥状态。
type Status string

// 密钥状态取值。
const (
	// StatusActive 用于**新签名**。同一时刻只能有一个 active
	StatusActive Status = "active"
	// StatusRetired 不再新签名,但仍保留用于验签历史令牌
	StatusRetired Status = "retired"
	// StatusRevoked 彻底作废
	StatusRevoked Status = "revoked"
)

// Algo 是签名算法。
const (
	// AlgoRS256 是 RSASSA-PKCS1-v1_5 + SHA-256。OIDC 客户端本地验签的默认选择
	AlgoRS256 = "RS256"
)

// KeyPair 是一对签名密钥的明文形态。
type KeyPair struct {
	// Kid 是密钥标识,会出现在 JWT header 里
	Kid string
	// Algo 是签名算法
	Algo string
	// Private 是 PKCS#8 PEM 编码的私钥
	Private *rsa.PrivateKey
}

// PublicKey 返回公钥。
func (k KeyPair) PublicKey() *rsa.PublicKey { return &k.Private.PublicKey }

// PublicPEM 返回 PKIX PEM 编码的公钥。
func (k KeyPair) PublicPEM() ([]byte, error) {
	der, err := x509.MarshalPKIXPublicKey(&k.Private.PublicKey)
	if err != nil {
		return nil, apperr.Newf(apperr.CodeInternal, "编码公钥失败: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), nil
}

// PrivatePEM 返回 PKCS#8 PEM 编码的私钥。
func (k KeyPair) PrivatePEM() ([]byte, error) {
	der, err := x509.MarshalPKCS8PrivateKey(k.Private)
	if err != nil {
		return nil, apperr.Newf(apperr.CodeInternal, "编码私钥失败: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), nil
}

// Store 存取加密后的密钥。
type Store interface {
	// Active 返回当前用于新签名的密钥
	Active(ctx context.Context) (Record, error)
	// Get 按 kid 取密钥(含已退役的,用于验签历史令牌)
	Get(ctx context.Context, kid string) (Record, error)
	// List 列出全部密钥
	List(ctx context.Context) ([]Record, error)
	// Save 写入新密钥并退役旧的
	Save(ctx context.Context, rec Record) error
}

// Record 是存储形态的一条密钥。
type Record struct {
	ID   uuid.UUID
	Kid  string
	Algo string
	// PublicPEM 是明文公钥,需要公开
	PublicPEM []byte
	// PrivateEnc 是 AES-256-GCM 加密后的私钥
	PrivateEnc []byte
	Status     Status
}

// Manager 负责密钥的生成、加密与解密。
type Manager struct {
	masterKey []byte
	store     Store
	kidPrefix string
	// bitSize 是 RSA 密钥长度。OIDC 客户端验签安全基线是 2048 位。
	bitSize int
}

// Options 是 Manager 的构造参数。
type Options struct {
	// MasterSecret 是 KEY_MASTER_SECRET。短于 32 字节会被 SHA-256 派生成 32 字节。
	MasterSecret string
	// 让 JWKS 里出现某个前缀的 kid 时,能一眼看出它属于哪个域。
	KidPrefix string
	// BitSize 是 RSA 长度
	BitSize int
}

// NewManager 创建密钥管理器。
func NewManager(opts Options, store Store) (*Manager, error) {
	if opts.MasterSecret == "" {
		return nil, apperr.New(apperr.CodeInternal, "主密钥未设置,无法初始化密钥管理器")
	}
	if opts.KidPrefix == "" {
		opts.KidPrefix = "key"
	}
	if opts.BitSize == 0 {
		opts.BitSize = 2048
	}

	// 统一派生成 32 字节:AES-256 的密钥长度要求是硬约束,
	// 而用户给的可能是任意长度的口令。
	sum := sha256.Sum256([]byte(opts.MasterSecret))
	return &Manager{
		masterKey: sum[:],
		store:     store,
		kidPrefix: opts.KidPrefix,
		bitSize:   opts.BitSize,
	}, nil
}

// Ensure 保证至少有一个 active 密钥,没有就生成一对。
//
// 服务启动时调用:首次部署自动生成,之后直接复用库里的。
func (m *Manager) Ensure(ctx context.Context) (Record, error) {
	rec, err := m.store.Active(ctx)
	if err == nil {
		return rec, nil
	}
	if !apperr.Is(err, apperr.CodeNotFound) {
		return Record{}, err
	}
	return m.Rotate(ctx)
}

// Rotate 生成新密钥并退役旧的。
//
// 轮换**不删除**旧密钥:已经发出去的令牌还在用旧 kid 签名,
// 删掉会让所有存量令牌立刻验签失败。
func (m *Manager) Rotate(ctx context.Context) (Record, error) {
	kid, err := m.newKid()
	if err != nil {
		return Record{}, err
	}

	priv, err := rsa.GenerateKey(rand.Reader, m.bitSize)
	if err != nil {
		return Record{}, apperr.Newf(apperr.CodeInternal, "生成 RSA 密钥失败: %v", err)
	}
	pair := KeyPair{Kid: kid, Algo: AlgoRS256, Private: priv}

	pubPEM, err := pair.PublicPEM()
	if err != nil {
		return Record{}, err
	}
	privPEM, err := pair.PrivatePEM()
	if err != nil {
		return Record{}, err
	}
	enc, err := m.Seal(privPEM)
	if err != nil {
		return Record{}, err
	}

	rec := Record{
		ID:         uuid.New(),
		Kid:        kid,
		Algo:       AlgoRS256,
		PublicPEM:  pubPEM,
		PrivateEnc: enc,
		Status:     StatusActive,
	}
	if err := m.store.Save(ctx, rec); err != nil {
		return Record{}, err
	}
	return rec, nil
}

// Decrypt 解出私钥。
func (m *Manager) Decrypt(enc []byte) (*rsa.PrivateKey, error) {
	plain, err := m.Unseal(enc)
	if err != nil {
		// 常见原因:KEY_MASTER_SECRET 改过。报明确错误而不是通用 500,
		// 否则排查时会以为是数据库问题。
		return nil, apperr.New(apperr.CodeInternal,
			"私钥解密失败,通常是 KEY_MASTER_SECRET 与入库时不一致")
	}

	block, _ := pem.Decode(plain)
	if block == nil || block.Type != "PRIVATE KEY" {
		return nil, apperr.New(apperr.CodeInternal, "私钥格式非法")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, apperr.Newf(apperr.CodeInternal, "解析私钥失败: %v", err)
	}
	rsaKey, ok := key.(*rsa.PrivateKey)
	if !ok {
		return nil, apperr.New(apperr.CodeInternal, "私钥类型非法")
	}
	return rsaKey, nil
}

// Seal 用主密钥加密任意数据(AES-256-GCM,nonce 前置)。
//
// 签名私钥与配置表里的敏感值共用这一个入口:同一把 KEY_MASTER_SECRET、
// 同一段实现,少一处就少一种「这个是加密的吗」的疑惑。
func (m *Manager) Seal(plain []byte) ([]byte, error) {
	aead := m.aead()
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, apperr.Newf(apperr.CodeInternal, "生成随机数失败: %v", err)
	}
	return aead.Seal(nonce, nonce, plain, nil), nil
}

// Unseal 解开 Seal 的产物。
//
// 解不开最常见的原因是 KEY_MASTER_SECRET 与入库时不一致 ——
// 错误信息必须把这句写上,否则排查方向会跑偏到数据库去。
func (m *Manager) Unseal(enc []byte) ([]byte, error) {
	plain, err := m.aead().Open(nil, nonceOf(enc), payloadOf(enc), nil)
	if err != nil {
		return nil, apperr.New(apperr.CodeInternal,
			"解密失败,通常是 KEY_MASTER_SECRET 与入库时不一致")
	}
	return plain, nil
}

func (m *Manager) aead() cipher.AEAD {
	block, err := aes.NewCipher(m.masterKey)
	if err != nil {
		// masterKey 恒为 32 字节,这里不可能失败
		panic("keys: AES 密钥长度非法: " + err.Error())
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		panic("keys: 创建 GCM 失败: " + err.Error())
	}
	return aead
}

func (m *Manager) newKid() (string, error) {
	kid, err := uuid.NewRandom()
	if err != nil {
		return "", apperr.Newf(apperr.CodeInternal, "生成 kid 失败: %v", err)
	}
	raw := base64.RawURLEncoding.EncodeToString(kid[:])
	return m.kidPrefix + "-" + strings.ToLower(raw), nil
}

func nonceOf(data []byte) []byte {
	const nonceSize = 12 // GCM 标准 nonce 长度
	if len(data) < nonceSize {
		return data
	}
	return data[:nonceSize]
}

func payloadOf(data []byte) []byte {
	const nonceSize = 12
	if len(data) < nonceSize {
		return nil
	}
	return data[nonceSize:]
}

// List 返回全部密钥记录。
func (m *Manager) List(ctx context.Context) ([]Record, error) {
	return m.store.List(ctx)
}

// PublicKey 解析出 RSA 公钥。
//
// JWKS 需要的是密钥对象而不是 PEM 文本 —— 标准客户端会按 JWK 的 n/e
// 字段验签,给它一串 PEM 它认不出来。
func (r Record) PublicKey() (*rsa.PublicKey, error) {
	block, _ := pem.Decode(r.PublicPEM)
	if block == nil {
		return nil, apperr.Newf(apperr.CodeInternal, "公钥 %s 不是合法的 PEM", r.Kid)
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, apperr.Newf(apperr.CodeInternal, "解析公钥 %s 失败: %v", r.Kid, err)
	}
	pub, ok := parsed.(*rsa.PublicKey)
	if !ok {
		return nil, apperr.Newf(apperr.CodeInternal, "公钥 %s 不是 RSA 类型", r.Kid)
	}
	return pub, nil
}

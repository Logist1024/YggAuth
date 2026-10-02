package minecraft

import (
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"

	"github.com/go-jose/go-jose/v3"
	"github.com/jackc/pgx/v5"

	"github.com/yggauth/yggauth/internal/platform/apperr"
	"github.com/yggauth/yggauth/internal/platform/db"
	"github.com/yggauth/yggauth/internal/platform/db/query"
	"github.com/yggauth/yggauth/internal/platform/keys"
)

// MCKeyStore 是 MC 域签名密钥的 PostgreSQL 存储。
//
// 与 OIDC 的 oidc.signing_key 是**两张表、两套密钥、两个 kid 命名空间**。
// MC 域出问题时 OIDC 必须照常签发令牌,反过来也一样 —— 隔离是 ADR-003
// 的硬要求,不是风格偏好。
type MCKeyStore struct {
	queries *query.Queries
	pool    *db.Pool
}

// NewMCKeyStore 创建存储。
func NewMCKeyStore(pool *db.Pool) *MCKeyStore {
	return &MCKeyStore{queries: query.New(pool), pool: pool}
}

// 确保满足 keys.Store。
var _ keys.Store = (*MCKeyStore)(nil)

// Active 返回当前用于新签名的密钥。
func (s *MCKeyStore) Active(ctx context.Context) (keys.Record, error) {
	row, err := s.queries.GetActiveMCSigningKey(ctx)
	if err != nil {
		if db.IsNoRows(err) {
			return keys.Record{}, apperr.ErrNotFound
		}
		return keys.Record{}, apperr.Newf(apperr.CodeInternal, "读取 MC 签名密钥失败: %v", err)
	}
	return mcRecordFromRow(row), nil
}

// Get 按 kid 取密钥。
func (s *MCKeyStore) Get(ctx context.Context, kid string) (keys.Record, error) {
	row, err := s.queries.GetMCSigningKey(ctx, kid)
	if err != nil {
		if db.IsNoRows(err) {
			return keys.Record{}, apperr.ErrNotFound
		}
		return keys.Record{}, apperr.Newf(apperr.CodeInternal, "读取 MC 签名密钥失败: %v", err)
	}
	return mcRecordFromRow(row), nil
}

// List 列出全部密钥。
func (s *MCKeyStore) List(ctx context.Context) ([]keys.Record, error) {
	rows, err := s.queries.ListMCSigningKeys(ctx)
	if err != nil {
		return nil, apperr.Newf(apperr.CodeInternal, "查询 MC 签名密钥失败: %v", err)
	}
	out := make([]keys.Record, 0, len(rows))
	for _, row := range rows {
		out = append(out, mcRecordFromRow(row))
	}
	return out, nil
}

// Save 写入新密钥并退役旧的。
//
// 拆成两条语句而不是一条 CTE:status 列上有部分唯一索引
// idx_...,而 PostgreSQL 在同一条语句里看不见数据修改 CTE 对该索引的效果,
// 合成一条必然撞 duplicate key(oidc 侧踩过同样的坑)。
func (s *MCKeyStore) Save(ctx context.Context, rec keys.Record) error {
	jwkBytes, err := mcPEMToJWK(rec.Kid, rec.Algo, rec.PublicPEM)
	if err != nil {
		return err
	}

	return db.InTx(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		q := s.queries.WithTx(tx)
		if err := q.RetireActiveMCSigningKey(ctx); err != nil {
			return apperr.Newf(apperr.CodeInternal, "退役旧 MC 签名密钥失败: %v", err)
		}
		if _, err := q.CreateMCSigningKey(ctx, query.CreateMCSigningKeyParams{
			Kid:           rec.Kid,
			Algo:          rec.Algo,
			PublicJwk:     jwkBytes,
			PrivateJwkEnc: rec.PrivateEnc,
		}); err != nil {
			return apperr.Newf(apperr.CodeInternal, "保存 MC 签名密钥失败: %v", err)
		}
		return nil
	})
}

func mcRecordFromRow(row query.MinecraftSigningKey) keys.Record {
	return keys.Record{
		ID:         row.ID,
		Kid:        row.Kid,
		Algo:       row.Algo,
		PublicPEM:  mcJWKToPEM(row.PublicJwk),
		PrivateEnc: row.PrivateJwkEnc,
		Status:     keys.Status(row.Status),
	}
}

// mcJWKToPEM 从库里存的 JWK 还原出 PEM 公钥。
//
// 坏数据返回 nil:调用方据此跳过这把密钥,而不是让整个 metadata 端点 500。
func mcJWKToPEM(raw []byte) []byte {
	var jwk jose.JSONWebKey
	if err := json.Unmarshal(raw, &jwk); err != nil {
		return nil
	}
	pub, ok := jwk.Key.(*rsa.PublicKey)
	if !ok {
		return nil
	}
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return nil
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})
}

// mcPEMToJWK 把内存里的 PEM 转成库里的 JWK 对象。
func mcPEMToJWK(kid, algo string, publicPEM []byte) ([]byte, error) {
	block, _ := pem.Decode(publicPEM)
	if block == nil {
		return nil, apperr.Newf(apperr.CodeInternal, "公钥 %s 不是合法的 PEM", kid)
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, apperr.Newf(apperr.CodeInternal, "解析公钥 %s 失败: %v", kid, err)
	}
	return json.Marshal(jose.JSONWebKey{
		Key:       parsed,
		KeyID:     kid,
		Algorithm: algo,
		Use:       "sig",
	})
}

// PublicKeyPEM 返回当前 active 密钥的 PEM 文本。
//
// Yggdrasil metadata 里的 signaturePublickey 要的是 PEM 而不是 JWK ——
// authlib-injector 读不懂 JWK,拿不到就拒绝启动。
func PublicKeyPEM(m *keys.Manager) func() string {
	return func() string {
		rec, err := m.Ensure(context.Background())
		if err != nil {
			return ""
		}
		pub, err := rec.PublicKey()
		if err != nil {
			return ""
		}
		der, err := x509.MarshalPKIXPublicKey(pub)
		if err != nil {
			return ""
		}
		return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
	}
}

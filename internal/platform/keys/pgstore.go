package keys

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
)

// PGStore 是密钥的 PostgreSQL 存储。
//
// 私钥以 AES-256-GCM 密文落库(Manager 负责加解密),公钥明文存 ——
// JWKS 本来就要公开它。
type PGStore struct {
	queries *query.Queries
	poolRef *db.Pool
}

// NewPGStore 创建存储。
func NewPGStore(pool *db.Pool) *PGStore {
	return &PGStore{queries: query.New(pool), poolRef: pool}
}

// 确保 PGStore 满足 Store。
var _ Store = (*PGStore)(nil)

// Active 返回当前用于新签名的密钥。
func (s *PGStore) Active(ctx context.Context) (Record, error) {
	row, err := s.queries.GetActiveSigningKey(ctx)
	if err != nil {
		if db.IsNoRows(err) {
			return Record{}, apperr.ErrNotFound
		}
		return Record{}, apperr.Newf(apperr.CodeInternal, "读取签名密钥失败: %v", err)
	}
	return recordFromRow(row), nil
}

// Get 按 kid 取密钥。
func (s *PGStore) Get(ctx context.Context, kid string) (Record, error) {
	row, err := s.queries.GetSigningKey(ctx, kid)
	if err != nil {
		if db.IsNoRows(err) {
			return Record{}, apperr.ErrNotFound
		}
		return Record{}, apperr.Newf(apperr.CodeInternal, "读取签名密钥失败: %v", err)
	}
	return recordFromRow(row), nil
}

// List 列出全部密钥。
func (s *PGStore) List(ctx context.Context) ([]Record, error) {
	rows, err := s.queries.ListSigningKeys(ctx)
	if err != nil {
		return nil, apperr.Newf(apperr.CodeInternal, "查询签名密钥失败: %v", err)
	}

	out := make([]Record, 0, len(rows))
	for _, row := range rows {
		out = append(out, recordFromRow(row))
	}
	return out, nil
}

// Save 写入新密钥并退役旧的。
//
// 「先退役旧的、再插入新的」必须原子完成 —— 否则中途崩溃会留下两个
// active 密钥,签出去的令牌 kid 就对不上验签端了。
func (s *PGStore) Save(ctx context.Context, rec Record) error {
	jwkBytes, err := pemToJWK(rec.Kid, rec.Algo, rec.PublicPEM)
	if err != nil {
		return err
	}

	return db.InTx(ctx, s.poolRef, pgx.TxOptions{}, func(tx pgx.Tx) error {
		q := s.queries.WithTx(tx)
		if err := q.RetireActiveSigningKey(ctx); err != nil {
			return apperr.Newf(apperr.CodeInternal, "退役旧签名密钥失败: %v", err)
		}
		if _, err := q.CreateSigningKey(ctx, query.CreateSigningKeyParams{
			Kid:           rec.Kid,
			Algo:          rec.Algo,
			PublicJwk:     jwkBytes,
			PrivateJwkEnc: rec.PrivateEnc,
		}); err != nil {
			return apperr.Newf(apperr.CodeInternal, "保存签名密钥失败: %v", err)
		}
		return nil
	})
}

func recordFromRow(row query.OidcSigningKey) Record {
	return Record{
		ID:         row.ID,
		Kid:        row.Kid,
		Algo:       row.Algo,
		PublicPEM:  jwkToPEM(row.PublicJwk),
		PrivateEnc: row.PrivateJwkEnc,
		Status:     Status(row.Status),
	}
}

// jwkToPEM 从库里存的 JWK 对象还原出 PEM 公钥。
//
// 列是 JSONB 而不是 BYTEA:JWKS 端点直接吃 JWK,存成 JSON 能省掉一次
// 转换,运维用 psql 也能直接读懂公钥长什么样。
//
// 坏数据返回 nil:调用方(PublicKey)会据此跳过这把密钥,
// 而不是让整个 JWKS 端点 500。
func jwkToPEM(raw []byte) []byte {
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

// pemToJWK 把内存里的 PEM 转成库里的 JWK 对象。
func pemToJWK(kid, algo string, publicPEM []byte) ([]byte, error) {
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

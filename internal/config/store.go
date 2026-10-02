package config

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/yggauth/yggauth/internal/platform/apperr"
	"github.com/yggauth/yggauth/internal/platform/db"
	"github.com/yggauth/yggauth/internal/platform/db/query"
)

// SettingStore 读写运行时可改的应用配置。
//
// app.setting 的优先级**高于**环境变量:后台调整后无需重启即可生效
// (docs/08-deployment.md 5.3)。环境变量负责提供首次启动的默认值,
// 迁移已经把同样的默认值写进了表里。
type SettingStore struct {
	q *query.Queries
}

// NewSettingStore 创建配置存储。
func NewSettingStore(pool *db.Pool) *SettingStore {
	return &SettingStore{q: query.New(pool)}
}

// Setting 是一条应用配置。
type Setting struct {
	Key       string          `json:"key"`
	Value     json.RawMessage `json:"value"`
	UpdatedAt string          `json:"updated_at"`
}

// Get 取单个配置项。
func (s *SettingStore) Get(ctx context.Context, key string) (Setting, error) {
	row, err := s.q.GetSetting(ctx, key)
	if err != nil {
		if db.IsNoRows(err) {
			return Setting{}, apperr.ErrNotFound
		}
		return Setting{}, apperr.Newf(apperr.CodeInternal, "读取配置失败: %v", err)
	}
	return Setting{
		Key:       row.Key,
		Value:     json.RawMessage(row.Value),
		UpdatedAt: row.UpdatedAt.UTC().Format(timeFormat),
	}, nil
}

// List 列出全部配置项。
func (s *SettingStore) List(ctx context.Context) ([]Setting, error) {
	rows, err := s.q.ListSettings(ctx)
	if err != nil {
		return nil, apperr.Newf(apperr.CodeInternal, "读取配置失败: %v", err)
	}
	out := make([]Setting, 0, len(rows))
	for _, row := range rows {
		out = append(out, Setting{
			Key:       row.Key,
			Value:     json.RawMessage(row.Value),
			UpdatedAt: row.UpdatedAt.UTC().Format(timeFormat),
		})
	}
	return out, nil
}

// Upsert 写入配置项。
func (s *SettingStore) Upsert(ctx context.Context, key string, value json.RawMessage, updatedBy *uuid.UUID) (Setting, error) {
	if !json.Valid(value) {
		return Setting{}, apperr.New(apperr.CodeInvalidArgument, "配置值必须是合法 JSON")
	}
	row, err := s.q.UpsertSetting(ctx, query.UpsertSettingParams{
		Key:       key,
		Value:     []byte(value),
		UpdatedBy: toUUID(updatedBy),
	})
	if err != nil {
		return Setting{}, apperr.Newf(apperr.CodeInternal, "写入配置失败: %v", err)
	}
	return Setting{
		Key:       row.Key,
		Value:     json.RawMessage(row.Value),
		UpdatedAt: row.UpdatedAt.UTC().Format(timeFormat),
	}, nil
}

// GetString 取字符串型配置项。
func (s *SettingStore) GetString(ctx context.Context, key, fallback string) string {
	row, err := s.q.GetSetting(ctx, key)
	if err != nil {
		return fallback
	}
	var v string
	if err := json.Unmarshal(row.Value, &v); err != nil {
		return fallback
	}
	return v
}

// GetInt 取整型配置项。
func (s *SettingStore) GetInt(ctx context.Context, key string, fallback int) int {
	row, err := s.q.GetSetting(ctx, key)
	if err != nil {
		return fallback
	}
	var v int
	if err := json.Unmarshal(row.Value, &v); err != nil {
		return fallback
	}
	return v
}

// GetBool 取布尔型配置项。
func (s *SettingStore) GetBool(ctx context.Context, key string, fallback bool) bool {
	row, err := s.q.GetSetting(ctx, key)
	if err != nil {
		return fallback
	}
	var v bool
	if err := json.Unmarshal(row.Value, &v); err != nil {
		return fallback
	}
	return v
}

func toUUID(id *uuid.UUID) pgtype.UUID {
	if id == nil || *id == uuid.Nil {
		return pgtype.UUID{}
	}
	return pgtype.UUID{Bytes: *id, Valid: true}
}

// timeFormat 是配置项 updated_at 的输出格式。
const timeFormat = "2006-01-02T15:04:05Z07:00"

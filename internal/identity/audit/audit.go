// Package audit 记录与检索审计事件。
//
// 审计表只追加:本包不提供任何删除接口(docs/09-security.md 9.2)。
// 写失败不阻断业务 —— 可靠性优先于完整性,但两者都要尽力。
package audit

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"net/netip"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/yggauth/yggauth/internal/domain"
	"github.com/yggauth/yggauth/internal/identity/account"
	"github.com/yggauth/yggauth/internal/platform/apperr"
	"github.com/yggauth/yggauth/internal/platform/db"
	"github.com/yggauth/yggauth/internal/platform/db/query"
)

// Event 是一条审计事件。
type Event = account.Event

// Record 写入一条审计事件。
type Record func(ctx context.Context, ev Event) error

// Filter 是审计检索条件。
type Filter struct {
	AccountID  *uuid.UUID
	Action     string
	Outcome    string
	TargetType string
	TargetID   string
	From       *time.Time
	To         *time.Time
	Limit      int32
	Offset     int32
}

// Repository 是审计的数据访问接口。
type Repository interface {
	Insert(ctx context.Context, ev Event) error
	Search(ctx context.Context, f Filter) ([]Entry, int64, error)
	Export(ctx context.Context, f Filter) ([]Entry, error)
	CountBefore(ctx context.Context, t time.Time) (int64, error)
}

// Entry 是一条审计记录。
//
// json tag 必须显式写出来:这个结构体是直接序列化给 /api/admin/audit 的。
// 少了 tag,Go 会按字段名导出成 PascalCase(OccurredAt/Actor/…),
// 而前端按 snake_case 读,结果是每一行都渲染成「Invalid Date + 空单元格」——
// 不报错、不 404,只是安静地显示成一片空白,最难查的一类问题。
//
// Metadata 不导出:它是 []byte,序列化出来是一串无意义的 base64,
// 前端也没有任何地方消费它。
type Entry struct {
	ID         int64      `json:"id"`
	OccurredAt time.Time  `json:"occurred_at"`
	AccountID  *uuid.UUID `json:"account_id,omitempty"`
	Actor      string     `json:"actor"`
	Action     string     `json:"action"`
	TargetType string     `json:"target_type"`
	TargetID   string     `json:"target_id"`
	Outcome    string     `json:"outcome"`
	IP         string     `json:"ip"`
	UserAgent  string     `json:"user_agent"`
	Metadata   []byte     `json:"-"`
}

// Service 是审计业务逻辑。
type Service struct {
	repo   Repository
	logger Logger
}

// Logger 是日志器最小接口。
type Logger interface {
	Warn(msg string, args ...any)
	Error(msg string, args ...any)
}

// NewService 创建审计服务。
func NewService(repo Repository, logger Logger) *Service {
	return &Service{repo: repo, logger: logger}
}

// Record 实现 account.Auditor。
//
// 审计写失败只记日志不返回错误:登录这类关键路径不能因为审计表写不进去就失败。
func (s *Service) Record(ctx context.Context, ev Event) error {
	if err := s.repo.Insert(ctx, ev); err != nil {
		s.logger.Error("audit write failed", "action", ev.Action, "error", err)
	}
	return nil
}

// Search 检索审计事件。
func (s *Service) Search(ctx context.Context, f Filter) ([]Entry, int64, error) {
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 50
	}
	if f.Offset < 0 {
		f.Offset = 0
	}
	return s.repo.Search(ctx, f)
}

// WriteCSV 把审计事件写成 CSV。
//
// 元数据字段序列化成紧凑 JSON 放进一个单元格,避免逗号把列冲散。
func (s *Service) WriteCSV(w io.Writer, entries []Entry) error {
	cw := csv.NewWriter(w)
	defer cw.Flush()

	header := []string{
		"id", "occurred_at", "account_id", "actor", "action",
		"target_type", "target_id", "outcome", "ip", "user_agent", "metadata",
	}
	if err := cw.Write(header); err != nil {
		return apperr.Newf(apperr.CodeInternal, "写入 CSV 表头失败: %v", err)
	}

	for _, e := range entries {
		accID := ""
		if e.AccountID != nil {
			accID = e.AccountID.String()
		}
		row := []string{
			fmt.Sprintf("%d", e.ID),
			e.OccurredAt.UTC().Format(time.RFC3339),
			accID,
			e.Actor,
			e.Action,
			e.TargetType,
			e.TargetID,
			e.Outcome,
			e.IP,
			e.UserAgent,
			string(e.Metadata),
		}
		if err := cw.Write(row); err != nil {
			return apperr.Newf(apperr.CodeInternal, "写入 CSV 行失败: %v", err)
		}
	}
	cw.Flush()
	if err := cw.Error(); err != nil {
		return apperr.Newf(apperr.CodeInternal, "CSV 写入失败: %v", err)
	}
	return nil
}

// CountBefore 统计某时间点之前的事件数,供归档任务使用。
func (s *Service) CountBefore(ctx context.Context, t time.Time) (int64, error) {
	return s.repo.CountBefore(ctx, t)
}

// ---------------------------------------------------------------- PG 实现

// PgRepository 是基于 sqlc 的审计仓储。
type PgRepository struct{ q *query.Queries }

// NewPgRepository 创建审计仓储。
func NewPgRepository(pool *db.Pool) *PgRepository { return &PgRepository{q: query.New(pool)} }

// Insert 写入一条审计事件。
func (r *PgRepository) Insert(ctx context.Context, ev Event) error {
	metadata, mErr := json.Marshal(ev.Metadata)
	if mErr != nil {
		metadata = []byte(`{}`)
	}
	if len(metadata) == 0 {
		metadata = []byte(`{}`)
	}
	if _, err := r.q.InsertAuditEvent(ctx, query.InsertAuditEventParams{
		AccountID:  pgUUID(ev.AccountID),
		Actor:      string(ev.Actor),
		Action:     ev.Action,
		TargetType: pgText(ev.TargetType),
		TargetID:   pgText(ev.TargetID),
		Outcome:    string(ev.Outcome),
		Ip:         toInet(ev.IP),
		UserAgent:  pgText(ev.UserAgent),
		Metadata:   metadata,
	}); err != nil {
		return apperr.Newf(apperr.CodeInternal, "写入审计事件失败: %v", err)
	}
	return nil
}

func filterParams(f Filter) query.SearchAuditEventsParams {
	return query.SearchAuditEventsParams{
		AccountID:  pgUUID(f.AccountID),
		Action:     pgText(f.Action),
		Outcome:    pgText(f.Outcome),
		From:       pgTime(f.From),
		To:         pgTime(f.To),
		TargetType: pgText(f.TargetType),
		TargetID:   pgText(f.TargetID),
		Limit:      f.Limit,
		Offset:     f.Offset,
	}
}

// Search 检索审计事件。
func (r *PgRepository) Search(ctx context.Context, f Filter) ([]Entry, int64, error) {
	rows, err := r.q.SearchAuditEvents(ctx, filterParams(f))
	if err != nil {
		return nil, 0, apperr.Newf(apperr.CodeInternal, "检索审计事件失败: %v", err)
	}
	total, err := r.q.CountAuditEvents(ctx, countParams(f))
	if err != nil {
		return nil, 0, apperr.Newf(apperr.CodeInternal, "统计审计事件失败: %v", err)
	}
	return toEntries(rows), total, nil
}

// Export 导出审计事件(不分页,由调用方按时间窗控制规模)。
func (r *PgRepository) Export(ctx context.Context, f Filter) ([]Entry, error) {
	if f.Limit <= 0 {
		f.Limit = 10000
	}
	rows, err := r.q.ExportAuditEvents(ctx, query.ExportAuditEventsParams{
		From:   pgTime(f.From),
		To:     pgTime(f.To),
		Limit:  f.Limit,
		Offset: f.Offset,
	})
	if err != nil {
		return nil, apperr.Newf(apperr.CodeInternal, "导出审计事件失败: %v", err)
	}
	return toEntries(rows), nil
}

// CountBefore 统计某时间点之前的事件数。
func (r *PgRepository) CountBefore(ctx context.Context, t time.Time) (int64, error) {
	n, err := r.q.CountAuditEventsBefore(ctx, t)
	if err != nil {
		return 0, apperr.Newf(apperr.CodeInternal, "统计审计事件失败: %v", err)
	}
	return n, nil
}

func toEntries(rows []query.IdentityAuditEvent) []Entry {
	out := make([]Entry, 0, len(rows))
	for _, row := range rows {
		e := Entry{
			ID:         row.ID,
			OccurredAt: row.OccurredAt,
			Actor:      row.Actor,
			Action:     row.Action,
			TargetType: row.TargetType.String,
			TargetID:   row.TargetID.String,
			Outcome:    row.Outcome,
			UserAgent:  row.UserAgent.String,
			Metadata:   row.Metadata,
		}
		if row.AccountID.Valid {
			if id, err := uuid.FromBytes(row.AccountID.Bytes[:]); err == nil {
				e.AccountID = &id
			}
		}
		if row.Ip != nil {
			e.IP = row.Ip.String()
		}
		out = append(out, e)
	}
	return out
}

// 确保与账号域的类型保持一致。
var _ domain.AuditOutcome = domain.OutcomeSuccess

func pgText(s string) pgtype.Text {
	if s == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: s, Valid: true}
}

func pgUUID(id *uuid.UUID) pgtype.UUID {
	if id == nil || *id == uuid.Nil {
		return pgtype.UUID{}
	}
	return pgtype.UUID{Bytes: *id, Valid: true}
}

func pgTime(t *time.Time) pgtype.Timestamptz {
	if t == nil {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: *t, Valid: true}
}

func toInet(ip string) *netip.Addr {
	if ip == "" {
		return nil
	}
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return nil
	}
	return &addr
}

// countParams 从检索参数里取过滤条件部分。
func countParams(f Filter) query.CountAuditEventsParams {
	p := filterParams(f)
	return query.CountAuditEventsParams{
		AccountID:  p.AccountID,
		Action:     p.Action,
		Outcome:    p.Outcome,
		From:       p.From,
		To:         p.To,
		TargetType: p.TargetType,
		TargetID:   p.TargetID,
	}
}

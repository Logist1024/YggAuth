package admin

import (
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/yggauth/yggauth/internal/platform/apperr"
	"github.com/yggauth/yggauth/internal/platform/httpx"
)

// ---------------------------------------------------------------- Minecraft 档案与材质
//
// 后台侧的**只读列表**。这两页此前是「功能暂未开放」的占位,而
// docs/05-api.md 早就把 GET /api/admin/mc/* 写进了接口表 ——
// 文档承诺了、后端没给,前端也就只能先不发请求。
// 这里补上两个 GET;改名/封禁与材质删除仍未实现,文档里已标注。

// profileWhere 是玩家档案的筛选条件。
//
// 用子串匹配而不是 LIKE 通配:后台要找的往往是名字的一部分,
// 而用户输入里的 %、_ 不该被解释成「任意字符」。
const profileWhere = `WHERE ($1 = '' OR strpos(lower(p.current_name), lower($1)) > 0)`

const profileListSQL = `
SELECT p.id, p.uuid, p.current_name, p.created_at,
       EXISTS(SELECT 1 FROM minecraft.profile_texture pt
              WHERE pt.profile_id = p.id AND pt.type = 'skin'),
       EXISTS(SELECT 1 FROM minecraft.profile_texture pt
              WHERE pt.profile_id = p.id AND pt.type = 'cape')
FROM minecraft.profile p
` + profileWhere + `
ORDER BY p.created_at DESC, p.id`

// ListMCProfiles 列出玩家档案。
//
// 参数与账号列表一致(limit/offset/search),前端的分页与搜索组件
// 可以原样复用 —— 后台两个列表长得一样,比各写一套分页逻辑省心。
func (h *Handler) ListMCProfiles(w http.ResponseWriter, r *http.Request) {
	search := strings.TrimSpace(r.URL.Query().Get("search"))
	limit := limitParam(r, 20)
	if limit > 100 {
		// 拖全表没有意义:一次最多一页。也省得 limit=1000000
		// 把「查询参数」变成「内存压力」。
		limit = 100
	}
	offset := intParam(r, "offset", 0)

	ctx := r.Context()

	var total int
	if err := h.deps.DB.QueryRow(ctx,
		`SELECT count(*) FROM minecraft.profile p `+profileWhere,
		search).Scan(&total); err != nil {
		h.failInternal(w, "查询玩家档案总数失败", err)
		return
	}

	rows, err := h.deps.DB.Query(ctx,
		profileListSQL+"\nLIMIT $2 OFFSET $3", search, limit, offset)
	if err != nil {
		h.failInternal(w, "查询玩家档案失败", err)
		return
	}
	defer rows.Close()

	out := make([]map[string]any, 0)
	for rows.Next() {
		var (
			id, mcUUID uuid.UUID
			name       string
			created    time.Time
			hasSkin    bool
			hasCape    bool
		)
		if err := rows.Scan(&id, &mcUUID, &name, &created, &hasSkin, &hasCape); err != nil {
			h.failInternal(w, "读取玩家档案失败", err)
			return
		}
		out = append(out, map[string]any{
			"id":           id.String(),
			"uuid":         mcUUID.String(),
			"current_name": name,
			"created_at":   created.UTC().Format(time.RFC3339),
			"has_skin":     hasSkin,
			"has_cape":     hasCape,
		})
	}
	if err := rows.Err(); err != nil {
		h.failInternal(w, "读取玩家档案失败", err)
		return
	}

	httpx.OK(w, map[string]any{"profiles": out, "total": total})
}

const textureListSQL = `
SELECT t.id, t.hash, t.type, t.size, t.width, t.height, t.ref_count, t.created_at
FROM minecraft.texture t
WHERE ($1 = '' OR t.type = $1)
ORDER BY t.created_at DESC, t.id
LIMIT $2 OFFSET $3`

// ListMCTextures 列出材质。
//
// kind 只认 skin/cape:静默忽略一个拼错的过滤条件会让运维对着
// 「为什么结果没变」发呆,直接说清楚更省事。
func (h *Handler) ListMCTextures(w http.ResponseWriter, r *http.Request) {
	kind := r.URL.Query().Get("kind")
	if kind != "" && kind != "skin" && kind != "cape" {
		httpx.Fail(w, apperr.Newf(apperr.CodeInvalidArgument,
			"kind 只能是 skin 或 cape,收到: %s", kind))
		return
	}

	limit := limitParam(r, 20)
	if limit > 100 {
		limit = 100
	}
	offset := intParam(r, "offset", 0)

	ctx := r.Context()

	var total int
	if err := h.deps.DB.QueryRow(ctx,
		`SELECT count(*) FROM minecraft.texture t WHERE ($1 = '' OR t.type = $1)`,
		kind).Scan(&total); err != nil {
		h.failInternal(w, "查询材质总数失败", err)
		return
	}

	rows, err := h.deps.DB.Query(ctx, textureListSQL, kind, limit, offset)
	if err != nil {
		h.failInternal(w, "查询材质失败", err)
		return
	}
	defer rows.Close()

	out := make([]map[string]any, 0)
	for rows.Next() {
		var (
			id        uuid.UUID
			hash      string
			kindVal   string
			size      int32
			width     int32
			height    int32
			refCount  int32
			createdAt time.Time
		)
		if err := rows.Scan(&id, &hash, &kindVal, &size, &width, &height,
			&refCount, &createdAt); err != nil {
			h.failInternal(w, "读取材质失败", err)
			return
		}
		out = append(out, map[string]any{
			"id":         id.String(),
			"hash":       hash,
			"type":       kindVal,
			"size":       size,
			"width":      width,
			"height":     height,
			"ref_count":  refCount,
			"created_at": createdAt.UTC().Format(time.RFC3339),
		})
	}
	if err := rows.Err(); err != nil {
		h.failInternal(w, "读取材质失败", err)
		return
	}

	httpx.OK(w, map[string]any{"textures": out, "total": total})
}

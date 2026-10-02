package minecraft

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"image"
	"image/png"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/yggauth/yggauth/internal/platform/apperr"
	"github.com/yggauth/yggauth/internal/platform/clock"
	"github.com/yggauth/yggauth/internal/platform/db"
	"github.com/yggauth/yggauth/internal/platform/db/query"
	"github.com/yggauth/yggauth/internal/platform/storage"
)

// TextureType 是纹理的种类。
type TextureType string

// 纹理类型。
const (
	TextureSkin TextureType = "skin"
	TextureCape TextureType = "cape"
)

// pngMagic 是 PNG 文件头。
//
// 只看魔数不够 —— 一个文件头合法但内容被截断的 PNG 会让后续的
// 客户端在解码时崩掉。这里同时做魔数检查与完整解码检查。
var pngMagic = []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}

// Texture 是已存储的一条纹理记录。
type Texture struct {
	Hash   string
	Type   TextureType
	Size   int32
	Mime   string
	Width  int32
	Height int32
}

// TextureOptions 是纹理服务的构造参数。
type TextureOptions struct {
	Storage  storage.Storage
	MaxSize  int64
	ReadOnly bool
	// GarbageGrace 是引用计数归零后仍保留文件的时间
	GarbageGrace time.Duration
	// Logger 记录可降级错误的细节
	Logger Logger
}

// TextureService 负责纹理的校验、去重、存储与回源。
type TextureService struct {
	queries *query.Queries
	pool    *db.Pool
	store   storage.Storage
	clock   clock.Clock
	opts    TextureOptions
	fetcher ExternalFetcher
}

// NewTextureService 创建纹理服务。
func NewTextureService(pool *db.Pool, store storage.Storage, clk clock.Clock, opts TextureOptions, fetcher ExternalFetcher) *TextureService {
	if opts.MaxSize <= 0 {
		opts.MaxSize = 2 << 20
	}
	if opts.GarbageGrace <= 0 {
		opts.GarbageGrace = 7 * 24 * time.Hour
	}
	return &TextureService{
		queries: query.New(pool),
		pool:    pool,
		store:   store,
		clock:   clk,
		opts:    opts,
		fetcher: fetcher,
	}
}

// ValidateTexture 校验上传的纹理。
//
// 返回解析后的尺寸。校验失败一律返回业务错误,不会把解码器
// 的原始报错透出去 —— 那里面带着内部实现细节。
func ValidateTexture(data []byte, kind TextureType, maxSize int64) (width, height int, err error) {
	if int64(len(data)) > maxSize {
		return 0, 0, apperr.Newf(apperr.CodeInvalidArgument, "文件超过 %d MB 上限", maxSize>>20)
	}
	if len(data) < len(pngMagic) || !bytes.Equal(data[:len(pngMagic)], pngMagic) {
		return 0, 0, apperr.New(apperr.CodeInvalidArgument, "只接受 PNG 格式的纹理")
	}

	cfg, format, decodeErr := image.DecodeConfig(bytes.NewReader(data))
	if decodeErr != nil {
		// 魔数对但解不开:文件被截断或内容被改过。
		return 0, 0, apperr.New(apperr.CodeInvalidArgument, "PNG 文件无法解析,可能已损坏")
	}
	if format != "png" {
		return 0, 0, apperr.New(apperr.CodeInvalidArgument, "只接受 PNG 格式的纹理")
	}

	// 完整解码一次。只看尺寸头的话,一个尺寸合法但数据流损坏的
	// 文件会被放行,然后在客户端渲染时炸掉。
	if _, decodeErr := png.Decode(bytes.NewReader(data)); decodeErr != nil {
		return 0, 0, apperr.New(apperr.CodeInvalidArgument, "PNG 文件无法完整解码,可能已损坏")
	}

	if err := validateDimensions(cfg.Width, cfg.Height, kind); err != nil {
		return 0, 0, err
	}
	return cfg.Width, cfg.Height, nil
}

// validateDimensions 校验尺寸。
//
// 同时支持 1.8+ 的 64×64 与 1.7 及更早的 64×32 皮肤。
func validateDimensions(width, height int, kind TextureType) error {
	if kind == TextureCape {
		if width != 64 || height != 32 {
			return apperr.Newf(apperr.CodeInvalidArgument,
				"披风尺寸必须是 64×32,实际 %d×%d", width, height)
		}
		return nil
	}

	// 64×32 是旧版皮肤;1.8 起统一为 64×64。
	if (width == 64 && height == 64) || (width == 64 && height == 32) {
		return nil
	}
	return apperr.Newf(apperr.CodeInvalidArgument,
		"皮肤尺寸必须是 64×64(现代)或 64×32(旧版),实际 %d×%d", width, height)
}

// UploadResult 是上传结果。
type UploadResult struct {
	Hash string `json:"hash"`
	// Deduplicated 为真表示这份内容此前已经存过,本次只加了引用。
	Deduplicated bool   `json:"deduplicated"`
	URL          string `json:"url"`
}

// Upload 上传并绑定纹理。
//
// **去重**是这里的核心:按内容 sha256 判定。同一张皮肤被换设备、
// 被复制粘贴、被反复上传都只占一份磁盘与一条记录。
func (s *TextureService) Upload(ctx context.Context, profileID uuid.UUID, kind TextureType, data []byte, textureURLBase string) (UploadResult, error) {
	if s.opts.ReadOnly {
		// 只读模式是运维的应急开关:磁盘或带宽出问题时先止血,
		// 而不是让每个玩家继续往里塞文件。
		return UploadResult{}, apperr.New(apperr.CodePermissionDenied,
			"皮肤站处于只读模式,暂时不接受上传")
	}

	width, height, err := ValidateTexture(data, kind, s.opts.MaxSize)
	if err != nil {
		return UploadResult{}, err
	}

	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])
	key := storage.TextureKey(hash)

	// 命中已有内容:只加引用,不重复落盘。
	existing, err := s.queries.GetTextureByHash(ctx, hash)
	switch {
	case err == nil:
		if err := s.queries.AddTextureReference(ctx, hash); err != nil {
			return UploadResult{}, apperr.Newf(apperr.CodeInternal, "更新纹理引用失败: %v", err)
		}
		if err := s.queries.TouchTexture(ctx, hash); err != nil {
			s.logWarn(ctx, "更新纹理访问时间失败", "hash", hash, "error", err)
		}
		result := UploadResult{
			Hash:         hash,
			Deduplicated: true,
			URL:          textureURLBase + "/mc/textures/" + hash,
		}
		if err := s.bind(ctx, profileID, kind, existing.ID); err != nil {
			return UploadResult{}, err
		}
		return result, nil

	case db.IsNoRows(err):
		// 新内容,继续往下走落盘
	default:
		return UploadResult{}, apperr.Newf(apperr.CodeInternal, "查询纹理失败: %v", err)
	}

	// 先落盘再写库:反过来会出现「库里有记录、文件没有」,
	// 那时下载会 404,而重传同一张图因为命中了去重记录又不会补文件 ——
	// 素材就永久丢了。
	if err := s.store.Put(ctx, key, data); err != nil {
		return UploadResult{}, apperr.Newf(apperr.CodeInternal, "存储纹理文件失败: %v", err)
	}

	row, err := s.queries.CreateTexture(ctx, query.CreateTextureParams{
		Hash:   hash,
		Type:   string(kind),
		Size:   int32(len(data)),
		Mime:   "image/png",
		Width:  int32(width),
		Height: int32(height),
	})
	if err != nil {
		if db.IsUniqueViolation(err) {
			// 并发上传同一张图:另一个请求已经写完了,直接当命中处理。
			return s.Upload(ctx, profileID, kind, data, textureURLBase)
		}
		// 写库失败就把刚落的文件删掉,否则它成了永远没人引用的孤儿。
		if delErr := s.store.Delete(ctx, key); delErr != nil {
			s.logWarn(ctx, "回滚纹理文件失败", "hash", hash, "error", delErr)
		}
		return UploadResult{}, apperr.Newf(apperr.CodeInternal, "保存纹理记录失败: %v", err)
	}

	if err := s.bind(ctx, profileID, kind, row.ID); err != nil {
		return UploadResult{}, err
	}

	return UploadResult{
		Hash:         hash,
		Deduplicated: false,
		URL:          textureURLBase + "/mc/textures/" + hash,
	}, nil
}

// bind 把纹理绑定到档案的某个类型位上,并使头像缓存失效。
func (s *TextureService) bind(ctx context.Context, profileID uuid.UUID, kind TextureType, textureID uuid.UUID) error {
	if err := s.queries.UpsertProfileTexture(ctx, query.UpsertProfileTextureParams{
		ProfileID: profileID,
		TextureID: textureID,
		Type:      string(kind),
	}); err != nil {
		return apperr.Newf(apperr.CodeInternal, "绑定材质失败: %v", err)
	}

	// 皮肤换了,之前渲染的头像就是过期的。删掉而不是留着 ——
	// 缓存表里留着 skin_hash 不匹配的记录,下次请求还是会重来一遍。
	if kind == TextureSkin {
		if _, err := s.queries.DeleteAvatar(ctx, profileID); err != nil {
			s.logWarn(ctx, "失效头像缓存失败", "profile_id", profileID, "error", err)
		}
	}
	return nil
}

// TextureFor 返回档案在某类型上的纹理记录。
func (s *TextureService) TextureFor(ctx context.Context, profileID uuid.UUID, kind TextureType) (Texture, bool, error) {
	row, err := s.queries.GetProfileTexture(ctx, query.GetProfileTextureParams{
		ProfileID: profileID,
		Type:      string(kind),
	})
	if err != nil {
		if db.IsNoRows(err) {
			return Texture{}, false, nil
		}
		return Texture{}, false, apperr.Newf(apperr.CodeInternal, "查询材质绑定失败: %v", err)
	}

	// 顺手刷新访问时间,后台回收任务靠它判断是否该清理。
	if err := s.queries.TouchTexture(ctx, row.Hash); err != nil {
		s.logWarn(ctx, "更新纹理访问时间失败", "hash", row.Hash, "error", err)
	}
	return Texture{
		Hash:   row.Hash,
		Type:   TextureType(row.Type),
		Size:   row.Size,
		Mime:   row.Mime,
		Width:  row.Width,
		Height: row.Height,
	}, true, nil
}

// TextureByHash 按内容哈希取纹理记录。
func (s *TextureService) TextureByHash(ctx context.Context, hash string) (Texture, error) {
	row, err := s.queries.GetTextureByHash(ctx, hash)
	if err != nil {
		if db.IsNoRows(err) {
			return Texture{}, apperr.ErrNotFound
		}
		return Texture{}, apperr.Newf(apperr.CodeInternal, "查询纹理失败: %v", err)
	}
	return Texture{
		Hash:   row.Hash,
		Type:   TextureType(row.Type),
		Size:   row.Size,
		Mime:   row.Mime,
		Width:  row.Width,
		Height: row.Height,
	}, nil
}

// TextureBytes 读取纹理内容,必要时回源。
//
// 回源只在「本地确实没有」且「配置允许」且「账号有外部绑定」时发生。
// 任何一步失败都降级为「没有纹理」,而不是把错误抛给调用方:
// MC 客户端在皮肤加载失败时会继续用默认皮肤,不会因此拒绝玩家进服。
func (s *TextureService) TextureBytes(ctx context.Context, profileID uuid.UUID, kind TextureType) ([]byte, error) {
	tex, found, err := s.TextureFor(ctx, profileID, kind)
	if err != nil {
		return nil, err
	}

	if found {
		data, err := s.store.Get(ctx, storage.TextureKey(tex.Hash))
		if err == nil {
			return data, nil
		}
		if !isStorageNotFound(err) {
			s.logWarn(ctx, "读取纹理文件失败,尝试回源", "hash", tex.Hash, "error", err)
		}
	}

	return s.fetchExternal(ctx, profileID, kind)
}

// fetchExternal 从外部皮肤站回源。
func (s *TextureService) fetchExternal(ctx context.Context, profileID uuid.UUID, kind TextureType) ([]byte, error) {
	if s.fetcher == nil {
		return nil, storage.ErrNotFound
	}
	if !s.fetcher.Enabled() {
		return nil, storage.ErrNotFound
	}

	binding, err := s.queries.GetExternalBinding(ctx, profileID)
	if err != nil {
		if db.IsNoRows(err) {
			return nil, storage.ErrNotFound
		}
		return nil, apperr.Newf(apperr.CodeInternal, "查询外部绑定失败: %v", err)
	}

	data, err := s.fetcher.Fetch(ctx, binding.ExternalUserID, kind)
	if err != nil || len(data) == 0 {
		// 回源失败一律降级,不向上抛。
		s.logWarn(ctx, "外部皮肤站回源失败", "username", binding.ExternalUserID, "error", err)
		return nil, storage.ErrNotFound
	}

	// 回源结果照常入缓存,下次直接命中本地,不再打扰外部站。
	if _, err := s.Upload(ctx, profileID, kind, data, ""); err != nil {
		s.logWarn(ctx, "缓存回源材质失败", "username", binding.ExternalUserID, "error", err)
	}
	return data, nil
}

// DeleteTexture 解绑并删除材质。
func (s *TextureService) DeleteTexture(ctx context.Context, profileID uuid.UUID, kind TextureType) error {
	tex, found, err := s.TextureFor(ctx, profileID, kind)
	if err != nil {
		return err
	}
	if !found {
		return apperr.ErrNotFound
	}

	if _, err := s.queries.DeleteProfileTexture(ctx, query.DeleteProfileTextureParams{
		ProfileID: profileID,
		Type:      string(kind),
	}); err != nil {
		return apperr.Newf(apperr.CodeInternal, "解绑材质失败: %v", err)
	}
	if _, err := s.queries.DropTextureReference(ctx, tex.Hash); err != nil {
		s.logWarn(ctx, "减少纹理引用失败", "hash", tex.Hash, "error", err)
	}

	if kind == TextureSkin {
		if _, err := s.queries.DeleteAvatar(ctx, profileID); err != nil {
			s.logWarn(ctx, "失效头像缓存失败", "profile_id", profileID, "error", err)
		}
	}
	return nil
}

// SetExternalBinding 绑定外部皮肤站账号。
//
// provider 决定用哪个回源实现,base_url 允许玩家指向自建镜像 ——
// 把「回源到哪里」做成数据而不是配置,换站不需要重启服务。
func (s *TextureService) SetExternalBinding(ctx context.Context, profileID uuid.UUID, provider, externalUserID, baseURL string) error {
	if err := s.queries.UpsertExternalBinding(ctx, query.UpsertExternalBindingParams{
		ProfileID:      profileID,
		Provider:       provider,
		ExternalUserID: externalUserID,
		BaseUrl:        baseURL,
		Enabled:        true,
	}); err != nil {
		return apperr.Newf(apperr.CodeInternal, "保存外部绑定失败: %v", err)
	}
	return nil
}

// CollectGarbage 清理无引用的纹理。
//
// 顺序很关键:先删文件,再删记录。反过来的话,一旦文件删除失败
// 就留下一条指向不存在文件的记录 —— 下载会 404,而引用计数已经是 0,
// 没有任何东西会再来清理它。
func (s *TextureService) CollectGarbage(ctx context.Context, batch int32) (int, error) {
	rows, err := s.queries.ListGarbageTextures(ctx, query.ListGarbageTexturesParams{
		Grace: pgtype.Interval{Microseconds: s.opts.GarbageGrace.Microseconds(), Valid: true},
		Batch: batch,
	})
	if err != nil {
		return 0, apperr.Newf(apperr.CodeInternal, "查询待回收纹理失败: %v", err)
	}

	removed := 0
	for _, row := range rows {
		if err := s.store.Delete(ctx, storage.TextureKey(row.Hash)); err != nil {
			s.logWarn(ctx, "删除纹理文件失败,保留记录待下次重试", "hash", row.Hash, "error", err)
			continue
		}
		if _, err := s.queries.DeleteTexture(ctx, row.Hash); err != nil {
			s.logWarn(ctx, "删除纹理记录失败", "hash", row.Hash, "error", err)
			continue
		}
		removed++
	}
	return removed, nil
}

func (s *TextureService) logWarn(_ context.Context, msg string, args ...any) {
	if s.opts.Logger != nil {
		s.opts.Logger.Warn(msg, args...)
	}
}

// AvatarRenderer 渲染头像。
type AvatarRenderer interface {
	Render(ctx context.Context, skinData []byte, size int) ([]byte, error)
	DefaultAvatar(profileUUID, name string, size int) []byte
}

// Avatar 是渲染结果。
type Avatar struct {
	Data     []byte
	Fallback bool
}

func isStorageNotFound(err error) bool { return errors.Is(err, storage.ErrNotFound) }

// dbNotFound 判断是否「查无此行」。
func dbNotFound(err error) bool { return db.IsNoRows(err) }

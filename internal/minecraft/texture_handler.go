package minecraft

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/yggauth/yggauth/internal/platform/apperr"
	"github.com/yggauth/yggauth/internal/platform/httpx"
	"github.com/yggauth/yggauth/internal/platform/storage"
)

// 皮肤站端点挂在 /mc 协议前缀下,与认证端点同一个 router。
//
// 材质下载是**公开**的:MC 客户端加载皮肤时不带任何令牌,
// 它只有 skinDomains 里那个公开 URL。鉴权只发生在上传侧。

// TextureHandler 处理材质上传与下载。
type TextureHandler struct {
	svc     *TextureService
	avatars *AvatarService
	// service 是 MC 域主服务,提供档案查询
	service  *Service
	profiles ProfileLookup
	readOnly bool
	// urlBase 是 PUBLIC_BASE_URL,用于拼材质 URL
	urlBase string
	// maxSize 是上传体积上限
	maxSize int64
}

// ProfileLookup 是材质端点需要的档案查询能力。
type ProfileLookup interface {
	ProfileByUUID(ctx context.Context, id uuid.UUID) (Profile, error)
	ProfileByName(ctx context.Context, name string) (Profile, error)
}

// TextureHandlerDeps 是纹理处理器的依赖。
type TextureHandlerDeps struct {
	// Service 负责纹理的校验、去重、存储与回源
	Service *TextureService
	// MainService 提供档案查询
	MainService *Service
	// Avatars 负责头像的异步渲染
	Avatars *AvatarService
	// Profiles 用于按 uuid 或名字解析档案
	Profiles ProfileLookup
	// URLBase 是 PUBLIC_BASE_URL,用于拼材质 URL
	URLBase string
	// MaxSize 是上传体积上限
	MaxSize int64
	// ReadOnly 为真时禁止上传
	ReadOnly bool
}

// NewTextureHandler 创建处理器。
func NewTextureHandler(d TextureHandlerDeps) *TextureHandler {
	return &TextureHandler{
		svc:      d.Service,
		service:  d.MainService,
		avatars:  d.Avatars,
		profiles: d.Profiles,
		urlBase:  d.URLBase,
		maxSize:  d.MaxSize,
		readOnly: d.ReadOnly,
	}
}

// MountTexture 挂载材质与头像端点。
func (h *TextureHandler) MountTexture(r chi.Router) {
	r.Get("/textures/{hash}", h.DownloadTexture)
	r.Get("/skin/{id}", h.DownloadSkin)
	r.Get("/avatar/{id}", h.Avatar)
}

// DownloadTexture 按内容哈希下载材质。
func (h *TextureHandler) DownloadTexture(w http.ResponseWriter, r *http.Request) {
	hash := strings.TrimSuffix(chi.URLParam(r, "hash"), ".png")

	// 哈希必须形如 64 位十六进制。不校验的话,路径里可以塞
	// ../../etc/passwd —— 存储层虽然挡了穿越,但那是最后一道防线,
	// 这里就该拦掉。
	if !isHex64(hash) {
		failYggdrasil(w, Errorf(ErrCodeInvalidRequest, "材质哈希格式不正确"))
		return
	}

	data, err := h.svc.store.Get(r.Context(), storage.TextureKey(hash))
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	// 内容寻址意味着内容永远不变,可以让 CDN 永久缓存。
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("ETag", `"`+hash+`"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// DownloadSkin 按玩家取皮肤。
func (h *TextureHandler) DownloadSkin(w http.ResponseWriter, r *http.Request) {
	profile, err := h.resolve(r, chi.URLParam(r, "id"))
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	data, err := h.svc.TextureBytes(r.Context(), profile.ID, TextureSkin)
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=300")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// Avatar 返回玩家头像。
//
// 永远 200:MC 客户端在头像加载失败时会持续重试,返回 404 只会
// 让它反复打过来。降级头像让请求安静结束。
func (h *TextureHandler) Avatar(w http.ResponseWriter, r *http.Request) {
	size := 64
	if raw := r.URL.Query().Get("size"); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v > 0 && v <= 512 {
			size = v
		}
	}

	profile, err := h.resolve(r, chi.URLParam(r, "id"))
	if err != nil {
		// 连档案都没有:给一个由该 id 派生的确定性默认头像。
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "public, max-age=300")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(defaultAvatarFor(chi.URLParam(r, "id"), size))
		return
	}

	result, err := h.avatars.Avatar(r.Context(), profile.ID, profile.CurrentName, size)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	// 默认头像短缓存:它会在下一次渲染完成后变成真实头像。
	cacheControl := "public, max-age=300"
	if result.Fallback {
		cacheControl = "public, max-age=30"
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", cacheControl)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(result.Data)
}

// resolve 按 uuid 或名字解析档案。
func (h *TextureHandler) resolve(r *http.Request, idOrName string) (Profile, error) {
	if trimmed := strings.ReplaceAll(idOrName, "-", ""); isUUIDHex(trimmed) {
		parsed, err := ParseUUID(trimmed)
		if err != nil {
			return Profile{}, err
		}
		return h.profiles.ProfileByUUID(r.Context(), parsed)
	}
	return h.profiles.ProfileByName(r.Context(), idOrName)
}

// isUUIDHex 判断是否是无横线的 32 位十六进制 UUID。
//
// **不能**复用 isHex64:那是 64 位的 sha256 判定。拿它判 UUID 会让
// 每一个合法的 32 位 UUID 都落到「按名字查」分支上 ——
// 症状是头像接口永远返回默认头像,而渲染队列一次都没被填过。
func isUUIDHex(in string) bool { return len(in) == 32 && isLowerHex(in) }

// isHex64 判断是否 64 位十六进制(sha256)。
func isHex64(in string) bool { return len(in) == 64 && isLowerHex(in) }

// isLowerHex 判断是否十六进制字符。
func isLowerHex(in string) bool {
	if in == "" {
		return false
	}
	for _, r := range in {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'f', r >= 'A' && r <= 'F':
		default:
			return false
		}
	}
	return true
}

// UploadTexture 上传并绑定材质。
func (h *TextureHandler) UploadTexture(w http.ResponseWriter, r *http.Request) {
	accountID, ok := mcAccountID(r)
	if !ok {
		httpx.Fail(w, apperr.ErrUnauthorized)
		return
	}
	if h.readOnly {
		httpx.Fail(w, apperr.New(apperr.CodePermissionDenied,
			"皮肤站处于只读模式,暂时不接受上传"))
		return
	}

	kind := TextureType(strings.ToLower(r.URL.Query().Get("type")))
	if kind == "" {
		kind = TextureSkin
	}
	if kind != TextureSkin && kind != TextureCape {
		httpx.Fail(w, apperr.New(apperr.CodeInvalidArgument, "type 只能是 skin 或 cape"))
		return
	}

	// 先按上限读再解析:不限制的话,一个 2GB 的上传会先被全部
	// 读进内存再被体积检查拒绝 —— 那正是「大文件拖垮请求处理」。
	data, err := io.ReadAll(io.LimitReader(r.Body, h.maxSize+1))
	if err != nil {
		httpx.Fail(w, apperr.New(apperr.CodeInvalidArgument, "读取上传内容失败"))
		return
	}
	if int64(len(data)) > h.maxSize {
		httpx.Fail(w, apperr.Newf(apperr.CodeInvalidArgument, "文件超过 %d MB 上限", h.maxSize>>20))
		return
	}

	profile, err := h.profileOf(r.Context(), accountID)
	if err != nil {
		httpx.Fail(w, err)
		return
	}

	result, err := h.svc.Upload(r.Context(), profile.ID, kind, data, h.urlBase)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.OK(w, result)
}

// GetTexture 查询当前材质。
func (h *TextureHandler) GetTexture(w http.ResponseWriter, r *http.Request) {
	accountID, ok := mcAccountID(r)
	if !ok {
		httpx.Fail(w, apperr.ErrUnauthorized)
		return
	}

	kind := TextureType(strings.ToLower(r.URL.Query().Get("type")))
	if kind == "" {
		kind = TextureSkin
	}

	profile, err := h.profileOf(r.Context(), accountID)
	if err != nil {
		httpx.Fail(w, err)
		return
	}

	tex, found, err := h.svc.TextureFor(r.Context(), profile.ID, kind)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	if !found {
		httpx.Fail(w, apperr.ErrNotFound)
		return
	}

	httpx.OK(w, map[string]any{
		"type":   string(kind),
		"hash":   tex.Hash,
		"size":   tex.Size,
		"url":    h.urlBase + "/mc/textures/" + tex.Hash,
		"mime":   tex.Mime,
		"width":  tex.Width,
		"height": tex.Height,
	})
}

// DeleteTextureHandler 删除材质。
func (h *TextureHandler) DeleteTextureHandler(w http.ResponseWriter, r *http.Request) {
	accountID, ok := mcAccountID(r)
	if !ok {
		httpx.Fail(w, apperr.ErrUnauthorized)
		return
	}

	kind := TextureType(strings.ToLower(chi.URLParam(r, "type")))
	if kind != TextureSkin && kind != TextureCape {
		httpx.Fail(w, apperr.New(apperr.CodeInvalidArgument, "type 只能是 skin 或 cape"))
		return
	}

	profile, err := h.profileOf(r.Context(), accountID)
	if err != nil {
		httpx.Fail(w, err)
		return
	}

	if err := h.svc.DeleteTexture(r.Context(), profile.ID, kind); err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.OK(w, map[string]any{"deleted": true})
}

// profileOf 返回账号的 MC 档案。
func (h *TextureHandler) profileOf(ctx context.Context, accountID uuid.UUID) (Profile, error) {
	profiles, err := h.service.ProfilesByAccount(ctx, accountID)
	if err != nil {
		return Profile{}, err
	}
	if len(profiles) == 0 {
		return Profile{}, apperr.New(apperr.CodeNotFound,
			"尚未绑定 Minecraft 档案,请先在游戏内登录一次")
	}
	return profiles[0], nil
}

// MountAccount 挂载账号侧的材质管理端点。
func (h *TextureHandler) MountAccount(r AccountMounter) {
	r.Get("/texture", h.GetTexture)
	r.Put("/texture", h.UploadTexture)
	r.Delete("/texture/{type}", h.DeleteTextureHandler)
}

// AccountMounter 是账号侧材质端点需要的路由注册能力。
type AccountMounter interface {
	Get(pattern string, h http.HandlerFunc)
	Put(pattern string, h http.HandlerFunc)
	Delete(pattern string, h http.HandlerFunc)
}

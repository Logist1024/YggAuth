package minecraft

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/stretchr/testify/require"

	"github.com/yggauth/yggauth/internal/platform/apperr"
	"github.com/yggauth/yggauth/internal/platform/storage"
)

// newSkinPNG 造一张合法的皮肤。
//
// width=64,height=64 在头部区域 (8,8) 填一块确定性的颜色,
// 这样测试可以断言「渲染出的头像确实是这块颜色」,而不是只断言
// 「渲染没有报错」—— 后者无法区分「渲染成功」与「渲染出了一张空白图」。
func newSkinPNG(t *testing.T, width, height int, headColor color.NRGBA) []byte {
	t.Helper()

	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := range headSize {
		for x := range headSize {
			img.SetNRGBA(headX+x, headY+y, headColor)
		}
	}
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	return buf.Bytes()
}

func redSkin(t *testing.T) []byte {
	return newSkinPNG(t, 64, 64, color.NRGBA{R: 0xC0, G: 0x20, B: 0x20, A: 0xFF})
}

func newLocal(t *testing.T) *localStorage {
	t.Helper()
	return &localStorage{dir: t.TempDir()}
}

// localStorage 是测试用的极简存储。
type localStorage struct {
	dir      string
	putCalls int
	putErr   error
}

func (s *localStorage) Put(_ context.Context, key string, data []byte) error {
	s.putCalls++
	if s.putErr != nil {
		return s.putErr
	}
	full := filepath.Join(s.dir, filepath.FromSlash(key))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return err
	}
	return os.WriteFile(full, data, 0o644)
}

func (s *localStorage) Get(_ context.Context, key string) ([]byte, error) {
	data, err := os.ReadFile(filepath.Join(s.dir, filepath.FromSlash(key)))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, storage.ErrNotFound
		}
		return nil, err
	}
	return data, nil
}

func (s *localStorage) Delete(_ context.Context, key string) error {
	err := os.Remove(filepath.Join(s.dir, filepath.FromSlash(key)))
	if err != nil && os.IsNotExist(err) {
		return nil
	}
	return err
}

func (s *localStorage) Exists(_ context.Context, key string) (bool, error) {
	_, err := os.Stat(filepath.Join(s.dir, filepath.FromSlash(key)))
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// TestValidateTextureAcceptsModernSkin 验证现代皮肤尺寸被接受。
func TestValidateTextureAcceptsModernSkin(t *testing.T) {
	_, _, err := ValidateTexture(redSkin(t), TextureSkin, 2<<20)
	require.NoError(t, err)
}

// TestValidateTextureAcceptsLegacySkin 验证旧版皮肤尺寸被接受。
//
// MC 1.7 及更早的皮肤是 64×32。这类存档在老服务器上仍然存在,
// 拒绝它们等于让老玩家无法更新皮肤。
func TestValidateTextureAcceptsLegacySkin(t *testing.T) {
	data := newSkinPNG(t, 64, 32, color.NRGBA{R: 0x10, G: 0xA0, B: 0x30, A: 0xFF})
	_, _, err := ValidateTexture(data, TextureSkin, 2<<20)
	require.NoError(t, err)
}

// TestValidateTextureRejectsBadInput 验证各类非法输入。
func TestValidateTextureRejectsBadInput(t *testing.T) {
	t.Run("非 PNG 文件头", func(t *testing.T) {
		_, _, err := ValidateTexture([]byte("这不是 PNG,只是一段文本"), TextureSkin, 2<<20)
		require.Error(t, err)
		require.True(t, apperr.Is(err, apperr.CodeInvalidArgument))
	})

	t.Run("尺寸不符", func(t *testing.T) {
		data := newSkinPNG(t, 128, 128, color.NRGBA{A: 0xFF})
		_, _, err := ValidateTexture(data, TextureSkin, 2<<20)
		require.Error(t, err)
	})

	t.Run("披风尺寸必须是 64×32", func(t *testing.T) {
		data := newSkinPNG(t, 64, 64, color.NRGBA{A: 0xFF})
		_, _, err := ValidateTexture(data, TextureCape, 2<<20)
		require.Error(t, err, "64×64 的披风必须被拒")
	})

	t.Run("超过体积上限", func(t *testing.T) {
		data := newSkinPNG(t, 64, 64, color.NRGBA{A: 0xFF})
		_, _, err := ValidateTexture(data, TextureSkin, 16)
		require.Error(t, err)
	})

	t.Run("魔数正确但内容损坏", func(t *testing.T) {
		good := redSkin(t)
		truncated := append([]byte{}, good[:40]...) // 保留 PNG 头,砍掉数据流
		_, _, err := ValidateTexture(truncated, TextureSkin, 2<<20)
		require.Error(t, err, "只看魔数会放过截断的文件")
	})
}

func TestTextureKeyShardsByHashPrefix(t *testing.T) {
	hash := "abcdef0123456789" + strings.Repeat("0", 48)
	key := storage.TextureKey(hash)

	require.Equal(t, "textures/ab/"+hash+".png", key)
	require.True(t, strings.HasPrefix(key, "textures/ab/"),
		"按哈希前两位分目录,避免单目录文件过多把 ext4 目录索引拖成平衡树")
}

// TestLocalStorageRoundTrip 验证本地存储的读写删。
func TestLocalStorageRoundTrip(t *testing.T) {
	store, err := storage.NewLocal(storage.LocalOptions{Root: t.TempDir()})
	require.NoError(t, err)

	ctx := t.Context()
	require.NoError(t, store.Put(ctx, "textures/aa/hash.png", []byte("payload")))

	data, err := store.Get(ctx, "textures/aa/hash.png")
	require.NoError(t, err)
	require.Equal(t, []byte("payload"), data)

	exists, err := store.Exists(ctx, "textures/aa/hash.png")
	require.NoError(t, err)
	require.True(t, exists)

	require.NoError(t, store.Delete(ctx, "textures/aa/hash.png"))
	exists, err = store.Exists(ctx, "textures/aa/hash.png")
	require.NoError(t, err)
	require.False(t, exists)

	// 删不存在的对象不算错误
	require.NoError(t, store.Delete(ctx, "textures/aa/hash.png"))
}

// TestLocalStorageRejectsPathTraversal 验证路径穿越被挡。
//
// 键来自数据库里的哈希,理论上不含 `..`;但这是一个可达的写盘路径,
// 与其相信「理论上」不如在这里挡一次。
func TestLocalStorageRejectsPathTraversal(t *testing.T) {
	store, err := storage.NewLocal(storage.LocalOptions{Root: t.TempDir()})
	require.NoError(t, err)

	ctx := t.Context()
	for _, key := range []string{"../../etc/passwd", "textures/../../../etc/passwd", "/etc/passwd"} {
		err := store.Put(ctx, key, []byte("x"))
		require.Error(t, err, "键 %q 必须被拒", key)
	}
}

// TestLocalStorageWritesReadableFile 验证文件权限。
func TestLocalStorageWritesReadableFile(t *testing.T) {
	root := t.TempDir()
	store, err := storage.NewLocal(storage.LocalOptions{Root: root})
	require.NoError(t, err)

	ctx := t.Context()
	require.NoError(t, store.Put(ctx, "textures/ab/x.png", []byte("payload")))

	info, err := os.Stat(filepath.Join(root, "textures", "ab", "x.png"))
	require.NoError(t, err)
	// 纹理由 nginx 之类的静态服务器直接读取,0644 让它们能读到;
	// 收紧到 0600 会迫使运维把权限一路放大到 0777。
	require.Equal(t, os.FileMode(0o644), info.Mode().Perm())
}

// TestRenderSkinExtractsHead 验证头部裁切与放大。
//
// 断言的是「输出左上角确实是皮肤头部的颜色」,而不是「没有报错」——
// 后者无法区分渲染成功和渲染出一张空白图。
func TestRenderSkinExtractsHead(t *testing.T) {
	skin := newSkinPNG(t, 64, 64, color.NRGBA{R: 0xC0, G: 0x20, B: 0x20, A: 0xFF})

	out, err := renderSkin(skin, 32)
	require.NoError(t, err)

	decoded, err := png.Decode(bytes.NewReader(out))
	require.NoError(t, err)
	require.Equal(t, 32, decoded.Bounds().Dx())

	r, g, b, a := decoded.At(16, 16).RGBA()
	require.Greater(t, a, uint32(0), "头像不能是全透明的")
	require.InDelta(t, 0xC0, r>>8, 12, "应取到皮肤头部的红色")
	require.InDelta(t, 0x20, g>>8, 12)
	require.InDelta(t, 0x20, b>>8, 12)
}

// TestRenderSkinHandlesLegacyLayout 验证旧版皮肤的第二层坐标。
func TestRenderSkinHandlesLegacyLayout(t *testing.T) {
	// 64×32 的皮肤:底层头部画红,第二层画绿。
	// 第二层坐标错误时(用了现代的 40)会画到别处,输出就还是红的。
	img := image.NewNRGBA(image.Rect(0, 0, 64, 32))
	for y := range headSize {
		for x := range headSize {
			img.SetNRGBA(headX+x, headY+y, color.NRGBA{R: 0xFF, A: 0xFF})
			img.SetNRGBA(overlayXLegacy+x, headY+y, color.NRGBA{G: 0xFF, A: 0xFF})
		}
	}
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))

	out, err := renderSkin(buf.Bytes(), 16)
	require.NoError(t, err)

	decoded, err := png.Decode(bytes.NewReader(out))
	require.NoError(t, err)

	r, g, _, _ := decoded.At(8, 8).RGBA()
	require.Equal(t, uint32(0), r>>8, "底层红色应被第二层盖掉")
	require.Greater(t, g>>8, uint32(0xE0), "应当取到第二层的绿色")
}

// TestDefaultAvatarIsDeterministic 验证默认头像确定性。
func TestDefaultAvatarIsDeterministic(t *testing.T) {
	id := uuid.MustParse("11111111-2222-3333-4444-555555555555")

	first := defaultAvatar(id, "Steve", 32)
	second := defaultAvatar(id, "Steve", 32)
	require.Equal(t, first, second, "同一玩家每次必须拿到同样的默认头像")

	other := defaultAvatar(uuid.MustParse("99999999-2222-3333-4444-555555555555"), "Steve", 32)
	require.NotEqual(t, first, other, "不同玩家应有不同的底色")

	decoded, err := png.Decode(bytes.NewReader(first))
	require.NoError(t, err)
	require.Equal(t, 32, decoded.Bounds().Dx())
}

// TestBreakerOpensAfterThreshold 验证熔断。
//
// 这是「外部站故障不拖垮本服务」的关键:失败达阈值后请求直接降级,
// 一次外部请求都不发。
func TestBreakerOpensAfterThreshold(t *testing.T) {
	base := time.Now()
	b := breaker{threshold: 3, resetTimeout: time.Minute}

	require.True(t, b.allow(base))
	b.recordFailure(base)
	require.True(t, b.allow(base))
	b.recordFailure(base)
	require.True(t, b.allow(base), "未达阈值前不应短路")
	b.recordFailure(base)
	require.False(t, b.allow(base), "达到阈值后必须短路")

	// 半开等待期内仍然拒绝
	require.False(t, b.allow(base.Add(30*time.Second)))

	// 超过半开等待后放一个探针
	require.True(t, b.allow(base.Add(61*time.Second)))

	// 探针成功后计数归零
	b.recordSuccess()
	require.True(t, b.allow(base.Add(61*time.Second)))
	b.recordFailure(base.Add(61 * time.Second))
	require.True(t, b.allow(base.Add(61*time.Second)), "成功后应重新开始计数")
}

// TestExternalFetcherDisabledMakesNoRequest 验证关闭时不发请求。
func TestExternalFetcherDisabledMakesNoRequest(t *testing.T) {
	f := NewRemoteFetcher(ExternalOptions{Enabled: false, BaseURL: "http://127.0.0.1:1"})
	require.False(t, f.Enabled())

	_, err := f.Fetch(t.Context(), "Steve", TextureSkin)
	require.Error(t, err)
}

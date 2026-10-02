//go:build integration

package minecraft_test

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// skinPNG 造一张头部填指定颜色的合法皮肤。
func skinPNG(t *testing.T, c color.NRGBA) []byte {
	t.Helper()

	img := image.NewNRGBA(image.Rect(0, 0, 64, 64))
	for y := range 8 {
		for x := range 8 {
			img.SetNRGBA(8+x, 8+y, c)
		}
	}

	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	return buf.Bytes()
}

// uploadWithCookie 上传一张皮肤。
func (e *env) upload(t *testing.T, cookie *http.Cookie, data []byte) *httptest.ResponseRecorder {
	t.Helper()

	req, err := http.NewRequest(http.MethodPut, "/api/account/mc/texture?type=skin", bytes.NewReader(data))
	require.NoError(t, err)
	req.Host = testHost
	req.Header.Set("Origin", testIssuer)
	req.Header.Set("Content-Type", "image/png")
	if cookie != nil {
		req.AddCookie(cookie)
	}

	rec := newRecorder()
	e.handler.ServeHTTP(rec, req)
	return rec
}

// hashOf 从上传响应里取出纹理哈希。
func hashOf(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()

	var out struct {
		Data struct {
			Hash         string `json:"hash"`
			Deduplicated bool   `json:"deduplicated"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	return out.Data.Hash
}

// TestUploadTextureDeduplicatesByContent 验证内容去重。
//
// 同一份内容重复上传必须只落一份盘、只留一条记录 —— 否则一次
// 「换设备重新上传」就会在磁盘上留下 N 份同样的 PNG。
func TestUploadTextureDeduplicatesByContent(t *testing.T) {
	e := newEnv(t)
	cookie := webLogin(t, e, "dedupe_user", "correct-horse-battery")

	skin := skinPNG(t, color.NRGBA{R: 0xC0, G: 0x30, B: 0x30, A: 0xFF})

	first := e.upload(t, cookie, skin)
	require.Equal(t, http.StatusOK, first.Code, "首次上传失败: %s", first.Body.String())
	firstHash := hashOf(t, first)
	require.Len(t, firstHash, 64)

	var firstOut struct {
		Data struct {
			Deduplicated bool   `json:"deduplicated"`
			URL          string `json:"url"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(first.Body.Bytes(), &firstOut))
	require.False(t, firstOut.Data.Deduplicated, "首次上传不应判为重复")
	require.Contains(t, firstOut.Data.URL, "/mc/textures/")

	second := e.upload(t, cookie, skin)
	require.Equal(t, http.StatusOK, second.Code)
	require.Equal(t, firstHash, hashOf(t, second), "相同内容哈希必须一致")

	var secondOut struct {
		Data struct {
			Deduplicated bool `json:"deduplicated"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(second.Body.Bytes(), &secondOut))
	require.True(t, secondOut.Data.Deduplicated, "相同内容第二次上传必须判为重复")

	matches, err := filepath.Glob(filepath.Join(e.textureDir, "*", "*", "*.png"))
	require.NoError(t, err)
	require.Len(t, matches, 1, "去重后磁盘只应有一份文件,实际 %d 份", len(matches))
}

// TestUploadRejectsInvalidTexture 验证非法上传被拒。
func TestUploadRejectsInvalidTexture(t *testing.T) {
	e := newEnv(t)
	cookie := webLogin(t, e, "invalid_uploader", "correct-horse-battery")

	cases := []struct {
		name string
		data []byte
	}{
		{"不是 PNG", []byte("plain text, definitely not a png")},
		{"尺寸不对", mustPNG(t, 128, 128)},
		{"空文件", []byte{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := e.upload(t, cookie, tc.data)
			require.Equal(t, http.StatusBadRequest, rec.Code, "响应: %s", rec.Body.String())
		})
	}
}

// TestDownloadTextureServesPublicly 验证材质可公开下载。
//
// MC 客户端加载皮肤时不带令牌 —— 它只有 metadata 里那个公开 URL。
// 给下载端点加鉴权会让所有人的皮肤在游戏里全部消失。
func TestDownloadTextureServesPublicly(t *testing.T) {
	e := newEnv(t)
	cookie := webLogin(t, e, "downloader", "correct-horse-battery")

	skin := skinPNG(t, color.NRGBA{R: 0x20, G: 0x90, B: 0x40, A: 0xFF})
	rec := e.upload(t, cookie, skin)
	require.Equal(t, http.StatusOK, rec.Code)
	hash := hashOf(t, rec)

	// 不带任何凭证
	rec = e.get(t, "/mc/textures/"+hash)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, skin, rec.Body.Bytes(), "下载内容必须与上传完全一致")
	require.Equal(t, "image/png", rec.Header().Get("Content-Type"))
	require.Contains(t, rec.Header().Get("Cache-Control"), "immutable")
}

// TestDownloadTextureRejectsBadHash 验证哈希格式校验。
func TestDownloadTextureRejectsBadHash(t *testing.T) {
	e := newEnv(t)

	for _, hash := range []string{"short", "..%2F..%2Fetc%2Fpasswd", "zz" + strings.Repeat("0", 62)} {
		rec := e.get(t, "/mc/textures/"+hash)
		require.NotEqual(t, http.StatusOK, rec.Code, "哈希 %q 必须被拒", hash)
	}
}

// TestAvatarFallsBackThenCaches 验证头像先降级、后台渲染后命中。
func TestAvatarFallsBackThenCaches(t *testing.T) {
	e := newEnv(t)
	cookie := webLogin(t, e, "avatar_user", "correct-horse-battery")
	_, playerUUID, _ := e.authenticate(t, "avatar_user", "correct-horse-battery", "c1")

	skin := skinPNG(t, color.NRGBA{R: 0xE0, G: 0x50, B: 0x50, A: 0xFF})
	require.Equal(t, http.StatusOK, e.upload(t, cookie, skin).Code)

	// 首次:降级为默认头像
	rec := e.get(t, "/mc/avatar/"+playerUUID+"?size=32")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "image/png", rec.Header().Get("Content-Type"),
		"头像端点返回了非图片响应: %s", rec.Body.String())

	// 等渲染 worker 跑完。
	//
	// 采样点取**左上角**而不是中心:默认头像在正中央画一个白色首字母,
	// 中心点取到的永远是它,分不出「渲染成功」与「还在降级」。
	require.Eventually(t, func() bool {
		r := e.get(t, "/mc/avatar/"+playerUUID+"?size=32")
		img, err := png.Decode(bytes.NewReader(r.Body.Bytes()))
		if err != nil {
			return false
		}
		cr, cg, _, _ := img.At(2, 2).RGBA()
		return cr>>8 > 0xC0 && cg>>8 < 0x90
	}, 15*time.Second, 100*time.Millisecond,
		"后台渲染应当产出按皮肤取色的头像:左上角应接近皮肤的 0xE0,0x50")
}

// TestAvatarUnknownPlayerStillReturnsImage 验证未知玩家也返回图片。
//
// MC 客户端在头像加载失败时会持续重试,404 只会让它反复打过来。
func TestAvatarUnknownPlayerStillReturnsImage(t *testing.T) {
	e := newEnv(t)

	rec := e.get(t, "/mc/avatar/0000000000000000000000000000dead?size=16")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "image/png", rec.Header().Get("Content-Type"))

	img, err := png.Decode(bytes.NewReader(rec.Body.Bytes()))
	require.NoError(t, err)
	require.Equal(t, 16, img.Bounds().Dx())
}

// TestReadOnlyModeBlocksUpload 验证只读模式拒绝上传。
func TestReadOnlyModeBlocksUpload(t *testing.T) {
	e := newEnvReadOnly(t)
	cookie := webLogin(t, e, "readonly_user", "correct-horse-battery")

	rec := e.upload(t, cookie, skinPNG(t, color.NRGBA{R: 0x11, G: 0x22, B: 0x33, A: 0xFF}))
	require.Equal(t, http.StatusForbidden, rec.Code,
		"只读模式必须拒绝上传,响应: %s", rec.Body.String())
}

// TestReadOnlyModeStillServesDownloads 验证只读不影响下载。
//
// 只读是运维的止血开关:磁盘或带宽出问题时先关上传。若连下载一起关,
// 故障就从「玩家换不了皮肤」升级成「所有人的皮肤全挂」——
// 那不是止血,是放大。
func TestReadOnlyModeStillServesDownloads(t *testing.T) {
	e := newEnv(t)
	cookie := webLogin(t, e, "ro_download", "correct-horse-battery")

	skin := skinPNG(t, color.NRGBA{R: 0x44, G: 0x55, B: 0x66, A: 0xFF})
	rec := e.upload(t, cookie, skin)
	require.Equal(t, http.StatusOK, rec.Code)
	hash := hashOf(t, rec)

	// 复用同一个存储根,切到只读模式
	ro := newEnvWithStorage(t, true, e.textureDir)
	rec = ro.get(t, "/mc/textures/"+hash)
	require.Equal(t, http.StatusOK, rec.Code, "只读模式不得影响下载")
	require.Equal(t, skin, rec.Body.Bytes())
}

// mustPNG 造一张指定尺寸的合法 PNG。
func mustPNG(t *testing.T, w, h int) []byte {
	t.Helper()

	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, image.NewNRGBA(image.Rect(0, 0, w, h))))
	return buf.Bytes()
}

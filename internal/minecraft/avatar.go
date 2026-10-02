package minecraft

import (
	"bytes"
	"context"
	"encoding/binary"
	"hash/fnv"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"sync"

	"github.com/google/uuid"

	"github.com/yggauth/yggauth/internal/platform/apperr"
	"github.com/yggauth/yggauth/internal/platform/db"
	"github.com/yggauth/yggauth/internal/platform/db/query"
	"github.com/yggauth/yggauth/internal/platform/storage"
)

// 皮肤布局中的头部区域坐标。
//
// Minecraft 的皮肤是「同一张图上叠了三层」:底层是头部的底色,
// 第二层是头部叠加(帽子、头发第二段),第三层是帽子专属。
// 只裁底层会把玩家的帽子、头发第二段全丢了,出来的头像是个秃头。
const (
	headSize = 8
	headX    = 8
	headY    = 8
	// overlayX 是第二层在图上的横坐标起点。
	// 现代皮肤(64×64)在 40,旧版皮肤(64×32)在 16。
	overlayXModern = 40
	overlayXLegacy = 16
)

// renderSkin 从皮肤提取头部并放大到正方形头像。
func renderSkin(skinData []byte, size int) ([]byte, error) {
	src, err := png.Decode(bytes.NewReader(skinData))
	if err != nil {
		return nil, apperr.New(apperr.CodeInvalidArgument, "皮肤无法解码")
	}

	bounds := src.Bounds()
	overlayX := overlayXModern
	if bounds.Dx() == 64 && bounds.Dy() == 32 {
		overlayX = overlayXLegacy
	}

	// 先裁底层。
	head := image.NewNRGBA(image.Rect(0, 0, headSize, headSize))
	draw.Draw(head, head.Bounds(), src,
		image.Pt(headX, headY), draw.Src)

	// 逐像素叠加第二层。必须逐像素而不是整块 draw:
	// 第二层有透明区域,整块覆盖会把头画成方的。
	for y := range headSize {
		for x := range headSize {
			srcX := overlayX + x
			if srcX >= bounds.Max.X || headY+y >= bounds.Max.Y {
				continue
			}
			r, g, b, a := src.At(srcX, headY+y).RGBA()
			if a == 0 {
				continue
			}
			// 颜色 alpha>128 才算「不透明」,否则半透明的头发边缘
			// 会把底层的脸整块盖掉。
			if a>>8 <= 128 {
				continue
			}
			head.SetNRGBA(x, y, color.NRGBA{
				R: uint8(r >> 8), G: uint8(g >> 8), B: uint8(b >> 8), A: 255,
			})
		}
	}

	return scaleNearest(head, size), nil
}

// scaleNearest 用最近邻放大。
//
// 刻意不用插值:头像是 8×8 放大到 64×64,双线性插值在头像内容上
// 产生的模糊比 Minecraft 客户端自己的渲染明显得多,玩家会觉得
// 「头像和游戏里长得不一样」。
func scaleNearest(src *image.NRGBA, size int) []byte {
	dst := image.NewNRGBA(image.Rect(0, 0, size, size))
	scale := src.Bounds().Dx()

	for y := range size {
		srcY := y * scale / size
		for x := range size {
			srcX := x * scale / size
			dst.Set(x, y, src.NRGBAAt(srcX, srcY))
		}
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, dst); err != nil {
		return nil
	}
	return buf.Bytes()
}

// defaultAvatar 生成默认头像。
//
// 按 UUID 哈希出确定性底色,再画上名字首字母:
//   - 确定性:同一个玩家每次拿到同样的默认头像,不会在好友列表里闪;
//   - 无外部依赖:不需要字体文件,一个字母用基本字模手绘就够了。
func defaultAvatar(profileID uuid.UUID, name string, size int) []byte {
	// 取哈希的低位字节做色相,得到一个稳定但分布均匀的颜色。
	h := fnv.New32a()
	_, _ = h.Write(profileID[:])
	bg := hslToRGBA(float64(h.Sum32()%360)/360, 0.55, 0.45)

	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	draw.Draw(img, img.Bounds(), &image.Uniform{C: bg}, image.Point{}, draw.Src)

	initial := initialRune(name)
	if initial > 0 {
		drawGlyph(img, initial, size, color.NRGBA{R: 0xFF, G: 0xFF, B: 0xFF, A: 0xFF})
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		// 走到这里说明连 encode 都能失败,返回一个 1×1 的纯色图
		// 好过返回 nil —— nil 会让调用方的 Set 崩掉。
		return []byte{
			0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A,
			0x00, 0x00, 0x00, 0x0D, 'I', 'H', 'D', 'R',
			0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
			0x08, 0x06, 0x00, 0x00, 0x00, 0x1F, 0x15, 0xC4,
			0x89,
			0x00, 0x00, 0x00, 0x0D, 'I', 'D', 'A', 'T',
			0x78, 0x9C, 0x63, 0x00, 0x01, 0x00, 0x00, 0x05,
			0x00, 0x01, 0x0D, 0x0A, 0x2D, 0xB4, 0x00, 0x00,
			0x00, 0x00, 'I', 'E', 'N', 'D', 0xAE, 0x42,
			0x60, 0x82,
		}
	}
	return buf.Bytes()
}

func initialRune(name string) rune {
	for _, r := range name {
		if r >= 'A' && r <= 'Z' {
			return r
		}
		if r >= 'a' && r <= 'z' {
			return r - 32
		}
		if r >= '0' && r <= '9' {
			return r
		}
	}
	return 0
}

// glyph5x7 是 A–Z 与 0–9 的 5×7 点阵字模。
//
// 用途单一(默认头像首字母),所以内嵌字模比引入字体解析器划算得多。
// 每一行是 5 位,高位在左。
var glyph5x7 = map[rune][7]string{
	'A': {"01110", "10001", "10001", "11111", "10001", "10001", "10001"},
	'B': {"11110", "10001", "11110", "10001", "10001", "10001", "11110"},
	'C': {"01110", "10001", "10000", "10000", "10000", "10001", "01110"},
	'D': {"11110", "10001", "10001", "10001", "10001", "10001", "11110"},
	'E': {"11111", "10000", "11110", "10000", "10000", "10000", "11111"},
	'F': {"11111", "10000", "11110", "10000", "10000", "10000", "10000"},
	'G': {"01110", "10001", "10000", "10111", "10001", "10001", "01110"},
	'H': {"10001", "10001", "10001", "11111", "10001", "10001", "10001"},
	'I': {"11111", "00100", "00100", "00100", "00100", "00100", "11111"},
	'J': {"00111", "00010", "00010", "00010", "00010", "10010", "01100"},
	'K': {"10001", "10010", "10100", "11000", "10100", "10010", "10001"},
	'L': {"10000", "10000", "10000", "10000", "10000", "10000", "11111"},
	'M': {"10001", "11011", "10101", "10101", "10001", "10001", "10001"},
	'N': {"10001", "11001", "10101", "10011", "10001", "10001", "10001"},
	'O': {"01110", "10001", "10001", "10001", "10001", "10001", "01110"},
	'P': {"11110", "10001", "10001", "11110", "10000", "10000", "10000"},
	'Q': {"01110", "10001", "10001", "10001", "10101", "10010", "01101"},
	'R': {"11110", "10001", "10001", "11110", "10100", "10010", "10001"},
	'S': {"01111", "10000", "10000", "01110", "00001", "00001", "11110"},
	'T': {"11111", "00100", "00100", "00100", "00100", "00100", "00100"},
	'U': {"10001", "10001", "10001", "10001", "10001", "10001", "01110"},
	'V': {"10001", "10001", "10001", "10001", "10001", "01010", "00100"},
	'W': {"10001", "10001", "10001", "10101", "10101", "11011", "10001"},
	'X': {"10001", "10001", "01010", "00100", "01010", "10001", "10001"},
	'Y': {"10001", "10001", "01010", "00100", "00100", "00100", "00100"},
	'Z': {"11111", "00001", "00010", "00100", "01000", "10000", "11111"},
	'0': {"01110", "10001", "10011", "10101", "11001", "10001", "01110"},
	'1': {"00100", "01100", "00100", "00100", "00100", "00100", "01110"},
	'2': {"01110", "10001", "00001", "00010", "00100", "01000", "11111"},
	'3': {"11111", "00010", "00100", "00010", "00001", "10001", "01110"},
	'4': {"00010", "00110", "01010", "10010", "11111", "00010", "00010"},
	'5': {"11111", "10000", "11110", "00001", "00001", "10001", "01110"},
	'6': {"00110", "01000", "10000", "11110", "10001", "10001", "01110"},
	'7': {"11111", "00001", "00010", "00100", "01000", "01000", "01000"},
	'8': {"01110", "10001", "10001", "01110", "10001", "10001", "01110"},
	'9': {"01110", "10001", "10001", "01111", "00001", "00010", "01100"},
}

// drawGlyph 把一个字符按 5×7 字模放大画到图上。
func drawGlyph(img *image.NRGBA, ch rune, size int, fg color.NRGBA) {
	rows, ok := glyph5x7[ch]
	if !ok {
		return
	}

	// 字模高 7 行,按边长等比放大,再居中。
	cell := size * 5 / 16
	if cell < 1 {
		return
	}
	glyphH := cell * 7 / 5
	x0 := (size - cell) / 2
	y0 := (size - glyphH) / 2

	for gy := range 7 {
		for gx := range 5 {
			if rows[gy][gx] != '1' {
				continue
			}
			for dy := range glyphH {
				for dx := range cell {
					img.SetNRGBA(x0+gx*cell+dx, y0+gy*glyphH+dy, fg)
				}
			}
		}
	}
}

// hslToRGBA 把 HSL 转成 RGBA。
func hslToRGBA(h, s, l float64) color.NRGBA {
	var r, g, b float64

	if s == 0 {
		r, g, b = l, l, l
	} else {
		var q float64
		if l < 0.5 {
			q = l * (1 + s)
		} else {
			q = l + s - l*s
		}
		p := 2*l - q
		r = hueToRGB(p, q, h+1.0/3.0)
		g = hueToRGB(p, q, h)
		b = hueToRGB(p, q, h-1.0/3.0)
	}
	return color.NRGBA{R: to8(r), G: to8(g), B: to8(b), A: 0xFF}
}

func hueToRGB(p, q, t float64) float64 {
	if t < 0 {
		t++
	}
	if t > 1 {
		t--
	}
	switch {
	case t < 1.0/6.0:
		return p + (q-p)*6*t
	case t < 1.0/2.0:
		return q
	case t < 2.0/3.0:
		return p + (q-p)*(2.0/3.0-t)*6
	default:
		return p
	}
}

func to8(v float64) uint8 {
	if v <= 0 {
		return 0
	}
	if v >= 1 {
		return 255
	}
	return uint8(v*255 + 0.5)
}

// AvatarResult 是头像查询结果。
type AvatarResult struct {
	Data     []byte
	Fallback bool
}

// avatarKey 是队列里的任务键。
type avatarKey struct {
	profileID uuid.UUID
	size      int
}

// AvatarService 负责头像的缓存与异步渲染。
type AvatarService struct {
	queries     *query.Queries
	pool        *db.Pool
	store       storage.Storage
	texture     *TextureService
	logger      Logger
	defaultSize int

	queue   chan avatarKey
	closed  chan struct{}
	once    sync.Once
	wg      sync.WaitGroup
	pending sync.Map
}

// AvatarOptions 是头像服务的构造参数。
type AvatarOptions struct {
	QueueSize   int
	DefaultSize int
	Workers     int
	Logger      Logger
}

// NewAvatarService 创建头像服务并启动 worker。
func NewAvatarService(pool *db.Pool, store storage.Storage, textures *TextureService, opts AvatarOptions) *AvatarService {
	if opts.QueueSize <= 0 {
		opts.QueueSize = 256
	}
	if opts.DefaultSize <= 0 {
		opts.DefaultSize = 64
	}
	if opts.Workers <= 0 {
		opts.Workers = 2
	}

	svc := &AvatarService{
		queries:     query.New(pool),
		pool:        pool,
		store:       store,
		texture:     textures,
		logger:      opts.Logger,
		queue:       make(chan avatarKey, opts.QueueSize),
		defaultSize: opts.DefaultSize,
		closed:      make(chan struct{}),
	}

	for range opts.Workers {
		svc.wg.Add(1)
		go svc.worker()
	}
	return svc
}

// Avatar 返回头像。
//
// **主请求路径上不执行任何像素运算。** 命中缓存直接返回;
// 未命中提交任务后立刻返回默认头像。这是本设计存在的全部理由:
// 渲染要解码一张 PNG 再放大,单个请求几百毫秒,足以让并发一上来
// 就把 worker 池耗尽。
func (s *AvatarService) Avatar(ctx context.Context, profileID uuid.UUID, name string, size int) (AvatarResult, error) {
	if size <= 0 {
		size = s.defaultSize
	}

	skinHash := ""
	if s.texture != nil {
		if tex, found, texErr := s.texture.TextureFor(ctx, profileID, TextureSkin); texErr == nil && found {
			skinHash = tex.Hash
		}
	}

	row, err := s.queries.GetAvatar(ctx, profileID)
	switch {
	case err == nil:
		// 哈希一致说明这张头像是从当前皮肤渲染的,直接用。
		if skinHash != "" && row.Hash == skinHash {
			if data, getErr := s.store.Get(ctx, storage.AvatarKey(profileID.String())); getErr == nil {
				return AvatarResult{Data: data}, nil
			}
			// 表里有记录但文件没了:清掉标记,重新排一次渲染。
			// 直接返回默认头像会让这张头像永远停在默认状态。
			s.forgetAvatar(ctx, profileID)
		}
	case dbNotFound(err):
		// 尚未渲染,正常路径
	default:
		s.logf("查询头像缓存失败", "profile_id", profileID, "error", err)
	}

	s.submit(avatarKey{profileID: profileID, size: size})
	return AvatarResult{Data: defaultAvatar(profileID, name, size), Fallback: true}, nil
}

// forgetAvatar 清掉失效的头像缓存标记。
func (s *AvatarService) forgetAvatar(ctx context.Context, profileID uuid.UUID) {
	if _, err := s.queries.DeleteAvatar(ctx, profileID); err != nil {
		s.logf("清除头像缓存标记失败", "profile_id", profileID, "error", err)
	}
	if err := s.store.Delete(ctx, storage.AvatarKey(profileID.String())); err != nil {
		s.logf("清除头像文件失败", "profile_id", profileID, "error", err)
	}
}

// submit 把渲染任务放进队列。
//
// **队列满时直接丢弃**,不阻塞。阻塞在这里等于把「渲染慢」变成
// 「整个皮肤站不可用」—— 而降级到默认头像本来就是完全可接受的。
func (s *AvatarService) submit(key avatarKey) {
	if _, busy := s.pending.LoadOrStore(key, true); busy {
		return
	}

	select {
	case s.queue <- key:
	default:
		// 队列满:任务被丢弃,但必须把 pending 清掉,
		// 否则这个玩家这个尺寸的渲染**永远**不会再被排上队 ——
		// 一次性的拥塞会变成永久性的功能缺失。
		s.pending.Delete(key)
		s.logf("头像渲染队列已满,本次降级为默认头像",
			"profile_id", key.profileID, "size", key.size)
	}
}

func (s *AvatarService) worker() {
	defer s.wg.Done()

	for {
		select {
		case <-s.closed:
			return
		case key := <-s.queue:
			s.renderOne(key)
		}
	}
}

func (s *AvatarService) renderOne(key avatarKey) {
	defer s.pending.Delete(key)

	ctx := context.Background()
	skin, err := s.texture.TextureBytes(ctx, key.profileID, TextureSkin)
	if err != nil {
		s.logf("读取皮肤失败,跳过渲染", "profile_id", key.profileID, "error", err)
		return
	}

	avatar, err := renderSkin(skin, key.size)
	if err != nil {
		s.logf("渲染头像失败", "profile_id", key.profileID, "error", err)
		return
	}

	// 先落文件再写标记。反过来的话,标记在而文件不在,
	// 每次请求都会走「文件没了 → 清标记 → 排渲染」的循环,
	// 渲染 worker 会被这个循环持续喂满。
	if err := s.store.Put(ctx, storage.AvatarKey(key.profileID.String()), avatar); err != nil {
		s.logf("写入头像文件失败", "profile_id", key.profileID, "error", err)
		return
	}

	skinHash := ""
	if tex, found, texErr := s.texture.TextureFor(ctx, key.profileID, TextureSkin); texErr == nil && found {
		skinHash = tex.Hash
	}
	if err := s.queries.UpsertAvatar(ctx, query.UpsertAvatarParams{
		ProfileID: key.profileID,
		Hash:      skinHash,
	}); err != nil {
		s.logf("写入头像缓存标记失败", "profile_id", key.profileID, "error", err)
	}
}

// Close 停掉 worker。
func (s *AvatarService) Close() {
	s.once.Do(func() {
		close(s.closed)
		s.wg.Wait()
	})
}

func (s *AvatarService) logf(msg string, args ...any) {
	if s.logger != nil {
		s.logger.Warn(msg, args...)
	}
}

// defaultAvatarFor 由任意标识串生成默认头像。
//
// 用于「档案还没建出来」的场景 —— 请求里的 id 可能对应一个从未
// 登录过的玩家。用字符串哈希而非 UUID 哈希,是为了让这种降级
// 在外观上与正常路径一致。
func defaultAvatarFor(idOrName string, size int) []byte {
	h := fnv.New32a()
	_, _ = h.Write([]byte(idOrName))

	// 拆成两个 16 位再格式化:%08x-%08x 会拼出 8+8 个十六进制字符,
	// 正好是 16 位 —— 但写成两个 %08x 加一个 4 段的模板时,
	// 很容易在某一处多写一位,让 uuid.Parse 直接 panic。
	// 用 uint16 截断可以从类型上排除这种错误。
	var b [16]byte
	binary.BigEndian.PutUint32(b[0:4], h.Sum32())
	binary.BigEndian.PutUint32(b[4:8], h.Sum32())
	b[8] = byte(h.Sum32())
	b[9] = byte(h.Sum32() >> 8)
	b[10] = 0x4a
	b[11] = 0x59

	return defaultAvatar(uuid.UUID(b), idOrName, size)
}

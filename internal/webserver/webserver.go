// Package webserver 把两个 SPA 的构建产物嵌入二进制并对外提供静态服务。
//
// 为什么要 embed:部署一个二进制 + 一个数据目录就够,不需要再单独
// 准备一份前端静态文件。两者的版本天然对齐 ——
// 「前端发新版、后端没更新」这种不一致在运维里非常常见,
// 而且症状是「按钮点不动」,排查方向完全跑偏。
//
// 为什么不用 embed.FS 直接匹配:两个 SPA 共用一个二进制,
// 但它们的路径前缀不同(account 在 /,admin 在 /admin),
// 且都需要 SPA 回退(index.html)。手写一套路由比绕开它更清楚。
package webserver

import (
	"bytes"
	"embed"
	"io/fs"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/yggauth/yggauth/internal/platform/httpx"
)

// assets 是构建产物。
//
// 用 //go:embed all:dist 保证子目录被打进来:
// 默认的 embed 会跳过以下划线或点开头的目录,加上 all: 之后
// 哈希命名的 assets/ 才能被带上 —— 而那正是 Vite 的输出目录。
//
//go:embed all:dist
var assets embed.FS

// SPA 是一个前端的静态资源树。
type SPA struct {
	// prefix 是挂载前缀。空串表示挂在根上。
	prefix string
	// fs 是该 SPA 的资源树,根已剥到 index.html 所在层。
	fsys fs.FS
	// name 用于日志。
	name string
}

// account 是终端用户的账号中心。
var account = mustLoad("account", "")

// admin 是管理后台。
var admin = mustLoad("admin", "/admin")

func mustLoad(name, prefix string) *SPA {
	sub, err := fs.Sub(assets, "dist/"+name)
	if err != nil {
		// 只有在构建产物缺失时才走到这里,而那说明镜像构建漏了
		// 前端构建步骤。直接 panic 比返回一个永远 404 的服务好 ——
		// 后者会让人以为是路由配错了。
		panic("前端构建产物缺失: " + err.Error() + "(需要先执行 pnpm build)")
	}
	return &SPA{prefix: prefix, fsys: sub, name: name}
}

// Mount 把两个 SPA 挂到 r 上。
//
// 顺序有意义:先挂 API 与协议前缀,再挂静态资源。
// 静态资源的回退 handler 什么路径都接受,放在前面会把
// /oauth/token 之类的请求也吞掉。
func Mount(r chi.Router) {
	account.mount(r)
	admin.mount(r)
}

// mount 挂载单个 SPA。
func (s *SPA) mount(r chi.Router) {
	// 只注册 "/*",不单独注册 "/"。
	//
	// chi 的 RedirectSlashes 在 "/" 与 "/*" 同时存在时会对根路径发出
	// Location: ./ 的**自指**重定向:浏览器要么一直重定向下去,
	// 要么直接报「重定向次数过多」。而 "/*" 本身就匹配 "/",
	// 多注册一条反而制造了这个问题。
	handler := s.handler()
	if s.prefix == "" {
		r.Handle("/*", handler)
		return
	}

	r.Route(s.prefix, func(r chi.Router) {
		r.Handle("/*", handler)
	})
}

// handler 返回该 SPA 的静态资源处理器。
//
// 不用 http.FileServer:它对目录一律发 301 到 "./",
// 而 SPA 的所有未知路径在语义上都是「同一个页面」,
// 每一次重定向都是一次白白的多余往返,某些客户端还会直接放弃。
// 自己读文件既精确又少一跳。
func (s *SPA) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 协议与 API 前缀一律让给后端,哪怕对应处理器没装配。
		//
		// 这个让位是必要的:某个部署可能只装了账号内核而没装 OIDC,
		// 此时 /oauth/par 本该得到一个 JSON 404。��� SPA 把它回退成
		// index.html,调用方拿到的是一段 HTML,错误信息完全丢失 ——
		// 排查时只能一路追「为什么这个接口返回了一坨前端代码」。
		if isBackendPrefix(r.URL.Path, s.prefix) {
			// 交给后端统一的 404 处理,而不是 http.NotFound ——
			// 后者吐的是纯文本,与 API 其余部分的响应包格式不一致。
			httpx.NotFound(w, r)
			return
		}

		upath := strings.TrimPrefix(r.URL.Path, s.prefix)
		upath = strings.TrimPrefix(upath, "/")

		if upath == "" || upath == "index.html" {
			s.serveIndex(w, r)
			return
		}

		data, err := fs.ReadFile(s.fsys, upath)
		if err != nil {
			// 未知路径回退到 index.html:刷新 /security 这样的深链接
			// 必须能打开页面,而不是拿到一个 404。
			s.serveIndex(w, r)
			return
		}

		s.setCacheHeaders(w, upath)
		http.ServeContent(w, r, filepath.Base(upath), time.Time{}, bytes.NewReader(data))
	})
}

// serveIndex 输出应用外壳。
func (s *SPA) serveIndex(w http.ResponseWriter, r *http.Request) {
	data, err := fs.ReadFile(s.fsys, "index.html")
	if err != nil {
		// 构建产物损坏。这不是 404,而是服务端自己的问题 ——
		// 明确报 500 比吐一个空白页有用得多。
		http.Error(w, "前端构建产物缺失", http.StatusInternalServerError)
		return
	}

	s.setCacheHeaders(w, "index.html")
	http.ServeContent(w, r, "index.html", time.Time{}, bytes.NewReader(data))
}

// setCacheHeaders 设置缓存策略。
//
// index.html 绝不能长缓存:它引用的是带哈希的 assets 文件名,
// 缓存住它等于让用户永远拿不到新版。带哈希的资源反而应该
// 永久缓存 —— 内容变了文件名就变了。
func (s *SPA) setCacheHeaders(w http.ResponseWriter, path string) {
	if strings.HasSuffix(path, ".html") {
		w.Header().Set("Cache-Control", "no-cache")
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
}

// Available 报告构建产物是否存在。
//
// 供启动自检使用:与其等到用户点不开页面才发现前端没打进去,
// 不如在启动日志里明说。
func Available() (accountOK, adminOK bool) {
	_, errAccount := assets.Open("dist/account/index.html")
	_, errAdmin := assets.Open("dist/admin/index.html")
	return errAccount == nil, errAdmin == nil
}

// backendPrefixes 是归后端所有的路径前缀。
//
// 第一个元素是 admin SPA 的挂载前缀 —— 管理后台自己的路由必须
// 由它自己处理,不能被账号站的 SPA 回退吃掉。
var backendPrefixes = []string{"/api", "/oauth", "/mc", "/sso", "/health", "/metrics", "/admin", "/device"}

// isBackendPrefix 判断路径是否属于后端。
func isBackendPrefix(path, spaPrefix string) bool {
	// 剥掉本 SPA 自己的前缀再判断:挂在 /admin 上的后台 SPA,
	// 它的 /admin/clients 是自己的页面而不是后端接口。
	rel := strings.TrimPrefix(path, spaPrefix)
	if rel == "" {
		// path 恰好等于本 SPA 的挂载前缀(如 /admin)—— 那是它自己的根,
		// 不是后端路径。
		//
		// 这里**不能**回退成完整 path:那样 /admin 会撞上 backendPrefixes
		// 里的 "/admin",于是后台首页被判成后端路径、返回 JSON 404。
		// 挂载在根上的 SPA(spaPrefix 为空)不会走到这里 ——
		// TrimPrefix 对空前缀是空操作,rel 不可能为空。
		return false
	}

	for _, prefix := range backendPrefixes {
		if rel == prefix || strings.HasPrefix(rel, prefix+"/") {
			return true
		}
	}
	return false
}

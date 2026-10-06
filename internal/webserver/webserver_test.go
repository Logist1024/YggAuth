package webserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// serve 用某个 SPA 的 handler 真跑一次请求。
//
// 这里用 context.Background() 而不是 t.Context():serve 是个无状态小工具,
// 把 *testing.T 传进来只是为了满足 linter,得不偿失。
func serve(spa *SPA, target string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	spa.handler().ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, target, nil))
	return rec
}

// 深链接必须拿到应用外壳 —— 刷新 /security 这样的页面是日常操作。
func TestSPADeepLinkServesShell(t *testing.T) {
	cases := []struct {
		spa    *SPA
		target string
	}{
		{account, "/"},
		{account, "/security"},
		{account, "/verify-email"},
		{admin, "/admin"},
		{admin, "/admin/clients"},
	}
	for _, c := range cases {
		rec := serve(c.spa, c.target)
		if rec.Code != http.StatusOK {
			t.Errorf("%s: 状态码 = %d,想要 200", c.target, rec.Code)
		}
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
			t.Errorf("%s: Content-Type = %q,想要 text/html", c.target, ct)
		}
		if !strings.Contains(rec.Body.String(), `id="app"`) {
			t.Errorf("%s: 返回的不是 index.html 外壳", c.target)
		}
	}
}

// 缺失的静态资源要 404,不能回退成 index.html。
//
// 回退会让浏览器把 HTML 当 JS/CSS 解析,报出「Unexpected token '<'」——
// 而真实原因往往是部署后老页面还开着、指着已经删掉的旧哈希文件,
// 被这条错完全掩盖;200 + HTML 在 vite 的 preload 眼里还是一次成功加载,
// error 分支根本不触发。
func TestSPAMissingAssetIs404(t *testing.T) {
	cases := []struct {
		spa    *SPA
		target string
	}{
		{account, "/assets/does-not-exist.js"},
		{account, "/assets/index-deadbeef.css"},
		{account, "/favicon-missing.svg"},
		{admin, "/admin/assets/nope.js"},
	}
	for _, c := range cases {
		rec := serve(c.spa, c.target)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: 状态码 = %d,想要 404", c.target, rec.Code)
		}
		if ct := rec.Header().Get("Content-Type"); strings.HasPrefix(ct, "text/html") {
			t.Errorf("%s: 缺失资源返回了 HTML(Content-Type = %q)", c.target, ct)
		}
	}
}

// 后端前缀不许被 SPA 回退吃掉:API 拿到 HTML 是最难查的一类问题。
func TestSPAKeepsBackendPrefix(t *testing.T) {
	rec := serve(account, "/api/definitely-not-a-route")
	if rec.Code != http.StatusNotFound {
		t.Errorf("/api 下的未知路径: 状态码 = %d,想要 404", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("/api 下的未知路径: Content-Type = %q,想要 JSON 错误包", ct)
	}
}

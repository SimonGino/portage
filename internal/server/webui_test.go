//go:build webui

// 这一份只在 `-tags webui` 下编译，也就是「二进制里真的 embed 了前端」的那种构建。
// 它守的是一类**只在 embed 后才出现**的故障：`npm run dev` 一切正常，装进容器里
// 白屏。默认构建（CI、没装 Node 的机器）不编译这个文件，所以 `go test ./...`
// 照常能过。
//
// 跑法：make build-ui 之后 `go test -tags webui ./internal/server`

package server_test

import (
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/SimonGino/portage/internal/gatewaytest"
)

// assetRef 从 index.html 里抠出它引用的资源地址。
var assetRef = regexp.MustCompile(`(?:src|href)="(/[^"]+\.(?:js|css))"`)

// TestEmbeddedUIReferencesAdminPrefixedAssets 是 base 路径那个坑的哨兵。
//
// Vite 默认 base 是 `/`，产物里的 index.html 会去请求 /assets/index-xxx.js。
// 而网关只在 /admin 下发静态文件，那些请求全 404，页面白屏——且这个故障
// 只在 embed 后的二进制里出现，前端开发时完全看不见。
func TestEmbeddedUIReferencesAdminPrefixedAssets(t *testing.T) {
	g := gatewaytest.Start(t, gatewaytest.NewDB(t))

	body, ct := getOK(t, g.URL+"/admin")
	if !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("/admin 的 Content-Type = %q，期望 text/html", ct)
	}

	refs := assetRef.FindAllStringSubmatch(body, -1)
	if len(refs) == 0 {
		t.Fatalf("index.html 里一个 js/css 引用都没有，前端多半没构建完整：\n%s", body)
	}
	for _, m := range refs {
		ref := m[1]
		if !strings.HasPrefix(ref, "/admin/") {
			t.Errorf("资源引用 %q 不在 /admin/ 下——vite.config.ts 的 base 掉了，装进容器会白屏", ref)
			continue
		}
		_, assetCT := getOK(t, g.URL+ref)
		// .js 必须是 text/javascript：判成 text/plain 的话浏览器直接拒绝执行模块，
		// 报的错是「MIME type not executable」，跟前端代码毫无关系。
		switch {
		case strings.HasSuffix(ref, ".js") && !strings.HasPrefix(assetCT, "text/javascript"):
			t.Errorf("%s 的 Content-Type = %q，期望 text/javascript", ref, assetCT)
		case strings.HasSuffix(ref, ".css") && !strings.HasPrefix(assetCT, "text/css"):
			t.Errorf("%s 的 Content-Type = %q，期望 text/css", ref, assetCT)
		}
	}
}

// TestEmbeddedUIServesDeepLinks：SPA 的深链接直接刷新要回同一份 index.html，
// 前端路由再自己接管。回 404 的话，「在 /admin/keys 上按 F5」就废了。
func TestEmbeddedUIServesDeepLinks(t *testing.T) {
	g := gatewaytest.Start(t, gatewaytest.NewDB(t))

	root, _ := getOK(t, g.URL+"/admin")
	for _, p := range []string{"/admin/keys", "/admin/access-points", "/admin/usage", "/admin/whatever"} {
		body, ct := getOK(t, g.URL+p)
		if !strings.HasPrefix(ct, "text/html") {
			t.Errorf("%s 的 Content-Type = %q，期望 text/html", p, ct)
		}
		if body != root {
			t.Errorf("%s 发的不是 /admin 那一份 index.html", p)
		}
	}
}

func getOK(t *testing.T, url string) (body, contentType string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("构造请求失败: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s 失败: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s = %d，期望 200", url, resp.StatusCode)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("读 %s 的响应失败: %v", url, err)
	}
	return string(raw), resp.Header.Get("Content-Type")
}

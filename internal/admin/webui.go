package admin

import (
	"io"
	"io/fs"
	"net/http"
	"path"
	"strings"

	"github.com/SimonGino/portage/internal/webui"

	"github.com/gin-gonic/gin"
)

// notBuiltPage 是没带前端的二进制在 /admin 上回的东西。
//
// 回一句人话而不是 404：`go build ./cmd/portage` 编出来的二进制默认就不含前端，
// 而 404 会让人以为路由挂错了，跑去翻 Mount。
const notBuiltPage = `<!doctype html><meta charset="utf-8">
<title>Portage 管理端</title>
<body style="font:14px/1.7 -apple-system,system-ui,sans-serif;max-width:44em;margin:4em auto;padding:0 1em">
<h1>管理端未编译进此二进制</h1>
<p>这个二进制是用默认构建编出来的，不含管理端前端。转发功能不受影响。</p>
<p>要带上管理端：</p>
<pre style="background:#f5f5f5;padding:1em;overflow-x:auto">make build-ui        # 或者 cd web &amp;&amp; npm ci &amp;&amp; npm run build
go build -tags webui -o portage ./cmd/portage</pre>
<p>Docker 镜像（<code>deploy/docker-compose.yml</code>）默认就是带管理端的构建。</p>
<p>开发前端时不用编 Go：<code>cd web &amp;&amp; npm run dev</code>，Vite 会把 /admin/api 代理到本网关。</p>
</body>`

// mountUI 把管理端 SPA 挂到 /admin 下。
//
// 走 NoRoute 而不是 r.Static("/admin", ...)：SPA 的深链接（/admin/keys 直接刷新）
// 必须回同一份 index.html，而 gin 的路由树不允许 `/admin/*filepath` 与已经注册的
// `/admin/api/...` 并存——注册时就 panic，不是运行期 404。
func (h *Handler) mountUI(r *gin.Engine) {
	assets, ok := webui.Files()

	r.NoRoute(func(c *gin.Context) {
		p := c.Request.URL.Path
		// 分开判 `/admin` 与 `/admin/` 前缀，而不是一句 HasPrefix(p, "/admin")：
		// 后者连 /administrator 这种毫不相干的路径也会吞掉，回一页管理端 HTML。
		if p != "/admin" && !strings.HasPrefix(p, "/admin/") {
			// 不是管理端的路径就照常 404。NoRoute 是全局的，这里必须放行，
			// 否则 /v1/ 上的笔误会拿到一个管理端页面。
			c.Status(http.StatusNotFound)
			return
		}
		// /admin/api 下的未知路径也不该回 HTML：前端拿到 200 + HTML 会在
		// JSON.parse 上炸，报出来的错跟真正的原因（接口写错了）毫无关系。
		if strings.HasPrefix(p, "/admin/api") {
			fail(c, http.StatusNotFound, "接口不存在")
			return
		}
		if !ok {
			c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(notBuiltPage))
			return
		}
		serveSPA(c, assets, strings.TrimPrefix(strings.TrimPrefix(p, "/admin"), "/"))
	})
}

// serveSPA 发一个静态文件；文件不存在就发 index.html，交给前端路由。
func serveSPA(c *gin.Context, assets fs.FS, name string) {
	if name == "" || !exists(assets, name) {
		name = "index.html"
	}
	f, err := assets.Open(name)
	if err != nil {
		c.Status(http.StatusNotFound)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		c.Status(http.StatusInternalServerError)
		return
	}
	// 带 hash 的资源名（Vite 默认产物）可以长缓存；index.html 绝不能，否则改了前端
	// 之后浏览器还拿着旧的 index 去引用已经不存在的 assets 文件名，页面直接白屏。
	if name == "index.html" {
		c.Header("Cache-Control", "no-cache")
	} else {
		c.Header("Cache-Control", "public, max-age=31536000, immutable")
	}
	// Content-Type 必须自己先写好：ServeContent 只在头里没有它的时候才去猜，
	// 而它猜的办法正是 mime.TypeByExtension——那就绕回上面 contentType 要躲开的
	// 「同一份二进制在两台机器上判得不一样」了。
	c.Header("Content-Type", contentType(name))
	// ReadSeeker 走 http.ServeContent 才有 Range 与 If-Modified-Since；embed.FS
	// 的文件满足这个接口。不满足时退回整份发出去。
	if rs, ok := f.(io.ReadSeeker); ok {
		http.ServeContent(c.Writer, c.Request, path.Base(name), info.ModTime(), rs)
		return
	}
	c.DataFromReader(http.StatusOK, info.Size(), contentType(name), f, nil)
}

func exists(assets fs.FS, name string) bool {
	f, err := assets.Open(name)
	if err != nil {
		return false
	}
	defer f.Close()
	info, err := f.Stat()
	return err == nil && !info.IsDir()
}

// contentType 只覆盖 Vite 产物里实际出现的那几种。
//
// 不用 mime.TypeByExtension：它在不同系统上读 /etc/mime.types，同一份二进制在
// 两台机器上可能给出不同结果——.js 被判成 text/plain 时浏览器直接拒绝执行模块。
func contentType(name string) string {
	switch path.Ext(name) {
	case ".html":
		return "text/html; charset=utf-8"
	case ".js", ".mjs":
		return "text/javascript; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".json":
		return "application/json"
	case ".svg":
		return "image/svg+xml"
	case ".woff2":
		return "font/woff2"
	case ".png":
		return "image/png"
	case ".ico":
		return "image/x-icon"
	}
	return "application/octet-stream"
}

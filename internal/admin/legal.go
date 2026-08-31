package admin

// legal.go 是 /privacy 与 /terms 两张站点级静态页。它们存在的直接原因是 Google
// OAuth 应用切「正式版」硬性要求一个公开可访问的隐私权政策网址（品牌验证还要
// 服务条款），Portage 此前没有任何门外可看的说明页。
//
// 服务端直出整页 HTML 而不进 SPA：这两页给的是「还没登录、可能永远不登录」的人
// （以及 Google 的审核爬虫），不该背上整个管理端 bundle；内容是编译期常量，
// 也没有任何运行期状态可注水。样式内联羊皮纸基调，与管理端同族但不共享 CSS——
// 共享意味着这两页要跟着前端构建走，而它们恰恰该在 `-tags webui` 缺席时也在。

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

const legalStyle = `<style>
  body { margin: 0; background: #f5f4ed; color: #2b2820; font: 15px/1.75 -apple-system, "Segoe UI", "PingFang SC", "Microsoft YaHei", sans-serif; }
  main { max-width: 680px; margin: 0 auto; padding: 56px 24px 72px; }
  h1 { font-family: Georgia, "Songti SC", "SimSun", serif; font-size: 28px; letter-spacing: -0.02em; margin: 0 0 6px; }
  h2 { font-size: 17px; margin: 32px 0 8px; }
  p, li { margin: 8px 0; color: #4a463c; }
  .meta { color: #8a8474; font-size: 13px; margin-bottom: 28px; }
  a { color: #1b365d; }
</style>`

const privacyHTML = `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1"><title>隐私权政策 · Portage</title>` + legalStyle + `</head><body><main>
<h1>隐私权政策</h1>
<p class="meta">适用于本 Portage 实例（个人运营的 AI 模型网关）。</p>
<h2>我们收集什么</h2>
<ul>
<li><b>账号信息</b>：注册邮箱、展示名。使用 GitHub / Google 登录时，我们从对方处获取你的已验证邮箱、名字与账号标识，用于创建和关联账号；<b>不获取、不存储</b>对方的访问令牌。</li>
<li><b>使用记录</b>：API 调用的模型名、token 用量、时间与所用的 API Key，用于用量统计与配额管理。</li>
<li><b>会话</b>：登录会话使用 Cookie 维持，仅用于鉴权。</li>
</ul>
<h2>请求内容如何处理</h2>
<p>你通过本网关发出的模型请求会转发至所选的上游模型服务商，内容按该服务商的条款处理；本站记录用量元数据，不以展示为目的存储你的请求正文。</p>
<h2>我们不做什么</h2>
<p>不出售、不出租、不与无关第三方共享你的任何信息；不投放广告与跟踪。</p>
<h2>数据删除</h2>
<p>如需删除账号及其关联数据，请联系为你发放邀请码的站点管理员。</p>
</main></body></html>`

const termsHTML = `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1"><title>服务条款 · Portage</title>` + legalStyle + `</head><body><main>
<h1>服务条款</h1>
<p class="meta">适用于本 Portage 实例（个人运营的 AI 模型网关）。</p>
<h2>服务性质</h2>
<p>本站是个人运营的非商业服务，仅面向受邀用户，按「现状」提供，不作可用性承诺，可能随时变更或中止。</p>
<h2>使用约束</h2>
<ul>
<li>API Key 仅限本人使用，请妥善保管；经由你的 Key 发生的调用计入你的用量与配额。</li>
<li>不得将服务用于违法用途，或对网关及上游服务进行滥用（含规避配额、批量爬取等）。</li>
</ul>
<h2>管理</h2>
<p>管理员可设定配额、停用违规账号。对上游模型服务的使用同时受各服务商自身条款约束。</p>
</main></body></html>`

func (h *Handler) privacyPage(c *gin.Context) {
	c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(privacyHTML))
}

func (h *Handler) termsPage(c *gin.Context) {
	c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(termsHTML))
}

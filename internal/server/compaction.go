package server

import (
	"net/http"

	"github.com/SimonGino/portage/internal/protocol"

	"github.com/gin-gonic/gin"
)

// Codex remote compaction 的透传半边闸（口径层 v0.54）原先在本文件；#54 之后判据收进
// openairesponses 的 RequestInspector，闸本身合并到 inspect.go。这里只剩 v1 compact 的
// 501 路由。

// compactUnsupported 应付 legacy 的 `POST /v1/responses/compact`（口径层 v0.54 裁定
// 501，不实现）。
//
// 之前它落在 NoRoute 上，被管理端的 SPA fallback 接走，客户端拿到的是一页 HTML 或一个
// 裸 404——两样都读不出「这个网关不做这件事」。501 是**明确拒绝**，与「路径打错了」
// 分得开。
//
// 不挂鉴权与流水中间件：它无条件回同一句话，既不碰上游也不碰库，鉴权只会让一个想
// 弄明白「这个端点到底有没有」的人先撞 401。
func compactUnsupported(c *gin.Context) {
	protocol.OpenAIResponses.WriteError(c.Writer, http.StatusNotImplemented,
		"网关不支持 v1 compact：请用 Codex 自带的压缩流程（input 带 compaction_trigger 的请求），并确认渠道已声明支持 Codex 压缩")
}

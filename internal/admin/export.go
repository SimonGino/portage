package admin

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/SimonGino/portage/internal/declcfg"

	"github.com/gin-gonic/gin"
)

// exportConfig 把库里的业务配置导成一份 channels.yaml 下载（口径层 §2.9 #32）。
//
// **沿用现有会话，不加 step-up**：登录者今天已经能批量读到全部秘密——凭证明文回读
// （v0.47）、`GET /keys` 直接带 key_plain——多一道口令只是仪式感，还得为它维护第二套
// 凭据。真正要守的是**别开第二条不鉴权的出口**：这个端点注册在 auth 组里，且没有
// CLI 导出子命令（v1 不做），于是拿到配置全文的路径有且只有「登进管理面」这一条。
//
// 响应体里是**明文秘密**，所以 no-store：它不该躺在任何中间层或磁盘缓存里。
func (h *Handler) exportConfig(c *gin.Context) {
	raw, skipped, err := declcfg.Export(c.Request.Context(), h.db)
	if err != nil {
		h.log.Error("导出声明文件失败", "err", err)
		// 这里回显 err 本身：导出的失败模式只有「哪几把 key 拿不到原值」这一类，
		// 报文里是实体名不是秘密值，而不报名字的话人根本不知道去删哪几把。
		fail(c, http.StatusInternalServerError, fmt.Sprintf("导出失败：%v", err))
		return
	}
	// 多用户库导出跳过非 admin 名下的 key（口径层 v1.04）：跳过不是丢，但必须
	// 明说——逐把记进日志，别让「文件里少了几把」无声发生。
	if len(skipped) > 0 {
		h.log.Info("导出略过了用户名下的 API Key（声明文件表达不了归属，不进文件）", "keys", strings.Join(skipped, "、"))
	}
	c.Header("Cache-Control", "no-store")
	c.Header("Content-Disposition", `attachment; filename="channels.yaml"`)
	c.Data(http.StatusOK, "application/yaml; charset=utf-8", raw)
}

package admin

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/SimonGino/portage/internal/selfupdate"

	"github.com/gin-gonic/gin"
)

// applyUpgrade 是 upgrade 接口的默认出口：替换的是本进程自己的可执行文件，下载前缀读
// **网关进程**的 PORTAGE_DOWNLOAD_BASE（口径层 v1.38 ⑪）。
func applyUpgrade(ctx context.Context, version string) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("%w: %v", selfupdate.ErrNotWritable, err)
	}
	return selfupdate.Apply(ctx, os.Getenv("PORTAGE_DOWNLOAD_BASE"), version, exe)
}

// upgrade 是 binary 形态的一键升级（口径层 v1.38 ⑥，展开层 §7.11）：**同步**做完下载 →
// 校验 → 替换，200 之后由 onUpgraded 通知 main 收场并 Exec。
//
// 不进声明文件的写闸：升级不是业务配置写（§7.11）。错误词表五个，前端按词落文案。
func (h *Handler) upgrade(c *gin.Context) {
	if h.distro == "docker" {
		fail(c, http.StatusBadRequest, "unsupported_distro")
		return
	}
	var in struct {
		Version string `json:"version"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	v := strings.TrimPrefix(in.Version, "v")
	if !selfupdate.Valid(v) {
		fail(c, http.StatusBadRequest, "版本号须为 x.y.z")
		return
	}
	// 成功之后**不复位**：进程已在收场，再来一次也该是 already_running。
	if !h.upgrading.CompareAndSwap(false, true) {
		fail(c, http.StatusConflict, "already_running")
		return
	}
	if err := h.apply(c.Request.Context(), v); err != nil {
		h.upgrading.Store(false)
		h.log.Error("升级失败", "version", v, "err", err)
		switch {
		case errors.Is(err, selfupdate.ErrNotWritable):
			fail(c, http.StatusConflict, "not_writable")
		case errors.Is(err, selfupdate.ErrChecksum):
			fail(c, http.StatusBadGateway, "checksum_mismatch")
		default:
			fail(c, http.StatusBadGateway, "download_failed")
		}
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
	// 收场由 srv.Shutdown 等在途请求，这一条响应也在其中，先回后通知不会被掐。
	if h.onUpgraded != nil {
		h.onUpgraded(v)
	}
}

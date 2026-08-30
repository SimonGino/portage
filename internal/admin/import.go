package admin

import (
	"errors"
	"io"
	"net/http"

	"github.com/SimonGino/portage/internal/declcfg"

	"github.com/gin-gonic/gin"
)

// maxImportBytes 兜住导入的请求体。声明文件是个位数渠道的 YAML，几十 KB 到头；
// 4MB 是「永远碰不到但挡得住手滑传错文件」的量级——比如把一份数据库或日志当成
// channels.yaml 选进来。
const maxImportBytes = 4 << 20

// readDeclaredFile 读请求体（4MB 闸）并严格解析成声明文件。导入与试算两条路共用——
// 试算要预演的就是导入那条链路，入口的两个闸（体积、解析）差一个就不叫预演。
// 返回 false 时响应已写完，调用方直接 return。
func readDeclaredFile(c *gin.Context) (*declcfg.File, bool) {
	raw, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, maxImportBytes))
	if err != nil {
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			fail(c, http.StatusRequestEntityTooLarge, "导入内容超过 4MB——channels.yaml 到不了这个量级，检查是不是选错了文件")
			return nil, false
		}
		fail(c, http.StatusBadRequest, "读取请求体失败")
		return nil, false
	}
	f, err := declcfg.Parse(raw, "(导入)")
	if err != nil {
		fail(c, http.StatusBadRequest, err.Error())
		return nil, false
	}
	return f, true
}

// importConfig 把一份 channels.yaml 一次性导入进库（#59）：请求体就是文件原文，
// 走启动 apply 同一条链路——严格解析（KnownFields）→ 自校验 → 单事务 reconcile →
// 库校验 → 提交，失败整份回滚、一次报全。**覆盖语义**与启动 apply 完全一致：文件里
// 没有的业务实体删掉，不另发明一套「合并」。
//
// 导入成功后**不切事实源**：DB 仍为准，管理端照常可写——这是一次性动作，不留任何
// 形态旗。声明文件形态（挂了 -channels 且开着管理面）下这个接口注册在写闸组里，
// 与其余业务配置写接口同一句 409：导进去的改动活不过下次重启 apply，「导入成功」
// 是假象（PO 2026-08-28 裁定）。
//
// 错误分两类：ConfigError（闸一/闸二打回）是校验原文、翻 400 原样回显——那段话
// 写成了「哪个实体、为什么、怎么补」的样子，重新包装只会丢信息；其余（事务开启/
// 提交）翻 500，文案不带细节。
func (h *Handler) importConfig(c *gin.Context) {
	f, ok := readDeclaredFile(c)
	if !ok {
		return
	}
	// 多用户闸（#66 ⑤）：库里有第一个 admin 之外的用户名下的 key 时拒绝导入——
	// 覆盖语义会把那些 key 静默清光，那是事故不是纪律。409 与写闸同款：不是参数错
	// （400 会引人改文件），是「库的状态与这套形态冲突」。启动 apply 不走这道闸，
	// 挂文件是显式切事实源（#66 ③），所以闸在这儿不在 declcfg.Apply 里。
	if err := declcfg.CheckSingleUser(c.Request.Context(), h.db); err != nil {
		fail(c, http.StatusConflict, err.Error())
		return
	}
	changes, err := declcfg.Apply(c.Request.Context(), h.db, f, h.log)
	if err != nil {
		if ce, ok := errors.AsType[declcfg.ConfigError](err); ok {
			fail(c, http.StatusBadRequest, ce.Error())
			return
		}
		h.log.Error("导入声明文件失败", "err", err)
		fail(c, http.StatusInternalServerError, "导入失败")
		return
	}
	// 只报规模不报内容——变更清单本身只有实体名、没有秘密（同 Apply 的日志纪律）。
	h.log.Info("管理端导入声明文件", "变更数", len(changes))
	if changes == nil {
		// 前端只判数组长度，别让「无变化」以 null 的形态多出第三种情况。
		changes = []string{}
	}
	c.JSON(http.StatusOK, gin.H{"changes": changes})
}

// previewImport 把「导入这份文件会发生什么」试算给人看（口径层 v1.03）：与
// importConfig 同一串闸——4MB、严格解析、多用户闸、声明文件形态写闸（注册在同一
// 组）、两道校验闸（在 declcfg.Preview 里）——只是最后不提交事务，库里一行不留。
// 回给前端的清单与真导入将产生的完全一致；试算被闸打回（400 原文）时真导入也会被
// 同一道闸打回，前端据此把确认键整个收起来。
//
// 试算不写库，却与导入同句 409：声明文件形态下「导入成功」是假象，「试算成功」
// 同样是假象——它预演的改动活不过下次重启 apply。
func (h *Handler) previewImport(c *gin.Context) {
	f, ok := readDeclaredFile(c)
	if !ok {
		return
	}
	if err := declcfg.CheckSingleUser(c.Request.Context(), h.db); err != nil {
		fail(c, http.StatusConflict, err.Error())
		return
	}
	changes, err := declcfg.Preview(c.Request.Context(), h.db, f)
	if err != nil {
		if ce, ok := errors.AsType[declcfg.ConfigError](err); ok {
			fail(c, http.StatusBadRequest, ce.Error())
			return
		}
		h.log.Error("试算声明文件失败", "err", err)
		fail(c, http.StatusInternalServerError, "试算失败")
		return
	}
	if changes == nil {
		// 前端只判数组长度，别让「无变化」以 null 的形态多出第三种情况。
		changes = []string{}
	}
	c.JSON(http.StatusOK, gin.H{"changes": changes})
}

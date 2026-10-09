package admin

// login.go 是订阅渠道登录弹层的三只接口（#212，展开层 §7.13 / 口径层 §2.2 v1.52）：
// start 拼授权 URL、complete 收粘贴回调换凭证落库、poll 报进行中状态（Copilot 设备
// 码形态的前端轮询口，随其实现票启用）。状态机在 internal/subscription——这里只管
// HTTP 面：认渠道类型、把引擎交出的凭证 JSON 落进写事务（与 store.Validate 同一个
// 事务，口径层 §8「能保存下去的配置一定是能启动的配置」）。
//
// 重新登录（reauth_required 行尾那颗按钮）与新增共用这两只接口：start 带
// credential_id 即替换那一行（换值 + 重新启用 + 清停用现场，名字不动——归因不断），
// 不带即追加。

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/SimonGino/portage/internal/store"
	"github.com/SimonGino/portage/internal/subscription"

	"github.com/gin-gonic/gin"
)

// loginStart 起一次登录，回授权页地址。请求体 {credential_id?}：给了就是「重新
// 登录」要替换的那一行（须属于本渠道）。
func (h *Handler) loginStart(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	credType, err := store.ChannelCredentialType(c.Request.Context(), h.db, id)
	switch {
	case errors.Is(err, store.ErrNotFound):
		fail(c, http.StatusNotFound, "记录不存在")
		return
	case err != nil:
		h.log.Error("读渠道凭证类型失败", "err", err)
		fail(c, http.StatusInternalServerError, "读取失败")
		return
	}
	switch credType {
	case store.CredentialTypeChatGPTAccount:
	case store.CredentialTypeCopilotAccount:
		fail(c, http.StatusBadRequest, "Copilot 设备码登录随 Copilot 实现票启用，先别用这个渠道")
		return
	default:
		fail(c, http.StatusBadRequest, "登录弹层只对订阅渠道开放：这个渠道的凭证是贴 key 的")
		return
	}
	var in struct {
		CredentialID *int64 `json:"credential_id"`
	}
	if c.Request.ContentLength != 0 {
		if err := c.ShouldBindJSON(&in); err != nil {
			fail(c, http.StatusBadRequest, "请求体不是合法 JSON")
			return
		}
	}
	if in.CredentialID != nil {
		chID, err := store.CredentialChannelID(c.Request.Context(), h.db, *in.CredentialID)
		if errors.Is(err, store.ErrNotFound) || (err == nil && chID != id) {
			fail(c, http.StatusBadRequest, "要重新登录的凭证不在这个渠道里")
			return
		}
		if err != nil {
			h.log.Error("读凭证归属失败", "err", err)
			fail(c, http.StatusInternalServerError, "读取失败")
			return
		}
	}
	var replaceID int64
	if in.CredentialID != nil {
		replaceID = *in.CredentialID
	}
	url, err := h.sub.StartLogin(c.Request.Context(), id, replaceID)
	if err != nil {
		if errors.Is(err, subscription.ErrLoginInProgress) {
			fail(c, http.StatusConflict, err.Error())
			return
		}
		h.log.Error("起登录失败", "err", err)
		fail(c, http.StatusInternalServerError, "起登录失败，请重试")
		return
	}
	c.JSON(http.StatusOK, gin.H{"url": url})
}

// loginComplete 收粘贴的回调整条 URL，换 token、验签后落库。成功回 204：新凭证行
// 的 id 与内容在列表重拉里自然可见，不需要单独回包。
func (h *Handler) loginComplete(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var in struct {
		CallbackURL string `json:"callback_url"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	cred, replaceID, err := h.sub.CompleteLogin(c.Request.Context(), id, in.CallbackURL)
	if err != nil {
		fail(c, http.StatusBadRequest, err.Error())
		return
	}
	raw, err := json.Marshal(cred)
	if err != nil {
		h.log.Error("拼凭证 JSON 失败", "err", err)
		fail(c, http.StatusInternalServerError, "保存失败")
		return
	}
	h.write(c, func(ctx context.Context, tx *sql.Tx) error {
		if replaceID > 0 {
			// 原行替换（口径层 v1.52）：换值 + 重新启用 + 清停用现场，名字不动。
			on := false
			return store.UpdateCredential(ctx, tx, replaceID, store.CredentialUpdate{
				Value: string(raw), Disabled: &on,
			})
		}
		return store.AddChannelCredentials(ctx, tx, id, []store.NewCredential{{Value: string(raw)}})
	})
}

// loginPoll 报一个渠道有没有进行中的登录（Copilot 形态的前端轮询口；ChatGPT 形态
// 的弹层不调它，但三只接口的结构按 §7.13 先立好）。
func (h *Handler) loginPoll(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, h.sub.PollLogin(id))
}

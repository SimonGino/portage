package store

// subcred.go 是订阅渠道（`chatgpt_account`，口径层 §2.2「订阅渠道」条 v1.52）凭证
// JSON 的形状：字段表见展开层 §7.13，值存 `channel_keys.credential` 一列、不开新表。
// 形状住在 store 是因为它是**存进库里的那一列的格式**——同 ValidateHeaders 的先例；
// 刷新引擎（internal/subscription）从这儿取类型，不自己再定义一份。

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// chatgpt.tokens.use.direct 是 Sign in with ChatGPT 直连 api.openai.com 的授权范围：
// 没有它，账号的订阅额度用不到 API 上（#205 真机证据的 scope 字段）。凭证缺这个
// scope 就是一把「登录了但用不上」的凭证，写侧当场拒比请求时 403 强。
const SIWCDirectScope = "chatgpt.tokens.use.direct"

// ChatGPTCredential 是 `chatgpt_account` 渠道一份凭证的 JSON 形状（展开层 §7.13
// 字段表）。原文落库、原样回读（v0.47），这个结构只在校验与解析时用。
//
// ExpiresAt 是 access token 的过期时刻，**unix 秒**——与 #205 证据里
// `earliest_refresh_at` 同一种形态；刷新一次由引擎整包重写（refresh token 会轮换）。
type ChatGPTCredential struct {
	ClientID     string   `json:"client_id"`
	HostID       string   `json:"host_id"`
	Sub          string   `json:"sub,omitempty"`
	Email        string   `json:"email,omitempty"`
	IDToken      string   `json:"id_token,omitempty"`
	AccessToken  string   `json:"access_token"`
	RefreshToken string   `json:"refresh_token"`
	ExpiresAt    int64    `json:"expires_at"`
	Scopes       []string `json:"scopes"`
}

// ParseChatGPTCredential 解析并校验一份 chatgpt_account 凭证 JSON。
//
// 必填 = client_id / host_id / access_token / refresh_token / expires_at，缺的
// 一次点名；scopes 必须含 SIWCDirectScope。错误文案直接面向贴 JSON 的人（#212
// 「拒因原文」的底座），不带任何凭证值本身。
func ParseChatGPTCredential(value string) (ChatGPTCredential, error) {
	var c ChatGPTCredential
	if err := json.Unmarshal([]byte(value), &c); err != nil {
		return ChatGPTCredential{}, fmt.Errorf("凭证 JSON 不合法：%w", err)
	}
	var missing []string
	for _, f := range []struct {
		name, v string
	}{
		{"client_id", c.ClientID},
		{"host_id", c.HostID},
		{"access_token", c.AccessToken},
		{"refresh_token", c.RefreshToken},
	} {
		if strings.TrimSpace(f.v) == "" {
			missing = append(missing, f.name)
		}
	}
	if c.ExpiresAt <= 0 {
		missing = append(missing, "expires_at")
	}
	if len(missing) > 0 {
		return ChatGPTCredential{}, fmt.Errorf("凭证 JSON 缺少字段：%s", strings.Join(missing, "、"))
	}
	if !slices.Contains(c.Scopes, SIWCDirectScope) {
		return ChatGPTCredential{}, fmt.Errorf("凭证 JSON 的 scopes 必须含 %s"+
			"——没有它，账号的订阅额度用不到 API 上", SIWCDirectScope)
	}
	return c, nil
}

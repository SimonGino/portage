package server

import (
	"github.com/SimonGino/portage/internal/store"
	"github.com/SimonGino/portage/internal/upstream"
)

// routeOf 把 store 解析出的候选映射成 upstream.Route——Do 发一次请求真正要用的那几
// 列（#55）。候选上其余的列（四价、输入上限、能力位、请求的模型名）各归各的闸与
// 流水，不进 upstream。
//
// 映射住在 server 而不是 store：store 不能 import upstream（upstream 为词表常量与
// 探测目标类型反向 import 它），而 server 本来就是装配这两头的地方。
func routeOf(cand store.Candidate) upstream.Route {
	creds := make([]upstream.Credential, len(cand.Credentials))
	for i, c := range cand.Credentials {
		creds[i] = upstream.Credential{Name: c.Name, Value: c.Value}
	}
	return upstream.Route{
		ChannelID:      cand.ChannelID,
		ChannelName:    cand.ChannelName,
		Protocol:       cand.Protocol,
		BaseURL:        cand.BaseURL,
		AuthScheme:     cand.AuthScheme,
		KeyMode:        cand.KeyMode,
		Credentials:    creds,
		MaxConcurrency: cand.MaxConcurrency,
	}
}

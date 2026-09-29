package store

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	"golang.org/x/net/http/httpguts"
)

// reservedHeaders 是渠道额外出站头（#137）不许声明的头名，小写。
//
// 两类（PO 2026-09-29 裁决）：①网关自己写的——凭证载体（Authorization / x-api-key）与
// 协议契约（Content-Type / Accept / Accept-Encoding / anthropic-version / anthropic-beta），
// 让渠道声明它们就成了「map 遍历顺序决定谁赢」；②Go 会静默忽略或重算的——Host、
// Content-Length 与逐跳头，放行只会让配置看上去生效、实际没生效。
//
// User-Agent 不在里面：它是运维人自己配的头，不是转发客户端指纹（口径层 v1.37）。
var reservedHeaders = map[string]bool{
	"authorization": true, "x-api-key": true,
	"content-type": true, "accept": true, "accept-encoding": true,
	"anthropic-version": true, "anthropic-beta": true,
	"host": true, "content-length": true,
	"connection": true, "keep-alive": true, "proxy-connection": true,
	"proxy-authenticate": true, "proxy-authorization": true,
	"te": true, "trailer": true, "transfer-encoding": true, "upgrade": true,
}

// ReservedHeader 报这个头名是不是网关自己管的（大小写不敏感）。
func ReservedHeader(name string) bool { return reservedHeaders[strings.ToLower(name)] }

// ValidateHeaders 是渠道额外出站头的写侧闸（writer 与声明文件 selfCheck 共用一份）。
// 按头名排序逐个看，报第一个问题：头名不合法、保留头名、大小写不同的重名、空值、值
// 带换行等非法字符。空值拒而不是照发：YAML 里 `x-foo:` 忘了写值解出来就是空串，发一个
// 空头给上游的表象和没配一样，人却以为配上了。
func ValidateHeaders(h map[string]string) error {
	seen := map[string]string{}
	for _, name := range slices.Sorted(maps.Keys(h)) {
		lower := strings.ToLower(name)
		var reason string
		switch v := h[name]; {
		case !httpguts.ValidHeaderFieldName(name):
			reason = "不是合法的 HTTP 头名"
		case reservedHeaders[lower]:
			reason = "是网关自己管的头（凭证、协议契约，或 Go 会忽略/重算的 Host、Content-Length、逐跳头），渠道不能声明"
		case seen[lower] != "":
			reason = fmt.Sprintf("与 %q 只差大小写，HTTP 头名不分大小写，两个只会剩一个", seen[lower])
		case v == "":
			reason = "值是空的"
		case !httpguts.ValidHeaderFieldValue(v):
			reason = "的值含换行等不能进 HTTP 头的字符"
		}
		if reason != "" {
			return InvalidInput{Reason: fmt.Sprintf("额外出站头 %q %s", name, reason)}
		}
		seen[lower] = name
	}
	return nil
}

// encodeHeaders 把头集合写成库里那一列：空即空串，否则 JSON（encoding/json 对 map 按键
// 排序，导出物因此字节稳定）。不用逗号分隔：头的值里带逗号是常态。
func encodeHeaders(h map[string]string) string {
	if len(h) == 0 {
		return ""
	}
	b, _ := json.Marshal(h) // map[string]string 编不失败
	return string(b)
}

// DecodeHeaders 读那一列。读侧宽容：解不动（只能是手写 SQL 灌的）当没有，理由同 key_mode。
func DecodeHeaders(s string) map[string]string {
	if s == "" {
		return nil
	}
	var h map[string]string
	if json.Unmarshal([]byte(s), &h) != nil {
		return nil
	}
	return h
}

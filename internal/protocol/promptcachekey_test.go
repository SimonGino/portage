package protocol_test

import (
	"testing"

	"github.com/SimonGino/portage/internal/protocol"
	"github.com/SimonGino/portage/internal/protocol/anthropic"
	"github.com/SimonGino/portage/internal/protocol/openaicc"
	"github.com/SimonGino/portage/internal/protocol/openairesponses"
)

// prompt_cache_key 在 CC↔R 间原值直传、不再进 Extras 登记，→A 照旧按 vendor_request
// 登记后丢（口径层 v1.25 ①，#115）。构造样本：两个 OpenAI 入口里这个键同名同形，
// 手搭最小请求即可钉住，不冒充 golden。
func TestPromptCacheKeyCrossesBetweenOpenAIProtocols(t *testing.T) {
	const (
		ccBody = `{"model":"m","prompt_cache_key":"sess-42","messages":[{"role":"user","content":"hi"}]}`
		rBody  = `{"model":"m","prompt_cache_key":"sess-42","input":"hi"}`
	)
	cases := []struct {
		name string
		dec  requestDecoder
		body string
		enc  protocol.RequestEncodeReporter
	}{
		{"CC→R", openaicc.NewCodec(), ccBody, openairesponses.NewCodec()},
		{"R→CC", openairesponses.NewCodec(), rBody, openaicc.NewCodec()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := decodeWith(t, tc.dec, tc.body)
			if _, ok := req.Extras["prompt_cache_key"]; ok {
				t.Error("prompt_cache_key 还留在 Extras 里")
			}
			keys, dropped := encodeWith(t, tc.enc, req)
			if got := string(keys["prompt_cache_key"]); got != `"sess-42"` {
				t.Errorf("prompt_cache_key = %s，期望原值直传", got)
			}
			if len(dropped) != 0 {
				t.Errorf("直传的键不该登记丢弃: %v", dropped.Strings())
			}
		})
	}

	t.Run("→A 登记 vendor_request 且不外带", func(t *testing.T) {
		req := decodeWith(t, openairesponses.NewCodec(), rBody)
		keys, dropped := encodeWith(t, anthropic.NewCodec(anthropic.Options{DefaultMaxTokens: 8192}), req)
		if _, ok := keys["prompt_cache_key"]; ok {
			t.Error("prompt_cache_key 漏进了 Anthropic 请求")
		}
		if !dropped.Has(anthropic.DropVendorRequest) {
			t.Errorf("没登记 vendor_request: %v", dropped.Strings())
		}
	})

	// 非字符串不认：留在 Extras 里照旧登记，不当场 400（与提成一等字段之前一致）。
	t.Run("null 留在 Extras", func(t *testing.T) {
		req := decodeWith(t, openaicc.NewCodec(), `{"model":"m","prompt_cache_key":null,"messages":[{"role":"user","content":"hi"}]}`)
		keys, dropped := encodeWith(t, openairesponses.NewCodec(), req)
		if _, ok := keys["prompt_cache_key"]; ok {
			t.Error("null 的 prompt_cache_key 被发出去了")
		}
		if !dropped.Has(openairesponses.DropVendorRequest) {
			t.Errorf("没登记 vendor_request: %v", dropped.Strings())
		}
	})
}

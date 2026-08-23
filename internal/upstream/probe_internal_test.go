package upstream

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SimonGino/portage/internal/protocol"
)

// 模型级判定与固定词表的单元测试（口径层 v0.43 三态，v0.96 词表纪律）。
// 主缝在 internal/server/probe_test.go；这里逐状态码钉词表——那边只从管理 API
// 外面看三态，摘要每一句写的是什么在这层才断得细。

// probeStatusUpstream 固定回一个状态码。
func probeStatusUpstream(t *testing.T, status int) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// 三态判定 + 固定词表逐条：2xx 通；404/405 不通；其余说不清、摘要用我方词表。
// 词表纪律两条一起钉：不带上游原文（这里上游没回原文，主缝的泄露闸管那半边），
// 永不出现「不支持」——检测是一次采样，定性交给人（口径层 v0.96 ③）。
func TestProbeModelVerdictAndVocabulary(t *testing.T) {
	cases := []struct {
		status int
		state  ProbeState
		detail string // 空 = 只验状态，不钉词句
	}{
		{200, ProbeOK, "通"},
		{404, ProbeMissing, "这一侧没有这个模型（或子路径不存在）"},
		{405, ProbeMissing, "这一侧没有这个模型（或子路径不存在）"},
		{400, ProbeUnclear, "参数被拒（模型多半存在，是请求形状问题）"},
		{401, ProbeUnclear, "凭证不对（401）"},
		{403, ProbeUnclear, "被拒（403）——可能是这把凭证没开通这个模型"},
		{429, ProbeUnclear, "限流（429）"},
		{500, ProbeUnclear, "上游错误"},
		{502, ProbeUnclear, "上游错误"},
		{418, ProbeUnclear, "说不清"},
	}
	for _, tc := range cases {
		url := probeStatusUpstream(t, tc.status)
		res := ProbeModel(context.Background(), url, protocol.OpenAI, "sk-test", "m")
		if res.State != tc.state {
			t.Errorf("状态码 %d 判成 %q，期望 %q", tc.status, res.State, tc.state)
		}
		if res.Status != tc.status {
			t.Errorf("状态码 %d 该原样回报，得到 %d", tc.status, res.Status)
		}
		if tc.detail != "" && res.Detail != tc.detail {
			t.Errorf("状态码 %d 的固定词表 = %q，期望 %q", tc.status, res.Detail, tc.detail)
		}
		if strings.Contains(res.Detail, "不支持") {
			t.Errorf("状态码 %d 的摘要定性了「不支持」：%q", tc.status, res.Detail)
		}
	}
}

// 连不上：说不清、状态码 0、摘要以「本次检测失败」开头且不带地址（Redact）。
func TestProbeModelTransportFailureWording(t *testing.T) {
	res := ProbeModel(context.Background(),
		"http://127.0.0.1:1/secret-path", protocol.OpenAI, "sk-test", "m")
	if res.State != ProbeUnclear || res.Status != 0 {
		t.Errorf("连不上应是 unclear 且状态码 0：%+v", res)
	}
	if !strings.HasPrefix(res.Detail, "本次检测失败：") {
		t.Errorf("传输失败的措辞该以「本次检测失败：」开头（口径层 v0.96 ③）：%q", res.Detail)
	}
	if strings.Contains(res.Detail, "secret-path") {
		t.Errorf("传输错误摘要泄露了上游地址：%q", res.Detail)
	}
}

// 最小真实请求体的形状：CC/Anthropic 用 max_tokens:1，Responses 用
// max_output_tokens:16（OpenAI 的下限）；模型名按 JSON 字符串正经编码。
func TestModelProbeBodyShapes(t *testing.T) {
	for _, tc := range []struct {
		proto protocol.Protocol
		key   string
		want  float64
	}{
		{protocol.OpenAI, "max_tokens", 1},
		{protocol.Anthropic, "max_tokens", 1},
		{protocol.OpenAIResponses, "max_output_tokens", 16},
	} {
		var body map[string]any
		raw := modelProbeBody(tc.proto, `quo"te`)
		if err := json.Unmarshal([]byte(raw), &body); err != nil {
			t.Fatalf("%s 的请求体不是合法 JSON（%v）：%s", tc.proto, err, raw)
		}
		if body["model"] != `quo"te` {
			t.Errorf("%s 的模型名没按 JSON 字符串编码：%v", tc.proto, body["model"])
		}
		if body[tc.key] != tc.want {
			t.Errorf("%s 的 %s = %v，期望 %v", tc.proto, tc.key, body[tc.key], tc.want)
		}
	}
}

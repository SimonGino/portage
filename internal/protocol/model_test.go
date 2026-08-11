package protocol_test

import (
	"encoding/json"
	"testing"

	"github.com/SimonGino/portage/internal/protocol"
)

// 帧形态的组合远多于值得跑一趟 HTTP 的量，所以改写器按纯函数单测——
// 与 Tap 同一条理由。端到端的「除 model 外逐字节相等」在主接缝里另有断言。
func TestRewriteModel(t *testing.T) {
	cases := []struct {
		name     string
		body     string
		upstream string
		want     string
	}{
		{
			name:     "改写顶层 model，其余字节不动",
			body:     `{"model":"gw","max_tokens":1024,"weird":1.50}`,
			upstream: "real-model",
			want:     `{"model":"real-model","max_tokens":1024,"weird":1.50}`,
		},
		{
			name:     "不碰嵌套的同名键",
			body:     `{"metadata":{"model":"nested"},"model":"gw","tools":[{"model":"also-nested"}]}`,
			upstream: "real-model",
			want:     `{"metadata":{"model":"nested"},"model":"real-model","tools":[{"model":"also-nested"}]}`,
		},
		{
			name:     "值已相同则原样返回",
			body:     `{"model":"same","x":1}`,
			upstream: "same",
			want:     `{"model":"same","x":1}`,
		},
		{
			name:     "保留缩进与空白",
			body:     "{\n  \"model\" :  \"gw\" ,\n  \"x\": 1\n}",
			upstream: "real-model",
			want:     "{\n  \"model\" :  \"real-model\" ,\n  \"x\": 1\n}",
		},
		{
			name:     "没有 model 键则原样返回",
			body:     `{"messages":[]}`,
			upstream: "real-model",
			want:     `{"messages":[]}`,
		},
		{
			name:     "model 不是字符串则不改",
			body:     `{"model":null,"x":1}`,
			upstream: "real-model",
			want:     `{"model":null,"x":1}`,
		},
		{
			name:     "顶层键重复时改最后一个，与 encoding/json 的 last-wins 一致",
			body:     `{"model":"first","model":"last"}`,
			upstream: "real-model",
			want:     `{"model":"first","model":"real-model"}`,
		},
		{
			name:     "源值含转义字符",
			body:     `{"model":"gw-sonnet","x":1}`,
			upstream: "real-model",
			want:     `{"model":"real-model","x":1}`,
		},
		{
			name:     "目标名含需转义字符",
			body:     `{"model":"gw"}`,
			upstream: `quote"and\slash`,
			want:     `{"model":"quote\"and\\slash"}`,
		},
		{
			name:     "根不是对象则原样返回",
			body:     `[1,2,3]`,
			upstream: "real-model",
			want:     `[1,2,3]`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := protocol.RewriteModel([]byte(tc.body), tc.upstream)
			if err != nil {
				t.Fatalf("改写失败: %v", err)
			}
			if string(got) != tc.want {
				t.Errorf("结果不符\n期望: %s\n实际: %s", tc.want, got)
			}
		})
	}
}

func TestRewriteModelKeepsBodyParseable(t *testing.T) {
	body := `{"model":"gw","system":[{"cache_control":{"type":"ephemeral"}}],"n":1.50}`

	got, err := protocol.RewriteModel([]byte(body), "real-model")
	if err != nil {
		t.Fatal(err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(got, &parsed); err != nil {
		t.Fatalf("改写后不是合法 JSON: %v；body=%s", err, got)
	}
	if parsed["model"] != "real-model" {
		t.Errorf("model = %v", parsed["model"])
	}
}

func TestRewriteModelRejectsMalformedJSON(t *testing.T) {
	if _, err := protocol.RewriteModel([]byte(`{"model":`), "real-model"); err == nil {
		t.Error("非法 JSON 未报错")
	}
}

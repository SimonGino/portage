package server

import (
	"net/http"
	"testing"
)

// contextTooLong 的判档：各家「上下文超长」的原话要认得，限流与回复额度的报错不能认。
// 原话取自 magpie 40812f1、mimo2codex contextOverflow.ts 所列的上游实例。
func TestContextTooLong(t *testing.T) {
	for _, tc := range []struct {
		status    int
		msg, code string
		want      bool
	}{
		{400, "This model's maximum context length is 131072 tokens. However, you requested 140000 tokens.", "", true},
		{400, "prompt is too long: 208310 tokens > 200000 maximum", "", true},
		{400, "Input exceeds the context limit (1048568 tokens)", "", true},
		{400, "Invalid request: Your request exceeded model token limit: 262144", "", true},
		{400, "Your input exceeds the context window of this model.", "", true},
		{400, "请求的上下文长度超过模型限制", "", true},
		{413, "输入超出了模型的最大上下文", "", true},
		{400, "whatever", "context_length_exceeded", true},
		// 回复额度太大，压缩救不了。
		{400, "max_tokens (300000) exceeds the maximum context length", "", false},
		// 429 说的是限流。
		{429, "maximum context length", "", false},
		{400, "model not found", "", false},
		{http.StatusBadRequest, "", "", false},
	} {
		if got := contextTooLong(tc.status, tc.msg, tc.code); got != tc.want {
			t.Errorf("contextTooLong(%d, %q, %q) = %v, 期望 %v", tc.status, tc.msg, tc.code, got, tc.want)
		}
	}
}

// tokenFloor 读上游报的 max_tokens 下限；说的不是下限的报错读成 0。
func TestTokenFloor(t *testing.T) {
	for msg, want := range map[string]int{
		`{"error":{"message":"max_tokens must be greater than 2"}}`:                3,
		`max_completion_tokens must be at least 16`:                                16,
		`Invalid 'max_output_tokens': integer below minimum value. Expected >= 16`: 16,
		`max_tokens: 300000 > 128000, which is the maximum allowed`:                0,
		`max_tokens is too large: 999999`:                                          0,
		`messages: at least 1 message is required`:                                 0,
	} {
		if got := tokenFloor([]byte(msg)); got != want {
			t.Errorf("tokenFloor(%q) = %d, 期望 %d", msg, got, want)
		}
	}
}

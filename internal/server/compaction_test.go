package server_test

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/SimonGino/portage/internal/gatewaytest"
)

// 本文件测 Codex 压缩在网关这一层的两种收场（口径层 v0.54）：转换路径**本地合成**，
// 透传路径没勾能力位就**明确拒绝**。
//
// 两边共同要钉的是同一句话：压缩 turn 绝不能表现成「一次成功的普通转发」——那正是让
// Codex 收到 0 个 compaction item、当场 Fatal 且不重试不降级的那条路。

// compactionRequest 是一次压缩 turn 的请求形态：正常历史 + 尾部一个 compaction_trigger。
const compactionRequest = `{"model":"gw-sonnet","stream":true,` +
	`"tools":[{"type":"custom","name":"exec"}],` +
	`"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]},` +
	`{"type":"compaction_trigger"}]}`

// plainResponsesRequest 是同一个渠道上的普通 turn，用来钉「闸不误伤」。
const plainResponsesRequest = `{"model":"gw-sonnet","stream":false,` +
	`"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`

// compactionEnvelopePrefix 是自造信封的判别前缀。测试里写死一份是有意的：它是**线上
// 常量**（发出去就进了客户端的会话历史），从实现里引用等于让「改了也不会红」。
const compactionEnvelopePrefix = "ptg1:"

// anthropicSummaryFrames 是假 Anthropic 上游对 summarizer turn 的回话：一段摘要正文。
func anthropicSummaryFrames() []string {
	return []string{
		sseFrame("message_start", `{"type":"message_start","message":{"id":"msg_up1","model":`+
			`"`+upstreamModel+`","usage":{"input_tokens":900,"output_tokens":1}}}`),
		sseFrame("content_block_delta", `{"type":"content_block_delta","index":0,`+
			`"delta":{"type":"text_delta","text":"1. 干了什么 2. 还差什么"}}`),
		sseFrame("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},`+
			`"usage":{"output_tokens":57}}`),
		sseFrame("message_stop", `{"type":"message_stop"}`),
	}
}

// TestCompactionSynthesizedOnConvert：转换路径把压缩 turn 改写成 summarizer 打给上游，
// 再把上游的摘要合成**恰好一个** compaction item 发回去（portage-legacy#74）。
func TestCompactionSynthesizedOnConvert(t *testing.T) {
	up := gatewaytest.NewUpstream(t)
	db := gatewaytest.NewDB(t)
	gatewaytest.SeedPassthrough(t, db, accessPointModel, "anthropic", up.URL, upstreamModel, anthropicCredential)
	gw := gatewaytest.StartWith(t, db, gatewaytest.Options{})
	streamUpstream(t, up, anthropicSummaryFrames()...)()

	resp := gw.Post(t, "/v1/responses", compactionRequest, nil)
	body := gatewaytest.ReadBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("状态码 = %d, 期望 200；body=%s", resp.StatusCode, body)
	}

	// 上游收到的是一次纯总结请求：工具剥光，末尾一句压缩指令。留着工具的话上游多半
	// 去调工具，这一轮就一个字的摘要都没有。
	sent := up.Last(t)
	var req struct {
		Tools    []json.RawMessage `json:"tools"`
		Messages []struct {
			Role    string `json:"role"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(sent.Body, &req); err != nil {
		t.Fatalf("上游收到的不是 Anthropic 请求: %v\n%s", err, sent.Body)
	}
	if len(req.Tools) != 0 {
		t.Errorf("summarizer turn 还带着 %d 个工具声明", len(req.Tools))
	}
	// 指令落在末条 user 消息的末块：它与前面那条 user 消息同侧，解码时并进了同一条
	// 消息（Anthropic 拒 user 连发，正是要避免的形态）。
	last := req.Messages[len(req.Messages)-1]
	tail := last.Content[len(last.Content)-1].Text
	if last.Role != "user" || !strings.Contains(tail, "CONTEXT CHECKPOINT COMPACTION") {
		t.Errorf("末尾不是那条压缩指令：%+v", last)
	}

	// 回给 Codex 的流里必须恰好一个 compaction item——0 个就是当场 Fatal。
	var items []string
	for _, line := range strings.Split(body, "\n") {
		raw, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			continue
		}
		var frame struct {
			Type string `json:"type"`
			Item struct {
				Type      string `json:"type"`
				Encrypted string `json:"encrypted_content"`
			} `json:"item"`
		}
		if json.Unmarshal([]byte(raw), &frame) != nil || frame.Type != "response.output_item.done" {
			continue
		}
		if frame.Item.Type != "compaction" {
			t.Errorf("output 里混进了别的 item：%s", frame.Item.Type)
			continue
		}
		items = append(items, frame.Item.Encrypted)
	}
	if len(items) != 1 {
		t.Fatalf("compaction item %d 个, 期望恰好 1 个；body=%s", len(items), body)
	}
	if !strings.HasPrefix(items[0], compactionEnvelopePrefix) {
		t.Errorf("信封前缀变了（长期兼容约束，改它会让在途会话的回带摘要全解不开）：%q", items[0])
	}
	summary, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(items[0], compactionEnvelopePrefix))
	if err != nil || !strings.Contains(string(summary), "干了什么") {
		t.Errorf("信封里不是上游那段摘要: %q (%v)", summary, err)
	}
	if !strings.Contains(body, "response.completed") {
		t.Errorf("终帧不是 completed：%s", body)
	}
	if strings.Contains(body, "干了什么") {
		t.Error("摘要正文以明文形态漏进了流：Codex 会把它连同 item 里那份记两遍")
	}
	if lines := gw.Lines("Codex 压缩 turn 本地合成"); len(lines) != 1 {
		t.Errorf("合成日志 %d 行, 期望 1 行；已落日志：%s", len(lines), gw.RawLog())
	}
}

// TestCompactionReplayRestoredOnConvert：上一轮压缩产出的 item 被 Codex 原样回带时，
// 摘要要还原进打给上游的历史（G2）。丢掉它等于整段失忆。
func TestCompactionReplayRestoredOnConvert(t *testing.T) {
	up := gatewaytest.NewUpstream(t)
	db := gatewaytest.NewDB(t)
	gatewaytest.SeedPassthrough(t, db, accessPointModel, "anthropic", up.URL, upstreamModel, anthropicCredential)
	gw := gatewaytest.StartWith(t, db, gatewaytest.Options{})
	up.RespondWith(http.StatusOK, map[string]string{"Content-Type": "application/json"},
		`{"id":"msg_1","model":"`+upstreamModel+`","type":"message","role":"assistant",`+
			`"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn",`+
			`"usage":{"input_tokens":1,"output_tokens":1}}`)

	env := compactionEnvelopePrefix + base64.StdEncoding.EncodeToString([]byte("早前干了 A 和 B"))
	replay := `{"model":"gw-sonnet","stream":false,"input":[` +
		`{"type":"compaction","id":"cmp_1","encrypted_content":"` + env + `"},` +
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"接着做"}]}]}`

	resp := gw.Post(t, "/v1/responses", replay, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("状态码 = %d；body=%s", resp.StatusCode, gatewaytest.ReadBody(t, resp))
	}
	got := string(up.Last(t).Body)
	if !strings.Contains(got, "早前干了 A 和 B") {
		t.Errorf("回带的摘要没还原进上游请求，这一段历史对上游是失忆的：%s", got)
	}
	if !strings.Contains(got, "Another language model started to solve this problem") {
		t.Errorf("引导语没套上，模型会把摘要当成用户说的话：%s", got)
	}
	// 回带的产物不是新的压缩请求：判错的话每一轮都会变成总结。
	if lines := gw.Lines("Codex 压缩 turn 本地合成"); len(lines) != 0 {
		t.Errorf("回带被误判成压缩 turn：%s", gw.RawLog())
	}
}

// TestCompactionReplayOpaqueLogsDrop：回带的信封解不开时那行归因日志要真的发得出来。
//
// 要害在于这一轮**不是**压缩 turn：Codex 只在需要压缩的那一轮发 trigger，回带发生在
// 之后的普通请求上。日志罩在压缩 turn 里面的话，混路场景（先经透传渠道压缩成功、之后
// 被路由到转换渠道）的头一次——也是最该归因的那次——会完全无声。
func TestCompactionReplayOpaqueLogsDrop(t *testing.T) {
	up := gatewaytest.NewUpstream(t)
	db := gatewaytest.NewDB(t)
	gatewaytest.SeedPassthrough(t, db, accessPointModel, "anthropic", up.URL, upstreamModel, anthropicCredential)
	gw := gatewaytest.StartWith(t, db, gatewaytest.Options{})
	up.RespondWith(http.StatusOK, map[string]string{"Content-Type": "application/json"},
		`{"id":"msg_1","model":"`+upstreamModel+`","type":"message","role":"assistant",`+
			`"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn",`+
			`"usage":{"input_tokens":1,"output_tokens":1}}`)

	// 别家网关（或透传渠道）压出来的密文：前缀不是 ptg1:，我们解不开。
	replay := `{"model":"gw-sonnet","stream":false,"input":[` +
		`{"type":"compaction","id":"cmp_1","encrypted_content":"gAAAAAB_someone_elses_blob"},` +
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"接着做"}]}]}`

	resp := gw.Post(t, "/v1/responses", replay, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("状态码 = %d；body=%s", resp.StatusCode, gatewaytest.ReadBody(t, resp))
	}
	// 这一轮没有 trigger，压缩 turn 的日志不该出现。
	if lines := gw.Lines("Codex 压缩 turn 本地合成"); len(lines) != 0 {
		t.Errorf("回带被误判成压缩 turn：%s", gw.RawLog())
	}
	lines := gw.Lines("回带的压缩摘要解不开，已降级为占位")
	if len(lines) != 1 {
		t.Fatalf("丢弃日志 %d 行, 期望 1 行——「模型好像忘了前半段」这类反馈只能靠它归因；已落日志：%s",
			len(lines), gw.RawLog())
	}
	// 占位确实进了打给上游的历史：直接丢掉这个 item，上游看到的历史会凭空少一段。
	if got := string(up.Last(t).Body); !strings.Contains(got, "早前的对话已被压缩") {
		t.Errorf("解不开的 item 没降级成占位：%s", got)
	}
}

// TestCompactionRejectedOnPassthroughWithoutCapability：透传路径同样拒——Responses
// 形状的 wire 不等于支持压缩，这一格正是能力位为否时保护的那格。
func TestCompactionRejectedOnPassthroughWithoutCapability(t *testing.T) {
	up := gatewaytest.NewUpstream(t)
	db := gatewaytest.NewDB(t)
	gatewaytest.SeedPassthrough(t, db, accessPointModel, "openai_responses", up.URL, "gpt-5.6", openaiCredential)
	gw := gatewaytest.Start(t, db)

	resp := gw.Post(t, "/v1/responses", compactionRequest, nil)
	body := gatewaytest.ReadBody(t, resp)

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("状态码 = %d, 期望 400；body=%s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "支持 Codex 压缩") {
		t.Errorf("错误文案没指出去哪儿把能力位勾上：%s", body)
	}
	if up.Count() != 0 {
		t.Errorf("上游收到了 %d 个请求，压缩 turn 不该透传出去", up.Count())
	}
	row := gw.LastCallRow(t)
	if row.Error.String != "compaction_unsupported" {
		t.Errorf("流水 error = %q, 期望 compaction_unsupported", row.Error.String)
	}
	if lines := gw.Lines("拒绝 Codex 压缩 turn：透传渠道未声明支持 compaction"); len(lines) != 1 {
		t.Fatalf("drop 日志不对（%d 行）：%s", len(lines), gw.RawLog())
	}
}

// TestCompactionPassthroughWithCapability：能力位勾上的透传渠道照旧放行，且 trigger
// 逐字节到达上游——闸只拦「压缩注定失败」的那两格，不改变本来就能用的那格。
func TestCompactionPassthroughWithCapability(t *testing.T) {
	up := gatewaytest.NewUpstream(t)
	db := gatewaytest.NewDB(t)
	channelID := gatewaytest.SeedChannel(t, db, "test-openai_responses", "openai_responses", up.URL, openaiCredential)
	gatewaytest.SetChannelCompaction(t, db, channelID, true)
	modelID := gatewaytest.SeedChannelModel(t, db, channelID, "gpt-5.6")
	apID := gatewaytest.SeedAccessPoint(t, db, accessPointModel)
	gatewaytest.SeedCandidate(t, db, apID, modelID, 100)
	gw := gatewaytest.Start(t, db)

	const upstreamBody = `{"id":"resp_1","object":"response","output":[{"type":"compaction"}]}`
	up.RespondWith(http.StatusOK, map[string]string{"Content-Type": "application/json"}, upstreamBody)

	resp := gw.Post(t, "/v1/responses", compactionRequest, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("状态码 = %d, 期望 200；body=%s", resp.StatusCode, gatewaytest.ReadBody(t, resp))
	}
	want := strings.Replace(compactionRequest, `"model":"`+accessPointModel+`"`, `"model":"gpt-5.6"`, 1)
	if got := string(up.Last(t).Body); got != want {
		t.Errorf("请求体除顶层 model 外应逐字节保真\n期望: %s\n收到: %s", want, got)
	}
}

// TestPlainTurnUnaffectedByCompactionGate：能力位为否只挡压缩 turn。普通请求照走，
// 否则这一位就成了「Responses 透传总开关」。
func TestPlainTurnUnaffectedByCompactionGate(t *testing.T) {
	up := gatewaytest.NewUpstream(t)
	db := gatewaytest.NewDB(t)
	gatewaytest.SeedPassthrough(t, db, accessPointModel, "openai_responses", up.URL, "gpt-5.6", openaiCredential)
	gw := gatewaytest.Start(t, db)
	up.RespondWith(http.StatusOK, map[string]string{"Content-Type": "application/json"}, `{"id":"resp_1"}`)

	resp := gw.Post(t, "/v1/responses", plainResponsesRequest, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("状态码 = %d, 期望 200；body=%s", resp.StatusCode, gatewaytest.ReadBody(t, resp))
	}
	if up.Count() != 1 {
		t.Errorf("上游收到 %d 个请求, 期望 1", up.Count())
	}
}

// TestV1CompactNotImplemented：legacy 的 v1 compact 回 501 而不是裸 404 或一页 SPA。
// 不带 key 也一样——它无条件拒绝，先撞 401 只会让人以为端点存在但没授权。
func TestV1CompactNotImplemented(t *testing.T) {
	db := gatewaytest.NewDB(t)
	up := gatewaytest.NewUpstream(t)
	gatewaytest.SeedPassthrough(t, db, accessPointModel, "openai_responses", up.URL, "gpt-5.6", openaiCredential)
	gw := gatewaytest.Start(t, db)

	for _, tc := range []struct {
		name   string
		header map[string]string
	}{
		{"带 key", nil},
		{"不带 key", map[string]string{"x-api-key": ""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := gw.Post(t, "/v1/responses/compact", `{"model":"gw-sonnet"}`, tc.header)
			body := gatewaytest.ReadBody(t, resp)
			if resp.StatusCode != http.StatusNotImplemented {
				t.Fatalf("状态码 = %d, 期望 501；body=%s", resp.StatusCode, body)
			}
			if !strings.Contains(body, "网关不支持 v1 compact") {
				t.Errorf("501 文案不对：%s", body)
			}
		})
	}
}

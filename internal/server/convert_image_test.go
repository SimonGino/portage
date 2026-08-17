package server_test

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 本文件测的是图片内容块跨协议转换（#1，口径层 §2.6）的**全链路**六个格子。
//
// 为什么不满足于三个 codec 包里的单测：图片这件事横跨两个半边——解码在入口协议那
// 边、编码在渠道协议那边，分开看两边都「对」，错的是中间那一环。展开层 v0.34 ① 为
// developer 角色归一立过同一条规矩（「钉这条的用例走全链路而不是单测」），图片是同
// 一个形态：CC 的 `image_url` 解成 `Block.Image` 是一回事，它到底有没有以 Anthropic
// 的 `image.source` 形态发出去是另一回事，中间任何一处把块滤掉都是静默丢图——而静默
// 丢图正是 #1 这张票立票的起因。
//
// 样本走 testdata/fixtures/（构造样本，PO 2026-08-17 裁）：真实 harness 至今没发过带
// 图的轮次，实采样本的 content 全是字符串。形状照三家官方文档写，图是 tiny.png 的真
// 字节——那正是这一格唯一值得测的东西：base64 载荷要**原样**穿过网关，一个字节不差。

// tinyPNGBase64 是 testdata/fixtures/tiny.png 的真字节。
//
// 断言拿它比对，而不是比对样本里那串 base64：后者只能证明「我们把样本里的串搬过去
// 了」，前者才证明「客户端那张图能被上游解出来」。
func tinyPNGBase64(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "fixtures", "tiny.png"))
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(raw)
}

func tinyPNGDataURI(t *testing.T) string {
	t.Helper()
	return "data:image/png;base64," + tinyPNGBase64(t)
}

// imageFixtureBody 读一份构造样本，把 model 换成接入点名。
//
// synthetic 闸与 canonical_coverage_test.go / cachehit_fixture_test.go 同一道，方向也
// 一样是反的：拦的是「构造样本被搬进 golden 冒充转录」。缘由见 testdata/fixtures/README.md。
func imageFixtureBody(t *testing.T, name string) string {
	t.Helper()
	dir := filepath.Join("..", "..", "testdata", "fixtures", name)

	metaRaw, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	if err != nil {
		t.Fatal(err)
	}
	var meta struct {
		Synthetic bool `json:"synthetic"`
	}
	if err := json.Unmarshal(metaRaw, &meta); err != nil {
		t.Fatalf("%s meta.json: %v", name, err)
	}
	if !meta.Synthetic {
		t.Fatalf("%s 的 meta.json 没有 synthetic:true——真录到的样本请放进 testdata/golden/ 走 verified 那道闸", name)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "request.json"))
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	body["model"] = accessPointModel
	out, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// ---- 三种出口协议各自的「上游收到了什么」形状 ----

type sentAnthropicBlock struct {
	Type   string `json:"type"`
	Text   string `json:"text"`
	Source struct {
		Type      string `json:"type"`
		MediaType string `json:"media_type"`
		Data      string `json:"data"`
		URL       string `json:"url"`
	} `json:"source"`
	ToolUseID string `json:"tool_use_id"`
	// Content 是 tool_result 的载荷：纯文本时是字符串，带图时是块数组，
	// 两种形态都合法，所以这里不能定死类型。
	Content json.RawMessage `json:"content"`
}

type sentAnthropicRequest struct {
	Messages []struct {
		Role    string               `json:"role"`
		Content []sentAnthropicBlock `json:"content"`
	} `json:"messages"`
}

type sentCCPart struct {
	Type     string `json:"type"`
	Text     string `json:"text"`
	ImageURL struct {
		URL string `json:"url"`
		// detail 在 CC 上住在 image_url 对象里面；Responses 那边它在 part 顶层。
		Detail string `json:"detail"`
	} `json:"image_url"`
}

type sentCCRequest struct {
	Messages []struct {
		Role       string          `json:"role"`
		Content    json.RawMessage `json:"content"`
		ToolCallID string          `json:"tool_call_id"`
	} `json:"messages"`
}

type sentResponsesItem struct {
	Type    string          `json:"type"`
	Role    string          `json:"role"`
	CallID  string          `json:"call_id"`
	Output  json.RawMessage `json:"output"`
	Content []struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		ImageURL string `json:"image_url"`
		// Responses 的 detail 与 image_url 平级，不在它里面（CC 正相反）。
		Detail string `json:"detail"`
	} `json:"content"`
}

type sentResponsesRequest struct {
	Input []sentResponsesItem `json:"input"`
}

func decodeSent(t *testing.T, body []byte, into any) {
	t.Helper()
	if err := json.Unmarshal(body, into); err != nil {
		t.Fatalf("上游收到的请求体解不开: %v\n%s", err, body)
	}
}

// parseCCParts 把一条 CC 消息的 content 解成 part 数组；它是字符串形态时返回 nil。
func parseCCParts(t *testing.T, raw json.RawMessage) []sentCCPart {
	t.Helper()
	var parts []sentCCPart
	if err := json.Unmarshal(raw, &parts); err != nil {
		return nil
	}
	return parts
}

// ---- 六个格子：入口协议 × 渠道协议，各验一次「图真的到了上游」 ----

// CC→A：`image_url` 的 data URI 要拆成 Anthropic 的 `source{base64, media_type, data}`。
func TestCC2AImageReachesUpstreamAsAnthropicImage(t *testing.T) {
	gw, up := newCC2AGateway(t)
	up.RespondWith(200, map[string]string{"Content-Type": "application/json"}, anthropicOKBody)

	gw.Post(t, "/v1/chat/completions", imageFixtureBody(t, "in-cc-image"), nil)

	var sent sentAnthropicRequest
	decodeSent(t, up.Last(t).Body, &sent)

	var img *sentAnthropicBlock
	var text string
	for _, m := range sent.Messages {
		for i, b := range m.Content {
			switch b.Type {
			case "image":
				img = &m.Content[i]
			case "text":
				text = b.Text
			}
		}
	}
	if img == nil {
		t.Fatalf("图没到上游——CC 的 image_url 在 Anthropic 出口消失了: %s", up.Last(t).Body)
	}
	if img.Source.Type != "base64" {
		t.Errorf("source.type = %q，期望 base64", img.Source.Type)
	}
	if img.Source.MediaType != "image/png" {
		t.Errorf("source.media_type = %q，期望 image/png", img.Source.MediaType)
	}
	if img.Source.Data != tinyPNGBase64(t) {
		t.Errorf("base64 载荷与 tiny.png 不一致——图在中途被改写了")
	}
	// 同一条消息里的正文不能因为多了张图就被挤掉。
	if text == "" {
		t.Errorf("同条消息的文本块丢了: %s", up.Last(t).Body)
	}
}

// CC→R：data URI 原样进 `input_image.image_url`（Responses 那边是字符串，不是对象）。
func TestCC2RImageReachesUpstreamAsInputImage(t *testing.T) {
	gw, up := newCC2RGateway(t)
	up.RespondWith(200, map[string]string{"Content-Type": "application/json"}, responsesOKBody)

	gw.Post(t, "/v1/chat/completions", imageFixtureBody(t, "in-cc-image"), nil)

	var sent sentResponsesRequest
	decodeSent(t, up.Last(t).Body, &sent)

	found, hasText := false, false
	for _, item := range sent.Input {
		for _, part := range item.Content {
			switch part.Type {
			case "input_image":
				found = true
				if part.ImageURL != tinyPNGDataURI(t) {
					t.Errorf("input_image.image_url 与 tiny.png 的 data URI 不一致")
				}
			case "input_text":
				hasText = true
			}
		}
	}
	if !found {
		t.Fatalf("图没到上游: %s", up.Last(t).Body)
	}
	if !hasText {
		t.Errorf("同条消息的文本 part 丢了: %s", up.Last(t).Body)
	}
}

// A→CC：Anthropic 的 `source{base64}` 要拼回 data URI 塞进 `image_url.url`。
//
// 顺带钉住一条容易回退的东西：**没有图的消息仍发字符串 content**。CC 出口的纯文本
// 走字符串形态是 §5 坑清单里的既有裁决（第三方上游拒收纯文本的数组形态），图片这一
// 路不许把所有消息都改成数组。
func TestA2CCImageReachesUpstreamAsImageURL(t *testing.T) {
	gw, up := newConvertGateway(t)
	up.RespondWith(200, map[string]string{"Content-Type": "application/json"},
		`{"id":"chatcmpl-9","model":"`+ccUpstreamModel+`","choices":[{"index":0,`+
			`"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],`+
			`"usage":{"prompt_tokens":1,"completion_tokens":1}}`)

	gw.Post(t, "/v1/messages", imageFixtureBody(t, "in-anthropic-image"), nil)

	var sent sentCCRequest
	decodeSent(t, up.Last(t).Body, &sent)

	found, hasText := false, false
	for _, m := range sent.Messages {
		for _, part := range parseCCParts(t, m.Content) {
			switch part.Type {
			case "image_url":
				found = true
				if part.ImageURL.URL != tinyPNGDataURI(t) {
					t.Errorf("image_url.url 与 tiny.png 的 data URI 不一致")
				}
			case "text":
				hasText = true
			}
		}
	}
	if !found {
		t.Fatalf("图没到上游——Anthropic 的 image 块在 CC 出口消失了: %s", up.Last(t).Body)
	}
	if !hasText {
		t.Errorf("同条消息的文本 part 丢了: %s", up.Last(t).Body)
	}
}

// A→CC：不带图的请求，content 必须仍是字符串——图片这一路不许波及纯文本形态。
func TestA2CCTextOnlyStillSendsStringContent(t *testing.T) {
	gw, up := newConvertGateway(t)
	up.RespondWith(200, map[string]string{"Content-Type": "application/json"},
		`{"id":"chatcmpl-9","model":"`+ccUpstreamModel+`","choices":[{"index":0,`+
			`"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],`+
			`"usage":{"prompt_tokens":1,"completion_tokens":1}}`)

	gw.Post(t, "/v1/messages", ccSampleBody(t, "in-anthropic-text", false), nil)

	var sent sentCCRequest
	decodeSent(t, up.Last(t).Body, &sent)
	if len(sent.Messages) == 0 {
		t.Fatalf("上游一条消息都没收到: %s", up.Last(t).Body)
	}
	for _, m := range sent.Messages {
		var s string
		if err := json.Unmarshal(m.Content, &s); err != nil {
			t.Errorf("role=%s 的 content 不是字符串（%s）——纯文本走数组形态会被第三方上游拒",
				m.Role, m.Content)
		}
	}
}

// A→R：Anthropic 的 `source{base64}` 拼成 data URI 进 `input_image`。
func TestA2RImageReachesUpstreamAsInputImage(t *testing.T) {
	gw, up := newA2RGateway(t)
	up.RespondWith(200, map[string]string{"Content-Type": "application/json"}, responsesOKBody)

	gw.Post(t, "/v1/messages", imageFixtureBody(t, "in-anthropic-image"), nil)

	var sent sentResponsesRequest
	decodeSent(t, up.Last(t).Body, &sent)

	found := false
	for _, item := range sent.Input {
		for _, part := range item.Content {
			if part.Type == "input_image" {
				found = true
				if part.ImageURL != tinyPNGDataURI(t) {
					t.Errorf("input_image.image_url 与 tiny.png 的 data URI 不一致")
				}
			}
		}
	}
	if !found {
		t.Fatalf("图没到上游: %s", up.Last(t).Body)
	}
}

// R→A：`input_image` 的 data URI 拆回 Anthropic 的 `source{base64}`。
func TestR2AImageReachesUpstreamAsAnthropicImage(t *testing.T) {
	gw, up := newR2AGateway(t)
	up.RespondWith(200, map[string]string{"Content-Type": "application/json"},
		`{"id":"msg_1","model":"`+anthropicUpstreamModel+`","type":"message","role":"assistant",`+
			`"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn",`+
			`"usage":{"input_tokens":1,"output_tokens":1}}`)

	gw.Post(t, "/v1/responses", imageFixtureBody(t, "in-responses-image"), nil)

	var sent sentAnthropicRequest
	decodeSent(t, up.Last(t).Body, &sent)

	found := false
	for _, m := range sent.Messages {
		for _, b := range m.Content {
			if b.Type != "image" {
				continue
			}
			found = true
			if b.Source.Type != "base64" || b.Source.MediaType != "image/png" {
				t.Errorf("source = %+v，期望 base64 + image/png", b.Source)
			}
			if b.Source.Data != tinyPNGBase64(t) {
				t.Errorf("base64 载荷与 tiny.png 不一致")
			}
		}
	}
	if !found {
		t.Fatalf("图没到上游: %s", up.Last(t).Body)
	}
}

// R→CC：`input_image` 的 data URI 原样进 `image_url.url`。
func TestR2CCImageReachesUpstreamAsImageURL(t *testing.T) {
	gw, up := newResponsesConvertGateway(t)
	up.RespondWith(200, map[string]string{"Content-Type": "application/json"},
		`{"id":"chatcmpl-9","model":"`+ccUpstreamModel+`","choices":[{"index":0,`+
			`"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],`+
			`"usage":{"prompt_tokens":1,"completion_tokens":1}}`)

	gw.Post(t, "/v1/responses", imageFixtureBody(t, "in-responses-image"), nil)

	var sent sentCCRequest
	decodeSent(t, up.Last(t).Body, &sent)

	found := false
	for _, m := range sent.Messages {
		for _, part := range parseCCParts(t, m.Content) {
			if part.Type == "image_url" {
				found = true
				if part.ImageURL.URL != tinyPNGDataURI(t) {
					t.Errorf("image_url.url 与 tiny.png 的 data URI 不一致")
				}
			}
		}
	}
	if !found {
		t.Fatalf("图没到上游: %s", up.Last(t).Body)
	}
}

// ---- image_url.detail：CC 与 Responses 之间要过河，到 Anthropic 是丢弃项 ----
//
// 这三条走全链路而不是单测，理由同本文件开头：detail 的形状**两边不对称**（CC 在
// image_url 对象里面，Responses 在 part 顶层），两个半边各自「对」的写法拼起来照样
// 可能对不上。而它此前是丢在中间那一环上的——CC 入口明着丢，Responses 入口看着进了
// Extras，其实 Extras 永不外带，到出口一样死（口径层 v0.78 ①）。

// CC→R：`image_url.detail` 要落到 part 顶层的 `detail`。
func TestCC2RImageDetailReachesUpstream(t *testing.T) {
	gw, up := newCC2RGateway(t)
	up.RespondWith(200, map[string]string{"Content-Type": "application/json"}, responsesOKBody)

	gw.Post(t, "/v1/chat/completions", imageFixtureBody(t, "in-cc-image"), nil)

	var sent sentResponsesRequest
	decodeSent(t, up.Last(t).Body, &sent)

	found := false
	for _, item := range sent.Input {
		for _, part := range item.Content {
			if part.Type != "input_image" {
				continue
			}
			found = true
			if part.Detail != "high" {
				t.Errorf("input_image.detail = %q，期望 high——detail 没过河: %s",
					part.Detail, up.Last(t).Body)
			}
		}
	}
	if !found {
		t.Fatalf("图没到上游: %s", up.Last(t).Body)
	}
}

// R→CC：part 顶层的 `detail` 要落回 `image_url` 对象**里面**。
func TestR2CCImageDetailReachesUpstream(t *testing.T) {
	gw, up := newResponsesConvertGateway(t)
	up.RespondWith(200, map[string]string{"Content-Type": "application/json"},
		`{"id":"chatcmpl-9","model":"`+ccUpstreamModel+`","choices":[{"index":0,`+
			`"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],`+
			`"usage":{"prompt_tokens":1,"completion_tokens":1}}`)

	gw.Post(t, "/v1/responses", imageFixtureBody(t, "in-responses-image"), nil)

	var sent sentCCRequest
	decodeSent(t, up.Last(t).Body, &sent)

	found := false
	for _, m := range sent.Messages {
		for _, part := range parseCCParts(t, m.Content) {
			if part.Type != "image_url" {
				continue
			}
			found = true
			if part.ImageURL.Detail != "high" {
				t.Errorf("image_url.detail = %q，期望 high——detail 没过河: %s",
					part.ImageURL.Detail, up.Last(t).Body)
			}
		}
	}
	if !found {
		t.Fatalf("图没到上游: %s", up.Last(t).Body)
	}
}

// CC→A：Anthropic 没有对等的一格，detail 不许出现在请求体里（丢弃由 codec 登记
// image_detail，那一层归 anthropic 包的单测钉）。
func TestCC2AImageDetailIsNotSentToAnthropic(t *testing.T) {
	gw, up := newCC2AGateway(t)
	up.RespondWith(200, map[string]string{"Content-Type": "application/json"}, anthropicOKBody)

	gw.Post(t, "/v1/chat/completions", imageFixtureBody(t, "in-cc-image"), nil)

	body := string(up.Last(t).Body)
	if strings.Contains(body, "detail") {
		t.Errorf("detail 混进了 Anthropic 请求体: %s", body)
	}
}

// ---- tool_result 带图：容器形状三协议不一样 ----

// A→CC：CC 的 role=tool content 只收字符串，图得抬成**后续**独立的 user 消息。
//
// 「后续」这个位置不是可有可无的：抬到前面去，上游看到的是「先有图、后有工具结果」，
// 与实际发生的顺序相反。
func TestA2CCToolResultImageIsLiftedIntoUserMessage(t *testing.T) {
	gw, up := newConvertGateway(t)
	up.RespondWith(200, map[string]string{"Content-Type": "application/json"},
		`{"id":"chatcmpl-9","model":"`+ccUpstreamModel+`","choices":[{"index":0,`+
			`"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],`+
			`"usage":{"prompt_tokens":1,"completion_tokens":1}}`)

	gw.Post(t, "/v1/messages", imageFixtureBody(t, "in-anthropic-toolresult-image"), nil)

	var sent sentCCRequest
	decodeSent(t, up.Last(t).Body, &sent)

	toolIdx, liftedIdx := -1, -1
	for i, m := range sent.Messages {
		if m.Role == "tool" {
			toolIdx = i
			// role=tool 的 content 只能是字符串，图不许留在里面。
			var s string
			if err := json.Unmarshal(m.Content, &s); err != nil {
				t.Errorf("role=tool 的 content 不是字符串（%s）——CC 那边只收字符串", m.Content)
			}
			continue
		}
		for _, part := range parseCCParts(t, m.Content) {
			if part.Type == "image_url" && liftedIdx < 0 {
				liftedIdx = i
				if m.Role != "user" {
					t.Errorf("抬出来的图挂在 role=%q 上，期望 user", m.Role)
				}
				if part.ImageURL.URL != tinyPNGDataURI(t) {
					t.Errorf("抬出来的图与 tiny.png 不一致")
				}
			}
		}
	}
	if toolIdx < 0 {
		t.Fatalf("没有 role=tool 消息: %s", up.Last(t).Body)
	}
	if liftedIdx < 0 {
		t.Fatalf("tool_result 里的图没被抬出来，整个丢了: %s", up.Last(t).Body)
	}
	if liftedIdx < toolIdx {
		t.Errorf("抬出来的 user 消息排在 role=tool 之前（%d < %d）——顺序反了", liftedIdx, toolIdx)
	}
}

// A→R：`function_call_output.output` 只收字符串，同样抬成后续 user 消息。
func TestA2RToolResultImageIsLiftedIntoUserMessage(t *testing.T) {
	gw, up := newA2RGateway(t)
	up.RespondWith(200, map[string]string{"Content-Type": "application/json"}, responsesOKBody)

	gw.Post(t, "/v1/messages", imageFixtureBody(t, "in-anthropic-toolresult-image"), nil)

	var sent sentResponsesRequest
	decodeSent(t, up.Last(t).Body, &sent)

	outputIdx, liftedIdx := -1, -1
	for i, item := range sent.Input {
		if item.Type == "function_call_output" || item.Type == "custom_tool_call_output" {
			outputIdx = i
			var s string
			if err := json.Unmarshal(item.Output, &s); err != nil {
				t.Errorf("%s.output 不是字符串（%s）——Responses 那边只收字符串",
					item.Type, item.Output)
			}
			continue
		}
		for _, part := range item.Content {
			if part.Type == "input_image" && liftedIdx < 0 {
				liftedIdx = i
				if item.Role != "user" {
					t.Errorf("抬出来的图挂在 role=%q 上，期望 user", item.Role)
				}
				if part.ImageURL != tinyPNGDataURI(t) {
					t.Errorf("抬出来的图与 tiny.png 不一致")
				}
			}
		}
	}
	if outputIdx < 0 {
		t.Fatalf("没有工具结果项: %s", up.Last(t).Body)
	}
	if liftedIdx < 0 {
		t.Fatalf("tool_result 里的图没被抬出来，整个丢了: %s", up.Last(t).Body)
	}
	if liftedIdx < outputIdx {
		t.Errorf("抬出来的 user 项排在工具结果之前（%d < %d）——顺序反了", liftedIdx, outputIdx)
	}
}

// A→A 同协议透传不走 codec，图片这一路碰不到它——那条路是字节直传，没有解码编码。
// 这里不重复钉，透传的字节保真由 relay_test.go 负责。

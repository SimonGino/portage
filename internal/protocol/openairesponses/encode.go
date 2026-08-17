package openairesponses

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/SimonGino/portage/internal/protocol"
)

// 本文件是 OpenAI Responses 的**编码**侧：canonical 事件序列 → 下行响应。
//
// 线格式照 `testdata/golden/responses-stream-{text,tool-turn1/2,parallel-turn1/2}`
// 五份**真实上游 SSE 转录**（Codex CLI 0.147 实跑，2026-08-14 入库，#79），不是照
// sub2api 复述的。M2-1 时 protocol/event.go 记了一笔「Responses 侧没有真实上游转录，
// 事件名以 sub2api 为准，M2 拿到真实上游流后须复核」——词表与次序 2026-08-10 复核过，
// 字段级 2026-08-14 对着上面五份逐项比完，结论列在这里：
//
//   - 帧序：response.created → response.in_progress → (每个 output item 一组
//     output_item.added … output_item.done) → response.completed。
//   - 正文 item 比工具 item 多一层 content_part：added → output_text.delta* →
//     output_text.done → content_part.done。工具 item **没有** content_part。
//   - 每一帧的 data 里都带 sequence_number，从 0 起全流连号（实测 9 / 76 / 133 帧
//     无一例外，末帧号恒等于帧数减一）。
//   - 流**不发 `data: [DONE]`**——那是 Chat Completions 的收尾，Responses 以
//     response.completed 为终点。五份转录的最后一个字节就是它。
//   - usage 只在终帧的 response.usage 里，键集与 usageBody 逐字对上（含
//     `total_tokens = input_tokens + output_tokens`，实采 14534+96=14630 印证）。
//   - custom 工具 item 的终态字段名是 `input` 而不是 `arguments`，`status` 走
//     in_progress → completed。
//
// 字段级比对里**没对上、但有意不改**的三处：
//
//   - `custom_tool_call_input.done`：实采只有 item_id / output_index / input（opencodex
//     `src/bridge.ts` 的合成同款），这里多发 call_id 与 name。多出来的两格 wire-legal
//     ——sub2api 的合成也带（`apicompat/responses_client_tools.go`），而 codex-rs 认的是
//     output_item.done 那个 item，delta/done 只作预览。多发不伤，删了也没收益，不动。
//   - delta 帧的 `obfuscation`：上游每片都带，这里不发。它是 padding 不是密钥
//     （opencodex 的 copilot repair 直接 delete 掉），漏发无语义损失。
//   - message item 的 `phase`（实采是 `commentary`）与 `metadata` /
//     `internal_chat_message_metadata_passthrough`（回显 turn_id 与 create_time）：
//     后两个是上游/中转的账本，我们没有对应物；`phase` 按 commentary / final
//     合成与否 PO 已裁（2026-08-14，#79）：**不合成，维持现状**——语义源头
//     （Anthropic/CC 上游）没有对应概念，opencodex 无 phase 也能跑，不发不是 bug；
//     哪天实测出客户端行为差异再立票。
//
// **终帧的 response.output 列表不能拿这批样本作数**：实采里它是中转重组的降级形态
// （工具 item 被改回 function_call、丢了 arguments，reasoning item 整个不见），压缩批与
// reasoning 批同渠道同形态。这里照 OpenAI 契约列全 item，与实采不同是**有意**的。
//
// 与 sub2api 的差异：事件名全部对得上。event.go 那条待复核注释据此销账。

// Flusher 是 EncodeStream 每帧之后要调的钩子。理由同 anthropic 包：codec 是纯编码
// 层，测试里的 w 是 bytes.Buffer，不该有 HTTP 概念。
type Flusher interface{ Flush() }

// EncodeStream 把事件流编成 Responses SSE 下发。
func (c *Codec) EncodeStream(w io.Writer, events <-chan protocol.Event) error {
	enc := c.newStreamEncoder(w)
	for ev := range events {
		if err := enc.event(ev); err != nil {
			return err
		}
	}
	return enc.finish()
}

// newStreamEncoder 把每请求状态从 codec 实例搬到编码器上，两条编码入口共用。
func (c *Codec) newStreamEncoder(w io.Writer) *streamEncoder {
	return &streamEncoder{
		w:           w,
		customTools: c.customTools,
		compaction:  c.compaction,
		now:         time.Now,
		beatEvery:   heartbeatInterval,
	}
}

type streamEncoder struct {
	w io.Writer

	// customTools 来自同一个 codec 实例的 DecodeRequest（见 codec.go 的实例约定）。
	customTools map[string]bool

	// compaction 打开本地合成模式（#74）：正常 output item 一个不发，assistant 正文
	// 攒起来，收尾时合成恰好一个 compaction item。同样来自 DecodeRequest。
	compaction     bool
	compactionText strings.Builder

	// now / beatEvery 只服务静默期心跳。now 可注入是为了让心跳节奏能被测——它是
	// 这台编码器里唯一与真实时间有关的行为。
	now       func() time.Time
	beatEvery time.Duration
	lastBeat  time.Time

	seq         int
	started     bool
	id          string
	model       string
	outputIndex int

	textOpen bool
	textItem string
	textBuf  strings.Builder

	// pending 是正在攒的工具调用。攒而不是逐片转发，理由见 flushTool。
	pending *toolPending
	// done 是已经收口的 output item，终帧的 response.output 要照原样列一遍。
	done []any

	usage protocol.Usage
	stop  string
	// truncated 记 EvDone 是不是解码侧兜底合成的（上游没声明 stop reason 就断了）。
	// 只有合成模式读它。
	truncated  bool
	finished   bool
	sawErrored bool
	// finalStatus 是终帧用的 status，由 finish 定；非流式那条路要用同一个判断，
	// 各算一遍必然漂移。
	finalStatus string
}

type toolPending struct {
	index int
	id    string
	name  string
	args  []string
}

func (e *streamEncoder) event(ev protocol.Event) error {
	if e.compaction {
		switch ev.Type {
		case protocol.EvTextDelta:
			// 摘要正文只进缓冲，不上线。发成普通 assistant 消息的话，Codex 会同时收到
			// 「一段摘要文本」和「一个装着同一段摘要的 compaction item」，而后者是要被
			// 当成**替换历史**装回去的——重复一份等于把摘要写进历史两次。
			e.compactionText.WriteString(ev.Text)
			if err := e.ensureStarted(); err != nil {
				return err
			}
			return e.heartbeat()
		case protocol.EvThinkingDelta, protocol.EvToolCallStart, protocol.EvToolArgsDelta, protocol.EvToolCallEnd:
			// summarizer turn 已经把 tools 剥了（decode.go 的 rewriteAsSummarizer），
			// 上游还是调了工具的话，那次调用对压缩没有意义，也不能占用 output——
			// 一个工具 item 混进去，「恰好一个 compaction item」就不成立了。
			//
			// 吞掉归吞掉，心跳照发：rewriteAsSummarizer **有意留着 reasoning**，开思考
			// 的上游会先想上几十秒再写第一个摘要 token，那段静默是 summarizer turn 的
			// 常态而不是边角。
			if err := e.ensureStarted(); err != nil {
				return err
			}
			return e.heartbeat()
		}
		// 其余事件（message_start / usage / done / error）照常走下面那台状态机。
	}
	switch ev.Type {
	case protocol.EvMessageStart:
		e.id, e.model = ev.ID, ev.Model
		return e.ensureStarted()

	case protocol.EvTextDelta:
		if ev.Text == "" {
			return nil
		}
		if err := e.ensureStarted(); err != nil {
			return err
		}
		if !e.textOpen {
			if err := e.openText(); err != nil {
				return err
			}
		}
		e.textBuf.WriteString(ev.Text)
		return e.frame("response.output_text.delta", map[string]any{
			"type":          "response.output_text.delta",
			"item_id":       e.textItem,
			"output_index":  e.outputIndex,
			"content_index": 0,
			"delta":         ev.Text,
			"logprobs":      []any{},
		})

	case protocol.EvThinkingDelta:
		// 丢弃——而且 #25（R→A）之后这条**真的会被走到**：Anthropic 解码侧产
		// thinking_delta 与 signature_delta（五份真实转录实测）。写这条注释时它还是
		// 死路（CC 解码侧不产推理事件），现在不是了。
		//
		// 仍然丢，理由没变：Responses 确实有 response.reasoning_summary_text.delta
		// 可以承接，但要写它得先有真实转录来钉 reasoning item 的生命周期——手上三份
		// Responses 转录里的 reasoning item 只有 encrypted_content，一条 delta 都
		// 没有。照文档猜着写一个会在流里凭空造 item 的分支，比明着丢更危险。
		//
		// 代价是 Codex 在 R→A 路径上看不到 Claude 的推理过程（§9.2 缺口）。signature
		// 尤其不能漏进正文：那是一串 base64，漏了客户端会把它当回答渲染出来。
		return nil

	case protocol.EvToolCallStart, protocol.EvToolArgsDelta:
		return e.bufferTool(ev)

	case protocol.EvToolCallEnd:
		return e.flushTool(ev.Index)

	case protocol.EvUsage:
		if ev.Usage != nil {
			// 累计快照语义：后来者的非零字段覆盖，不做加法（protocol/event.go）。
			e.usage.MergeSnapshot(*ev.Usage)
		}
		return nil

	case protocol.EvDone:
		e.stop = ev.StopReason
		e.truncated = ev.Truncated
		return nil

	case protocol.EvError:
		return e.writeError(ev)
	}
	return nil
}

func (e *streamEncoder) bufferTool(ev protocol.Event) error {
	if e.pending == nil || e.pending.index != ev.Index {
		if ev.Type != protocol.EvToolCallStart {
			// 没见过 Start 的 index 冒出来了：补空壳而不是丢弃，同 anthropic 侧的
			// 理由——半个调用也比凭空消失强。
			e.pending = &toolPending{index: ev.Index}
		} else {
			e.pending = &toolPending{index: ev.Index, id: ev.ToolID, name: ev.ToolName}
			return nil
		}
	}
	if ev.Type == protocol.EvToolCallStart {
		e.pending.id, e.pending.name = ev.ToolID, ev.ToolName
		return nil
	}
	if ev.Text != "" {
		e.pending.args = append(e.pending.args, ev.Text)
	}
	return nil
}

// flushTool 把攒好的工具调用整组写出去。
//
// 为什么必须攒满再发：custom 工具的入参要**对称解包**——CC 出口把非 JSON 入参包成
// 了 `{"input":"<原文>"}`（openaicc/encode.go 的 argsWrapKey），这里得原样拆回来，
// 而 JSON 字符串的转义没法按分片增量解。代价是丢掉了上游的分片节奏，但这条路上
// 本来就没有节奏可丢：CC 流里没有逐条工具终止符，解码侧已经把分片攒到流末尾一次性
// 冲出（§5 坑清单）。
func (e *streamEncoder) flushTool(index int) error {
	if e.pending == nil || e.pending.index != index {
		return nil
	}
	pending := e.pending
	e.pending = nil

	if err := e.ensureStarted(); err != nil {
		return err
	}
	// 正文 item 先收口：Responses 的 output 是**有序 item 列表**，一个 item 没
	// done 就开下一个，output_index 会对不上。
	if err := e.closeText(); err != nil {
		return err
	}

	args := strings.Join(pending.args, "")
	custom := e.customTools[pending.name]

	itemID, itemType, argsField := "fc_"+rand.Text(), "function_call", "arguments"
	deltaEvent, doneEvent := "response.function_call_arguments.delta", "response.function_call_arguments.done"
	if custom {
		args = protocol.UnwrapCustomToolArgs(args)
		itemID, itemType, argsField = "ctc_"+rand.Text(), "custom_tool_call", "input"
		deltaEvent, doneEvent = "response.custom_tool_call_input.delta", "response.custom_tool_call_input.done"
	} else if args == "" {
		// function 形态的 arguments 按契约是 JSON 字符串，空串客户端解不动。
		args = "{}"
	}

	item := map[string]any{
		"id": itemID, "type": itemType, "status": "in_progress",
		"call_id": pending.id, "name": pending.name, argsField: "",
	}
	if err := e.frame("response.output_item.added", map[string]any{
		"type": "response.output_item.added", "output_index": e.outputIndex, "item": item,
	}); err != nil {
		return err
	}
	if args != "" {
		if err := e.frame(deltaEvent, map[string]any{
			"type": deltaEvent, "item_id": itemID, "output_index": e.outputIndex, "delta": args,
		}); err != nil {
			return err
		}
	}
	if err := e.frame(doneEvent, map[string]any{
		"type": doneEvent, "item_id": itemID, "output_index": e.outputIndex,
		"call_id": pending.id, "name": pending.name, argsField: args,
	}); err != nil {
		return err
	}

	final := map[string]any{
		"id": itemID, "type": itemType, "status": "completed",
		"call_id": pending.id, "name": pending.name, argsField: args,
	}
	if err := e.frame("response.output_item.done", map[string]any{
		"type": "response.output_item.done", "output_index": e.outputIndex, "item": final,
	}); err != nil {
		return err
	}
	e.done = append(e.done, final)
	e.outputIndex++
	return nil
}

func (e *streamEncoder) openText() error {
	e.textItem = "msg_" + rand.Text()
	e.textBuf.Reset()
	if err := e.frame("response.output_item.added", map[string]any{
		"type": "response.output_item.added", "output_index": e.outputIndex,
		"item": map[string]any{
			"id": e.textItem, "type": "message", "status": "in_progress",
			"role": "assistant", "content": []any{},
		},
	}); err != nil {
		return err
	}
	if err := e.frame("response.content_part.added", map[string]any{
		"type": "response.content_part.added", "item_id": e.textItem,
		"output_index": e.outputIndex, "content_index": 0,
		"part": textPart(""),
	}); err != nil {
		return err
	}
	e.textOpen = true
	return nil
}

func (e *streamEncoder) closeText() error {
	if !e.textOpen {
		return nil
	}
	e.textOpen = false
	text := e.textBuf.String()

	if err := e.frame("response.output_text.done", map[string]any{
		"type": "response.output_text.done", "item_id": e.textItem,
		"output_index": e.outputIndex, "content_index": 0,
		"text": text, "logprobs": []any{},
	}); err != nil {
		return err
	}
	if err := e.frame("response.content_part.done", map[string]any{
		"type": "response.content_part.done", "item_id": e.textItem,
		"output_index": e.outputIndex, "content_index": 0,
		"part": textPart(text),
	}); err != nil {
		return err
	}
	item := map[string]any{
		"id": e.textItem, "type": "message", "status": "completed",
		"role": "assistant", "content": []any{textPart(text)},
	}
	if err := e.frame("response.output_item.done", map[string]any{
		"type": "response.output_item.done", "output_index": e.outputIndex, "item": item,
	}); err != nil {
		return err
	}
	e.done = append(e.done, item)
	e.outputIndex++
	return nil
}

func (e *streamEncoder) ensureStarted() error {
	if e.started {
		return nil
	}
	e.started = true
	if e.id == "" {
		e.id = fallbackResponseID()
	}
	// 心跳的计时从「流真的开了」起算：created/in_progress 两帧本身就是新鲜字节，
	// 紧接着再补一行注释没有意义。
	e.lastBeat = e.now()
	// created 与 in_progress 载的是同一个 response 对象（实采转录里两帧逐字节相同，
	// 只差 sequence_number）。两帧都发是因为 Codex 按 in_progress 判定「上游真的开工
	// 了」，只发 created 会让它一直等。
	body := e.responseBody("in_progress", nil, false)
	if err := e.frame("response.created", map[string]any{
		"type": "response.created", "response": body,
	}); err != nil {
		return err
	}
	return e.frame("response.in_progress", map[string]any{
		"type": "response.in_progress", "response": body,
	})
}

func (e *streamEncoder) finish() error {
	if e.finished || e.sawErrored {
		return nil
	}
	e.finished = true
	if err := e.ensureStarted(); err != nil {
		return err
	}
	if e.compaction {
		return e.finishCompaction()
	}
	// 上游流断在半截（没等到 EvToolCallEnd）时，攒着的那个调用照样放出去。
	if e.pending != nil {
		if err := e.flushTool(e.pending.index); err != nil {
			return err
		}
	}
	if err := e.closeText(); err != nil {
		return err
	}

	status, event := "completed", "response.completed"
	if e.stop == "length" {
		// 截断有独立的终帧与状态：客户端据此决定要不要续写。混在 completed 里发，
		// 被截断的回答看上去就是「模型说完了」。
		status, event = "incomplete", "response.incomplete"
	}
	e.finalStatus = status
	return e.frame(event, map[string]any{
		"type": event, "response": e.responseBody(status, e.done, true),
	})
}

// heartbeatInterval 是静默期心跳的最小间隔。取 15 秒：要显著小于常见反代的空闲超时
// （nginx proxy_read_timeout 默认 60 秒），又不至于把一条流灌满注释行。
const heartbeatInterval = 15 * time.Second

// heartbeat 在合成期的静默里发一行 SSE 注释（#74 范围 5）。
//
// 合成模式下从 response.in_progress 到终帧之间下行零字节——上游正在写摘要，可能是几十
// 秒。portage 没有 wire keepalive 层（writeDeadline 只管「写出去要多久」，它不发字节），
// 中间那道反代会按空闲超时把连接掐了，客户端看到的是一次莫名其妙的断流。
//
// 注释行（`:` 开头）是 SSE 规范里的合法帧，任何合规解析器都忽略它，因此不会被误当成
// 事件、也不进 sequence_number 的连号。
//
// **能盖住的只有「增量在流、但被我们吞掉」这一种静默**——正文增量与思考增量都算
// （思考那段往往是最长的一截，见 event 里的吞掉分支）。上游整体卡住时它不发：那种
// 情况本来就该由上游超时接管，不该由一条假装还活着的心跳掩盖。
func (e *streamEncoder) heartbeat() error {
	now := e.now()
	if now.Sub(e.lastBeat) < e.beatEvery {
		return nil
	}
	e.lastBeat = now
	if _, err := io.WriteString(e.w, ": portage compaction in progress\n\n"); err != nil {
		return err
	}
	if f, ok := e.w.(Flusher); ok {
		f.Flush()
	}
	return nil
}

// compactionFailure 说明这次压缩 turn 为什么产不出 item。
//
// 两个字段分得开是有用的：wireReason 非空时它是 Responses 线格
// `incomplete_details.reason` 的**合法取值**，终帧走 response.incomplete；为空则线格上
// 没有对得上的取值，只能走 response.failed，把 message 那句给人看。混成一个 string 的
// 话，`empty_summary` 这种自造哨兵会混进线格字段冒充 reason 码。
type compactionFailure struct {
	wireReason string
	message    string
}

// compactionNoItem 判这次压缩 turn 能不能产 item；能产返回 nil。
//
// 五种不能产，共用一条理由：compaction item 会被 Codex 当作**替换历史**装回去，把一份
// 残缺或空白的摘要装进去，等于永久删掉了这段会话的前半程。宁可让这次压缩明着失败。
//
// 前四支合起来对 canonical 的停因全集（stop / length / content_filter / tool_calls）
// 是完备的：只有 `stop` 能往下走到产 item 那一步。这不是凑数——漏掉任何一个非 stop
// 停因，都等于把「上游没写完」当成「上游写完了」。
//
// content_filter 单列一条不是多余的：正常路径把它并进 completed（见 finish），而合成
// 模式下「completed + 零个 item」正是 #71 要杀的那个静默 Fatal 形态。
//
// truncated 那条是这里唯一**看不出**破绽的一种：解码侧为了给下游一个合法取值，会把
// 断流兜成 `stop`，wire 上与真正的收尾一模一样，只有 EvDone 的 Truncated 位分得开。
// 光看 e.stop 的话，一段写到一半的摘要会带着 completed 装回历史里。
func (e *streamEncoder) compactionNoItem() *compactionFailure {
	switch {
	case e.stop == "length":
		return &compactionFailure{wireReason: "max_output_tokens", message: "压缩未完成：摘要被上游截断"}
	case e.stop == "content_filter":
		return &compactionFailure{wireReason: "content_filter", message: "压缩未完成：摘要被上游内容过滤拦下"}
	case e.stop == "tool_calls":
		// rewriteAsSummarizer 剥了 tools，合规上游到不了这里；但 event() 那条吞工具
		// 事件的分支已经承认「上游照调不误」是可能的（兼容网关自带服务端工具），而
		// 挡住事件却收下停在工具调用前的半截正文，等于那道防线只修了一半。
		//
		// 没有对得上的 incomplete_details.reason，所以留空走 response.failed。
		return &compactionFailure{message: "压缩未完成：上游改去调工具了，摘要停在半截"}
	case e.truncated || e.stop == "":
		// e.stop 空是给直接喂事件的调用方留的余量——今天两个解码器都会兜成 stop，
		// 兜不着的路径（将来的解码器、测试直喂）不该因此漏过去。
		return &compactionFailure{message: "压缩未完成：上游流在摘要收尾前断了"}
	case e.compactionText.Len() == 0:
		return &compactionFailure{message: "压缩未完成：上游没有产出摘要正文"}
	}
	return nil
}

// finishCompaction 收合成模式的尾：合成恰好一个 compaction item + response.completed，
// 或者一个 item 都不产、发一个明着失败的终帧。
//
// 只发 output_item.done，不发配套的 output_item.added——与 opencodex
// （`src/bridge.ts` 的合成分支）一致。这个 item 不是逐步生成出来的，added 描述的那个
// 「开始了」的时刻并不存在。
func (e *streamEncoder) finishCompaction() error {
	fail := e.compactionNoItem()
	if fail == nil {
		item := map[string]any{
			"id":                "cmp_" + rand.Text(),
			"type":              itemCompaction,
			"encrypted_content": encodeCompactionSummary(e.compactionText.String()),
		}
		if err := e.frame("response.output_item.done", map[string]any{
			"type": "response.output_item.done", "output_index": e.outputIndex, "item": item,
		}); err != nil {
			return err
		}
		e.done = append(e.done, item)
		e.outputIndex++
		e.finalStatus = "completed"
		return e.frame("response.completed", map[string]any{
			"type": "response.completed", "response": e.responseBody("completed", e.done, true),
		})
	}

	if fail.wireReason == "" {
		// 线格上没有对得上的 incomplete_details 取值（上游正常收尾却一个字没写、或者
		// 流断在收尾之前），而 completed + 零 item 又恰恰是要避免的那个形态——发
		// response.failed，把话说清楚。
		e.finalStatus = "failed"
		body := e.responseBody("failed", e.done, true)
		body["error"] = map[string]any{
			"code":    "server_error",
			"message": fail.message,
		}
		e.sawErrored = true
		return e.frame("response.failed", map[string]any{
			"type": "response.failed", "response": body,
		})
	}

	e.finalStatus = "incomplete"
	body := e.responseBody("incomplete", e.done, true)
	// responseBody 对 incomplete 默认写 max_output_tokens（正常路径只有截断一种）；
	// 合成模式下 content_filter 也走 incomplete，所以这里按真实理由覆盖掉。
	body["incomplete_details"] = map[string]any{"reason": fail.wireReason}
	return e.frame("response.incomplete", map[string]any{
		"type": "response.incomplete", "response": body,
	})
}

// responseBody 造 response 对象。created / in_progress / completed 三帧共用一个形状，
// 差别只在 status、output 与 usage。
func (e *streamEncoder) responseBody(status string, output []any, withUsage bool) map[string]any {
	if output == nil {
		output = []any{}
	}
	body := map[string]any{
		"id":                   e.id,
		"object":               "response",
		"created_at":           time.Now().Unix(),
		"status":               status,
		"model":                e.model,
		"output":               output,
		"error":                nil,
		"incomplete_details":   nil,
		"instructions":         nil,
		"previous_response_id": nil,
		"metadata":             map[string]any{},
		"parallel_tool_calls":  false,
		"tool_choice":          "auto",
		"tools":                []any{},
	}
	if status == "incomplete" {
		// 正常路径上 incomplete 只有截断一种成因（见 finish）。合成模式还会因内容过滤
		// 走到 incomplete，那条由 finishCompaction 覆盖这一项。
		body["incomplete_details"] = map[string]any{"reason": "max_output_tokens"}
	}
	if withUsage {
		body["usage"] = usageBody(e.usage)
	}
	return body
}

// writeError 把流内错误按 Responses 的 response.failed 发下去。
//
// 只在首字节写出之后才走得到这里——此前的错误由 EncodeError 走 HTTP 状态码那条路。
func (e *streamEncoder) writeError(ev protocol.Event) error {
	e.sawErrored = true
	msg := ev.Message
	if msg == "" {
		msg = "上游响应流异常中断"
	}
	if err := e.ensureStarted(); err != nil {
		return err
	}
	body := e.responseBody("failed", e.done, true)
	body["error"] = map[string]any{"code": "server_error", "message": msg}
	return e.frame("response.failed", map[string]any{
		"type": "response.failed", "response": body,
	})
}

func (e *streamEncoder) frame(event string, payload map[string]any) error {
	// sequence_number 全流连号，实采每帧都有。客户端拿它判丢帧，缺了或跳号等于告诉
	// 对方「这条流不完整」。
	payload["sequence_number"] = e.seq
	e.seq++

	data, err := marshal(payload)
	if err != nil {
		return err
	}
	var buf strings.Builder
	buf.WriteString("event: ")
	buf.WriteString(event)
	buf.WriteString("\ndata: ")
	buf.Write(data)
	buf.WriteString("\n\n")
	if _, err := io.WriteString(e.w, buf.String()); err != nil {
		return err
	}
	// 每帧一 flush：攒着发等于把逐字输出重新变成一次性吐出。
	if f, ok := e.w.(Flusher); ok {
		f.Flush()
	}
	return nil
}

// EncodeFullBody 把完整事件序列聚合成非流式 Responses 响应体。
//
// 复用流式那台状态机：把事件喂给同一个 encoder，写到一个丢弃 writer 上，只为让它
// 把 done 列表和 usage 攒齐。两套聚合逻辑各写一遍必然漂移——非流式路径的样本远比
// 流式少，漂了也不容易发现。
func (c *Codec) EncodeFullBody(events []protocol.Event) ([]byte, error) {
	enc := c.newStreamEncoder(io.Discard)
	for _, ev := range events {
		if ev.Type == protocol.EvError {
			return nil, fmt.Errorf("openairesponses: 上游响应错误: %s", ev.Message)
		}
		if err := enc.event(ev); err != nil {
			return nil, err
		}
	}
	if err := enc.finish(); err != nil {
		return nil, err
	}
	if c.compaction && len(enc.done) == 0 {
		// 非流式压缩 turn 没能产出 item。回一份「completed 但空 output」的响应就是
		// #71 要杀的静默 Fatal，所以这里报错让调用方按上游错误处理（转换路径会回一个
		// 带原因的 5xx，客户端至少看得见出了事）。
		msg := "上游没有产出摘要正文"
		if fail := enc.compactionNoItem(); fail != nil {
			msg = fail.message
		}
		return nil, fmt.Errorf("openairesponses: 压缩 turn 未产出 compaction item: %s", msg)
	}
	return marshal(enc.responseBody(enc.finalStatus, enc.done, true))
}

// usageBody 按 Responses 的 usage 形状写计数。
//
// 直映即可：canonical 的 InputTokens 与 Responses 的 input_tokens 同为**毛值**
// （protocol.Usage 的约定），cached_tokens 是它的**明细**而非另一笔，不再相加。
// total_tokens 因此不会低估——Codex 拿它判自动压缩的触发点。
func usageBody(u protocol.Usage) map[string]any {
	return map[string]any{
		"input_tokens": u.InputTokens,
		"input_tokens_details": map[string]any{
			"cached_tokens":      u.CacheReadTokens,
			"cache_write_tokens": u.CacheWriteTokens,
		},
		"output_tokens": u.OutputTokens,
		// 这一格此前硬编码 0（口径层 v0.66，#97 订正）：canonical 装不下这个数的时候
		// 写 0 还算凑合，装得下之后就是把上游报的思考成本抹成零。
		//
		// 与 CC 出口「有数才写」不同，这里键**恒在**：Responses 的 usage 契约里
		// output_tokens_details 是必有项（真实转录里非推理轮也带 reasoning_tokens:0），
		// 缺键可能被 Codex 判为非法。所以这一侧「没报」只能落回 0，分不出来——那正是
		// 流水另立一列的理由（Summary.HasReasoningTokens）。
		"output_tokens_details": map[string]any{"reasoning_tokens": u.ReasoningTokens},
		"total_tokens":          u.InputTokens + u.OutputTokens,
	}
}

func textPart(text string) map[string]any {
	return map[string]any{
		"type": "output_text", "text": text,
		"annotations": []any{}, "logprobs": []any{},
	}
}

// fallbackResponseID 在上游一个 id 都没给时补一个。
//
// 与 anthropic 侧同规矩（口径层 v0.31 + 展开层 §7.4）：有上游 id 一律原样透传，所以
// `chatcmpl-` 出现即代表「上游给的」、`resp_` 出现即代表「网关补的」，排障一眼能分。
func fallbackResponseID() string { return "resp_" + rand.Text() }

// marshal 关掉 HTML 转义：工具入参里 `<` `>` `&` 很常见（exec 收的是 JS 源码），
// 转义后语义不变但人工排障与样本比对时满屏 < 没法看。
func marshal(v any) ([]byte, error) {
	var buf strings.Builder
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, fmt.Errorf("openairesponses: 编码响应: %w", err)
	}
	return []byte(strings.TrimRight(buf.String(), "\n")), nil
}

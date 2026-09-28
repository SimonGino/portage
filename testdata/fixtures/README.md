# testdata/fixtures —— 构造样本（**不是** golden）

这里放的是**照官方文档形状手工构造**的样本。它与 `testdata/golden/` 是两回事，别混：

| | `testdata/golden/` | `testdata/fixtures/`（本目录） |
| --- | --- | --- |
| 出处 | 真实 harness 发包 / 真实上游 SSE 转录（`cmd/goldenrec` 录） | 照官方文档改真实转录的数字构造出来的 |
| meta 闸门 | `verified: true`（人核过才准进） | `synthetic: true`（钉死它不是转录） |
| 能证明什么 | 上游真的这么发 | 我们的解析对**这个形状**是对的 |
| 不能证明什么 | —— | 上游真的这么发 |

**构造样本不得改名搬进 `golden/`。** 真录到了就在 `golden/` 下新开目录、走 `verified` 那道闸；
本目录这份留着当形状回归即可。

`tiny.png` 是 #1 图片转换的像素（1×1 真 PNG，不是假 base64 串）。

## in-cc-image / in-anthropic-image / in-responses-image / in-anthropic-toolresult-image

#1 图片跨协议转换的入站样本，四份都是**手工构造**：真实 harness 至今没发过带图的轮次——
六份 `in-cc-*`、五份 `in-anthropic-*`、四份 `in-responses-*` 实采样本的 `content` 全是字符串。
形状照三家官方文档写，图是 `tiny.png` 的真字节（口径层 v0.39：不用假 base64 串、不截断存
hash——往返验不了，而往返正是图片这格唯一值得测的东西）。

前三份各一张图配一段正文，钉六个转换格子的载荷保真；第四份把图放进 `tool_result.content`，
钉「CC / Responses 出口要把它抬成后续独立 user 消息」这条转换约束。

**它们一度放在 `golden/` 且 meta 写 `verified: true`**（PO 2026-08-17 裁定挪回本目录）：
那既违反上面那句「不得改名搬进 golden/」，也把 `verified` 的含义从「人核过的转录」稀释成
「人核过」。代价是 `TestCanonicalModelCoversInboundSamples` 要多扫一个根——它扫两处，各走
各的闸（`golden/in-*` 认 `verified`、`fixtures/in-*` 认 `synthetic`）。**这是刻意的**：
那张覆盖表要证的是「canonical 装不装得下」，而能证明形状的样本不一定是转录；把构造样本挡在
表外，图片那几行会当场被判成「表随样本烂掉」的陈旧项。

消费方：`internal/protocol/canonical_coverage_test.go`（覆盖表）、
`internal/server/convert_image_test.go`（六格全链路 + 抬图）。

## in-anthropic-toolresult-imageonly

[#110](https://github.com/SimonGino/portage/issues/110)：tool_result 只有一张图、没有文本，**手工构造**——
在 `in-anthropic-toolresult-image` 基础上去掉那段正文（真实 harness 至今没发过只带图不带文本的工具结果）。
钉的是：CC 出口 role=tool 的 `content`、R 出口 `function_call_output.output` 在只有图被抬出时都发占位串
`"[image]"`，不发空串——模型读到空 content 后紧跟一条图片消息会把因果关系搞错。图是 `tiny.png` 的真字节。

消费方：`internal/protocol/canonical_coverage_test.go`（覆盖表）、
`internal/server/convert_image_test.go`（`TestA2CCImageOnlyToolResultGetsImagePlaceholder`、
`TestA2RImageOnlyToolResultGetsImagePlaceholder`）。

## in-anthropic-redacted-thinking

#99 的入站样本，**手工构造**：本库实采的 `in-anthropic-*` 与 `golden/in-anthropic-thinking-replay`
只有明文 `thinking`，没有 `redacted_thinking`（Fable 5.1 不产，Opus/Sonnet 4.x 才产，本机无转录）。
形状照 Anthropic extended-thinking 文档写：`{"type":"redacted_thinking","data":...}` 后接同轮
`tool_use`，data 是编的 base64 串。

钉的是：解码归一成 `BlockThinking`（密文落 `Extras["data"]`），三个出口一律丢并登记
`DropThinking`，不落 `vendor_content`。Anthropic 出口也丢（口径层 v0.62 ③，回带一律丢），
不原样回写——生产上 A→A 走透传到不了编码器；真录到带 redacted_thinking 的回放再进 `golden/`。

消费方：`internal/protocol/canonical_coverage_test.go`（覆盖表）、
`anthropic.TestDecodeRequestRedactedThinkingBecomesBlockThinking`、
`anthropic.TestEncodeRequestDropsRedactedThinking`、
`openaicc.TestEncodeRedactedThinkingDroppedAsThinking`、
`openairesponses.TestEncodeRedactedThinkingDroppedAsThinking`。

## in-anthropic-document / in-cc-file / in-responses-input-file

[#100](https://github.com/SimonGino/portage/issues/100) 文件块记档，三份都是**手工构造**：实采入站样本里没有带文件的轮次。
形状照三家官方文档写（A `document` + `source.base64` + `title`；CC `{type:file, file:{filename, file_data}}`；
R `input_file` 的 `filename` + `file_data`），litellm 的 A→CC / A→R 适配器读写的是同一形状。PDF 载荷是
`%PDF-1.4` / `%%EOF` 两行的真字节。A 与 R 两份另在工具结果里嵌一个文件块。

钉的是：解码归一成 `BlockDocument`（载荷进 Extras），跨协议两个出口登记 `document` 档、不落
`vendor_content`、载荷不外带；工具结果里那块单独也要登记（A→R / R→A 此前静默丢）。映成对端文件块
不在本票，另见 [#101](https://github.com/SimonGino/portage/issues/101)。

消费方：`internal/protocol/document_fixture_test.go`、`internal/protocol/canonical_coverage_test.go`（覆盖表）。

## anthropic-cache-hit / anthropic-stream-cache-hit

补的是 #37 第 2 项的**一半**：`anthropic-*` 六份真实样本的 cache 计数全是 0（中转那侧压根
不回），于是 `cache_read_input_tokens` / `cache_creation_input_tokens` 的解析路径此前只有 CC
样本走到过——Anthropic 侧读错了没有任何样本会发现。

这两份从 `golden/anthropic-text` 与 `golden/anthropic-stream-text` 的真实转录派生，**只改
usage 里的数字**，其余字节一字未动。数字取值依据 platform.claude.com/docs/en/api/rate-limits
（2026-08-13 核对）：`input_tokens` 只算最后一个缓存断点**之后**的 token，与两项缓存互不相交，
即 `total_input = cache_read + cache_creation + input`。

**真实转录已随 [#13](https://github.com/SimonGino/portage/issues/13) 入库**（2026-08-20，
pollo-sub2api——响应体逐字节透明的中转，按上面说的形状连打两遍）：`golden/` 下
`anthropic-cache-write` / `anthropic-cache-hit` 与 `anthropic-stream-cache-*` 四份，流式与
非流式各一对「建缓存 + 命中」。本目录这两份照首段的规矩**留作形状回归**，不删不搬。
官方**直连**实测那一格仍归 [#2](https://github.com/SimonGino/portage/issues/2)——中转透明
证明的是「这条链路没吃 usage 字段」，不是「官方就这么发」。

## in-responses-namespace-collision / in-responses-namespace-badname

[#94](https://github.com/SimonGino/portage/issues/94) Responses `type=namespace` 摊平的两道就地 400
（口径层 v1.14 ③④），两份都是**手工构造**——ADE 实采 `golden/in-responses-namespace-turn1`
的 55 个工具既不撞名、最长的摊平名也只有 48 字符，真实发包里撞不到这两条线。

- `collision`：`request_user_input` 既是顶层 function（ADE 的摆法）又是 `functions` 默认命名
  空间的子项（Codex 的摆法）。摊平后一名两源，`DecodeRequest` 回 400 **点名两个来源**，
  `param` 指后来的那个（`tools[1].tools[0].name`）。
- `badname`：命名空间 `mcp__ade_asset_knowledge`（ADE 真名，含 `__`）加一个 46 字符的子工具名，
  摊平后 72 字符，超过 CC 与 Anthropic 共同的 64 上限；同壳里另放一个合规子项，钉「点名的是
  超限那一个」（`param` = `tools[0].tools[1].name`）。

形状照 ADE 实采裁剪，工具的描述与 schema 是编的。消费方：
`internal/protocol/openairesponses/namespace_test.go`（错误对象）、`internal/server/namespace_test.go`
（HTTP 面：400 信封 + 上游零请求）、`internal/protocol/canonical_coverage_test.go`（覆盖表）。

## responses-reasoning-text

[#107](https://github.com/SimonGino/portage/issues/107)：非流式 Responses 的 reasoning item 把推理**正文**
放在 `content[].type=="reasoning_text"`，不在 `summary[]`（CLIProxyAPI `17a65ee5`，MiniMax 一类上游）。
**手工构造**——十份 Responses 转录全是 `stream: true`，非流式线格本就没有真实样本。形状照 CLIProxyAPI
那条提交的单测，外加一段摘要与 `encrypted_content`，钉「摘要走 ThinkingSummary 在前、正文走
ThinkingBody、密文不出」。消费方：`internal/protocol/openairesponses/decode_response_test.go`。

## cc-stream-refusal / cc-refusal

[#113](https://github.com/SimonGino/portage/issues/113)：CC 解码此前完全不认 `refusal` 字段
（流式 `delta.refusal`、非流式 `message.refusal`），静默整段丢掉，客户端只收到空回复 + `end_turn`。
**手工构造**——CC 侧目前没有任何真实拒答转录。流式那份照 `golden/cc-stream-text` 的帧序写
（首帧只带 role，正文逐片下发，`finish_reason` 单独占一帧，usage 帧 `choices` 为空）；非流式
那份是单帧 `message.refusal` + `finish_reason:"stop"`。形状依据 litellm
`types/llms/openai.py` 的 `ChatCompletionResponseMessage` 与 OpenAI 官方 refusals 文档。
钉「拒答当正文放出、`finish_reason:"stop"` 改判 canonical `content_filter`」。消费方：
`internal/protocol/openaicc/decode_test.go`。

## responses-stream-done-only-text / responses-stream-done-only-tool

[#108](https://github.com/SimonGino/portage/issues/108)：有的第三方 Responses 上游不发 delta，正文只在
`output_text.done` 的 `text`、工具入参只在 `function_call_arguments.done` 的 `arguments` 上（sub2api
`1ed36679b` / `6271c517d` / `4f3b5110e`）。**手工构造**——手上没有这类上游的转录。text 那份照
`golden/responses-stream-text` 的帧序与键集写、删掉 delta；tool 那份是一路 `function_call`，
done 帧键集照 sub2api `apicompat.ResponsesStreamEvent`。钉「done 帧按账补后缀、纯 done 时整段放出」。
消费方：`internal/protocol/openairesponses/decode_response_test.go`。

## cc-cache-write / cc-stream-cache-write / cc-stream-cache-write-bailian / cc-cache-write-both-keys / responses-cache-write

[#111](https://github.com/SimonGino/portage/issues/111)：缓存写入量。OpenAI 官方报在 CC 的
`prompt_tokens_details.cache_write_tokens` / R 的 `input_tokens_details.cache_write_tokens`（new-api
`48068ce92`、sub2api `4a2b10c94`），阿里百炼报在 `prompt_tokens_details.cache_creation_input_tokens`
（litellm `645b87fae1`）。**手工构造**——手上没有官方直连与百炼的转录。四份 CC 从 `golden/cc-text` /
`golden/cc-stream-text` 派生、只改 usage 一处；R 那份键集照 `golden/responses-stream-text` 终帧（实采
`cache_write_tokens` 恒 0），外壳裁成一条 message 的非流式响应。`cc-stream-cache-write` 取 new-api
单测的数（prompt 3619 / cached 2921 / write 3616，cached + write > prompt），钉「原数照记、只钳 NetInput」；
`both-keys` 钉「两键取大」。消费方：`internal/protocol/cachehit_fixture_test.go`（Tap、canonical、跨出口）。

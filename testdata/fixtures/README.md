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

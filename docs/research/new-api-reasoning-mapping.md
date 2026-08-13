# new-api relaykit 的 reasoning/thinking 四方向映射

调查对象：`~/Code/GitHub/new-api`，HEAD `ccd535ef8e50cf6e5846a59278c40b7ff59d1b7d`（2026-08-10）。
一手来源：`relaykit/` 源码与其测试/golden。行号均对该 commit。
本文中 `dto` 一律指上游 canonical `relaykit/dto/`。

## 0. 直接答裁决票 #91 的两问

**「new-api 敢不敢造 signature？」——对 Anthropic 不敢，对 Gemini 敢。**

- 整个 `relaykit/` 里，Anthropic 方向唯一出现 `signature` 字样的地方是 `relaykit/dto/claude.go:27`（DTO 字段定义）和 `relaykit/relayconvert/internal/claude_messages/to_oai_chat_resp.go:77`（**读**上游的 `signature_delta`）。合成 thinking 块的那段代码里没有任何 `Signature` 赋值，见 `relaykit/relayconvert/internal/oai_chat/to_claude_messages_resp.go:203-219` 与 `:373-386`。对该文件（含 relaykit 抽模块前的旧路径）跑全历史 `git log --all -S'signature'` **零命中**——从来没写过，也不存在被回退的尝试。
- 反面对照：Gemini 方向**明目张胆造假**。硬编码常量 `ThoughtSignatureBypassValue = "context_engineering_is_the_way_to_go"`（`relaykit/relayconvert/internal/shared/gemini/request.go:42`），由 `AttachThoughtSignatureBypass` 塞进请求 part（`:56-61`），受渠道开关 `Options.Gemini.FunctionCallThoughtSignatureEnabled` 控制（`relaykit/relayconvert/convmeta/options.go:48-50`），并被 golden 钉死（`relaykit/relayconvert/testdata/golden/request/openai_to_gemini.golden.json:27`）。
- 结论：new-api 的口径不是「不敢造假签名」，而是「Gemini 的签名校验能被这个魔法字符串绕过，所以造；Anthropic 的不能，所以干脆不发」。

**「造了之后回带那一轮怎么活下来？」——靠回带那一轮把 thinking 块整个丢掉。**

new-api 合成的无 signature thinking 块**永远不会被发回任何上游**，因为请求侧四条路径全部不携带 thinking/reasoning 正文：

| 请求方向 | thinking / reasoning 正文 | 出处 |
| --- | --- | --- |
| Claude Messages → CC | 静默丢弃（`switch` 无 `thinking` / `redacted_thinking` 分支） | `internal/claude_messages/to_oai_chat_req.go:150-195` |
| CC → Claude Messages | 从不读 `message.reasoning_content`，从不生成 thinking 块 | `internal/oai_chat/to_claude_messages_req.go:339-393` |
| Responses → CC | reasoning item 落 `default` 分支，退化成一条空 `user` 消息 | `internal/oai_responses/to_oai_chat_req.go:173-201` |
| Responses → Claude Messages | reasoning item 落 `default` 分支，parts 为空 → 补一条 `"..."` 的 `user` 消息 | `internal/oai_responses/to_claude_messages_req.go:85-117`、`:184-227` |

Claude→CC 这条被 golden 钉死：fixture 里的 assistant 轮是 `{"type":"thinking","thinking":"Let me look.","signature":"sig"}` + tool_use（`relaykit/relayconvert/golden_test.go:135`），转换结果 `testdata/golden/request/claude_to_openai.golden.json` 里**只有 tool_calls，thinking 与 signature 均无踪迹**。

**没有任何针对「无 signature 的 thinking 块回发给 Anthropic 上游」的特判。** 全仓（不止 relaykit）搜不到相关分支。这个场景在 new-api 里被绕开而非被处理：CC 上游合成的块在下一轮进 `ClaudeMessagesRequestToOpenAIChat` 时被丢掉，而 Anthropic→Anthropic 是原样透传（`relay/channel/claude/` 无任何 thinking 清洗逻辑），此时块本来就是真上游发的、带真签名。**中途换渠道（同一会话从 Anthropic 渠道切到 CC 渠道再切回）这个漏洞 new-api 未覆盖，也未登记。**

## 1. 方向一：CC → Claude Messages（响应侧）

文件：`relaykit/relayconvert/internal/oai_chat/to_claude_messages_resp.go`

### 1.1 流式：做映射

`reasoning_content` → `content_block_start{type:"thinking"}` + `content_block_delta{type:"thinking_delta"}`。

- 首块（`SendResponseCount == 1`）分支：`:194-245`。取 `Delta.GetReasoningContent()` 与 `Delta.GetContentString()`，**reasoning 优先**（`if reasoning != "" ... else if content != ""`，`:198`/`:221`）——同一 chunk 里若两者都有，正文那半被丢。
- 后续块分支：`:365-408`。同样 `reasoning` 优先于 `textContent`（`:369`/`:387`）。开块只在类型切换时发一次（`:370-381`），delta 每块都发。
- `content_block_start` 的 `content_block` 是 `{type:"thinking", thinking:""}`（`:206-209`、`:376-379`）——**没有 `signature` 字段**。
- 流末也不补 `signature_delta`：关块只发 `content_block_stop`（`:13-18`、`:20-36`）。

### 1.2 块的开闭、index 分配、与正文交错

状态在 `convmeta.ClaudeConvertInfo`（`relaykit/relayconvert/convmeta/meta.go:54-70`）：`LastMessagesType`（none/text/thinking/tools）、`Index`、`ToolCallBaseIndex`、`ToolCallMaxIndexOffset`。

- `stopOpenBlocksAndAdvance()`（`:118-132`）是核心：类型一变就先关当前块，再 `Index++`（tools 时跳到 `ToolCallBaseIndex + ToolCallMaxIndexOffset + 1`），然后把 `LastMessagesType` 置 none。注释明写这是为了避免「同一 index 上先发 text 块后发 thinking_delta」触发 Anthropic 的 *Mismatched content block type*（`:113-117`）。
- 因此 thinking 与正文**不能交错回同一个 index**：thinking→text→thinking 会得到 index 0/1/2 三个独立块，每次切换一对 stop+start。
- 工具块是唯一可能同时开多个的（并行 tool_calls），关块时逐个 `content_block_stop`（`:27-32`）。
- 终局（`finish_reason` 或 usage-only 收尾块）统一 `appendStopOpenBlocks()` → `message_delta` → `message_stop`（`:247-269`、`:273-297`、`:416-436`），兜底路径 `FinalizeStreamResponseOpenAI2Claude`（`:442-468`）。
- 已知取舍：`finish_reason` 先到而 usage 未到时**推迟收尾**并直接 return（`:301-310`），等 usage-only chunk 再关块。

### 1.3 非流式：**不做映射**（#87 note 的「流式与非流式两条都做」有误）

`ResponseOpenAI2Claude`（`:470-510`）只处理 `text` 与 `tool_use`，**完全不读 `choice.Message.ReasoningContent`**。golden 钉死：fixture 的 CC 响应带 `"reasoning_content": "Deep thought."`（`golden_test.go:200`），产物 `testdata/golden/response/openai_to_claude.golden.json` 的 `content` 数组里只有 text 与 tool_use，无 thinking 块。

### 1.4 适用面比想象的宽

Gemini→Claude 与 Responses→Claude 都是**以 oai_chat 为中转的链式转换**（`relaykit/relayconvert/text_converter_registry.go:181-198`、`:219-232`），所以上面这套 thinking 合成对这两个上游同样生效。new-api 自评质量：`ConverterOpenAIChatToClaudeMessages` = `fair`（`:65-68`），`requestConverterGeminiToClaude` = `discouraged`（`:181-184`）。

### 1.5 测试形态

`internal/oai_chat/to_claude_messages_resp_test.go:106-205`，`TestStreamResponseOpenAI2ClaudeClosesTextThinkingAndToolBlocks`，一条流四次调用：

1. `SendResponseCount=1`，delta 只有 `content:"hello"` → 期望 3 条：`message_start` / `content_block_start`(index 0) / `content_block_delta`。
2. `SendResponseCount=2`，delta 只有 `reasoning_content:"thinking"` → 期望 3 条：`content_block_stop`(index **0**) / `content_block_start`(index **1**, `ContentBlock.Type == "thinking"`) / `content_block_delta`。
3. `SendResponseCount=3`，delta 是 tool_call → `content_block_stop`(1) / `content_block_start`(2, tool_use) / `content_block_delta`。
4. `SendResponseCount=4`，`finish_reason:"tool_calls"` + usage → `content_block_stop`(2) / `message_delta`(stop_reason=tool_use, 带 usage) / `message_stop`。

用例**只断言块类型与 index 递增，不断言 signature**——即 new-api 自己钉住的口径就是「thinking 块无签名亦可」。

## 2. 方向二：Claude Messages → CC（响应侧）

文件：`relaykit/relayconvert/internal/claude_messages/to_oai_chat_resp.go`

- 流式 `thinking_delta` → `choice.Delta.ReasoningContent`，原样透传（`:80-81`）。
- 流式 **`signature_delta` → `reasoning_content = "\n"`**（`:77-79`）。签名值被**整个扔掉**，只留一个换行当分段符。这是全仓唯一一处 signature 的读取点，且是有损的。
  - 出处可追到 `7160012fe feat: Add Claude 3.7 Sonnet thinking mode support`（2025-02-25，原在 `relay/channel/claude/relay-claude.go`），当时代码里带一行注释 **`// 加密的不处理`**——作者的原始意图就是「签名这类加密物不处理」，注释在后续搬迁中被删掉但逻辑原样保留至今。
- `content_block_start{type:"thinking"}` 在 `content_block_start` 分支里无对应处理（`:46-64` 只认 text 与 tool_use）——所以 CC 侧看不到「thinking 块开始」这个信号，只能靠 `reasoning_content` 首次出现推断。
- 非流式 `ResponseClaude2OpenAI`（`:105-165`）：遍历 content，`case "thinking"` 取 `message.Thinking` 存进 `thinkingContent`（`:136-139`），最后写 `choice.Message.ReasoningContent`（`:158-160`）。signature 同样丢弃。
  - 附带一处可疑写法：`:114-119` 另外从 `Content[0].Thinking` 取了一份 `responseThinking` 写进 `choice.ReasoningContent`（`:152-154`，注意是 choice 层不是 message 层），与循环里那份并存。多 thinking 块时只保留最后一个（`thinkingContent` 被覆盖而非累加）。
- 计费/日志侧：`FormatClaudeResponseInfo` 把 `Delta.Thinking` 和 `Delta.Text` **一起写进 `ResponseText`**（`:355-363`），即 thinking 正文被计入响应文本用于估算。
- **本方向零测试**：`signature_delta → "\n"` 与 thinking→reasoning_content 都没有任何单测或 golden 覆盖（stream golden 的 Claude fixture 只有纯 text 块，`golden_test.go:276-283`）。

## 3. 方向三：Responses ↔ CC（响应侧）

### 3.1 CC → Responses

- 非流式：`reasoning_content` → 追加一个 `output` item `{type:"reasoning", id:"<id>_reasoning_0", content:[{type:"summary_text", text:<reasoning>}]}`（`internal/oai_chat/to_oai_responses_resp.go:75-86`）。
- 流式：首个 reasoning delta 触发 `response.output_item.added`（item 为空 content 的 reasoning，status `in_progress`），随后每个 delta 发 `response.reasoning_summary_text.delta`（`internal/oai_chat/to_oai_responses_stream_resp.go:167-191`）；收尾发 `response.reasoning_summary_text.done` + `response.output_item.done`，把累积文本作为 `summary_text` part 带上（`:261-277`）。item id 恒为 `<id>_reasoning_0`（`:374-376`）。
- **形态偏差**：new-api 把 summary 文本写进 `ResponsesOutput.Content`，而真实 OpenAI Responses 的 reasoning item 用的是 `summary` 数组。`relaykit/dto/openai_response.go:327-339` 的 `ResponsesOutput` **根本没有 `Summary` 字段**。golden 钉死了这个形态：`testdata/golden/response/openai_to_openai_responses.golden.json:25-39`，且 reasoning item 被排在 message item **之后**（真实 OpenAI 是 reasoning 在前）。
- 全程无 `encrypted_content`。

### 3.2 Responses → CC

- 流式：`response.reasoning_text.delta` 与 `response.reasoning_summary_text.delta` **合并**成 `reasoning_content`（`internal/oai_responses/to_oai_chat_stream_resp.go:80-81`）。对应的 `.done` 事件不产出 chunk，只置 `needsReasoningSummaryBreak`（`:82-85`），下一段 reasoning 开头补 `\n\n`（不足则补齐，`:195-217`）——这是多段 summary 的分隔口径。
- 终局兜底：`response.completed` 里若还有未发过的 reasoning output item，把它的 `Content[].Text` 拼起来补发（`:178-188`）。
- 非流式 `ExtractReasoningTextFromResponses`（`internal/oai_responses/to_oai_chat_resp.go:212-228`）**只读 `out.Content`，不读 `summary`**。fixture 用的是真实形态 `{"type":"reasoning","summary":[{"type":"summary_text","text":"Deep thought."}]}`（`golden_test.go:250`），产物 `testdata/golden/response/openai_responses_to_openai.golden.json` 里**没有 reasoning_content**——即真实 OpenAI 上游的非流式 reasoning summary 在这条路上被静默丢弃。CC→R→CC 往返之所以看起来没丢，只是因为 CC→R 也写进了 `content`。
- **`encrypted_content` 在整个 new-api 仓库里零出现**（`grep -rn 'encrypted_content\|EncryptedContent'` 无命中）。既不生成、不透传、不消费。
- usage 侧 `completion_tokens_details.reasoning_tokens` 两向都保留（`to_oai_chat_resp.go:164-168`、`to_oai_responses_resp.go:145-150`）。
- 测试：`internal/oai_responses/to_oai_chat_resp_test.go:211-247` `TestResponsesStreamEventToChatChunksCustomToolAndReasoning`，喂一个 `response.reasoning_text.delta{delta:"thinking"}`，断言 `chunks[1].Choices[0].Delta.GetReasoningContent() == "thinking"`。只覆盖 delta→reasoning_content 这一跳，不覆盖 `\n\n` 分隔与非流式 summary。

## 4. 方向四：请求侧回带（细节）

见 §0 表格与结论。补充：

- **Claude → CC**（`internal/claude_messages/to_oai_chat_req.go:150-195`）：content 分支只认 `text`/`input_text`、`image`、`tool_use`、`tool_result`。`thinking` 与 `redacted_thinking` 无分支 → 落空，既不转 `reasoning_content` 也不降级成文本。若某轮 assistant 只有 thinking 块，`:204` 的空消息保护会让整条消息被跳过。
- **CC → Claude**（`internal/oai_chat/to_claude_messages_req.go`）：请求侧只映射**思考参数**，不映射思考内容——`reasoning_effort` low/medium/high → `thinking{enabled, budget_tokens 1280/2048/4096}`（`:180-198`）；OpenRouter 风格 `reasoning.max_tokens` → `budget_tokens`（`:200-213`）；模型名 `-max/-xhigh/-high/...` 后缀（opus-4-6/4-7/4-8）→ `thinking{adaptive}` + `output_config.effort`（`:133-151`）；`-thinking` 后缀 + `ThinkingAdapterEnabled` → 按比例算 budget，并把 `max_tokens` 抬到 ≥1280（`:152-178`）。**正文侧完全无 thinking。**
- **Responses → Claude**（`internal/oai_responses/to_claude_messages_req.go`）：`applyResponsesReasoningToClaude`（`:163-182`）同样只映射 effort → budget_tokens。input item 的 `switch`（`:87-116`）只认 function_call / custom_tool_call / function_call_output / custom_tool_output，`reasoning` item 落 `default`：`responsesClaudeRole` 无 `role` 字段 → 返回 `"user"`（`:303-312`），`responsesInputContentToClaudeMediaMessages` 对 `summary`/`encrypted_content` 无分支 → parts 为空 → 被补成 `{type:"text", text:"..."}`（`:104-111`）。**结果是每个 reasoning item 变成一条内容为 `"..."` 的 user 消息注入对话**，比丢弃更糟。
- **Responses → CC**（`internal/oai_responses/to_oai_chat_req.go:173-201`）：reasoning item 同样落 `default`，`role` 空 → `"user"`，`content` 为 nil → `""`，追加一条空 user 消息。
- Responses→CC 请求侧对有状态字段直接报错拒绝（`conversation` / `previous_response_id` / `prompt` / `context_management`，`:110-128`），但 reasoning item 不在此列。

## 5. relaykit 抽模块（#6369）前后有无实质变化

**无。** `86ac0f774 refactor: extract protocol conversion layer into standalone relaykit module (#6369)`（2026-07-27）是纯机械搬迁：`service/relayconvert/` → `relaykit/relayconvert/`，`common.GetPointer` → `kitutil.GetPointer`，`*relaycommon.RelayInfo` → `convmeta.Meta` 接口，`info.ClaudeConvertInfo.X` → `state.X`。逐行 diff 未见任何 reasoning/thinking 语义改动。thinking_delta 合成逻辑本身可追到更早：`7160012fe feat: Add Claude 3.7 Sonnet thinking mode support`，此后经 `9e4506eba`、`5171070f7`（修 block index/type 迁移）、`c36418c86`（#5825 高级路由）演进，**全程从未出现 signature 相关改动**。

## 6. 对口径层复议（#87 / #91）的可用要点

1. new-api 已经在生产里做「`reasoning_content` → `thinking` 块（无 signature）」的伪映射，只做流式，只在 oai_chat→claude 这一跳，链式覆盖 Gemini/Responses 上游。口径层 v0.10「跨协议丢弃 + 不做伪映射」的立论之一（signature 校验风险）在 new-api 的实现里是靠**请求侧全线丢弃 thinking 正文**规避的，不是靠不合成。
2. 「合成但不回带」是一个自洽的最小组合：出向合成让 Anthropic 客户端有东西可渲染，入向丢弃让无签名块永远碰不到 Anthropic 校验。代价是多轮里模型看不到自己上一轮的思考——但 CC 侧本来也不回传 `reasoning_content`，所以没有额外损失。
3. new-api 未处理的缺口，portage 若照抄需自行决定：非流式 CC→Claude 完全没有 thinking（体验不一致）；同会话跨渠道切换时旧的合成块会流向 Anthropic；`signature_delta` 被降级成 `"\n"` 导致 Claude→CC→Claude 往返无法保真；Responses 请求侧 reasoning item 会退化成 `"..."` 垃圾消息。
4. 造签名这件事 new-api 是**按上游是否可绕过来分别决策**的（Gemini 造、Anthropic 不造），且对 Gemini 那份还挂了渠道开关。这个「按上游能力分档 + 开关兜底」的形态可以直接借。

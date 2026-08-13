# litellm 与 sub2api 的 reasoning 那一格横比

> Research 票 #90（wayfinder 地图 #87）。调查对象：`~/Code/GitHub/litellm`（HEAD `f318ef0`，2026-05-04）、`~/Code/GitHub/sub2api`（HEAD `5a61430`，2026-07-29）。new-api 一行为一眼粗结论，详见 #88。
> 只查 reasoning/thinking 一格，不做全仓盘点。所有结论带 `文件:行号`。

## 0. 结论先行

1. **出向合成（上游 reasoning_content → 给 Anthropic 客户端造 thinking 块）：三家都做，且都不带 signature。** 这一格上「不做伪映射」没有同行支持——litellm、sub2api、new-api 三家一致。
2. **回带处置（客户端把 thinking 发回来 → 送给上游）：三家都**不**把无 signature 的 thinking 当 thinking 回带。** sub2api 直接丢（`anthropic_to_responses.go:292`，有测试锁）或降级成 `<thinking>` 文本（`chatcompletions_to_responses.go:169`）；litellm 在 bedrock 路径显式把无 signature 的块降级成 text 块（`factory.py:4931-4934`），并对 Anthropic 上游备了一层「400 invalid signature → 剥掉 thinking 重试」（`common_utils.py:772-819`）。
3. **所以 v0.10 的立论只有半条站得住**：「伪造 thinking 有 signature 校验与续轮回发风险」在**回带方向**是真的，三家都为此写了专门代码；但在**出向方向**它不成立——出向合成不产生任何回发风险，风险发生在下一轮客户端把它发回来的时候，而三家都在**回带那一侧**拦，不在出向那一侧拦。v0.10 用一个回带侧的风险论证了出向侧的丢弃，这一步在三家实现里都找不到对应。

唯一的例外方向是 **Anthropic 作为上游**：portage 的 A→CC 场景里合成的 thinking 最终会被 Claude Code 原样发回，若下一跳是 Anthropic 官方，无 signature 的 thinking 块会被 400。这一格 v0.10 的顾虑是真实的，但同行的解法是**在入向剥/降级**，不是**在出向不合成**。

---

## 1. litellm

### 1.1 出向合成：做，且显式为「无 thinking_blocks 只有 reasoning_content」的上游兜底

非流式，OpenAI 响应 → Anthropic 响应（`LiteLLMAnthropicMessagesAdapter._translate_openai_content_to_anthropic`）：

- 有 `thinking_blocks` 时逐块转 `AnthropicResponseContentBlockThinking`，signature 原样带过去 —— `litellm/llms/anthropic/experimental_pass_through/adapters/transformation.py:1229-1252`
- **没有 `thinking_blocks`、只有 `reasoning_content` 时，合成一个 `thinking` 块，`signature=None`** —— 同文件 `:1261-1272`

```python
# Handle reasoning_content when thinking_blocks is not present
elif (hasattr(choice.message, "reasoning_content") and choice.message.reasoning_content):
    new_content.append(
        AnthropicResponseContentBlockThinking(
            type="thinking",
            thinking=str(choice.message.reasoning_content),
            signature=None,
        ).model_dump()
    )
```

流式同理，`_translate_streaming_openai_chunk_to_anthropic` 把 `delta.reasoning_content` 累加后发 `thinking_delta` —— `transformation.py:1493-1513`。注释直接写了动机：

> `# Handle reasoning_content when thinking_blocks is not present`
> `# This handles providers like OpenRouter that return reasoning_content` （`:1493-1494`）

这与 portage 遇到的 glm-5.2 场景是同一类：上游只发 `reasoning_content`，litellm 选择合成而不是丢。

**顺带发现一处不一致（可当 portage 实现时的坑清单条目）**：流式的 `content_block_start` 类型判定 `_translate_streaming_openai_chunk_to_anthropic_content_block` **只认 `thinking_blocks`**，不认 `reasoning_content`（`transformation.py:1428-1448`）；而 `Delta` 在 `reasoning_content` 为空时会 `del self.thinking_blocks`（`litellm/types/utils.py:1312-1316`），因此纯 `reasoning_content` 的流会走到 `:1450` 的 `return "text", TextBlock(...)` 兜底——**开的是 text 块，发的是 thinking_delta**。合成与块类型两处判定不同源。

另有一条硬约束写在断言里：同一个流式 chunk 里 `thinking` 与 `signature` 不能同时出现，否则直接 `raise ValueError`（`transformation.py:1441-1444`、`:1501-1504`），理由是 Anthropic 线格式里两者是分开的 `thinking_delta` / `signature_delta`。

### 1.2 回带处置：Anthropic 上游路径无前置校验，靠事后重试兜

OpenAI 格式的 assistant 消息（含 `thinking_blocks`）转成 Anthropic 请求时，`anthropic_messages_pt` **无条件把 thinking_blocks 摊进 assistant content，不看 signature** —— `litellm/litellm_core_utils/prompt_templates/factory.py:2811-2814`：

```python
if (thinking_blocks is not None and not _list_has_thinking):  # IMPORTANT: ADD THIS FIRST, ELSE ANTHROPIC WILL RAISE AN ERROR
    assistant_content.extend(thinking_blocks)
```

顺序有讲究，注释给了理由（Anthropic 按位置校验 signature）：

> `# We must preserve this interleaved order because Anthropic verifies thinking block signatures based on position.` （`factory.py:2650-2652`）

前置防御只有一条，且不是针对 signature 的，而是针对「开了 thinking 但历史里没有 thinking 块」：

> `# Drop thinking param if thinking is enabled but thinking_blocks are missing`
> `# This prevents the error: "Expected thinking or redacted_thinking, but found tool_use"`
> `# IMPORTANT: Only drop thinking if NO assistant messages have thinking_blocks. If any message has thinking_blocks, we must keep thinking enabled, otherwise Anthropic errors with: "When thinking is disabled, an assistant message cannot contain thinking"`
> —— `litellm/llms/anthropic/chat/transformation.py:1544-1561`

真正针对 signature 的防御是**事后的**：`/v1/messages` 路径上，上游回 400 且报文匹配 "invalid … signature … thinking … block" 时，剥掉全部 thinking / redacted_thinking 块再打一次（上限 2 次）：

- 检测：`litellm/llms/anthropic/common_utils.py:772-788`（docstring 写了触发场景：`用户轮换 API key 或切换 model endpoint`，即 signature 与当前 deployment 不匹配）
- 剥离：`common_utils.py:791-819`（顺带处理「剥空后 content 为空数组会被 Anthropic 拒」）
- 挂载：`litellm/llms/base_llm/anthropic_messages/transformation.py:133-165`，重试上限 `:124-131`

**这条路径的语义正好是 portage 关心的那一格**：litellm 承认无效 signature 会炸，但处理方式是「先发，炸了再剥」，而不是「合成时就不做」。

### 1.3 signature：留则透传，无则留空/None，只有 bedrock 路径做降级

- Anthropic 请求入站 → OpenAI 消息：`signature=content.get("signature") or ""`，即无 signature 时填空串而非丢块 —— `adapters/transformation.py:618-625`
- **bedrock 是唯一显式对「无 signature 的 thinking」做防御的路径**，直接降级成普通 text 块，函数 docstring 把理由写死了 —— `factory.py:4910-4939`：

  > `If contains 'signature', it is a thinking block.`
  > `If missing 'signature', it is a text block - e.g. when using a non-anthropic model.`
  > `Handle error raised by bedrock if thinking blocks are provided for a non-thinking model (e.g. nova with tool use)`
  > `Relevant Issue: https://github.com/BerriAI/litellm/issues/9063`

  实现见 `factory.py:4931-4934`：`if reasoning_text and not reasoning_text.get("signature"): assistant_parts.append(BedrockContentBlock(text=...))`

- **反例：Gemini 路径会主动伪造 signature。** 历史从 gemini-2.5 搬到 gemini-3 时，缺失的 `thought_signature` 用一个官方推荐的 dummy 值填上 —— `factory.py:1316-1322` + `:1326-1336`：

  > `# Below dummy signature is recommended by google - https://ai.google.dev/gemini-api/docs/thought-signatures#faqs`
  > `dummy_data = b"skip_thought_signature_validator"`

  即：当上游给了「跳过校验」的正式口子时，litellm 就伪造；Anthropic 没给这种口子，所以它选择事后剥离。

---

## 2. sub2api（`backend/internal/pkg/apicompat/`）

三条路各自的 reasoning 走向：

### 2.1 Responses ↔ Chat Completions

- **R → CC（响应）**：`reasoning` output item 的 `summary_text` 拼成 `msg.ReasoningContent`，`encrypted_content` **不带**（合成侧无 signature 概念）—— `responses_to_chatcompletions.go:53-58`、`:72-74`
- **R → CC（流式）**：`response.reasoning_summary_text.delta` 与 `response.reasoning_text.delta` 都映到 `ChatDelta.ReasoningContent` —— `responses_to_chatcompletions.go:155-159`、`:285-290`。中文注释：「原始推理文本增量（真实 Codex 客户端消费的 reasoning_text.delta），与 reasoning summary 一样映射为 reasoning_content」（`:156-157`）
- **CC → R（请求回带）**：assistant 的 `reasoning_content` **降级成 `<thinking>…</thinking>` 文本**塞进 `output_text`，不产 reasoning item —— `chatcompletions_to_responses.go:168-170`；结构化的 `thinking`/`reasoning` content part 同样包 `<thinking>` 标签 —— `:242-269`，函数 docstring 给了理由：

  > `For structured thinking/reasoning parts, it preserves semantics by wrapping the text in explicit tags so downstream can still distinguish it from normal text.` （`:218-219`）

### 2.2 Responses ↔ Anthropic

**出向（R → A）合成，且把 `encrypted_content` 当 signature 用**——这是三家里唯一一条 signature 有真值的合成路：

- 非流式：`reasoning` item → `thinking` 块，`Signature = item.EncryptedContent` —— `responses_to_anthropic.go:29-45`。注释：

  > `// Always surface encrypted_content as thinking.signature so Claude Code / multi-turn clients can send it back. Signature-only thinking blocks are valid when the model omits a visible summary.` （`:36-38`）

- 流式：`response.output_item.added` 开 thinking 块并把 `encrypted_content` 存进 `PendingThinkingSignature`（`:365-380`），关块前补一个 `signature_delta`（`:686-699`）。刻意不在 `reasoning_summary_text.done` 关块，注释写了原因：

  > `// Keep the thinking block open until response.output_item.done. Grok/Codex attach encrypted_content on the finished reasoning item; closing early would drop signature_delta and break multi-turn cache.` （`:239-241`）

  测试锁：`anthropic_responses_test.go:423-466`（`TestResponsesToAnthropic_StreamEmitsThinkingSignature`）、`:860-878`

**回带（A → R）按 signature 分流，无 signature 的 thinking 直接丢**——最强的一条数据点：

`anthropic_to_responses.go:257-299`，函数 docstring：

> `// thinking blocks with signature → reasoning items (encrypted_content) so multi-turn Grok/Codex prompt cache can reuse prior reasoning prefixes.`
> `// thinking without signature remains ignored (not accepted as plain text input).`

实现 `:289-294`：

```go
sig := strings.TrimSpace(b.Signature)
// Only replay provider ciphertext. Skip GPT/Codex-style gAAAA blobs and
// empty placeholders — xAI returns 400 on decrypt for foreign signatures.
if sig == "" || strings.HasPrefix(sig, "gAAAA") {
    continue
}
```

注意它连**跨厂商的真 signature** 也拦（`gAAAA` 前缀 = OpenAI/Codex 的密文），理由是 xAI 解密失败会 400。测试锁：`anthropic_responses_test.go:149-174`（`TestAnthropicToResponses_ThinkingWithoutSignatureIgnored`）、`:176-…`（有 signature 时变 reasoning item）。

### 2.3 Chat Completions ↔ Anthropic bridge

- **出向（CC → A）合成，无 signature**——与 new-api 同构：
  - 非流式：`message.ReasoningContent != ""` → `AnthropicContentBlock{Type:"thinking", Thinking: …}`，`Signature` 字段留零值（`json:"signature,omitempty"`，即线上根本不出现该键）—— `chatcompletions_anthropic_bridge.go:424-432`、`types.go:64-68`
  - 还有一条 DeepSeek 兜底：无正文无工具调用时，把 reasoning 再当可见文本发一遍，免得这一轮是空的 —— `:435-438`，注释：`// DeepSeek reasoning-only fallback: when there is no text and no tool calls, surface the reasoning content as visible text so the turn isn't empty.`
  - 流式：`delta.reasoning_content` → `ensureCCAnthropicThinkingBlock` 开 `content_block_start{type:thinking, thinking:""}` + `thinking_delta` —— `:610-618`、`:715-730`；**全程不发 `signature_delta`**（该文件无任何 signature 写入）
- **回带（A → CC）：thinking 块整块丢弃**，docstring 写了理由 —— `:250-253`：

  > `// thinking blocks are dropped (Chat Completions has no inbound thinking field, matching anthropicAssistantToResponses).`

### 2.4 顺带：signature_delta 在 A → R 方向被直接吞掉

`anthropic_to_responses_response.go:393-394`：`// Anthropic signature deltas have no Responses equivalent; skip`。

---

## 3. new-api（一眼粗结论，详见 #88）

`~/Code/GitHub/new-api/relaykit/relayconvert/internal/oai_chat/to_claude_messages_resp.go`：流式 `:195-220`、非流式分支 `:366-386`，`Delta.GetReasoningContent()` 非空即发 `content_block_start{type:"thinking", thinking:""}` + `thinking_delta`；全文件无 `signature` / `Signature` 字样，也没有 `signature_delta`。**出向合成做、signature 完全不管**。回带方向不在本票范围，见 #88。

---

## 4. 横比表

| | 出向合成（上游 reasoning → 给 Anthropic 客户端造 thinking） | 回带处置（客户端发回的 thinking → 给上游） | signature |
|---|---|---|---|
| **litellm** | **做。** 有 `thinking_blocks` 走原块；只有 `reasoning_content` 时合成 `thinking` 块，`signature=None`（`adapters/transformation.py:1261-1272`；流式 `:1493-1513`，注释点名 OpenRouter 这类只发 `reasoning_content` 的上游）。流式块类型判定不认 `reasoning_content`，会开成 text 块（`:1428-1450`，与合成两处不同源） | **→ Anthropic：无前置校验，全量摊进 assistant content**（`factory.py:2811-2814`），保序因为「Anthropic 按位置验签」（`:2650-2652`）；**事后**兜底——400 命中 invalid-signature 就剥掉全部 thinking 重发一次（`anthropic/common_utils.py:772-819` + `base_llm/anthropic_messages/transformation.py:133-165`）。**→ bedrock：无 signature 的块前置降级成 text 块**（`factory.py:4910-4939`，理由挂 issue #9063） | 有则原样透传（`:1234-1252`）；合成时为 `None`；入站填 `""`（`:618-625`）。同一 chunk 里 thinking+signature 直接 `raise`（`:1441-1444`、`:1501-1504`）。**Gemini 路径反而主动伪造 signature**，用 Google 官方推荐的 `skip_thought_signature_validator` dummy（`factory.py:1316-1336`） |
| **sub2api** | **三条路都做。** R→CC：reasoning summary → `reasoning_content`（`responses_to_chatcompletions.go:53-58`、`:285-290`）。R→A：reasoning item → thinking 块（`responses_to_anthropic.go:29-45`；流式 `:365-380`）。CC→A：`reasoning_content` → thinking 块 / `thinking_delta`（`chatcompletions_anthropic_bridge.go:424-432`、`:610-618`） | **按 signature 分流，无 signature 一律不当 thinking 回带。** A→R：有 signature 才转 reasoning item，无 signature **直接丢**，连纯文本都不给（`anthropic_to_responses.go:289-294`，理由「xAI returns 400 on decrypt for foreign signatures」，测试 `anthropic_responses_test.go:149-174`）。A→CC：thinking 块整块丢（`chatcompletions_anthropic_bridge.go:250-253`，理由「CC 没有入站 thinking 字段」）。CC→R：`reasoning_content` 降级成 `<thinking>…</thinking>` 文本（`chatcompletions_to_responses.go:168-170`、`:242-269`） | **只有 R→A 那条有真 signature**：`encrypted_content` 直接当 `thinking.signature` 发，流式补 `signature_delta`（`responses_to_anthropic.go:36-45`、`:686-699`），动机是 Grok/Codex 多轮 prompt cache。**CC→A 完全无 signature**（`types.go:68` omitempty，线上不出现该键）。A→R 方向 `signature_delta` 直接吞（`anthropic_to_responses_response.go:393-394`） |
| **new-api**（粗，详见 #88） | **做。** `reasoning_content` 非空即 `content_block_start{type:thinking}` + `thinking_delta`，流式与非流式两条都做（`relaykit/relayconvert/internal/oai_chat/to_claude_messages_resp.go:195-220`、`:366-386`） | 未查，见 #88 | **完全不管。** 该文件无 `signature` 字样，无 `signature_delta`（详见 #88） |

---

## 5. 对口径层 v0.10 的影响

v0.10 原文（`docs/口径层设计.md:132`、版本记录 `:213`）：

> thinking / reasoning 内容块：同协议透传保留；**跨协议丢弃 + 日志警告，不做伪映射**（伪造 thinking 块有 signature 校验与续轮回发风险）。

按上表逐项对照：

- **「不做伪映射」在出向没有同行支持。** 三家一致做出向合成，其中两家（litellm、sub2api CC→A）合成时明确不带 signature，且没有任何一家为此写过「因为有 signature 风险所以不合成」的注释。litellm 甚至专门为「只发 reasoning_content 的上游」补了兜底分支——那正是 portage 遇到 glm-5.2 的场景。
- **「signature 校验与续轮回发风险」是真的，但落点在回带侧。** 三家都在回带侧写了专门代码：sub2api 前置丢弃（并有测试锁 + 明确的 400 原因），litellm 前置降级（bedrock）+ 事后剥离重试（Anthropic）。也就是说这条风险在业界是**用回带侧的策略**解决的，不是用出向侧的丢弃解决的。
- **v0.10 的立论目前只剩一格站得住**：portage 的 A→CC 场景中，合成出的无 signature thinking 会被 Claude Code 原样回发，若下一跳是 Anthropic 官方上游则必然 400。但同行给的解法是三选一——(a) 回带时剥掉无 signature 的 thinking（sub2api A→R 式）、(b) 回带时降级成 text（litellm bedrock 式）、(c) 先发、400 了再剥重试（litellm Anthropic 式）——**没有一家选「出向就不合成」**。
- **一条可选的加分项**：sub2api 的 R→A 证明当上游有可搬运的密文（`encrypted_content`）时，signature 那一格是能填真值的，portage 的 R→A 路径日后可以照办；但 CC 侧（`reasoning_content`）天然没有对应密文，那条路只能无 signature。

因此裁决票 #91 要回答的实际上是两个可拆的问题，而不是一个：**出向合不合成**（业界共识：合成）与**回带怎么处置**（业界共识：无 signature 不当 thinking 送上游，剥或降级）。v0.10 把两者绑成一条，是它现在讲不通的地方。

---

## 附：本票未覆盖

- new-api 的回带方向与请求侧参数映射 —— #88
- 请求侧 `reasoning_effort` / `thinking.budget_tokens` 的映射（litellm `adapters/transformation.py:669-768`、sub2api `anthropic_to_responses.go:58-66` 都有实现）—— 地图 #87 列为 Not yet specified，本票不展开
- usage 的 `reasoning_tokens` 口径（litellm 用 `token_counter` 反推，`anthropic/chat/transformation.py:1909-1912`）—— 同上

# Research：CLIProxyAPI 的 reasoning/thinking 处理与入册定位（#89）

来源：本地 `~/Code/GitHub/CLIProxyAPI`（router-for-me/CLIProxyAPI，**MIT**），HEAD `d757063c` / 2026-08-13，只读求证。下文所有行号均指该工作副本；`docs/` 只有 SDK 使用文档，没有任何协议/reasoning 说明，一手事实全部来自源码与 `AGENTS.md`、`config.example.yaml`。

**结论先行**：**首要参考，但限定主题——「thinking/reasoning 跨协议保真与 signature 处置」。** 架构（点对点 N×M 原始 JSON 互转，无 canonical 事件模型）与 portage 相反，不可抄；但它是目前四个参考仓库里唯一把「signature 的来源判定 → 可否回放 → 回放不了怎么收场」做成独立子系统（`internal/signature/`，7 个 provider 家族、逐条写明理由）的实现，且这些理由是拿真实 400 撞出来的。portage 的 #87 复议直接需要它的第 2、3 节。

---

## 0. 对 #87 口径复议的直接结论

口径层 v0.10 把「出向合成」和「回带回放」捆成一件事否掉了（立论：伪造 thinking 块有 signature 校验与续轮回发风险）。**CLIProxyAPI 把这两件事完全解耦**，并且两边给出的答案相反：

| | CLIProxyAPI 的做法 |
|---|---|
| **出向合成**（上游 `reasoning_content` → 客户端 thinking 块） | **做，且不带 signature**。`internal/translator/openai/claude/openai_claude_response.go:181` 发的就是 `{"type":"thinking","thinking":""}`，全文件无 signature 字样——与 new-api `ccd535ef8` 的做法一致 |
| **回带回放**（客户端把 thinking 块送回来） | **默认整块丢弃**，除非 signature 能被判定为目标 provider 自己签的。`internal/translator/openai/claude/openai_claude_request.go:376-388`：signature 为空或非 GPT 来源 → `return false`，文本连带丢掉 |

也就是说：**「出向合成」的风险不在出向，在回带；把回带侧的 provenance 检查做出来，出向就可以放心合成。** v0.10 的风险陈述是对的，但结论（因此不合成）是过强的——风险有一个更便宜的收场点。

另外两条可直接采信的 Anthropic 行为事实，都在 `internal/translator/claude/openai/responses/claude_openai-responses_request.go:548-556` 的同一段注释里：

- **`Anthropic requires a signature on every thinking block and rejects an absent or empty one`**（`:550-551`）——所以合成的 unsigned thinking 块**绝不能**原样回带到真 Anthropic 上游，这正是 v0.10 担心的那个风险，也正是 §3 那套摘除逻辑存在的原因。
- **`Anthropic does not verify the text against the signature, which is what makes the summarized text safe to restore alongside it`**（`:555-556`）——Anthropic **不校验 thinking 文本与 signature 的对应关系**，所以 Responses 的 summary 文本（模型摘要，不是原文）配上原始 signature 一起回放是安全的。这解掉了 R→A 方向「摘要文本配不上原 signature」的顾虑。

---

## 1. 协议转换层：在哪、怎么组织、与 portage canonical 的可比性

### 1.1 位置与形状

- 注册表在 `sdk/translator/`，薄封装在 `internal/translator/translator/translator.go:24-26`：
  ```go
  func Register(from, to string, request interfaces.TranslateRequestFunc, response interfaces.TranslateResponse)
  ```
- 七种 format 是字符串枚举，不是类型：`sdk/translator/formats.go:5-11` —— `openai`、`openai-response`、`claude`、`gemini`、`codex`、`antigravity`、`interactions`。`Format` 就是 `type Format string`（`sdk/translator/format.go:3-4`）。
- 转换单元的签名是 **`[]byte` 进、`[]byte` 出**，中间态是 gjson/sjson 直接在原始 JSON 上打补丁：`translator.go:39-41`（请求）、`:69-71`（流式响应，`[][]byte` 是一批 SSE 行）、`:87-89`（非流式）。流式转换的跨 chunk 状态挂在 `param *any` 里，由各 pair 自己定义结构体。

### 1.2 组织方式：点对点，不是枢纽式

目录布局是 `internal/translator/<上游 format>/<客户端 format>/`，每个叶子一个 `init.go` 调 `translator.Register`。例如 `internal/translator/claude/openai/chat-completions/init.go:9-19` 注册的是 `Register(OpenAI, Claude, ConvertOpenAIRequestToClaude, {Stream: ConvertClaudeResponseToOpenAI, ...})`——即「客户端说 CC、上游说 Anthropic」这一格。

- **共 30 个注册对**（`grep -rn 'translator.Register(' --include=init.go internal/translator | wc -l`）。
- 没有任何 canonical 中间模型。`internal/translator/common/` 只有 9 个纯工具文件（`cache_control.go`、`claude_messages.go`、`bytes.go` 等），是共享 helper，不是共享事件模型。
- 每加一个 format 要写 2N 个转换函数，且每一格的 thinking 处理都是独立实现——本文第 2 节能看到同一件事在四个文件里各写一遍，细节还不一样。

### 1.3 与 portage 的可比性

**架构层面不可比，也不该抄。** portage 的 canonical 事件模型（`docs/MVP设计草案.md` 的 `EvThinkingDelta` 一族）正是为了避免这个 N×M 爆炸。CLIProxyAPI 走到 30 个 pair 之后的代价在代码里看得见：

- 同一个「reasoning_content → thinking 块」逻辑在 `openai/claude/openai_claude_response.go:169-194`（流式）、`:420-427`（非流式 choices）、`:727-734`（非流式 message）三处各写一遍。
- `reasoning_tokens` 在 `claude/openai/responses/claude_openai-responses_response.go:695-706` 是**按 `len(text)/4` 估算**出来的（因为 Anthropic 不给这一格），而在 `gemini/openai/chat-completions/gemini_openai_response.go:118-121` 是从 `thoughtsTokenCount` 直接搬的。同一字段两套口径，就是没有 canonical 层的直接后果。

**可比的是它的"决策表"层。** `internal/signature/provider_compatibility.go` 是唯一一个横切所有 pair 的策略中心——portage 若要在 canonical 层加 thinking，这张表的**内容**（谁能回放谁的 signature、回放不了怎么收场）可以整体搬过来，搬的是知识不是代码。

---

## 2. 出向：各方向 thinking/reasoning 的处置与 signature

按 portage 的四条路径口径整理（"A→CC" = 客户端 Anthropic、上游 Chat Completions）。

### 2.1 A→CC（客户端 Anthropic ← 上游 Chat Completions）——**合成，不带 signature**

`internal/translator/openai/claude/openai_claude_response.go`

- 流式：`:170` 见到 `delta.reasoning_content` 即 `:181` 发 `content_block_start{"type":"thinking","thinking":""}`、`:188-192` 发 `thinking_delta`。**block 里没有 signature 字段，全流程也不发 `signature_delta`**。
- 先收文本块再开思考块：`:175 stopTextContentBlock` / `:200 stopThinkingContentBlock`，thinking 与 text 的 block index 独立分配（`:176-180`），保证 Anthropic 客户端拿到的 index 序列合法。
- 非流式：`:420-427`（`choices.0.message.reasoning_content`）、`:727-734`（`message.reasoning_content`），都是 `{"type":"thinking","thinking":...}` 无 signature。
- **`collectOpenAIReasoningTexts`（`:508-538`）容忍四种上游形状**：裸字符串、字符串数组、`{text: ...}` 对象、对象数组——这是给各家"OpenAI 兼容"上游擦屁股的地方，portage 解码侧值得照抄这个宽容度。
- `:704-707` 还额外吃一种畸形：CC 响应体里塞了 Responses 形状的 `output[].type == "reasoning"` 项，也按 thinking 收。

> 这一格就是 #87 里那个「glm-5.2 只发 `reasoning_content`、Anthropic 客户端空白几秒」的场景。CLIProxyAPI 与 new-api 的处理完全一致，且已跑了几百个 commit。

### 2.2 CC→A（客户端 Chat Completions ← 上游 Anthropic）——**降级为纯文本，signature 直接丢**

`internal/translator/claude/openai/chat-completions/claude_openai_response.go`

- 流式：`:183-186` `thinking_delta` → `choices.0.delta.reasoning_content`。
- 非流式：`:371-374` 累积、`:427-431` 落 `choices.0.message.reasoning`（注意流式用 `reasoning_content`、非流式用 `reasoning`，字段名不一致）。
- **signature 无处安放，直接不搬**。CC 协议没有这一格，这是唯一诚实的处理。

### 2.3 R→A（客户端 Responses ← 上游 Anthropic）——**signature 塞进 `encrypted_content`，还给 redacted 造了信封**

`internal/translator/claude/openai/responses/claude_openai-responses_response.go`，本仓最讲究的一格：

- Anthropic `thinking` 块 → Responses `reasoning` item：文本走 `reasoning_summary_text.delta`（`:437-446`），**signature 走 `encrypted_content`**（`:626-634` 流式收尾、`:960-968` 非流式）。
- `redacted_thinking` 没有对应 item 类型，于是造了个前缀信封，`:74-81` 的注释是整份调研里最值得抄的一段：
  ```go
  // ClaudeResponsesRedactedThinkingPrefix marks a Responses reasoning item whose
  // encrypted_content carries an Anthropic redacted_thinking payload instead of a
  // thinking signature. ... The marker is not a valid signature for any provider,
  // so a foreign upstream drops the block instead of replaying an unusable value.
  const ClaudeResponsesRedactedThinkingPrefix = "claude-redacted-thinking:"
  ```
  **设计要点：故意选一个对任何 provider 都非法的前缀**，这样即使这段历史被路由到别的上游，那边的 provenance 检查也会自然丢弃它，而不是把一个不可用的值原样回放上去。
- `claudeReasoningCarrier`（`:88-99`）统一出口；`:85-87` 的注释记录了一个坑：流式 thinking 块先报空 signature、后经 `signature_delta` 补齐（`:449-452` 落 `st.ReasoningSignature`），所以开块时拿到空值是正常的。
- `reasoning_tokens` 在 `:695-706` 按 `len(text)/4` 估算——Anthropic 侧没有这一格，它选择了造一个近似值而不是省略。**portage 若在 `call_logs` 认这一格，注意这是估算不是实测。**

### 2.4 R→CC（客户端 Responses ← 上游 Chat Completions）——**合成 reasoning item，`encrypted_content` 留空**

`internal/translator/openai/openai/responses/openai_openai-responses_response.go`

- 流式：`:616-644`，`delta.reasoning_content`（回退 `delta.reasoning`）首次出现时合成 `output_item.added{type:reasoning}` + `reasoning_summary_part.added`，之后走 `reasoning_summary_text.delta`。
- 非流式：`:796-814`，`encrypted_content` 建了但留空（`:808`）。
- **`:800` 有一道闸**：`includeReasoning = gjson.GetBytes(requestRawJSON, "reasoning").Exists()`——客户端请求里没有 `reasoning` 参数就不合成 reasoning item。出向合成是**按客户端意图开关的**，不是无条件。

### 2.5 Gemini 方向——**thoughtSignature 当作不透明载荷原样搬进 Anthropic 的 signature 槽**

`internal/translator/gemini/claude/gemini_claude_response.go`

- `:118-125` 读 `thoughtSignature`（兼容 `thought_signature` 蛇形），`:79-83` 的 `appendSignatureDelta` **把 Gemini 的签名原样发成 Anthropic 的 `signature_delta`**。
- `:132` 判定为 thinking 的条件是 `part.thought == true` **或** `hasThoughtSignature`——即带签名的 part 一律当思考块处理。
- 到 CC 客户端则相反：`gemini/openai/chat-completions/gemini_openai_response.go:170-188`，`thought:true` 的文本进 `reasoning_content`，**只带 thoughtSignature 没有 payload 的 part 直接跳过**（`:178` 注释「Skip pure thoughtSignature parts but keep any actual payload in the same part」）；`:118-121` `thoughtsTokenCount` → `completion_tokens_details.reasoning_tokens`。

### 2.6 Codex(Responses)→A——**`encrypted_content` ↔ `signature` 双向不透明搬运，且允许"只有签名没有文本"的块**

`internal/translator/codex/claude/codex_claude_response.go`，几处注释是明确的踩坑化石：

- `:110-128`：
  > Codex splits a single reasoning item into several summary parts, but only `output_item.done` carries that item's final `encrypted_content`. Keep one thinking block open for the whole item and separate the parts with a blank line, **so the only signature ever emitted is the final one.**

  即多个 `reasoning_summary_part` 必须合并进**同一个** Anthropic thinking 块，`reasoning_summary_part.done` 故意不关块（`:126-128`）。
- `:185-191`：`output_item.added` 上带的 `encrypted_content` 是「pre-content snapshot, never the final value」，只当 `output_item.done` 缺字段时的兜底。
- `:243-252` + `:890-898` `finalizeCodexSignatureOnlyThinkingBlock`：**当一个 reasoning item 只有 `encrypted_content`、没有任何 summary 文本时，仍然合成一个空文本的 thinking 块，专门用来携带签名**。因为丢了这个块，下一轮就续不上 Codex 的推理链。
- 非流式 `:410-415`：`thinkingBuilder.Len() > 0 || signature != ""` 才产块，产块时 `signature` 直接来自 `encrypted_content`。

> **横切结论**：Anthropic 的 `signature` 槽在这个仓库里被当成**通用不透明载荷槽**使用——Gemini 的 `thoughtSignature`、Codex 的 `encrypted_content` 都往里塞，回程再原样取出。这是「不需要理解内容就能保真回放」的最省事做法，代价全部转移到第 3 节的回带判定上。

---

## 3. 回带：客户端把 thinking 块送回来时的处置

**总口径：默认丢，除非能证明这个 signature 是目标上游自己签的。** 判定中心是 `internal/signature/provider_compatibility.go`，函数 `DecideSignatureCompatibilityForModel`（`:246-306`）。

### 3.1 按目标 provider 的收场词（`provider_compatibility.go:269-303`）

| 目标上游 | Action | 仓库给的理由（原文） | 行号 |
|---|---|---|---|
| Gemini（functionCall / model-part / unknown 块） | 换成官方 bypass 哨兵 | "Gemini can bypass synthetic or incompatible model-part signatures with the documented sentinel" | `:271-277` |
| Gemini（其他块） | **整块丢** | "…not a bypass-safe Gemini model part" | `:278-279` |
| Claude | **整块丢** | "Claude has no cross-provider bypass sentinel for thinking blocks" | `:280-282` |
| GPT | **整块丢** | "GPT reasoning encrypted_content cannot be synthesized from another provider signature" | `:283-285` |
| Kimi | 只删 signature 字段，**块和文本留下** | "Kimi does not validate replayed thinking signatures, so the block survives without one" | `:286-294` |
| Grok/xAI | **整块丢** | "xAI verifies encrypted_content on replay and rejects foreign or mutated blobs" | `:295-300` |
| 未知 | `NoCompatibleReplacement` | "unknown target provider" | `:301-303` |

配套注释记了两条实测行为，portage 可直接采信：
- **Kimi**（`:287-292`）：「Its Messages endpoint never reads the field back: a mutated, truncated, non-base64 or absent signature all return 200」——因为 Kimi 的推理连续性走 `reasoning_content` 而不是 signature。
- **xAI**（`:296-297`）：「xAI decrypts `encrypted_content` and rejects the request with 400 `'Could not decrypt'` when the blob is foreign or mutated」——这是明确会 400 的那一家。

### 3.2 唯一一处"伪造"签名，且是官方哨兵

`internal/signature/gemini_validation.go:83-84`：
```go
GeminiSkipThoughtSignatureValidator = "skip_thought_signature_validator"
GeminiContextEngineeringBypass      = "context_engineering_is_the_way_to_go"
```
`gemini_validation.go:15-24` 写明这是 Gemini **官方文档给的合成历史逃生口**，不是伪造密码学签名；只在「合成 model turn 的第一个 functionCall 缺少兼容签名」时发（`provider_compatibility.go:271-277`、`gemini_sanitize.go:15-24`）。同一字面量在 `internal/translator/gemini/claude/gemini_claude_request.go:19` 又独立声明了一份。

**Claude / GPT / Grok 一律没有等价物 —— 这三家的收场词只有"整块丢"。** 这条直接回答了 #87 的核心疑问：跨协议合成的 thinking 块回带到真 Anthropic 上游时，正确做法不是造一个假 signature，而是在请求侧把它摘掉。

### 3.3 摘除的具体实现

- `internal/signature/claude.go:15-53` `StripInvalidClaudeThinkingBlocks`：遍历 `messages[].content[]`，signature 过不了 `IsValidClaudeThinkingSignature` 的 thinking part 整个删掉；`:57-76` 的变体连带删掉内容被清空的整条 message（Anthropic 拒收空 content 数组）。
- 例外闸门 `AllowEmptySignatureWithEmptyText`（`claude.go:78-90`）：**空 signature + 空文本**的占位块可以留。
- 严格模式 `claude_validation.go:222-256` `ValidateClaudeThinkingSignatures` 则直接报错 `"messages[i].content[j]: missing thinking signature"`，用在要硬拒的入口。
- OpenAI Responses 侧的口径不同，更宽：`internal/runtime/executor/openai_responses_signature.go:93-129` 只删 `encrypted_content`（`store != true` 时连带删孤儿 `id`），**保留 reasoning item 本身并继续转发**——`sdk/api/handlers/openai/openai_responses_signature_test.go:37-49` 用一个非法 Fernet 载荷验证仍返回 200。

### 3.4 请求侧各 pair 的默认行为（客户端回带 → 上游）

| 客户端 → 上游 | 默认（非 compat） | signature 处置 | 行号 |
|---|---|---|---|
| Claude → CC | 转成 `reasoning_content` 文本 | signature 须被判定为 GPT 来源，否则**整块丢**；`redacted_thinking` 永远丢 | `openai/claude/openai_claude_request.go:177-193`、`:376-388` |
| CC → Claude | **整块丢**（默认根本不读 `reasoning_content`） | compat 模式才转成 `{"type":"thinking","thinking":...,"signature":""}` | `claude/openai/chat-completions/claude_openai_request.go:216-222` |
| Responses → Claude | `reasoning` item → `thinking` / `redacted_thinking` | `encrypted_content` 须能验为 Claude 签名，否则**整块丢**；带 redacted 前缀的还原成 `redacted_thinking` | `claude/openai/responses/claude_openai-responses_request.go:376-379`、`:557-582` |
| Responses → CC | `summary[].text` → `reasoning_content` | 目标无 signature 槽，纯文本搬运；空文本时用字面量 `"[reasoning unavailable]"` 占位 | `openai/openai/responses/openai_openai-responses_request.go:231-233`、`:527-542` |
| Claude → Gemini | **整块丢** | compat 模式把 Claude signature 原样写进 `thoughtSignature`，**此文件不做任何 provenance 检查**（全仓最弱的一格） | `gemini/claude/gemini_claude_request.go:112-119` |
| Claude → Codex | `thinking` → `reasoning` item | 三档：GPT 来源直接放行 / 目标模型名含 grok 且过 `InspectGrokEncryptedContent` 放行 / 否则丢。**`summary` 恒为空数组，只回放不透明载荷不回放文本** | `codex/claude/codex_claude_request.go:148-173`、`:401-404` |

`codex_claude_request.go:392` 还固定发 `include: ["reasoning.encrypted_content"]`，主动向上游要下一轮能用的载荷。

### 3.5 `is-compat`：把"放行未签名块"做成 per-model 开关

配置项 `is-compat`（`config.example.yaml:311`，注释原文 `preserve thinking blocks with empty signatures for compatible upstreams`），落在每个 provider 的 model 定义上：`internal/config/config_types.go:395-398`（Claude）、`:580-582`（Gemini）、`:679-681`(OpenAI 兼容)。运行时通过 `internal/runtime/executor/helps/model_capabilities.go:11-13 APIKeyModelIsCompat` 取出，逐个 pair 传进请求转换函数。

**这是 portage 最该抄的一条工程口径**：「上游是不是真 Anthropic」不是猜的，是配置里声明的；声明为 compat 的上游（各种 OpenAI 兼容网关、自建推理服务）根本不校验 signature，未签名的 thinking 块可以放行；未声明的一律按真上游对待，摘干净。

### 3.6 附：signature 走私信封

两处自定义信封，用来把 Gemini 的 `thoughtSignature` 塞过没有对应槽位的协议：
- `internal/translator/gemini/openai/responses/signature_carrier.go:14`：`cpa-gemini-responses-carrier-v1:` + `direction:targetKind:base64(sig)`，塞进 Responses 的 `encrypted_content`；`:126-169` 是回程解封 + 相邻项语义校验（`direction` ∈ `next|previous|standalone`，`targetKind` ∈ `text|function|any`），对不上就删。
- `internal/translator/antigravity/claude/signature_validation.go:18-27`：`cpa-gemini-carrier-v1:`，同样的方案塞进 Claude `thinking.signature`。

portage 短期用不上（没有 Gemini 路径），但**信封思路**（自造前缀 + 版本号 + 相邻语义元数据 + 回程强校验）在需要跨协议保存不可翻译状态时是现成模板。

---

## 4. 请求侧的思考参数：唯一一处真正的 canonical 层

`internal/thinking/` 是本仓唯一符合「canonical 表示 → per-provider 翻译」的模块，`AGENTS.md` 明文要求不许破坏这个架构（"Do not break this 'canonical representation → per-provider translation' architecture"）。#87 把「请求侧思考参数怎么映」列为 Not-yet-specified，这里有现成答案：

- canonical 结构 `ThinkingConfig{Mode, Budget, Level}`（`internal/thinking/types.go:70-78`），Mode ∈ `Budget|Level|None|Auto`。
- 各协议入口：Anthropic `thinking.type` + `thinking.budget_tokens`（`apply.go:635-680`）、Gemini `thinkingConfig.thinkingLevel`/`.thinkingBudget`（`:692-735`）、CC `reasoning_effort`（`:794-805`）、Responses/Codex `reasoning.effort`（`:857-868`）、Kimi `thinking.type`/`thinking.effort`（`:807-849`）。
- level↔budget 换算表（`convert.go:11-22`）：`none=0 auto=-1 minimal=512 low=1024 medium=8192 high=24576 xhigh=32768 max=128000`，反向按阈值分桶（`:60-96`）。
- **「思考多少」与「显不显示思考」是两个正交轴**：后者由 `summary.go` 单独处理（`SummaryConfig{Mode,Detail}`，`summary.go:20-25`），能吃 OpenRouter 的 `reasoning.exclude`、Google 的 `include_thoughts`、Anthropic 的 `thinking.display` 等各家方言（`summary.go:34-115`）。**portage 若要放开思考参数，这条正交拆分值得先定下来。**
- 模型名后缀 `model(value)`（如 `gemini-2.5-pro(8192)`、`gpt-5.2(high)`）优先级高于请求体（`suffix.go:12-44`、`apply.go:253-263`）。
- 目标模型不支持思考时**静默剥掉**参数而不是报错：`strip.go:25-74` + `apply.go:237-251`。
- 校验/夹取：`validate.go:38-191`。跨 provider 家族或"协议说是 A、模型其实是 B"（Claude Code 打 Kimi 的 `/v1/messages`）时放宽为夹取而非报错（`:56-77`）；Anthropic 特有的 `budget_tokens < max_tokens` 约束在 `provider/claude/apply.go:169-208`。

---

## 5. 客户端行为特判（踩坑化石）

这是本仓最有价值的一节：它是给 Claude Code / Codex CLI / Gemini CLI 做代理的，所有针对客户端真实行为的补丁都留了注释说明为什么。

### 5.1 服务端 reasoning replay 账本——「客户端把签名块弄丢了，服务端补回去」

一类横跨四个 provider 的机制：代理**自己缓存上游发过的带签名 reasoning 块**，下一轮请求进来时按内容/ID/指纹把客户端那份（可能已被客户端自己的历史压缩剥掉或改掉）替换回原件。

- Kimi：`internal/runtime/executor/kimi_thinking_replay.go:144-177` `restoreKimiThinkingReplayContent`——倒着找最后一条「非 thinking 部分与缓存原件逐字匹配、但客户端这份没有 thinking 块」的 assistant turn，整条内容换成缓存原件。只缓存**同时带签名 thinking 与 `tool_use`** 的 turn（`:122-142`），即这个坑专门发生在工具调用轮。上游 400/422 时作废该缓存项（`:101-111`）。
- Claude 兼容端点：`internal/runtime/executor/claude_thinking_replay.go:20-34` 直接复用 Kimi 那套，仅对 **API-key 且 `is-compat` 的 Claude 模型**启用。`internal/config/config_types.go:395-397` 的注释把原因写死了：`IsCompat ... enables provider-aware signed-thinking replay for Claude-compatible API-key models`。
- Antigravity/Gemini：`internal/runtime/executor/antigravity_reasoning_replay.go:292-324`，按 call ID、由 name+args 派生的稳定 Claude tool-use ID、或上下文内容哈希指纹（`:980-1067`）三级匹配回填 `thought_signature`。仅 Gemini 家族启用，**显式排除 claude**（`:2131-2136`）。
- Codex：`internal/runtime/executor/codex_executor_reasoning.go:72-74`——**只在 source format 是 Claude 时启用**，即"Claude Code 打 Codex 后端"；原生 Codex CLI 有 `previous_response_id`/websocket 状态兜底不需要。`:217-281` 按轮次分段重锚，`:465-498` 的前缀指纹索引是性能补丁，注释写明朴素 O(n²) 重算「stalled large long-context requests for minutes before anything was sent upstream」。
- xAI：`internal/runtime/executor/xai_reasoning_replay.go:70-89` 把 session key 用调用方自己的 API key 哈希隔离，注释原因是**防止两个调用方靠复用 `prompt_cache_key`/window/session 头互相读到对方的加密推理与 assistant 文本**——多租户泄漏化石。`:115-186` 客户端最后一条 assistant 消息与缓存对不上时**宁可不回放也不猜**。

失败收场统一：上游报 `thinking_signature_invalid` 或 400 里含 `thoughtsignature` 就删缓存项（`codex_executor_reasoning.go:817-826`、`antigravity_reasoning_replay.go:277-290`、`kimi_thinking_replay.go:101-111`），下一轮退回用客户端原样发的内容，而不是永远重复同一个错猜。

> **对 portage 的意义**：这是有状态方案，代价是一份跨轮次会话缓存 + 一套指纹匹配 + 一套失效策略，共约五个文件上千行。portage 现阶段**不建议抄**；但它反证了一件事——如果 portage 选择「出向合成 unsigned thinking」，客户端回带时把它摘掉（§3）是正确且足够的收场，服务端补签名那条路成本高得多。

### 5.2 Anthropic 上游的三条实测约束

`internal/runtime/executor/claude_executor_request.go`：

- `:144-157`：**`thinking.display` 与 `redact-thinking-2026-02-12` beta 同时发，Anthropic 会认可 redaction，返回带 signature 但 `thinking` 字段为空的块**，摘要显示静默失效。注释标注实测环境：`Verified on api.anthropic.com with claude-opus-4-8: display=summarized yields thinking text only when the beta is absent.`
- `:352-369` `disableThinkingIfToolChoiceForced`：**`tool_choice` 为 `any` 或指定工具时 Anthropic 不允许 thinking**，代理会主动剥掉 `thinking` 与 `output_config.effort`。portage 的 CC→A、R→A 方向若把客户端的强制工具选择透传上去，同一冲突会原样出现。
- `:73-171`：向真 Anthropic 转发时按 Claude Code 的**精确 beta 头顺序**重建请求。

### 5.3 客户端身份识别

`internal/runtime/executor/helps/claude_client_detection.go:128-152` `DetectClaudeCodeRequest`：确认一个请求真是 Claude Code 需要四项同时成立——`X-App: cli` 头、匹配 `claude-cli/…` 模式的 User-Agent（`:32`）、`Anthropic-Beta` 里有 `claude-code-20250219`、以及格式合法的 `metadata.user_id`。再按 User-Agent 细分 entrypoint（`cli`/`mcp`/`sdk-ts`/`claude-vscode`/`claude-desktop`…，`:35-61`），只有 `cli`/`sdk-cli`/`claude-vscode` 算 native 可直通（`:66-70`）。

更细的一处：`:63-192` 为 Claude Code 内部那个 Haiku "helper" 子请求（标题生成一类，它**故意不带** `claude-code-20250219` beta）维护了一张 **6 条精确 beta 组合**的白名单，注释写明取样来源是「14 个无标记的 native Claude Code 2.1.220 Haiku helper 请求」，且刻意保持精确匹配以免变成通用绕过。请求体形状也要对（`:312-393`：model 必须是 `claude-haiku-4-5-20251001`、`thinking.type == "disabled"`、`output_config.format` 是 JSON schema…）。

会话键：`internal/runtime/executor/helps/claude_code_session.go` 从 `X-Claude-Code-Session-Id` / `X-Claude-Code-Agent-Id`（或 `metadata.user_id` 的 `_session_<uuid>` 后缀）派生；原生 Codex/OpenAI 客户端则退回 `Session-Id` / `prompt_cache_key` / `X-Codex-Window-Id`（`codex_executor_reasoning.go:85-120`）。

出向也做伪装：`internal/runtime/executor/codex_executor_request.go:305-306` **不转发下游客户端的 User-Agent**，注释原因是「reduce Cloudflare 1010 blocks」。

### 5.4 Codex 压缩（与 portage #65/#68 的既有调研互补）

- `previous_response_id` 在 **HTTP 路径上一律删掉**（`codex_executor_execute.go:58`、`codex_executor_stream.go:57`），代理每轮把完整历史展开进 `input` 再叠 replay 账本；只有 **websocket v2** 路径才用它（`sdk/api/handlers/openai/openai_responses_websocket_requests.go:93-103`，注释：`Websocket v2 mode uses response.create with previous_response_id + incremental input. Do not expand it into a full input transcript`）。
- `store` 语义：`internal/runtime/executor/openai_responses_signature.go:25-30` 注释原文——Codex 后端**拒绝 `store=true`**，而 `store=false` 时又不持久化 item，于是一个「有 id 但 `encrypted_content` 不可用」的 reasoning item 会被当成服务端查表并返回 `Item with id '...' not found. Items are not persisted when store is set to false.`。所以 `store` 不为 true 时连 `id` 一起删（`:30`、`:76-90`、`:112-124`）。
- **Codex CLI 本地压缩摘要的精确提示词**被写死在 `sdk/api/handlers/openai/openai_responses_websocket.go:41`（`codexLocalCompactionSummaryPrefix = "Another language model started to solve this problem and produced a summary of its thinking process..."`），用来识别「客户端刚做完本地压缩」。识别命中且没有 `previous_response_id` 时走**整段 transcript 替换**而不是增量合并（`openai_responses_websocket_requests.go:181-209`），注释解释：当增量处理时会重复注入陈旧 turn state 并留下孤儿 `function_call`。
- 服务端 compaction item 检测：`openai_responses_websocket_prewarm.go:108-134`；命中时跳过与陈旧 `lastRequest`/`lastResponseOutput` 的合并，注释直接引了 issue #2207（合并会打断 `function_call`/`function_call_output` 配对）。
- `/v1/responses/compact` 端点：`sdk/api/handlers/openai/openai_responses_handlers.go:545-587`，拒绝 `stream:true`；Claude executor 明确不支持（`claude_executor_execute.go:20-22` 返回 501 `"/responses/compact not supported"`）。
- xAI 在压缩轮完成后主动清空 reasoning 账本（`xai_reasoning_replay.go:296-306`），避免把压缩前的加密推理回放到压缩后。

---

## 6. MIT 许可：义务边界

`LICENSE` 是标准 MIT（`Copyright (c) 2025-2005.9 Luis Pater` / `Copyright (c) 2025.9-present Router-For.ME`），仓库无 NOTICE、无 CLA、源码文件无逐文件版权头，`go.sum` 无 GPL/LGPL 传染依赖。

对照 CLAUDE.md 里 sub2api 那条（LGPL-3.0，"整包复制需评估义务，Go 静态链接下约等于整项目跟随"）：

- **读源码、学思路、抄字段语义与坑清单**：无任何义务。与 LGPL 一致。
- **复制成片代码进 portage**：MIT 只要求**在分发物中保留版权声明与许可全文**。做法是 portage 仓库加 `THIRD_PARTY_LICENSES` 之类文件收录该 MIT 文本，或在被复制文件顶部保留出处 + 版权行。**没有 copyleft，不影响 portage 自身许可，不因 Go 静态链接而传染** —— 这与 sub2api 的关键差别。
- **建议的入册措辞**：可以整段复制并保留许可声明；实践上仍建议只搬**决策表内容**（第 3.1 节那张表）与**注释里的踩坑事实**，因为它的点对点架构与 portage canonical 层不兼容，成片代码搬过来也接不上。

---

## 7. 入册定位与可借鉴清单

**定位：首要参考，主题限定为「thinking/reasoning 跨协议保真与 signature 处置」；架构不参考。**

理由：仓库总计 3407 个 commit（2025-07-02 起），其中 **466 个** commit message 命中 `reason|thinking|signature|thought`（约 14%），且 `internal/signature/` 是一个 19 文件的独立子系统、逐 provider 写明"为什么这家能/不能回放"。这个密度在四个参考仓库里是唯一的——new-api 只做了 A→CC 一个方向的合成、sub2api 与 opencodex 的关注点在 Responses/压缩。

### 直接可用（建议进 #87 裁决材料）

1. **出向合成与回带回放解耦**（§0、§2.1、§3）——推翻 v0.10 的核心论据。
2. **回带侧 provenance 决策表**（`provider_compatibility.go:269-303`，§3.1）——包括 Kimi 不校验、xAI 400 这两条实测事实。
3. **`is-compat` per-model 声明**（§3.5）——把"上游是不是真 Anthropic"从猜测变成配置。
4. **Anthropic 的两条 signature 行为**（§0）：拒收无签名/空签名的 thinking 块；但不校验文本与 signature 的对应关系。前者是"回带必须摘"的依据，后者解掉"摘要文本配原签名"的顾虑。
5. **`collectOpenAIReasoningTexts` 的四形状容忍**（§2.1）——portage 解码侧直接可用。
6. **thinking 参数的正交拆分**（"思考多少" vs "显不显示"，§4）——#87 Not-yet-specified 那两条的现成模型。
7. **Codex reasoning item 的三条坑**（§2.6）：多 summary part 合一块、`output_item.added` 的 `encrypted_content` 是快照不是终值、只有签名没有文本的 item 也必须成块。
8. **Anthropic 上游的两条硬约束**（§5.2）：`tool_choice` 强制时不允许 thinking；`thinking.display` 撞上 redact beta 会静默返回空 thinking。

### 值得知道，但现阶段不建议抄

- **服务端 reasoning replay 账本**（§5.1）——有状态方案，五个文件上千行 + 会话缓存 + 指纹匹配 + 失效策略。它的存在恰好反证：portage 走「出向合成 unsigned + 回带摘除」这条无状态路线是成本合理的选择。
- **客户端身份精确识别**（§5.3）——portage 目前不需要区分 Claude Code 的 entrypoint；但那张 6 条 beta 组合白名单说明「客户端会发出你没预料的请求形状」，portage 的 golden 样本覆盖面要按这个心理准备来定。

### 不参考

- 点对点 N×M 转换架构（§1.3）——与 portage canonical 事件模型方向相反。
- `interactions` / `antigravity` 两条 format 线（Google 内部/Antigravity 客户端专用），portage 无对应场景。
- 运营侧（OAuth 池、轮询、配额、管理端、TUI、WebSocket relay、插件宿主）。

---

## 8. 版本记录

| 日期 | 修改人 | 变更 |
|---|---|---|
| 2026-08-13 | jinpenga | 初稿，对应 #89；调查对象 CLIProxyAPI `d757063c` |

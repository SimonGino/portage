# Research：opencodex 的 Responses 转译与压缩兼容盘点（#65）

来源：本地 `~/Code/GitHub/opencodex`（MIT），只读求证；codex-rs 约束另经 GitHub 代码搜索对照 `openai/codex`（Apache-2.0）原文核实。下文行号均指该仓库当前工作副本。

## 0. 背景：为什么 routed 模型会收到远程压缩请求

Codex 客户端按 provider 名（内置 `OpenAI`）判断"该 provider 支持远程压缩"，而 opencodex 的 Design B 把这个 provider 指向代理，于是**每个 routed 模型都会收到 remote compaction v2 请求**（`src/responses/compaction.ts:1-16` 顶部注释）。请求就是一个普通 `/responses` 调用，input 尾部多一个 `{"type":"compaction_trigger"}` 项。

## 1. Remote compaction v2 turn 的识别与合成

### 1.1 识别

- 解析层：`src/responses/parser.ts:355-358` —— input 里遇到 `type === "compaction_trigger"` 项即置 `compactionRequest = true` 并把该项吞掉（不进消息列表）；标志落在 `parsed._compactionRequest`（`src/types.ts:75-81`）。
- 服务层：`src/server/responses/core.ts:1993-1994` —— `routedCompaction = parsed._compactionRequest === true && !isCanonicalOpenAiForwardProvider(route.provider)`。关键点：**Responses 形状的 wire ≠ 支持 `compaction_trigger`**，只有 canonical ChatGPT 后端懂这个私有契约；一个 API-key 网关会把 trigger 当普通输入、回一条普通消息，让 Codex 因缺 compaction item 而 fatal（core.ts:1989-1992，#422）。所以只有真 OpenAI passthrough 才原样转发，其余一律走代理合成。

### 1.2 turn 的改写：把模型当纯 summarizer 跑

`src/server/responses/core.ts:1995-2010`：routedCompaction 时删除 `tools`、`_webSearch`、`toolChoice`、`parallelToolCalls`、`textFormat`/`_structuredOutput`（结构化输出若存活会把 schema 约束的 JSON 灌进合成 compaction item；Kiro 的能力守卫还会直接拒绝该 turn），并向 context 追加一条 user 消息 `COMPACT_PROMPT`。`COMPACT_PROMPT` 镜像 codex-rs `core/templates/compact/prompt.md` 的本地压缩指令（`src/responses/compaction.ts:20-29`）。

同一改写在 key-mode openai-responses 适配器里**再做一遍**：`src/adapters/openai-responses.ts:1151-1167` `buildRoutedCompactionBody` —— 因为该适配器从 `parsed._rawBody` 构建请求，core.ts 对 `parsed.context` 的修改根本到不了 wire，所以要在 `_rawBody` 上重新剥 trigger、tools、`additional_tools`、`text`，深度剥图片（`input_image` → `[image omitted for compaction]`，1130-1138），再追加 COMPACT_PROMPT。

另外 `src/server/responses/core.ts:2721-2723`：routed compaction turn 不得进 image bridge sidecar——那会返回一个普通 completion 而不是合成 compaction item（#424）。

### 1.3 accumulate → 恰好一个合成 item → response.completed

流式路径 `bridgeToResponsesSSE`（`src/bridge.ts:158`，选项 `compaction?: boolean` 见 166-175）：

- `src/bridge.ts:489-492`：`compactionText` 收集器，跨 message 边界累计整个 turn 的 assistant 文本。
- `src/bridge.ts:802-818`：compaction turn 中，`text_delta` 只累计不发射；**除 `done`/`incomplete`/`error` 以外的所有事件一律 `continue` 跳过**（thinking、tool call、web search 等都不产生 wire 事件）。
- `src/bridge.ts:1111-1120`：`done` 时合成 `{type:"compaction", id:"cmp_<uuid>", encrypted_content: encodeCompactionSummary(compactionText)}`，只发一条 `response.output_item.done`（**没有**配对的 `response.output_item.added`），item 进 `finishedItems`，随后 1137-1145 发 `response.completed`（带 usage）。

非流式路径 `buildResponseJSONWithBudget`（`src/bridge.ts:1356`）：`src/bridge.ts:1576-1582` 同样把 text 累进 `compactionText` 而不进正常 output；`src/bridge.ts:1736-1746` 只在 turn **干净完成**时（无 error/incomplete、stopReason 非 `max_tokens`/`content_filter`）才 push 合成 compaction item——注释明言"截断的 turn 绝不能被装成替换历史"（#422）。

`compaction: true` 的注入点：`src/server/responses/core.ts:2985、3032、3714、3772`（passthrough-SSE 转译、流式 bridge、非流式 bridge 各分支），都来自同一个 `routedCompaction`。

### 1.4 codex-rs `collect_compaction_output` 的约束（对照原文核实）

`openai/codex` 仓库 `codex-rs/core/src/compact_remote_v2.rs`：

- `collect_compaction_output`（约 386 行起）遍历 `ResponseStream`，统计 `OutputItemDone` 里 `ResponseItem::Compaction` 的个数，保留**第一个**（`if compaction_output.is_none() { … }`）。
- 约 423 行：`if compaction_count != 1 { return Err(CodexErr::Fatal(format!("remote compaction v2 expected exactly one compaction output item, got {compaction_count} from {output_item_count} output items"))) }`——**0 个和 ≥2 个都 fatal**，不只是 0 个。

opencodex 内部注释（`src/bridge.ts:1112`"takes the first and fatals on 0"）比真实检查略窄；`src/responses/compaction.ts:7-9` 的表述（"requires EXACTLY ONE … or it fatals"）与原文一致。这就是"恰好一个"的硬约束来源：发两个 compaction item 会 fatal，漏发也 fatal（普通 message item 不计入 compaction_count，所以约束只针对 compaction 类型的 item）。

## 2. `encodeCompactionSummary` 编码格式与解码回带

### 2.1 编码

`src/responses/compaction.ts:18、36-38`：`"ocx1:" + base64(utf8(summary))`。透明信封，不加密——routed 模型产不出 OpenAI 的加密 blob，代理就自造一个可逆的（文件头注释 11-15 行）。

### 2.2 解码与回带还原

- `decodeCompactionSummary`（`compaction.ts:41-48`）：有 `ocx1:` 前缀则 base64 解回文本；真正的 OpenAI 加密 blob（无前缀）或垃圾返回 null。
- 后续请求里 Codex 会把存储的 compaction item 回带进 input。`src/responses/parser.ts:372-390`：遇到 `type` 为 `compaction`/`compaction_summary`/`context_compaction` 的输入项，经 `compactionItemToText`（`compaction.ts:51-54`）还原成一条 **user 消息**：`ocx1` 信封解出的文本前面加 `SUMMARY_PREFIX`（镜像 codex-rs `core/templates/compact/summary_prefix.md`，`compaction.ts:31-32`——"另一个语言模型产出的摘要…"的框架文案）；解不开（真加密 blob）则降级为一句 `OPAQUE_COMPACTION_NOTE`（"[earlier conversation was compacted; …]"，`compaction.ts:34`）。
- `context_compaction` 是 codex-rs 本地压缩的 marker，无 `encrypted_content` 时是纯标记，静默丢弃、不置 `_compactionRequest`（parser.ts:375-382 注释）。只有**本请求新出现**的 marker 才置 `_contextCompactionBoundary`（parser.ts:380，`src/types.ts:82-87`），previous_response_id 展开回来的前缀里的 marker 不重复触发 provider 私有续接缓存的重置。

### 2.3 v1 unary 端点（`POST /responses/compact`）复用 v2 合成

`src/server/responses/compact.ts:651-713`：routed 模型的 v1 压缩不再单独实现——在 input 尾部拼 `{type:"compaction_trigger"}`（657 行）内部调 `handleResponses` 跑一遍 v2 合成 turn（非流式），然后：校验 `status === "completed"`（686-692）、校验 output 里 compaction item **恰好一个**（693-703）、解 `ocx1` 信封且摘要非空（704-709），最后 `buildCompactV1Output` 组装 v1 替换历史（711）。`buildCompactV1Output`（`compaction.ts:98-124`）镜像 codex-rs `build_compacted_history`：按 20k token（~4 chars/token）预算倒序保留最近真实 user 消息（部分截断时保尾部、避开半个 surrogate pair），末尾追加一条 `SUMMARY_PREFIX\n<summary>` user 消息——codex-rs 靠这个精确前缀识别存储的摘要（122 行注释）。校验失败宁可回 502 也不把 "(no summary available)" 装成替换历史静默丢会话（677-679 注释，#422）。

## 3. context window / 模型信息如何影响客户端压缩触发时机

两个客户端表面要分开看。

### 3.1 Codex 表面：catalog 里直接下发 `auto_compact_token_limit`

- `src/codex/catalog/effort.ts:124-131`：routed 模型的 catalog entry 写入 `context_window`、`max_context_window`，并且 **`auto_compact_token_limit = min(floor(contextWindow * 0.9), maxInputTokens)`**——代理直接告诉 Codex 在窗口 90%（或上游 max_input 上限）处触发自动压缩。`src/codex/catalog/parsing.ts:268-271` 对用户 override 的 contextWindow 同样重算 `auto_compact_token_limit = floor(cw * 0.9)`；`parsing.ts:321` 缺省窗口按 128000 兜底。
- native 模型窗口来自 override 表 `src/codex/catalog/metadata.ts:89-95`（如 gpt-5.5 = 272k、gpt-5.4 = 1M）。
- `src/codex/inject.ts:429-431`：丢弃用户 config.toml 根级的 `model_context_window` override——那会让所有模型都谎报 1M 窗口，压缩触发点整体后移到真实 API 限制之后。

所以对 Codex 而言，压缩时机 = 代理在 catalog 里报的窗口 × 0.9；报大了客户端会晚压、先撞上游 400，报小了会过早压缩。

### 3.2 Claude Code 表面：`[1m]` marker + AUTO_COMPACT_WINDOW + `max_input_tokens`

`src/claude/context-windows.ts`：

- Claude Code CLI 对带 `[1m]` 后缀（大小写不敏感，25 行）的模型 id **恰好按 1M token 记账**；auto-context 机制的常量：`AUTO_COMPACT_WINDOW_DEFAULT = 350_000`、`AUTO_CONTEXT_FLOOR = 200_000`、合法区间 100k–1M（18-22 行，2.1.207 二进制核实）。
- 标记谓词 `shouldMarkOneMillion`（83-87 行）：窗口 ≥1M 恒标；auto-context 开启时额外标"窗口 >200k 且 ≥ compactWindow"的模型——**给真实窗口小于 compactWindow 的模型打标会把压缩安全网放到真实 API 限制之后，导致会话中途 400**（78-82 行注释）。
- `resolveAutoContext`（64-75 行）：用户关掉 autoContext 或设了 legacy `maxContextTokens`（CLI 内 MAX_CONTEXT_TOKENS+DISABLE_COMPACT 组合优先级最高）则整套失效；用户自己 export 的 `CLAUDE_CODE_AUTO_COMPACT_WINDOW` 合法值优先、非法值直接禁用自动打标（否则 CLI 忽略环境变量，被标记的 sub-1M 模型失去安全网）。
- `buildClaudeContextWindows`（89-122 行）：窗口表只登记**权威**值（native override 表 / adapter 上报的 `CatalogModel.contextWindow`），"nothing is guessed"（8-9 行）；anthropic passthrough 的 sub-1M 行不登记（115 行）——避免给不能承载 1M-beta 的 canonical claude 模型打标。

`src/claude/model-info.ts`：

- Anthropic 风味 `/v1/models` 的 `ModelInfo.max_input_tokens` 报权威窗口、否则 null；`max_tokens` 恒 null（代理侧没有权威输出上限，15-16 行）。
- `[1m]` picker 变体行（114-132 行）：**只有权威窗口 ≥1M 的模型**才多一条 `id[1m]` 可选行（`max_input_tokens` 定为 1M）——auto-context 的放宽曾让一个 372K 路由带上 marker 被 Claude Code 灌爆，即 #854 缺陷，这里明确不回归（120-125 行注释）。
- capabilities 里 `context_management.compact_20260112` 等一律报 `supported:false`（52-57 行）——不向 Claude Code 谎报服务端压缩能力，压缩仍由客户端本地触发。

## 4. 压缩 turn 的 SSE 事件线取舍：为什么"只发合成 item + completed"不丢内容

确认：`src/bridge.ts:802-818` 中 compaction turn 只有三类事件穿透（`done`/`incomplete`/`error`），`text_delta` 静默累计，其余全部跳过；正常输出事件（`output_item.added`、`output_text.delta`、reasoning、tool call…）一个都不发，合成 item 也只有一条 `output_item.done`（1117 行）。为什么可行且无损，三条腿：

1. **内容没有被丢，只是换了载体**：压缩 turn 的全部可见产出就是摘要文本本身，它整体进了 `ocx1` 信封，下一轮请求回带时由 parser 还原成 user 消息（§2.2）——用户视角信息守恒。
2. **Codex 的压缩 UI 中途本来就什么都不渲染**（bridge.ts:806 注释"its compaction UI renders nothing mid-turn, so nothing is lost visually"），Codex 对多余 item 也只是忽略（805 行注释），所以省掉的事件没有观感损失。
3. **replay 去重**：若把摘要同时作为普通 assistant message 发出，一旦该 response 经 previous_response_id 展开被回放（`rememberResponseState` 存 input+output），摘要会出现两份（bridge.ts:802-805；非流式对应 1576-1577）。

诚实记一处张力：上述第 3 条的注释以 `rememberResponseState` 回放为由，但 `src/server/responses/core.ts:3723-3734`（流式）与 3782-3790（非流式）实际上**对 compaction turn 完全跳过 continuation 缓存**——理由是 `_rawBody` 还揣着完整的压缩前历史，缓存它会让后续 previous_response_id 展开把 Codex 刚替换掉的巨长旧链条重新灌回来；passthrough 分支同理（core.ts:2029-2032）。两处事实并存，bridge 注释可能早于 core.ts 的排除逻辑；但即便缓存已跳过，"摘要只走 compaction item 单一载体"仍避免了任何路径上的双份摘要，取舍不受影响。

心跳保活不受影响：compaction 抑制的是内容事件，wire 层 keepalive/stall 节拍照常（bridge.ts:797-800，adapter 心跳只算上游活性、不压制 wire keepalive）。

## 5. reasoning 回放与压缩的交互

两套信封是平行且独立的：压缩用 `ocx1:`（`compaction.ts:18`），reasoning 用 `ocxr1:`（`src/responses/reasoning-envelope.ts:15`，内容是 JSON：Anthropic 签名 `sig`、redacted 块 `red`、隐藏思考文本 `txt`、Kiro KMS blob `krc`，17-33 行）。两者都遵循"无前缀 = 真加密 blob = 不碰"的约定（reasoning-envelope.ts:39-41；compaction.ts:41-42）。

交互点三处：

1. **压缩边界丢弃悬挂 reasoning**：parser 里 reasoning 输入项先进 `pendingReasoning`、等归属的 assistant 到来（`src/responses/parser.ts:306-319`）；而回带的 compaction item 变成 user 消息前会 `pendingReasoning.length = 0`（parser.ts:383）——压缩边界之前的孤立 reasoning 被清空，不会错挂到边界之后的 turn 上（agent_message 同样处理，409 行）。
2. **压缩 turn 自身不产 reasoning item**：bridge 的事件抑制（§4）让 thinking/签名/redacted 块在 compaction turn 里全部不上线，摘要信封里也只有纯文本——压缩产物不携带任何需要回放的 reasoning 状态。
3. **回放缓存补偿"压缩后丢失的 reasoning_content"**：`src/responses/reasoning-replay-cache.ts` 是进程内有界缓存（64 条 / 256 KiB / 1h TTL，29-31 行），bridge 关闭 reasoning 块且后随 tool call 时记录原始 reasoning（bridge.ts:972、1641），key 按"客户端线程 + provider + 目的地 + adapter + 模型 + 凭证身份 + call id"限定（65-85 行，防 `call_1` 这类不唯一 id 串会话，#950/#971）。消费侧 `src/adapters/openai-chat.ts:358-383`：DeepSeek thinking 模式要求 tool-call assistant 消息必须带 `reasoning_content`，而**压缩后的历史（compacted history）、丢失的 assistant turn、孤儿修复的 tool result**里 thinking 部分已不存在——此时用 `peekReasoningForCall` 找回缓存文本；缓存 miss（长会话必然发生，371-380 行注释）则按 `requiresReasoningPlaceholderModels` 注入占位符 `" "`，否则上游 400（#1193）；孤儿 tool result 路径同样处理（openai-chat.ts:430-448）。即：压缩把 reasoning 从历史里挤掉之后，这个缓存是让 thinking-mode provider 仍能接受续接请求的兜底。

## 6. 其他与压缩/长会话相关的 Responses 转译兼容手段

- **previous_response_id 本地展开 + 溢写**（`src/responses/state.ts`、`src/responses/spill-store.ts`）：Codex 每个请求都 `store:false` 却仍用 previous_response_id 链接 WS turn，代理用进程内缓存（`force` 绕过 store:false 跳过，state.ts:958-963）存 `input+output`（state.ts:979-981），`expandPreviousResponseInput`（843-863 行）把存储项拼回 input 前缀并记录 `replayedInputPrefixLength`（parser 靠它区分"回放前缀"与"本请求新增"，见 §2.2 的 marker 判定）。内存上限 64 MiB / 1000 条 / 1h TTL（state.ts:17-23）；超限条目溢写磁盘（spill-store.ts，单 payload 上限 256 MiB，55 行），溢写丢失/损坏时回**显式结构化 400**（`previous_response_not_found`，state.ts:62-65）而非静默截断。**压缩 turn 被排除在这个缓存之外**（§4 的张力条目）——这是压缩与长会话状态机制最重要的交汇点。
- **压缩 turn 的效果上限豁免**：`src/server/responses/core.ts:1107`，effort cap 判定把 `_compactionRequest` 作为输入之一（压缩 turn 的效率档位单独考量）。
- **Cursor 会话隔离**：`src/server/responses/core.ts:1586`，`_compactionRequest === true` 时置 `_cursorIsolateConversation`——压缩 turn 不得复用 Cursor 的服务端会话续接。
- **尾部元数据的位置约定**：`compaction_trigger`/`additional_tools` 被视为"尾随元数据而非更新的用户 turn"——加密 agent task 检测跳过它们找真正的尾项（`src/server/responses/encrypted-payload.ts:185-195`）；developer message 注入时若尾项是 `compaction_trigger` 则插到它前面（`src/server/responses/collaboration.ts:432-440`）。
- **v1 端点的池路由**：`src/server/responses/compact.ts` 前半段（canonical 路径）带账号池 rotate/quota 记录，routed 路径（651 行起）复用 v2 合成——压缩请求与普通请求共享同一套上游健康/配额机制。

## 7. 对 portage 的启示（摘要）

1. 若 portage 面向 Codex 客户端做 Responses 转译，**必须**处理 `compaction_trigger`：要么确认上游真是 canonical OpenAI 后端才透传，要么自己合成"恰好一个" `{type:"compaction", encrypted_content}` item——`compaction_count != 1` 即 Codex fatal，且截断 turn 不能产出 item。
2. 透明信封（前缀 + base64）是"上游产不出加密 blob"时的最小可行方案，配套要求：回带解码、真加密 blob 的降级文案、以及**摘要只走单一载体**避免回放重复。
3. 对外报告的 context window 就是客户端压缩触发器：报什么客户端就按什么的 ~90% 触发，谎报（或放任用户 override 谎报）会把压缩安全网推到真实 API 限制之后。
4. 压缩 turn 要从续接缓存 / provider 会话续接 / 工具面 / 结构化输出 / 图片管线里全部摘出来，这些交互点每一处都有对应的 issue 编号（#422/#424/#854/#950/#1193），说明都是踩过的坑。

# Research：portage 现状差距清单草稿（#66）

对照材料：#68（Codex CLI 压缩客户端事实）、#64（sub2api 的 compact 兼容做法）、#65（opencodex 的合成路子）。盘点对象为 portage 本仓现状，全部结论读代码求证，附文件:行号。

**范围**：只限入口方向（Codex CLI → portage 的 `/v1/responses`）。出口转 Responses（A→R / CC→R）已划出范围，本文不涉及。

**必过档口径**（引 #68）：Codex 默认走 remote compaction v2——普通流式 `/v1/responses` 请求、input 末尾带 `{"type":"compaction_trigger"}`，要求响应流里**恰好一个** `{"type":"compaction","encrypted_content":...}` output item + `response.completed`，`compaction_count != 1` 即 `CodexErr::Fatal`，不重试、不降级。压缩成功后，**每个后续请求的 input 都会回带**该 compaction item。触发阈值 = Codex 自认的 context_window × 90%，用量口径 = `response.completed` 的 `usage.total_tokens`。

## 0. 现状速写：三条 Responses 入口路径

| 路径 | 入口 → 渠道 | 实现 | compaction 相关行为 |
|---|---|---|---|
| 透传 | Responses → Responses | 字节复制，只 splice 改写 model（`internal/server/server.go:363-369`），body 其余原样到上游 | `compaction_trigger`、`compaction` item、`previous_response_id` 全部原样透传 |
| R→A | Responses → Anthropic | `conversionOpen` 已放开（`internal/server/convert.go:35-36`），decode→canonical→encode | 见 G1/G2/G4 |
| R→CC | Responses → Chat Completions | 已放开（`convert.go:33-34`） | 见 G1/G2 |

全仓 `internal/` 无任何 `compact` 字样（grep 证实）：没有 compact 端点、没有 compaction item 识别、没有能力标注。

---

## G1（Blocker）：转换路径把 `compaction_trigger` 静默丢弃 → Codex Fatal，长会话到阈值即砖死

### 现象与故障模式

`openairesponses.DecodeRequest` 的 `decodeInput` 只认 `message` / `reasoning` / `additional_tools` / 工具调用与结果六类 item；`compaction_trigger` 落进 default 分支被**静默跳过、不报错、不登记 drop、无日志**（`internal/protocol/openairesponses/decode.go:208-217`）。于是压缩 turn 变成一个普通采样请求发往 Anthropic/CC 上游，上游回一条普通 assistant 消息，portage 编出的响应流里 **compaction item 数为 0**。

Codex 侧后果（#68 §3/§5）：`collect_compaction_output` 数出 0 个 → `CodexErr::Fatal("expected exactly one compaction output item, got 0 …")`，**不重试不降级**；pre-turn 压缩失败则整个 turn 不执行、历史不变，下一 turn 阈值仍超 → 再压缩 → 再 Fatal。**会话在触到阈值后永久卡死**，且因为丢弃无日志，网关侧看到的只是一次「成功」的普通转发，排障无从下手。

对必过档 Codex CLI 的影响：R→A（Codex 挂 Claude，口径层优先级②）和 R→CC 两条路径上的**每一个长会话必然触发**。严重度：Blocker。

### 参考实现怎么做（两家路子不同）

- **opencodex：本地合成**。解析层识别 trigger 并吞掉（parser.ts:355-358）；把该 turn 改写成纯 summarizer——剥 tools/tool_choice/结构化输出/图片，追加 COMPACT_PROMPT（core.ts:1995-2010）；流内累计全部 assistant 文本、抑制中间事件，`done` 时合成恰好一条 `{type:"compaction", encrypted_content:"ocx1:"+base64(摘要)}` 的 `output_item.done` + `response.completed`；截断/出错的 turn **绝不**产出 item（宁可报错也不把残缺摘要装成替换历史，#422）。关键判定：**Responses 形状的 wire ≠ 支持 compaction_trigger**，只有 canonical OpenAI 后端才透传（core.ts:1993）。
- **sub2api：转发到真 compact 端点**。它面向的上游（ChatGPT backend / api.openai.com）真有 `/responses/compact`，所以做的是端点归一 + body-signal 提升 + unary→SSE 桥；只有 Grok（无 compact 端点）走与 opencodex 同构的本地模拟（注入总结 prompt 跑普通 turn 再转回 compact 形态）。另有账号级 compact 能力三态（支持/未知/不支持）+ 探测。

### 建议修法与代价（PO 裁决选项）

1. **透传声明 + 渠道能力标注**（工作量小）：只承诺「Responses 渠道且上游为 canonical OpenAI」时压缩可用（现状透传已可用）；转换路径遇 trigger 明确报 400 或流内 `response.failed`，同时补 drop 日志。代价：R→A/R→CC 的 Codex 长会话仍不可用（报错可读但会话照样卡死在阈值——Codex 压缩失败不降级），只是把静默 Fatal 换成可诊断的错误。注意「透传安全」有前提：portage 现无渠道级 compaction 能力位，把一个不认 trigger 的 Responses 兼容网关配成渠道，透传同样让 Codex Fatal（opencodex core.ts:1993 的判定、sub2api 的能力三态即为此而生）；此选项应含最小的渠道标注（如渠道配置一个布尔位）。
2. **本地合成**（工作量中-大，让必过档在 R→A/R→CC 真正可用的唯一修法）：照 opencodex 的路子——decode 侧识别 trigger 置标志；转换 turn 剥工具面 + 注入压缩 prompt；encode 侧压缩模式下抑制正常 item、累计文本、合成恰好一个 compaction item（透明信封 `前缀:+base64`）；截断 turn 不产 item。配套必须同时做 G2（回带解码），并处理静默期问题：合成期间下行 SSE 零字节直到摘要生成完（opencodex 底下有 wire keepalive 层，portage 没有——`writeDeadline` 只管「写出后客户端收多慢」，不会自己发心跳；前置 nginx 的空闲超时会掐线，同 sub2api #3887 的教训）。风险：压缩 prompt 质量、信封格式的长期兼容。
3. **明确拒绝**（工作量最小）：转换路径遇 trigger 回 400 并写文档「Codex 压缩仅支持 Responses 透传渠道」。与选项 1 差别只在不做渠道标注。

建议：至少先做选项 1（含 drop 日志与渠道标注），选项 2 作为 R→A 必过档的后续票立项。

---

## G5（High，且是 G1 的前置症状）：对外无 context window 元数据，Codex 触发点错位 → 压缩还没触发先撞上游 400

### 现象与故障模式

portage 的 `/v1/models` 只报 `id/object/created/owned_by` 四个字段（`internal/server/server.go:263-281`），没有也无法按 OpenAI list 格式携带 context window；此外没有任何模型元数据端点。而 Codex 的窗口认知完全来自**它自己的**内置模型目录 + config 覆盖（#68 §1）：portage 暴露的接入点名/限定名（如 `渠道名/纳管模型名`）不在 Codex 目录里，吃到的是 fallback 元数据 `context_window: 272_000`，触发点 ≈ 245k。

后果链（必过档 R→A 场景）：真实上游 Claude 窗口 200k → 会话到 ~200k 时上游先报 context 超限（转换路径译成 400/502 给客户端）→ Codex 把用量标记为打满（#68 §1(3)）→ 下一 turn 触发 pre-turn 压缩 → **落进 G1 的 Fatal**。即用户实际观测到的故障序列是「先莫名 400，再永久卡死」，G5 是第一症状、G1 是终态，两者在旗舰路径上是复合故障，不是独立现象。

### 参考实现怎么做

- **opencodex**：它是 Codex 的注入层，直接改写 Codex 的模型目录——catalog entry 写入 `context_window` 并算好 `auto_compact_token_limit = min(floor(cw×0.9), maxInputTokens)`（effort.ts:124-131），还丢弃用户谎报窗口的全局 override（inject.ts:429-431）。
- **sub2api**：无此问题面——它面向的客户端配置里模型名就是上游真名，Codex 用自己目录里的真元数据。

### 建议修法与代价

portage 是纯网关，**没有通道把窗口值推给 Codex**（Codex 不读网关的 `/v1/models` 取窗口）。选项：

1. **文档指引**（工作量最小，建议）：在使用文档里写明——把 portage 配为 Codex custom provider 时，须在 config.toml 里为每个模型设 `model_context_window`（及可选 `model_auto_compact_token_limit`），按真实上游窗口填。
2. **模型命名建议**：接入点名沿用 Codex 认得的真名（如 `gpt-5.x` 系）时目录元数据自然对上；限定名场景仍需选项 1。
3. **接受错位**（不建议单独选）：不处理，则 G1 修好之后此项降级为「压缩偏早/偏晚」，但在 G1 修好前它是首发故障。

---

## G2（High）：`compaction` item 回带被静默丢弃 → 压缩摘要整段失忆

### 现象与故障模式

压缩成功后，Codex 后续每个请求的 input 都回带 `{"type":"compaction","encrypted_content":"..."}`（#68 §6）。该 item 在 `decodeInput` 同样落 default 分支被静默丢弃（`decode.go:208-217`，`compaction_summary`/`context_compaction` 两个别名同理）。后果：转换路径上，**替换历史的核心——压缩摘要——整段消失**，模型只看到压缩后保留的少量 user 消息，此前几十万 token 的上下文无声蒸发，客户端与网关都无任何警告。

现状触发面：转换路径上 G1 使压缩根本成不了，所以此项今天主要在**混路场景**暴露——同一模型先经 Responses 透传渠道（真 OpenAI）压缩成功，后续请求被 `store.Resolve` 路由到 Anthropic/CC 渠道时失忆。一旦 G1 选了本地合成，此项立刻成为硬前置。另注：透传路径把 encrypted blob 原样带给上游，同一 OpenAI 上游解得开；换成另一家 Responses 兼容上游则解不开（blob 是上游侧密文，decode.go:184-186 的注释对 reasoning 的判断同样适用于 compaction）。

### 参考实现怎么做

- **opencodex**：parser 遇 `compaction`/`compaction_summary`/`context_compaction` 输入项，`ocx1` 信封解得开就还原成一条 `SUMMARY_PREFIX + 摘要` 的 user 消息（镜像 codex-rs 本地压缩的存储形态，Codex 靠这个前缀识别摘要）；解不开（真加密 blob）降级为一句 `OPAQUE_COMPACTION_NOTE`；同时清空悬挂的 pendingReasoning，防止压缩边界前的孤立 reasoning 错挂（parser.ts:372-390）。
- **sub2api**：不需要——它不做 Responses→其他协议的历史转换，compact 请求自带完整 input 直达真 compact 端点。

### 建议修法与代价

1. **还原为 user 消息**（工作量小-中，建议）：decode 侧加一个 case——信封可解（配合 G1 选项 2 的自造信封）时还原 `SUMMARY_PREFIX + 摘要`；解不开时降级为一条固定的「早前对话已压缩」占位 user 消息，并登记 drop 日志。这是 G1 本地合成的必要配套，单独先做也能治混路失忆。
2. **只补日志**（工作量最小）：default 分支对 `compaction*` 类型登记 warn。失忆照旧，但至少可诊断。
3. **维持现状**：不建议——静默失忆是最难排障的一类故障。

---

## G4（Medium 现在 / High 将来）：R→A 的 usage 口径是 Anthropic 净值，Responses 契约要毛值

### 现象与故障模式

canonical `Usage` 明文约定**不归一各协议语义**（`internal/protocol/event.go:71-78`）：Anthropic 的 `input_tokens` 不含缓存（净值），OpenAI 的含（毛值）。而 Responses 编码侧 `usageBody` 把 `InputTokens` 直接写进 `input_tokens`、`total_tokens = input + output`（`internal/protocol/openairesponses/encode.go:455-466`）。R→CC 方向 CC 的 `prompt_tokens` 本就是毛值，直映正确（`internal/protocol/openaicc/decode.go:239-254`）；**R→A 方向写出的却是净值**——sub2api 在同一转换上显式做 `input_tokens = anthropic.input + cache_read + cache_creation`（#64 §6 的核心不变量），portage 没做。

对 Codex 的影响：`usage.total_tokens` 被低估（少掉全部缓存命中量）→ 压缩触发点后移 → 在真正该压缩之前撞上游 context 超限，故障形态与 G5 同构。

**当前被掩蔽**：portage 的 R→A 出口不注入 `cache_control` 断点、且把 `prompt_cache_key` 一类字段明置丢弃（`internal/protocol/anthropic/encode_request.go:103`），Anthropic 缓存字段今天几乎恒为 0，净值==毛值。缓存一旦落地（或某个 Claude 兼容上游自行上报缓存字段），此项立即转为真实故障。定级 Medium-now / High-later。

三字段齐全性没有问题：`usageBody` 恒发 `input_tokens`/`output_tokens`/`total_tokens` 三个整数（sub2api 特意兜底的「缺任一字段 Codex 拒收 completed」在 portage 不会发生）。tap 旁路对透传无影响：观察者挂 `io.TeeReader`、Write 恒不报错（`server.go:387-401`、`convert.go:109-121`），且 `openairesponses/tap.go` 是纯被动解析。

**附带一处小缺口**：`event.go:41-46` 承诺 EvUsage「后来者的**非零字段**覆盖先前值」，但编码侧实现是整结构体覆盖 `e.usage = *ev.Usage`（`encode.go:129-134`）。真实 Anthropic 的 `message_delta` usage 带全字段（golden 转录证实）所以现在不炸；某些兼容上游只发 `{"output_tokens":N}` 时 input 会被清零。一行修复（按字段非零合并）。

### 参考实现怎么做

- **sub2api**：apicompat 把毛/净不变量钉死在双向转换里（Anthropic→Responses：`input_tokens = input + cache_read + cache_creation`，cache_read 落 `input_tokens_details.cached_tokens`），流式由 `message_start`/`message_delta` 累积状态。
- **opencodex**：bridge 同样在合成 `response.completed` 时按毛值口径组 usage。

### 建议修法与代价

1. **在 Responses 编码侧归一**（工作量小，建议）：`usageBody` 需要知道来源语义——最干净的做法是把归一职责放 decode 侧（canonical `Usage.InputTokens` 定死为毛值，Anthropic 解码时加回 cache 两项），与 sub2api 同构；这要改 `event.go` 的约定注释 + anthropic 解码 + anthropic 编码（A 出口要减回去）+ 两侧测试。风险低，但动的是「不在 canonical 归一」这条既有约定，需 PO 拍板。
2. **维持现状 + 待缓存落地再修**：接受 latent 定级；但 EvUsage 整体覆盖那一行建议现在就修。

---

## G3（Low）：legacy v1 `POST /v1/responses/compact` 未路由 → 裸 404

### 现象与故障模式

路由表只注册四个转发端点（`internal/server/server.go:202-211`），`/v1/responses/compact` 落到 NoRoute；非 `/admin` 前缀路径回**空体 404**（`internal/admin/webui.go:39-48`，不会拿到 SPA HTML）。v1 客户端（feature flag 关闭或旧版 Codex）压缩请求得到 404 → 压缩失败 → turn 不执行，历史不变，下一 turn 再触发再失败——与 G1 同样卡死，但错误可见（非 Fatal 静默）。

**实际权重低**：`remote_compaction_v2` 是 `Stage::Stable` 且默认开启（#68 §0），当前版本 Codex 对 OpenAI provider 一律走 v2 普通 `/responses`；v1 只剩「用户手工关 flag」与「老版本」两个入口。

### 参考实现怎么做（两家差异）

- **sub2api**：把 `/v1/responses/compact` 当**一等 canonical 入口**——归一化先于根 `/v1/responses` 匹配、多种路径变体、unary→SSE 桥 + 心跳（unary 上游静默期防反代掐线）、独立记账维度。
- **opencodex**：v1 不单独实现——input 尾部自己拼一个 `compaction_trigger` 内部复用 v2 合成 turn，校验后组装 v1 `{"output":[...]}` 替换历史。

### 建议修法与代价

1. **维持 404 + 文档记录**（工作量零，建议）：v2 默认开，404 语义也算「明确失败」。
2. **路由到 501 明确拒绝**：一行路由 + 错误文案，把「网关不支持 v1 compact」说清楚，比裸 404 可诊断。工作量极小。
3. **实现**（不建议现在做）：若 G1 选了本地合成，可照 opencodex 复用合成路径低成本补上；独立实现（sub2api 式转发 + 心跳）在 portage 无真 compact 上游的前提下没有意义。

---

## G6（Low）：`previous_response_id` 丢弃策略在压缩场景基本够用，但依赖 G2

### 现象与故障模式

转换路径 decode 时直接丢弃 `previous_response_id`、不进 Extras（`decode.go:26-32`、`62-66`，依据是口径层已决的「v1 只支持无状态用法，失配时删掉改用完整 input 重建上下文」，同 sub2api `RemovePreviousResponseIDFromBody` 的语义）；透传路径原样带给上游（body 不动）。

压缩场景检验：Codex API-key 模式每请求发完整 input、压缩后的替换历史也整体在 input 里（#68 §6），不依赖服务端会话链——**丢弃策略本身在压缩场景仍然成立**。但「靠完整 input 重建上下文」这个前提要求 input 里的 compaction item 被正确消费，而它正被 G2 丢掉：策略的正确性**依赖 G2 修复**，否则「重建的上下文」缺了摘要主体。透传路径无此问题（同一上游认自己的 response_id 与 blob）。

### 参考实现怎么做

- **sub2api**：HTTP 入口对 `previous_response_id` 直接 400（仅 WS 支持），compact 对齐靠完整 input + `session_id` 头，同样不依赖会话链。
- **opencodex**：反向做了本地展开（缓存 input+output 按 id 拼回前缀），但**压缩 turn 显式排除在该缓存外**——缓存它会把刚被替换掉的旧长历史重新灌回来。若 portage 将来做 previous_response_id 本地展开，此坑必须带上。

### 建议修法与代价

维持现状（工作量零），在 G2 的票里标注依赖关系即可。无独立修法需求。

---

## 总表（按严重度排序）

| # | 差距 | 严重度 | 对必过档 Codex CLI 的影响 | 建议（给 #67 裁决） |
|---|---|---|---|---|
| G1 | 转换路径静默丢 `compaction_trigger`，回 0 个 compaction item | **Blocker** | R→A / R→CC 长会话到阈值必 Fatal 砖死，无日志 | 先做：明确拒绝 + drop 日志 + 渠道能力位；立项：本地合成（opencodex 路子） |
| G5 | 无 context window 通道，Codex 按 fallback 272k 定触发点 | **High**（G1 的首发症状，复合故障） | 真窗口 < 假定窗口时先撞 400，再落 G1 | 文档指引 config.toml `model_context_window`；网关侧无修法 |
| G2 | 回带的 `compaction` item 被静默丢弃 | **High** | 混路场景摘要失忆；G1 本地合成的硬前置 | decode 还原为 SUMMARY_PREFIX user 消息 + 降级占位 + 日志 |
| G4 | R→A usage 净值直映 Responses 毛值契约；EvUsage 整体覆盖 | **Medium 现在 / High 将来**（被无缓存现状掩蔽） | 缓存落地后 total_tokens 低估，触发点后移撞 400 | 归一职责挪到 decode 侧（动 canonical 约定，需拍板）；覆盖语义一行修 |
| G3 | v1 `POST /v1/responses/compact` 裸 404 | **Low**（v2 默认开） | 仅关 flag / 老版本客户端受影响，错误可见 | 维持 404 记档，或一行 501；不单独实现 |
| G6 | `previous_response_id` 丢弃策略 | **Low** | 压缩场景下策略成立，但正确性依赖 G2 | 维持现状，G2 票里标依赖 |

**一句话给 #67**：透传路径今天就能过必过档（前提上游是 canonical OpenAI 且模型名/窗口配置对得上）；转换路径（R→A/R→CC）离必过档差 G1+G2+G5 三件事，其中 G1 的「本地合成 vs 明确拒绝」是唯一真正的大裁决，其余是配套与文档。

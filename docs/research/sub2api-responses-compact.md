# Research：sub2api 的 Responses 入口与 compact 兼容盘点（#64）

盘点基于本地 checkout `~/Code/GitHub/sub2api`（main @ `5a61430`，读档日期 2026-08-13）。所有路径均相对 `backend/`。sub2api 为 LGPL-3.0：本文只提炼思路与字段语义，短引用带出处，不整段复制实现。

## 结论先行

1. `/responses/compact` 在 sub2api 里是**独立 canonical 入口**（`EndpointResponsesCompact = "/v1/responses/compact"`），归一化时必须先于根 `/v1/responses` 匹配；此外还有 body-signal 提升：裸 `/responses` 请求若 input 里带 `compaction_trigger` item 且客户端未声明 `remote_compaction_v2`，路径被就地改写成 `/compact`。
2. compact 转发是**三分叉**，不是一句"透传"：OpenAI OAuth 账号 → `chatgpt.com/backend-api/codex/responses/compact`（上游 unary JSON）；API-key 账号 → `api.openai.com/v1/responses/compact` 或自定义 base；Grok 账号**本地模拟**（注入总结 prompt 当普通 Responses turn 跑，响应再转成 compaction item）。下游按客户端原始 stream 意图回 JSON 或合成的最小 Responses SSE。
3. compact 需要心跳，因为上游 compact 是 unary：模型压缩期间零字节可达数分钟，下游反代（Nginx/CF Tunnel）会按空闲超时掐线，Codex 盲重连重复烧 compact 配额。心跳以 SSE 注释行发送、首拍延迟一个间隔；一旦提交 200，错误只能降级为 `response.failed` 流内事件，且 failover 的"已写响应"判定必须扣除心跳字节。
4. `ErrNoAvailableCompactAccounts` 来自账号级 compact 能力三态（显式支持 / 未知 / 显式不支持），能力由管理端探测（真发一次 `/responses/compact`）或人工 force-on/off 写入账号 Extra；调度时 requireCompact 会过滤显式不支持的账号并把已知支持的排前面，全池被 compact 过滤时才报这个专用错误（对外 503 `compact_not_supported`）。
5. `previous_response_id` 在 HTTP `/v1/responses` 入口被**直接 400 拒绝**（仅 WSv2 支持）；消费它的是 WS 链路：跨组失配时用 `RemovePreviousResponseIDFromBody` 剥离重建上下文，WSv2 收到 `previous_response_not_found` 自动去掉重试一次。compact 白名单虽保留该字段，但 HTTP 路径上到不了上游。
6. apicompat 的 usage 语义锚点：**Responses `input_tokens` 是含缓存的毛值，Anthropic `input_tokens` 是不含缓存的净值**，双向转换按此加/减；解析侧对 `prompt_tokens`/`cache_write_*` 等别名做容错归一。compact 的 SSE bridge 还要求 usage 三个整数字段齐全，否则整个删掉（Codex 解析器会因缺字段拒收 `response.completed`）。

---

## 1. `/responses/compact` 端点归一化

事实源：`internal/handler/endpoint.go`。

- Canonical 常量集中在一处：`EndpointResponses = "/v1/responses"`、`EndpointResponsesCompact = "/v1/responses/compact"`（endpoint.go:22-23）。
- `NormalizeInboundEndpoint` 把带前缀的原始路径归一到 canonical 入口；compact 分支必须**先于**根 Responses 分支判断，否则 `/v1/responses` 作为 `/v1/responses/compact` 的前缀会先命中（endpoint.go:77-79、105-108）。
- 路径变体（endpoint.go:64-75 注释逐条列出）：
  - `/v1/responses/compact[/detail]`、`/openai/v1/responses/compact[/detail]` → 靠 `strings.Contains(path, EndpointResponsesCompact)` 命中；
  - 裸路由 `/responses/compact[/*]` 与 Codex 直连路由 `/backend-api/codex/responses/compact[/*]` → 靠 `isResponsesCompactAliasPath`（endpoint.go:130-136），锚定在路径开头（`isBareOrSubpathOf`，endpoint.go:165-167），避免 `/foo/responses` 之类误匹配。
- 与普通 `/v1/responses` 的处理差异：
  - 独立的 canonical inbound 值，用于 usage 记账的 `InboundEndpoint` 维度（endpoint.go:273-297 `GetInboundEndpoint`）。
  - `DeriveUpstreamEndpoint` 对 OpenAI/Grok 平台保留 `/responses` 之后的子资源后缀（`responsesSubpathSuffix`，endpoint.go:194-206、229-243）；拿不到原始路径时按 inbound 回退到 compact，不让它被静默当成根端点。
  - handler 侧另有网关判定 `isOpenAIRemoteCompactPath`（后缀 `/responses/compact`，`internal/handler/openai_gateway_handler.go:719-725`），驱动 requireCompact 调度与 compact 结局日志。
- **body-signal 提升**（`normalizeOpenAIResponsesCompactRequest`，openai_gateway_handler.go:756-785）：裸 `/responses` 请求若 input 含 `type=="compaction_trigger"` 的 item（`internal/service/openai_compact_body_signal.go:9-26`）：
  - 客户端带 `x-codex-beta-features: remote_compaction_v2` 且 `stream:true` → 保持原生流式 `/responses` wire，不提升（openai_gateway_handler.go:737-750、759-761）；
  - 否则把 `c.Request.URL.Path` 改写为 `.../compact`，原始 `stream:true` 意图用 `MarkOpenAICompactClientStream` 记进 gin context（openai_gateway_handler.go:762-767）。
- compact 请求体做**白名单归一**：只保留 `model/input/instructions/tools/parallel_tool_calls/reasoning/text/previous_response_id`，丢弃 `prompt_cache_key/store/stream` 等请求级字段（`internal/service/openai_gateway_request_body.go:289-322`）。`prompt_cache_key` 在丢弃前被存为 compact 会话种子（openai_gateway_handler.go:773-775），用于上游 `session_id` 头（见 §2）。

## 2. compact 的转发/转换路径

事实源：`internal/service/openai_gateway_forward.go`、`openai_gateway_request_body.go`、`openai_gateway_response_handling.go`、`openai_gateway_grok_compact.go`。

**上游侧（按账号类型三分叉）：**

- OAuth（ChatGPT 内部 API）：目标 URL 是 `chatgptCodexURL = "https://chatgpt.com/backend-api/codex/responses"`（`openai_gateway_service.go:31`）+ 原始路径的 `/compact` 后缀（forward.go:986-1008 `buildUpstreamRequest` + `appendOpenAIResponsesRequestPathSuffix`）。上游是 **unary JSON 协议**：`Accept: application/json`（非 compact 是 `text/event-stream`），并设置隔离后的 `session_id` 头（来源优先级：客户端 `session_id`/`conversation_id` 头 → `prompt_cache_key` 种子 → 随机 UUID，request_body.go:350-365；forward.go:1061-1070）。透传 OAuth body 归一时 compact 删除 `store` 与 `stream`（非 compact 是 `store=false`、保持 stream），见 request_body.go:668-735。
- API-key：`api.openai.com/v1/responses` 或账号自定义 base URL，同样拼 `/compact` 后缀（forward.go:993-1008）；也显式 `Accept: application/json`，防 OpenAI 兼容网关按 SSE 返回（forward.go:1078-1082）。
- Grok：Grok 没有 compact 端点，**本地模拟**——把 compact 请求改写为普通 Responses turn，末尾追加一段完整的"生成会话总结"用户消息（内容为 grok-build 的 `build_compaction_prompt`，多段长文，此处不转录），`store=false`、`stream=false`、有 tools 时 `tool_choice="none"`（`openai_gateway_grok_compact.go:11-60`）；响应再经 `convertGrokResponseToOpenAICompact` 转回 compact 形态（response_handling.go:1136-1141）。

其他上游侧修饰：compact 模型可按账号单独重映射（`compact_model_mapping`，只影响 compact 不影响普通 Responses，`account.go:889-897`；应用在 forward.go:276-288）；OpenAI OAuth 的 GPT-5.6 compact 把 `reasoning.effort: max` 降为 `xhigh`（ChatGPT compact 端点只收到 xhigh，request_body.go:334-348）；compact 不注入 image generation 工具、不补 `client_metadata`（forward.go:312-314、397-398）。

**下游侧（回客户端）：**

- 客户端原始意图非流式：上游 JSON 原样（经模型名回写等修饰）返回。
- 客户端原始意图 `stream:true`（body-signal 场景，Codex remote compact v2 消费协议）：unary JSON 被**合成为最小 Responses SSE 流**——每个 `output[]` item 一条 `response.output_item.done`，最后一条 `response.completed` 带完整 response 对象（`openai_compact_stream_bridge.go:137-204`）。Codex 只从 `output_item.done` 收集 item、要求恰好一个 `type=compaction` 的 item 和 `response.completed`，否则报 "stream closed before response.completed" 并无限重连（stream_bridge.go:16-22 注释，引 issue #3875）。
- 上游若违约回了 SSE（Content-Type 或 body 帧探测，response_handling.go:1114-1135），走 `handleSSEToJSON` 提取终态 JSON，并用 `supplementCompactionItemFromSSE` 保证终态 output 携带 compaction item（`compaction`/别名 `compaction_summary`，response_handling.go:1207-1277、1563-1571、1574-1580）。

## 3. compact SSE keepalive 心跳

事实源：`internal/service/openai_compact_sse_keepalive.go`、`internal/handler/failover_loop.go`、`internal/handler/openai_gateway_handler.go`。

**为什么需要**（keepalive.go:17-26 注释，引 issue #3887）：上游 `/responses/compact` 是 unary，模型压缩期间不发任何字节（大上下文可长达数分钟）；下游经反向代理时零字节静默触发代理空闲/读超时掐断连接，Codex 只会盲重连并重复消耗上游 compact 配额。心跳是 SSE 注释行 `": keepalive\n\n"`（keepalive.go:106），eventsource 解析层直接忽略，不进客户端事件流。

**机制要点：**

- 仅对标记了 body-signal 客户端流式的 compact 请求启动（`StartOpenAICompactSSEKeepalive`，keepalive.go:45-48）；间隔复用流式 keepalive 配置 `StreamKeepaliveInterval`（openai_gateway_handler.go:2593-2599），在 `Responses` handler 里 compact 归一化之后、调度之前挂上（openai_gateway_handler.go:290-294）。
- **首拍延迟一个 interval**：绝大多数硬错误（鉴权/参数/限流）在首拍前返回，仍走原 JSON+状态码链路；首拍之后响应头被提交为 200 + `text/event-stream`（keepalive.go:24-26、91-114），后续错误只能降级为 `response.failed` 流内终止事件（Codex 把 `response.failed` 当合法终止事件，普通 error 帧不识别，stream_bridge.go:106-135）。
- `c.Writer` 被换成包装 writer：请求侧任何响应构造（含 Header 访问）先在互斥锁下停拍心跳，避免与心跳 goroutine 数据竞争或字节交错（keepalive.go:42-56、184-232）。

**`StopOpenAICompactSSEKeepaliveCommitted` 与 failover 的交互：**

- 函数语义：停心跳并报告响应头是否已被心跳提交为 200；调用方以此决定走原 JSON/状态码链路还是流内降级（keepalive.go:131-152）。
- `failover_loop.go:188-203`（`failoverClientGone`）：判定客户端断开时**先停心跳**（接管 ResponseWriter、建立 happens-before），与 `handleStreamingAwareError`/`errorResponse` 等终结路径对齐；心跳已提交 200 时状态码已固化，不再标 499。
- handler 各终结路径同样先停心跳再写回（openai_gateway_handler.go:2402-2404、2471-2488、2577-2580、2868-2871）。
- **failover 判定不得被心跳污染**：handler 以"Forward 前后已写字节数是否变化"判定是否已向客户端写出响应（变化则放弃换号）。心跳字节不构成语义响应，快照与对比一律用 `OpenAICompactKeepaliveAdjustedWrittenSize`（扣除心跳字节，仅心跳时归一化为 gin 的未写出哨兵 -1；keepalive.go:154-182），见 openai_gateway_handler.go:527-529、2520-2523、2551。否则 compact 一旦发过心跳，上游 429/5xx 就永远不换号（keepalive.go:156-158 注释）。
- compact 结局日志同理：心跳提交后 wire 状态码固化 200，真实结局以 `MarkOpsStreamError` 标记的流内错误为准（openai_gateway_handler.go:809-819）。

## 4. `ErrNoAvailableCompactAccounts` 与账号池语义

事实源：`internal/service/openai_gateway_service.go`、`openai_gateway_scheduling.go`、`openai_account_scheduler.go`、`account.go`、`openai_compact_probe.go`。

- 错误定义：`"no available accounts support /responses/compact"`（openai_gateway_service.go:383-385）。
- **账号级 compact 能力是三态**（`openAICompactSupportTier`，openai_gateway_scheduling.go:234-254）：0=显式不支持、1=未知（未探测）、2=显式支持；Grok 恒为 2（本地模拟，无所谓上游支持）。
- 能力事实存在账号 `Extra`：`openai_compact_supported`（bool）+ `openai_compact_mode`（auto/force-on/force-off 人工覆盖），读取见 `account.go:851-887`。写入方是**compact 探测**：管理端账号测试的 `compact` 模式真发一次最小 `/responses/compact` 请求，2xx 记 supported=true；404/405/501、或 400/403/422 且错误文案含 "compact"+"unsupported/not support/..." 记 supported=false；其余失败只记错误不改能力位（`openai_compact_probe.go:26-99`）。未知能力**保持可用**，避免老账号在探测前被一刀切（account.go:875-887）。
- 调度语义（requireCompact 由 `isOpenAIRemoteCompactPath` 决定，openai_gateway_handler.go:424）：
  - 候选过滤：tier 0（显式不支持）直接不参与（openai_gateway_scheduling.go:300-302；openai_account_scheduler.go:823、1150-1152、1240、1620-1621）。
  - 排序偏好：已知支持（tier 2）排在未知（tier 1）之前（openai_account_scheduler.go:1040-1058；fallback 层 `prioritizeOpenAICompactAccounts`，openai_gateway_scheduling.go:592-595、1165-1167）。
  - 报错口径：**池非空但全被 compact 过滤**才报 `ErrNoAvailableCompactAccounts`（openai_gateway_scheduling.go:1188-1190、208-211；openai_account_scheduler.go:1475-1487），与普通 `ErrNoAvailableAccounts` 区分；handler 对外回 503 + `compact_not_supported`（openai_gateway_handler.go:475-478），ops 侧与 no-account 同归类为容量受限（`ops_error_logger.go:513`）。
- 之所以要独立判定：compact 是 ChatGPT backend 的**能力子集**——同平台账号池里只有部分账号（OAuth/特定网关）真的支持 `/responses/compact`，普通"有号但都不支持 compact"与"完全没号"是两种不同的运维事实，错误必须可区分。

## 5. `previous_response_id` 处理与 compact 的关系

事实源：`internal/service/openai_previous_response_id.go`、`channel_service.go`、`openai_gateway_forward.go`、`openai_ws_forwarder_v2.go`、`internal/handler/openai_gateway_handler.go`。分层描述：

- **分类器**（openai_previous_response_id.go:8-37）：按形态分四类——`resp_*` 是 response_id；`msg_/message_/item_/chatcmpl_*` 判为 message_id（客户端拿错 ID 的常见 bug）；其余 unknown。仅用于诊断与差异化报错。
- **(a) HTTP `/v1/responses` 入口：一律 400 拒绝**（openai_gateway_handler.go:327-347）。message_id 形态单独报 "must be a response.id (resp_*)"；其余报 "previous_response_id is only supported on Responses WebSocket v2"。因此 HTTP compact 请求即使带该字段也到不了上游。
- **(b) WS 链路才消费它**：客户端 WS 入口 `ResponsesWebSocket`（openai_gateway_handler.go:1439 起）在首轮首包做**跨组/会话失配防护**——`previous_response_id` 未命中当前分组的粘连账号（StickyPreviousHit=false）时，说明会话链不属于本次调度到的账号，原样转发会触发上游会话链鉴权失败；故剥离首包的 `previous_response_id`，改用首包 `input` 重建上下文；带 `function_call_output` 的工具续链无法重建则保持原样（openai_gateway_handler.go:1999-2010）。剥离用的就是 `RemovePreviousResponseIDFromBody`（`channel_service.go:609-622`，语义即注释所述"会话失配时改用完整 input 重建上下文"）。
- **(c) WSv2 上游自愈**：转发层收到 `previous_response_not_found` 时自动去掉 `previous_response_id` 重试一次（工具续链除外），开关 `IngressPreviousResponseRecoveryEnabled` 默认开（`openai_ws_forwarder_v2.go:573-593`；`config.go:1117`）。
- **(d) forward 层兜底**：上游 transport 非 WSv2 时，body 里的 `previous_response_id` 一律删除后再转发（`openai_gateway_forward.go:457-458`）。
- **与 compact 的关系**：compact 白名单归一**保留** `previous_response_id` 字段（request_body.go:305）——即白名单不替上游做语义裁决；但当前 HTTP 链路上它先于转发被 (a) 拦截，实际到不了上游。压缩上下文的对齐靠的是 compact 请求自带完整 `input` + `session_id` 头（§2），不依赖 `previous_response_id` 会话链。

## 6. Responses 转换路径上的 usage 语义（apicompat）

事实源：`internal/pkg/apicompat/`。canonical 结构 `ResponsesUsage`：`input_tokens`/`output_tokens`/`total_tokens` + 扩展的 `cache_creation_input_tokens` + `input_tokens_details.cached_tokens` 等明细（`types.go:471-481、552-566`）。

- **核心不变量：Responses 的 `input_tokens` 是毛值（含缓存读/写），Anthropic 的 `input_tokens` 是净值（不含缓存）。**
  - Responses→Anthropic：`anthropic.input_tokens = responses.input_tokens - cached_tokens - cache_creation_input_tokens`，`cache_read_input_tokens = input_tokens_details.cached_tokens`（`responses_to_anthropic.go:98-118`）。
  - Anthropic→Responses：反向相加，`input_tokens = input + cache_read + cache_creation`，`total = 该毛值 + output`；cache_read 落到 `input_tokens_details.cached_tokens`（`anthropic_to_responses_response.go:97-114`，注释明说这是为让下游拿到 OpenAI 语义）。流式同理由 `message_start`/`message_delta` 累积状态（同文件 251-258、455-464）。
- **Responses→ChatCompletions**：`prompt_tokens = input_tokens`（毛值直映，无需加减）、`completion_tokens = output_tokens`、`total = 两者之和`；明细映射到 `prompt_tokens_details`（cached/audio/cache_creation/cache_write）与 `completion_tokens_details`（reasoning/audio/accepted+rejected_prediction），全零时省略（`responses_to_chatcompletions.go:341-400`）。流式 usage 从终止事件取（顶层 `usage` 或 `response.usage` 均可，responses_to_chatcompletions.go:297-303），仅在客户端 `include_usage` 时随终帧下发（188-195、327-334）。
- **解析容错**（`ResponsesUsage.UnmarshalJSON`，types.go:483-550）：兼容把 usage 写成 ChatCompletions 形态的上游——`prompt_tokens`/`completion_tokens` 回填 input/output；`cache_creation_tokens`/`cache_write_input_tokens`/`cache_write_tokens` 及嵌套明细里的同类别名按优先级归一到 `cache_creation_input_tokens`；`total_tokens` 缺失时用 input+output 补齐。终止事件顶层 usage 与 `response.usage` 双位置兼容（types.go:578-580）。
- **compact 专属的 usage 兜底**：SSE bridge 合成 `response.completed` 前，若 usage 存在但 `input_tokens/output_tokens/total_tokens` 任一不是整数，整个 usage 字段删除——Codex 的解析器会因缺字段拒收整条 completed 事件（`openai_compact_stream_bridge.go:139-142 注释、165-171、206-216`）。
- 网关记账维度上，compact 请求以独立的 `InboundEndpoint = /v1/responses/compact` 进 usage 记录（endpoint.go:268-297 注释 + openai_gateway_handler.go:1966-1987 的 RecordUsage 输入）。

## 对 portage 的启示（一句话）

compact 若纳入范围，需要的是"独立 canonical 入口 + 上游 unary/下游合成 SSE 的桥 + 账号能力三态"三件套，而非在 `/v1/responses` 上加分支；usage 转换必须钉死"Responses 毛值 vs Anthropic 净值"这一不变量（与口径层 §5 坑清单对齐）。

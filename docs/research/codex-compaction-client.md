# Research：Codex CLI（codex-rs）自动压缩客户端机制盘点（#68）

事实源：`openai/codex` 主仓 `main`，commit `7093e8c480715667a5a75b602fd8c9ca2cad1780`（2026-08-13 抓取）。所有路径均相对 `codex-rs/`。交叉印证：本地 `~/Code/GitHub/opencodex/src/responses/compaction.ts` 的适配注释（与线上源码一致）。

## 0. 总览：三套实现与派发规则

Codex 客户端有三套压缩实现，按 provider 能力 + feature flag 派发（`core/src/session/turn.rs` `run_auto_compact`、手动 `/compact` 走 `core/src/tasks/compact.rs`，逻辑相同）：

| 实现 | 代码 | 端点 | 何时选用 |
|---|---|---|---|
| Remote v2（当前默认） | `core/src/compact_remote_v2.rs` | 普通流式 `/responses`，input 末尾追加 `compaction_trigger` item | provider 能力为 V2 且 feature `remote_compaction_v2` 开启 |
| Remote v1（legacy） | `core/src/compact_remote.rs` + `client.rs` | 专用一元 `POST {base_url}/responses/compact` | provider 能力为 V1，或 V2 但 feature 关闭 |
| 本地摘要 | `core/src/compact.rs` | 普通 `/responses`，把摘要 prompt 当 user 消息发 | provider 不支持 remote compaction |

- Provider 能力：`model-provider/src/provider.rs` `capabilities()` —— **OpenAI 官方 provider 或 Azure responses provider ⇒ `RemoteCompactionSupport::V2`，其余一律 `Unsupported`**（`RemoteCompactionSupport` 枚举注释明确：V1 = 仅 `/v1/responses/compact` 专用端点，V2 = 专用端点 + `compaction_trigger` item 两者都支持）。
- Feature flag：`features/src/lib.rs` —— `remote_compaction_v2`，`Stage::Stable`，`default_enabled: true`。即**默认走 v2（compaction_trigger）路径**。
- 另有 `Feature::TokenBudget` 实验路径（`core/src/compact_token_budget.rs`）：不做任何摘要请求，直接开新 context window，不在本盘点重点内。

## 1. 触发时机与阈值

### 阈值计算（`core/src/session/context_window.rs` `context_window_token_status`）

- `active_context_tokens = sess.get_total_token_usage()`。
- `token_limit_reached = (auto_compact_scope_tokens >= 带 buffer 的 auto_compact_limit) || (active_context_tokens >= full_context_window_limit)`。
- 默认 scope 为 `AutoCompactTokenLimitScope::Total`（`protocol/src/config_types.rs`，`#[default]`）：scope_tokens 即全量 active tokens，limit 取 `model_info.auto_compact_token_limit()`。
- `ModelInfo::auto_compact_token_limit()`（`protocol/src/openai_models.rs`）：**缺省 = context_window × 9/10（整数除法）；若模型元数据带了显式 `auto_compact_token_limit`，取 `min(显式值, 90% 窗口)`**。
- fallback buffer 只在配置了 `token_budget` 时加到 limit 上，默认为 0。
- 用户可在 config.toml 覆盖 `model_context_window` / `model_auto_compact_token_limit` / `..._scope`（`config/src/config_toml.rs`）。

### context window 信息从哪来

`turn_context.model_context_window()` ← `ModelInfo::resolved_context_window() = context_window.or(max_context_window)`（`protocol/src/openai_models.rs`）。`ModelInfo` 由 models-manager 提供：内置模型目录（`models-manager/src/model_info.rs`，如 fallback 元数据 `context_window: 272_000`）+ 服务端模型目录（`ModelsResponse`，`models-manager/src/config.rs`）+ config 覆盖。

### 以哪个 usage 字段计

- SSE `response.completed` 的 `response.usage` 映射进 `TokenUsage`：`total_tokens` 直接取 API 的 `usage.total_tokens`，`reasoning_output_tokens` 取 `output_tokens_details.reasoning_tokens`（`codex-api/src/sse/responses.rs` `ResponseCompletedUsage` → `From<> for TokenUsage`）。
- `get_total_token_usage`（`core/src/context_manager/history.rs`）= **上一次响应的 `last_token_usage.total_tokens`**（含 reasoning，因为 API 的 total = input + output 且 output 含 reasoning）+ 最后一个模型产出 item 之后本地新增 item 的估算 tokens；当服务端未把历史 reasoning 计入（`server_reasoning_included == false`）时，再加上历史中带 `encrypted_content` 的 reasoning item 的估算 tokens。

### 触发点（`core/src/session/turn.rs`）

1. **Pre-turn**（`run_pre_sampling_compact`）：每个用户 turn 开始、发首个采样请求之前检查 `token_limit_reached`，超限则以 `CompactionReason::ContextLimit` / `CompactionPhase::PreTurn` 压缩。同函数还处理两个特殊 reason：`CompHashChanged`（模型的 compaction 兼容性 hash 变了）和 `ModelDownshift`（切到更小窗口的模型且旧用量超过新限额）。
2. **Mid-turn**：每次采样 + 工具执行完成后重算 token status；`should_roll_over = needs_follow_up && (显式 new_context 请求 || token_limit_reached)` —— 即**只有模型还要继续跑（有后续工具循环/待处理输入）且超限时才 mid-turn 压缩**，phase 为 `MidTurn`。
3. 采样请求本身报 `ContextWindowExceeded` 时不当场压缩，只 `set_total_tokens_full` 标记用量打满后向上抛错（turn.rs `try_run_sampling_request` 错误分支）；下一轮/下一 turn 的阈值检查随之触发压缩。
4. 手动 `/compact`：`core/src/tasks/compact.rs`，trigger=Manual，同样按上表派发。

## 2. v2 请求：普通 `/responses` + `compaction_trigger` input item

`core/src/compact_remote_v2_attempt.rs` `run_remote_compact_v2_attempt`：

- 取当前完整历史；先 `trim_function_call_history_to_fit_context_window`（`compact_remote.rs`）：若估算超窗，从最旧的 function/custom tool output 开始把 body 替换为 `"Output exceeded the available model context and was truncated"`。
- `input = 历史 for_prompt 结果`，然后 **`input.push(ResponseItem::CompactionTrigger {})`** —— 线上序列化为 `{"type":"compaction_trigger"}`（`protocol/src/models.rs`，`ResponseItem` 为 `#[serde(tag="type", rename_all="snake_case")]`；该 variant 无任何字段，注释：“Compaction triggers are request controls, not durable response items”）。
- 其余字段与普通采样请求一致：`tools`（当前 turn 的完整工具表）、`parallel_tool_calls`、`base_instructions`、reasoning effort/summary、service_tier，metadata 里带 `request_kind: compaction` 与 trigger/reason/implementation/phase（`core/src/responses_metadata.rs`）。
- 走与采样相同的流式 `client_session.stream(...)`（即普通 `/v1/responses` SSE），**没有专门的 v2 端点**。

## 3. v2 期待的响应：`collect_compaction_output`（`compact_remote_v2.rs`）

- 消费 SSE 流，只关心 `OutputItemDone` 与 `Completed`：
  - 统计 output item 总数；凡 `ResponseItem::Compaction { .. }` 计入 `compaction_count`，保留第一个。
  - `Compaction` 的线格式：`{"type":"compaction","encrypted_content":"<不透明字符串>"}`（必填 `encrypted_content: String`；反序列化接受别名 `"compaction_summary"`；可选 `id`）。
- 精确约束：
  - **流在 `response.completed` 之前结束 ⇒ `CodexErr::Stream("remote compaction v2 stream closed before response.completed")`（可重试）**。
  - **`compaction_count != 1`（0 个或多个）⇒ `CodexErr::Fatal("remote compaction v2 expected exactly one compaction output item, got {n} from {m} output items")`（不可重试）**。
  - 其它非 compaction 的 output item（如模型顺带发的 assistant message）**被忽略、不报错**（测试 `collect_compaction_output_accepts_additional_output_items`）。
- 成功时取走 `response_id` 与 `usage`（input_tokens 记为压缩前用量、output_tokens 记为摘要 tokens，进分析事件与 rollout budget）。

## 4. v1 请求/响应：专用一元端点 `POST {base_url}/responses/compact`

- 端点：`core/src/client.rs` `RESPONSES_COMPACT_ENDPOINT = "/responses/compact"`；`codex-api/src/endpoint/compact.rs` `CompactClient::path() = "responses/compact"`，POST、非流式，超时 = provider `stream_idle_timeout × 4`。
- 请求体 `CompactionInput`（`codex-api/src/common.rs`）：`{model, input: [ResponseItem...], instructions, tools, parallel_tool_calls, reasoning, service_tier?, prompt_cache_key?, text}` —— 即把普通 Responses 请求体原样发到 compact 端点，**不追加 compaction_trigger**。
- 响应体：`{"output": [ResponseItem...]}`（`CompactHistoryResponse`），**服务端直接返回整份替换用历史**；JSON 解析失败报 `ApiError::Stream`。

## 5. 压缩失败时的降级行为

- **失败不落地**：`replace_compacted_history` 只在成功路径执行；任何失败都保持原历史不变（下一次阈值检查会再次触发压缩）。
- 传输层重试：v2 走 `handle_retryable_response_stream_error`，重试上限 = `min(provider stream_max_retries, 2)`（`MAX_REMOTE_COMPACTION_V2_STREAM_RETRIES`）；只有 `err.is_retryable()` 的错误重试，**`Fatal`（如 compaction item 数不对）不重试**。
- 模型回退：inline 压缩若带 `fallback_step_context`（用于"上一个模型的压缩"场景），且错误属于 `should_retry_with_current_model`（`core/src/compact_model_fallback.rs`：InvalidRequest / UnexpectedStatus / ContextWindowExceeded / UsageLimitReached / ServerOverloaded / InternalServerError / RetryLimit），则换当前模型重试一次；仍失败则报原错误。
- **v1/v2 之间、remote→本地摘要之间没有自动降级**：派发是静态的（provider 能力 + feature flag），运行期请求失败不会改走另一套实现。
- 对 turn 的影响（`turn.rs`）：pre-turn 压缩失败 ⇒ 发 turn error 生命周期事件，`return Ok(None)`，**整个 turn 不执行**；mid-turn 压缩失败 ⇒ 同样发错误事件并结束本 turn（`TurnAborted` 原样上抛）。会话本身可继续，历史未被破坏。
- 本地摘要实现（`compact.rs`）自带一个特殊降级：压缩请求本身报 `ContextWindowExceeded` 时，从头部逐条删历史 item 后重试，删到只剩 1 条仍超限才放弃。

## 6. 压缩产物如何进入后续请求

### v2（`compact_remote_v2.rs` `build_v2_compacted_history`）

客户端**在本地重建替换历史**（不是服务端返回整份历史）：

1. 从本次压缩请求的 `input`（去掉末尾 trigger）过滤保留：`user` / `developer` / `system` 角色的 Message，以及非 FINAL_ANSWER 且估算 ≤ 10_000 tokens 的 AgentMessage（`MAX_RETAINED_AGENT_MESSAGE_TOKENS`）；再经 `should_keep_compacted_history_item` 二次过滤（developer 消息、指令包装的伪 user 消息被丢弃）。**旧的 `compaction` item、reasoning、所有 tool call/output 全部丢弃。**
2. 保留集从新到旧套 64_000 tokens 总预算（`RETAINED_MESSAGE_TOKEN_BUDGET`，镜像服务端 `/responses/compact` 的保留默认值），超预算的最旧一条做文本截断。
3. **把服务端返回的 `compaction` item 追加为替换历史的最后一项**；mid-turn 时把 initial context 插到最后一条真实 user 消息之前（模型被训练成期望压缩后 compaction item 位于历史末尾，见 `compact.rs` `InitialContextInjection` 注释）。
4. `replace_compacted_history` 落地 + `recompute_token_usage`。此后**每个普通采样请求的 `input` 都会原样回放 `{"type":"compaction","encrypted_content":"..."}` item**（历史序列化直出；`prepare_response_items_for_request` 只清理非法 id）。`encrypted_content` 对客户端完全不透明，从不解码。

### v1（`compact_remote.rs` `process_compacted_history`）

服务端返回的 `output` 数组本身就是替换历史：经 `should_keep_compacted_history_item` 过滤（保留 user/assistant 消息、AgentMessage、`compaction`/`context_compaction` item；丢 developer 消息与 tool 相关 item）后整体落地。

### 本地摘要（`compact.rs`）

无 compaction item：替换历史 = 保留的近期真实 user 消息（20_000 tokens 预算，`COMPACT_USER_MESSAGE_MAX_TOKENS`）+ 一条 user 角色的摘要消息（`"{SUMMARY_PREFIX}\n{摘要}"`，SUMMARY_PREFIX 来自 `codex-prompts` 的 summary_prefix 模板），后续请求把它当普通 user 消息回放。

## 7. 对 Portage 网关的含义（非结论，供后续票引用）

- Codex 判定"支持 remote compaction"看 provider 身份（内置 OpenAI/Azure），不做能力探测；把 OpenAI provider 指向网关时，网关会收到带 `{"type":"compaction_trigger"}` 的普通 `/responses` 请求（默认 v2 路径），必须在流里返回恰好一个 `{"type":"compaction","encrypted_content":...}` output item + `response.completed`，否则客户端 Fatal（不降级、不重试）。
- `encrypted_content` 是客户端不透明字符串，网关可自定义编码（本地 `opencodex` 用 `"ocx1:" + base64(摘要)` 方案即基于此事实）。
- feature flag 关闭或旧版客户端会打一元 `POST /responses/compact`，期待 `{"output":[...]}`。
